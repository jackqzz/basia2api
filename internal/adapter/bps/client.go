package bps

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"slices"
	"strconv"
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
	c.prepareImages(ctx, sec, nr)
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
			// 图片 URL 被上游 fetch 403（SAS 过期/跨账号复用）：废掉缓存、按当前
			// 账号重新上传、重试一次——救回整轮而不是直接拒。
			if resp.StatusCode == http.StatusBadRequest &&
				strings.Contains(upErr.Msg, "downloading file") && c.refreshImages(ctx, sec, nr) {
				if nb, merr := json.Marshal(buildRequestBody(nr)); merr == nil {
					body = nb
					lastErr = upErr
					continue
				}
			}
			if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
				lastErr = upErr
				continue
			}
			// 4xx 拒单：把出站请求体落盘供排查（入站客户端不可见，含我们拼的 envelope）。
			if resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusUnprocessableEntity {
				dumpRejectedBody(body, nr.Model)
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

// dumpRejectedBody 上游 4xx 时把出站请求体落盘：/tmp/bps-reject-<ts>-<model>.json。
// 含 run_officejs envelope 明文——仅本地排查用，不回客户端。
func dumpRejectedBody(body []byte, model string) {
	name := fmt.Sprintf("/tmp/bps-reject-%d-%s.json", time.Now().UnixMilli(), strings.NewReplacer("/", "_", " ", "_").Replace(model))
	if err := os.WriteFile(name, body, 0o600); err == nil {
		log.Printf("bps: upstream rejected request body -> %s (model=%q, %d bytes)", name, model, len(body))
	}
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
	if os.Getenv("WEB2API_DUMP_OUT") != "" {
		os.WriteFile("/tmp/bps-out.json", body, 0o600)
		log.Printf("bps: outbound body -> /tmp/bps-out.json (%d bytes)", len(body))
	}
	return c.httpClientRef().Do(req)
}

// ListModels 拉上游模型目录（GET /responses/models）：返回该账号实际可用的
// 模型 + effort 档位；静态表只是拿不到账号时的兜底。
func (c *httpClient) ListModels(ctx context.Context) ([]adapter.ModelInfo, error) {
	sec, err := c.credential()
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.origin()+PathResponses+"/models", nil)
	if err != nil {
		return nil, err
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
		return nil, fmt.Errorf("bps: models: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<10))
		return nil, fmt.Errorf("bps: models: status %d: %s", resp.StatusCode, truncate(strings.TrimSpace(string(raw)), 200))
	}
	var body struct {
		Models []struct {
			ID            string `json:"id"`
			Label         string `json:"label"`
			DefaultEffort string `json:"default_effort"`
			Efforts       []struct {
				Value string `json:"value"`
			} `json:"efforts"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("bps: models: decode: %w", err)
	}
	// 上游响应不带别名表——按 ID 把静态表的别名合进 live 条目，
	// 否则 codex/sol/default 这类短名会被入口校验判成未知模型。
	staticAlias := map[string][]string{}
	for _, sm := range staticModels() {
		staticAlias[sm.ID] = sm.Aliases
	}
	out := make([]adapter.ModelInfo, 0, len(body.Models))
	for _, m := range body.Models {
		mi := adapter.ModelInfo{
			ID:                m.ID,
			ServerModelName:   m.ID,
			DisplayName:       firstNonEmpty(m.Label, m.ID),
			SupportsThinking:  len(m.Efforts) > 1, // 单档 none = 无可调推理
			SupportsImages:    true,
			ContextTokenLimit: DefaultContextLimit,
			ThinkingLevel:     m.DefaultEffort,
		}
		for _, e := range m.Efforts {
			if v := strings.TrimSpace(e.Value); v != "" && !slices.Contains(mi.Efforts, v) {
				mi.Efforts = append(mi.Efforts, v)
			}
		}
		// 兜底：efforts 列表缺 default_effort 时补上，保证白名单至少含默认档。
		if def := strings.TrimSpace(m.DefaultEffort); def != "" && !slices.Contains(mi.Efforts, def) {
			mi.Efforts = append(mi.Efforts, def)
		}
		mi.Aliases = staticAlias[mi.ID]
		out = append(out, mi)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("bps: models: empty catalog")
	}
	return out, nil
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

// whamInt 同 whamFloat：窗口秒数在部分账号上也是字符串形态。
type whamInt int64

// UnmarshalJSON 解析 number 或 string 为 int64；null/空串/非标量视为缺省。
func (i *whamInt) UnmarshalJSON(b []byte) error {
	s := strings.TrimSpace(string(b))
	if s == "" || s == "null" {
		return nil
	}
	if s[0] == '"' {
		var str string
		if err := json.Unmarshal(b, &str); err != nil {
			return err
		}
		s = strings.TrimSpace(str)
		if s == "" {
			return nil
		}
	} else if !(s[0] >= '0' && s[0] <= '9' || s[0] == '-' || s[0] == '+') {
		return nil
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		*i = whamInt(int64(f))
		return nil
	}
	return nil
}

// whamWindow 是 rate_limit 下的单个限流窗口（5h 主窗 / 周窗）。
type whamWindow struct {
	UsedPercent       whamFloat `json:"used_percent"`
	LimitWindowSecs   whamInt   `json:"limit_window_seconds"`
	ResetAfterSeconds whamInt   `json:"reset_after_seconds"`
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
	var w whamPayload
	if err := json.NewDecoder(resp.Body).Decode(&w); err != nil {
		return fmt.Errorf("wham/usage: decode: %w", err)
	}
	mapWhamUsage(&w, snap)
	return nil
}

// whamFloat 容忍上游数字/字符串两种形态：credits.balance 在部分账号上
// 返回 "12.34" 字符串（ted_ramirez 实测），窗口制账号返回裸数字；
// approx_*_messages 是数组 [a,b] —— 非标量一律静默跳过，不拖死整次解码。
type whamFloat float64

// UnmarshalJSON 解析 number 或 string 为 float64；null/空串/对象/数组视为缺省。
func (f *whamFloat) UnmarshalJSON(b []byte) error {
	s := strings.TrimSpace(string(b))
	if s == "" || s == "null" {
		return nil
	}
	if s[0] == '"' {
		var str string
		if err := json.Unmarshal(b, &str); err != nil {
			return err
		}
		s = strings.TrimSpace(str)
		if s == "" {
			return nil
		}
	} else if !(s[0] >= '0' && s[0] <= '9' || s[0] == '-' || s[0] == '+' || s[0] == '.') {
		return nil // 数组/对象/true 等：字段不可用而非报错
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return nil // 不可解析的字符串也跳过
	}
	*f = whamFloat(v)
	return nil
}

// ptr 返回 float64 指针（值非零时才算"有数据"交给消费端判 nil）。
func (f *whamFloat) ptr() *float64 {
	if f == nil {
		return nil
	}
	v := float64(*f)
	return &v
}

// whamPayload 是 wham/usage 的响应体。usage_based 套餐 rate_limit 为 null——
// 额度按 credits/spend_control 判定，没有窗口百分比。
type whamPayload struct {
	Email     string `json:"email"`
	UserID    string `json:"user_id"`
	PlanType  string `json:"plan_type"`
	RateLimit *struct {
		Allowed         bool        `json:"allowed"`
		LimitReached    bool        `json:"limit_reached"`
		PrimaryWindow   *whamWindow `json:"primary_window"`
		SecondaryWindow *whamWindow `json:"secondary_window"`
	} `json:"rate_limit"`
	Credits struct {
		HasCredits          bool       `json:"has_credits"`
		Unlimited           bool       `json:"unlimited"`
		OverageLimitReached bool       `json:"overage_limit_reached"`
		Balance             *whamFloat `json:"balance"`
		ApproxLocalMessages *whamFloat `json:"approx_local_messages"`
		ApproxCloudMessages *whamFloat `json:"approx_cloud_messages"`
	} `json:"credits"`
	SpendControl struct {
		Reached         bool       `json:"reached"`
		IndividualLimit *whamFloat `json:"individual_limit"`
	} `json:"spend_control"`
	RateLimitReachedType whamReachedType `json:"rate_limit_reached_type"`
}

// whamReachedType 兼容 rate_limit_reached_type 的三种形态：
// null（ted_ramirez）、{"type":"..."} 对象（pemny）、"..." 裸字符串。
type whamReachedType struct {
	Type string
}

// UnmarshalJSON 取对象 .type 或字符串本身；null/其它形态置空。
func (t *whamReachedType) UnmarshalJSON(b []byte) error {
	s := strings.TrimSpace(string(b))
	if s == "" || s == "null" {
		return nil
	}
	if s[0] == '"' {
		return json.Unmarshal(b, &t.Type)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil
	}
	t.Type, _ = m["type"].(string)
	return nil
}

// mapWhamUsage 把 wham 响应映射进用量快照（纯函数，便于测试）。
func mapWhamUsage(w *whamPayload, snap *adapter.UsageSnapshot) {
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
	hasWindow := false
	if rl := w.RateLimit; rl != nil {
		if pw := rl.PrimaryWindow; pw != nil {
			v := float64(pw.UsedPercent)
			snap.AutoPercentUsed = &v
			peak = v
			hasWindow = true
		}
		if sw := rl.SecondaryWindow; sw != nil {
			v := float64(sw.UsedPercent)
			snap.APIPercentUsed = &v
			if v > peak {
				peak = v
			}
			if sw.ResetAfterSeconds > 0 {
				snap.BillingCycleEnd = time.Now().Unix() + int64(sw.ResetAfterSeconds)
			}
			hasWindow = true
		}
		if hasWindow {
			if rl.LimitReached && peak < quotaHardLimit {
				peak = quotaHardLimit
			}
			snap.TotalPercentUsed = &peak
		}
	}
	// usage_based/计费制账号：rate_limit 为 null，额度看 credits/spend_control。
	// 欠费（workspace_member_credits_depleted 等）→ 顶满总用量，让 QuotaDisableReason 熔断。
	depleted := w.SpendControl.Reached || w.Credits.OverageLimitReached || w.RateLimitReachedType.Type != "" ||
		(w.RateLimit != nil && w.RateLimit.LimitReached && !hasWindow)
	if !hasWindow && depleted {
		v := quotaHardLimit
		snap.TotalPercentUsed = &v
	}
	if t := w.RateLimitReachedType.Type; t != "" {
		snap.OnDemandLimitType = t
	}
	// 消费上限/余额（credits.balance 单位是美分剩余额度）。
	if lim := w.SpendControl.IndividualLimit; lim != nil && *lim > 0 {
		snap.PlanLimitCents = lim.ptr()
		if bal := w.Credits.Balance; bal != nil {
			used := float64(*lim) - float64(*bal)
			if used < 0 {
				used = 0
			}
			snap.PlanUsedCents = &used
		}
	}
	if w.Credits.Unlimited {
		snap.Unlimited = true
	}
	if w.Credits.HasCredits {
		on := true
		snap.OnDemandEnabled = &on
	}
}

// quotaHardLimit 触发限流熔断的百分比（与 adapter.quotaFullPercent 对齐）。
const quotaHardLimit = 100.0
