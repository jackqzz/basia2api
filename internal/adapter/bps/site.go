// Package bps 是 bps.openai.com（ChatGPT Excel 插件 agent 端点）的 2API 适配器。
//
// 上游是标准 SSE 的 Responses API：POST /basispoints/api/responses。
// 与 prism 的差异：认证用 ChatGPT access token（Bearer + account id 头），
// 请求体必须带 metadata.task_id / metadata.turn_id；instructions 由上游注入，客户端无法覆盖。
package bps

import (
	"bps-2api/internal/adapter"
)

// DisplayName 是站点标识，出现在日志 / Owner 字段。
const DisplayName = "bps"

// Adapter 是 bps 站点实现。
type Adapter struct {
	apiBase       string
	websiteURL    string
	clientVersion string
	clientType    string
	mock          bool
}

// Config 是启动期站点设置（来自 env / flag）。
type Config struct {
	APIBaseURL    string
	WebsiteURL    string
	ClientVersion string
	ClientType    string
	Mock          bool
}

// New 构造站点适配器，cmd/server 负责 adapter.Bind。
func New(cfg Config) *Adapter {
	ct := cfg.ClientType
	if ct == "" {
		ct = "chatgpt"
	}
	base := cfg.APIBaseURL
	if base == "" {
		base = DefaultBaseURL
	}
	return &Adapter{
		apiBase:       base,
		websiteURL:    cfg.WebsiteURL,
		clientVersion: cfg.ClientVersion,
		clientType:    ct,
		mock:          cfg.Mock,
	}
}

// Name 返回站点标识。
func (a *Adapter) Name() string { return DisplayName }

// NewClient 为单个账号构造上游客户端。
func (a *Adapter) NewClient(cfg adapter.ClientConfig) adapter.Client {
	if a.mock {
		return newMockClient()
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = a.apiBase
	}
	if cfg.ClientVersion == "" {
		cfg.ClientVersion = a.clientVersion
	}
	if cfg.ClientType == "" {
		cfg.ClientType = a.clientType
	}
	return newHTTPClient(cfg)
}

// NewCatalog 返回静态模型目录（上游没有 /models 接口）。
func (a *Adapter) NewCatalog() adapter.Catalog {
	return adapter.NewStaticCatalog(staticModels())
}

// Auth 返回站点认证实现（导入 + OAuth 刷新）。
func (a *Adapter) Auth() adapter.Auth { return a }

// MapChat 把内核请求转成站点原生请求（生成 task/turn、解析模型与档位）。
func (a *Adapter) MapChat(req adapter.ChatRequest) (*adapter.NativeRequest, error) {
	return mapChat(req)
}
