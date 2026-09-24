package bps

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"bps-2api/internal/adapter"
	"bps-2api/internal/egress"
)

// ============================================================
// 上游客户端：POST {base}/responses（SSE）。
// 认证 = ChatGPT access token + chatgpt-account-id（从 JWT 推导）。
// ============================================================

// streamTimeout 是单次 SSE 请求的总超时（agent 长任务需要宽裕时间）。
const streamTimeout = 30 * time.Minute

// upstreamError 让 failclass 能按状态码分类（401/403 → 换号，429/5xx → 冷却换号）。
type upstreamError struct {
	Status int
	Msg    string
}

// Error 实现 error。
func (e *upstreamError) Error() string {
	if e.Status <= 0 {
		return "bps: " + e.Msg
	}
	return fmt.Sprintf("bps: status %d: %s", e.Status, e.Msg)
}

// StatusCode 供 failclass 读取上游 HTTP 状态码。
func (e *upstreamError) StatusCode() int { return e.Status }

// httpClient 是一个账号的上游会话。
type httpClient struct {
	baseURL       string
	clientVersion string
	clientType    string
	token         func() (string, error)

	mu   sync.Mutex
	http *http.Client
}

// newHTTPClient 构造上游客户端。
func newHTTPClient(cfg adapter.ClientConfig) *httpClient {
	return &httpClient{
		baseURL:       cfg.BaseURL,
		clientVersion: cfg.ClientVersion,
		clientType:    cfg.ClientType,
		token:         cfg.TokenProvider,
		http:          &http.Client{Timeout: streamTimeout},
	}
}

// SetBaseURL 更新上游基址（管理台可改）。
func (c *httpClient) SetBaseURL(url string) {
	if strings.TrimSpace(url) != "" {
		c.baseURL = url
	}
}

// SetHTTPClient 替换底层 HTTP 客户端。
func (c *httpClient) SetHTTPClient(h *http.Client) {
	if h == nil {
		return
	}
	c.mu.Lock()
	c.http = h
	c.mu.Unlock()
}

// SetEgress 按出口设置重建 HTTP 客户端（Resin/HTTP 代理）。
func (c *httpClient) SetEgress(s egress.Settings) error {
	if s.Account == "" {
		s.Account = DisplayName
	}
	cli, err := egress.HTTPClient(s, streamTimeout)
	if err != nil {
		return err
	}
	c.mu.Lock()
	c.http = cli
	c.mu.Unlock()
	return nil
}

// httpClientRef 取当前 HTTP 客户端引用。
func (c *httpClient) httpClientRef() *http.Client {
	c.mu.Lock()
	h := c.http
	c.mu.Unlock()
	return h
}

// origin 返回规范化基址。
func (c *httpClient) origin() string {
	if strings.TrimSpace(c.baseURL) != "" {
		return strings.TrimRight(c.baseURL, "/")
	}
	return DefaultBaseURL
}

// userAgent 构造 Codex 身份 UA（与内核出站身份规范一致）。
func (c *httpClient) userAgent() string {
	v := firstNonEmpty(strings.TrimSpace(c.clientVersion), defaultClientVersion)
	return "codex-tui/" + v + " (Windows 10.0.26200; x86_64) unknown (codex-tui; " + v + ")"
}

// Stream 执行一轮对话：POST /responses 并解析 SSE。
// 只对「建连失败 / 429 / 5xx」在未收到任何字节前重试一次；SSE 一旦开始不重试。
func (c *httpClient) Stream(ctx context.Context, nr *adapter.NativeRequest, emit func(adapter.Event) bool) error {
	sec, err := c.credential()
	if err != nil {
		return err
	}
	body, err := json.Marshal(buildRequestBody(nr))
	if err != nil {
		return fmt.Errorf("bps: encode request: %w", err)
	}

	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(800 * time.Millisecond):
			}
		}
		resp, err := c.do(ctx, sec, body)
		if err != nil {
			lastErr = fmt.Errorf("bps: request failed: %w", err)
			continue
		}
		if resp.StatusCode != http.StatusOK {
			raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
			resp.Body.Close()
			upErr := &upstreamError{Status: resp.StatusCode, Msg: truncate(strings.TrimSpace(string(raw)), 400)}
			if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
				lastErr = upErr
				continue
			}
			return upErr
		}
		err = consumeSSE(resp.Body, emit)
		resp.Body.Close()
		if err == errClientGone {
			return nil // 客户端主动断开：不算上游失败
		}
		return err
	}
	return lastErr
}

// credential 取当前 token 并解析出 access token / account id。
func (c *httpClient) credential() (Secret, error) {
	if c.token == nil {
		return Secret{}, fmt.Errorf("bps: no credential configured")
	}
	raw, err := c.token()
	if err != nil {
		return Secret{}, err
	}
	sec := ParseSecret(raw)
	if sec.AccessToken == "" {
		return Secret{}, fmt.Errorf("bps: credential missing access token")
	}
	if sec.AccountID == "" {
		sec.AccountID = AccountID(sec.AccessToken)
	}
	if sec.AccountID == "" {
		return Secret{}, fmt.Errorf("bps: credential missing chatgpt_account_id (re-import the ChatGPT auth JSON)")
	}
	return sec, nil
}

// do 发送一次请求。
func (c *httpClient) do(ctx context.Context, sec Secret, body []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.origin()+PathResponses, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+sec.AccessToken)
	req.Header.Set("chatgpt-account-id", sec.AccountID)
	req.Header.Set("x-openai-account-id", sec.AccountID)
	req.Header.Set("x-basispoints-auth-mode", "chatgpt")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("OpenAI-Beta", "responses=experimental")
	req.Header.Set("originator", "codex-tui")
	req.Header.Set("version", firstNonEmpty(strings.TrimSpace(c.clientVersion), defaultClientVersion))
	req.Header.Set("User-Agent", c.userAgent())
	return c.httpClientRef().Do(req)
}

// ListModels 上游没有模型目录接口，目录走静态表。
func (c *httpClient) ListModels(ctx context.Context) ([]adapter.ModelInfo, error) {
	return nil, adapter.ErrNotImplemented
}

// FetchUsage 用量页快照：不请求上游，直接用 token 载荷里的身份/套餐信息。
// TODO: 需要额度百分比时接 chatgpt.com/backend-api/wham/usage（见 docs/TODO）。
func (c *httpClient) FetchUsage(ctx context.Context, websiteURL string) (adapter.UsageSnapshot, error) {
	raw, err := c.token()
	if err != nil {
		return adapter.UsageSnapshot{}, err
	}
	sec := ParseSecret(raw)
	if sec.AccessToken == "" {
		return adapter.UsageSnapshot{}, fmt.Errorf("bps: credential missing access token")
	}
	snap := adapter.UsageSnapshot{
		Email:          sec.Email,
		PlanLabel:      sec.PlanType,
		IndividualPlan: sec.PlanType,
		FetchedAt:      time.Now().Unix(),
	}
	if snap.Email == "" {
		snap.Email = Email(sec.AccessToken)
	}
	return snap, nil
}
