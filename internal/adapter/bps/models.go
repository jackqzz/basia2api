package bps

import (
	"strings"

	"bps-2api/internal/adapter"
)

// ============================================================
// 模型目录：首选上游实时目录（GET /responses/models，按账号 entitlement 返回），
// 静态表只是拿不到账号时的兜底。客户端模型名经 resolveModel 映射成上游名；
// model_selection:"explicit" 生效后上游按名字真实路由，名字必须在上游目录里。
// ============================================================

// DefaultModel 是默认模型（也是上游实际使用的模型）。
const DefaultModel = "gpt-5.6-sol"

// DefaultContextLimit 是账号上下文档位的兜底值（gpt-6-astra 实测 272000）。
const DefaultContextLimit = 272000

// staticModels 返回静态目录。SupportsThinking 全开：档位由请求 effort 控制。
func staticModels() []*adapter.ModelInfo {
	return []*adapter.ModelInfo{
		{
			ID:                "gpt-5.6-sol",
			ServerModelName:   "gpt-5.6-sol",
			DisplayName:       "5.6 Sol",
			Aliases:           []string{"sol", "default", "gpt-5.6", "gpt-5-codex", "gpt-5.6-codex", "codex"},
			SupportsThinking:  true,
			SupportsImages:    true,
			ContextTokenLimit: DefaultContextLimit,
			Efforts:           []string{"none", "low", "medium", "high", "xhigh"},
		},
		{
			ID:                "gpt-6-astra",
			ServerModelName:   "gpt-6-astra",
			DisplayName:       "6 Astra",
			Aliases:           []string{"astra", "gpt-6"},
			SupportsThinking:  true,
			SupportsImages:    true,
			ContextTokenLimit: DefaultContextLimit,
			Efforts:           []string{"low", "medium", "high", "xhigh"},
		},
		{
			ID:                "gpt-5.6-terra",
			ServerModelName:   "gpt-5.6-terra",
			DisplayName:       "5.6 Terra",
			Aliases:           []string{"terra"},
			SupportsThinking:  true,
			SupportsImages:    true,
			ContextTokenLimit: DefaultContextLimit,
			Efforts:           []string{"none", "low", "medium", "high", "xhigh"},
		},
		{
			ID:                "gpt-5.6-luna",
			ServerModelName:   "gpt-5.6-luna",
			DisplayName:       "5.6 Luna",
			Aliases:           []string{"luna"},
			SupportsThinking:  true,
			SupportsImages:    true,
			ContextTokenLimit: DefaultContextLimit,
			Efforts:           []string{"none", "low", "medium", "high", "xhigh"},
		},
	}
}

// resolveModel 把客户端模型名换成目录里的服务端模型名；未命中回退默认模型。
// 入口层（api.validateUpstreamModel）已先按目录拦掉未知模型/档位——正常推理流量
// 到这里必然命中；此兜底只覆盖绕过入口校验的内部调用。
// model_selection:"explicit" 下上游按名字真实路由——不在目录里的名字会 403，
// 别名靠这里吸收；彻底不认识的回退默认（比 403 友好）。
func resolveModel(id string) string {
	m := strings.TrimSpace(id)
	if m == "" {
		return DefaultModel
	}
	base, _, _ := adapter.ParseModelID(m)
	for _, mi := range staticModels() {
		if mi.ID == m || mi.ID == base {
			return firstNonEmpty(mi.ServerModelName, mi.ID)
		}
		for _, a := range mi.Aliases {
			if a == m || a == base {
				return firstNonEmpty(mi.ServerModelName, mi.ID)
			}
		}
	}
	return DefaultModel
}

// reasoningEffort 把内核档位映射成上游 reasoning.effort。
// max/ultra 归一到 xhigh；none 透传（上游部分模型支持显式关思考）。
// 空档位返回空串（请求体不写 reasoning 字段，使用上游默认）。
func reasoningEffort(nr *adapter.NativeRequest) string {
	effort := strings.TrimSpace(nr.Thinking)
	if effort == "" {
		_, effort = splitModelSuffix(strings.TrimSpace(nr.Model))
	}
	return adapter.NormalizeEffort(effort)
}

// splitModelSuffix 从模型名里拆出档位后缀（-low/-medium/-high/-xhigh/-max）。
func splitModelSuffix(model string) (string, string) {
	if base, level, _ := adapter.ParseModelID(model); level != "" {
		return base, level
	}
	return model, ""
}

func firstNonEmpty(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}
