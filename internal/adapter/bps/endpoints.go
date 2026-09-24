package bps

// ============================================================
// 上游端点常量。base = VENDOR_API_BASE_URL，默认 https://bps.openai.com/basispoints/api。
// 认证不是 cookie，而是 ChatGPT access token（Bearer）+ chatgpt-account-id 头。
// ============================================================

const (
	// PathResponses 对话主端点（SSE）。
	PathResponses = "/responses"
	// PathCompact 历史压缩端点（本项目暂不使用，保留常量便于排障）。
	PathCompact = "/responses/compact"

	// DefaultBaseURL 上游 API 基址。
	DefaultBaseURL = "https://bps.openai.com/basispoints/api"
	// DefaultWebsiteURL 站点首页（后台展示用）。
	DefaultWebsiteURL = "https://bps.openai.com"

	// AuthBaseURL 是 ChatGPT OAuth 端点基址（token 刷新）。
	AuthBaseURL = "https://auth.openai.com"
	// OAuthTokenPath 刷新 access token 的路径。
	OAuthTokenPath = "/oauth/token"
	// OAuthClientID 是 ChatGPT Codex 客户端 ID（与官方 CLI 一致）。
	OAuthClientID = "app_EMoamEEZ73f0CkXaXp7hrann"
	// RefreshScope 是刷新时请求的 scope（不含 offline_access，与官方一致）。
	RefreshScope = "openid profile email"

	// defaultClientVersion 是兜底 Codex 客户端版本号（身份头用）。
	defaultClientVersion = "0.153.4"
)
