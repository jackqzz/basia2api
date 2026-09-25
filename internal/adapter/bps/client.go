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
		err = consumeSSE(resp.Body, emit, nr.Tools)
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
	// Excel 插件客户端画像：服务端按画像注入系统提示词+工具集。
	// run_officejs 传输通道只在 excel 画像下注入（codex-tui 画像没有它，
	// 客户端工具目录就会无 transport 可用）。与官方插件/bridge 同款默认值。
	req.Header.Set("x-openai-internal-basispoints-client-agent-profile", "excel")
	req.Header.Set("x-openai-internal-basispoints-client-editor", "excel")
	req.Header.Set("x-openai-internal-basispoints-client-host", "office")
	req.Header.Set("x-openai-internal-basispoints-client-platform", "excel")
	req.Header.Set("x-openai-internal-basispoints-client-platform-class", "PC")
	req.Header.Set("x-openai-internal-basispoints-client-product", "basispoints-excel-plugin")
	req.Header.Set("x-openai-internal-basispoints-client-runtime", "desktop")
	req.Header.Set("x-openai-internal-basispoints-office-host", "Excel")
	req.Header.Set("x-openai-internal-basispoints-office-platform", "PC")
	req.Header.Set("x-stainless-lang", "js")
	req.Header.Set("x-stainless-package-version", "6.31.0")
	req.Header.Set("x-stainless-runtime", "browser:chrome")
	req.Header.Set("x-stainless-arch", "unknown")
	req.Header.Set("x-stainless-os", "Unknown")
	req.Header.Set("x-stainless-retry-count", "0")
	req.Header.Set("origin", "https://bps.openai.com")
	req.Header.Set("originator", "codex-tui")
	req.Header.Set("version", firstNonEmpty(strings.TrimSpace(c.clientVersion), defaultClientVersion))
	req.Header.Set("User-Agent", c.userAgent())
	return c.httpClientRef().Do(req)
}

// ListModels 上游没有模型目录接口，目录走静态表。
func (c *httpClient) ListModels(ctx context.Context) ([]adapter.ModelInfo, error) {
	return nil, adapter.ErrNotImplemented
}

// whamUsageURL 是 ChatGPT 官方额度接口（与网页端/codex 同一数据源）。
const whamUsageURL = "https://chatgpt.com/backend-api/wham/usage"

// FetchUsage 用量页快照：先打 wham/usage 拿真实额度（rate_limit 窗口/credits），
// 失败则回退到 token 载荷里的身份/套餐信息。
func (c *httpClient) FetchUsage(ctx context.Context, websiteURL string) (adapter.UsageSnapshot, error) {
	sec, err := c.credential()
	if err != nil {
		return adapter.UsageSnapshot{}, err
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
	if err := c.fillWhamUsage(ctx, sec, &snap); err != nil {
		return snap, err
	}
	return snap, nil
}

// whamWindow 是 rate_limit 下的单个限流窗口（5h 主窗 / 周窗）。
type whamWindow struct {
	UsedPercent       float64 `json:"used_percent"`
	LimitWindowSecs   int64   `json:"limit_window_seconds"`
	ResetAfterSeconds int64   `json:"reset_after_seconds"`
}

// fillWhamUsage 请求 wham/usage 并把额度字段填进快照。
// primary_window（短窗）→ Auto，secondary_window（长窗）→ API，
// TotalPercentUsed 取两者峰值（额度满时 QuotaDisableReason 能正确熔断账号）。
func (c *httpClient) fillWhamUsage(ctx context.Context, sec Secret, snap *adapter.UsageSnapshot) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, whamUsageURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+sec.AccessToken)
	req.Header.Set("chatgpt-account-id", sec.AccountID)
	req.Header.Set("x-openai-account-id", sec.AccountID)
	req.Header.Set("x-basispoints-auth-mode", "chatgpt")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("originator", "codex-tui")
	req.Header.Set("User-Agent", c.userAgent())
	resp, err := c.httpClientRef().Do(req)
	if err != nil {
		return fmt.Errorf("wham/usage: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<10))
		return fmt.Errorf("wham/usage: status %d: %s", resp.StatusCode, truncate(strings.TrimSpace(string(raw)), 200))
	}
	var w struct {
		Email     string `json:"email"`
		UserID    string `json:"user_id"`
		PlanType  string `json:"plan_type"`
		RateLimit struct {
			Allowed          bool        `json:"allowed"`
			LimitReached     bool        `json:"limit_reached"`
			PrimaryWindow    *whamWindow `json:"primary_window"`
			SecondaryWindow  *whamWindow `json:"secondary_window"`
		} `json:"rate_limit"`
		Credits struct {
			HasCredits bool `json:"has_credits"`
			Unlimited  bool `json:"unlimited"`
		} `json:"credits"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&w); err != nil {
		return fmt.Errorf("wham/usage: decode: %w", err)
	}
	if w.Email != "" {
		snap.Email = w.Email
	}
	// wham 的 user_id 是成员级身份（account_id 是 workspace 级、全 team 共享，
	// 不能当身份用）——写进 WorkOSID 供 FindDuplicate 判重。
	if w.UserID != "" {
		snap.WorkOSID = w.UserID
	}
	if w.PlanType != "" {
		snap.PlanLabel = w.PlanType
		snap.IndividualPlan = w.PlanType
	}
	var peak float64
	if pw := w.RateLimit.PrimaryWindow; pw != nil {
		v := pw.UsedPercent
		snap.AutoPercentUsed = &v
		peak = v
	}
	if sw := w.RateLimit.SecondaryWindow; sw != nil {
		v := sw.UsedPercent
		snap.APIPercentUsed = &v
		if v > peak {
			peak = v
		}
		if sw.ResetAfterSeconds > 0 {
			snap.BillingCycleEnd = time.Now().Unix() + sw.ResetAfterSeconds
		}
	}
	if w.RateLimit.PrimaryWindow != nil || w.RateLimit.SecondaryWindow != nil {
		if w.RateLimit.LimitReached && peak < quotaHardLimit {
			peak = quotaHardLimit
		}
		snap.TotalPercentUsed = &peak
	}
	if w.Credits.Unlimited {
		snap.Unlimited = true
	}
	if w.Credits.HasCredits {
		on := true
		snap.OnDemandEnabled = &on
	}
	return nil
}

// quotaHardLimit 触发限流熔断的百分比（与 adapter.quotaFullPercent 对齐）。
const quotaHardLimit = 100.0
