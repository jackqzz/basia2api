package bps

import (
	"strings"

	"bps-2api/internal/adapter"
)

// ============================================================
// 模型目录（静态）。上游忽略请求里的 model，实测恒定使用 gpt-5.6-sol；
// 目录只负责把客户端可读的模型名/别名映射成上游名，并暴露思考档位后缀。
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
		},
		{
			ID:                "gpt-6-astra",
			ServerModelName:   "gpt-6-astra",
			DisplayName:       "6 Astra",
			Aliases:           []string{"astra", "gpt-6"},
			SupportsThinking:  true,
			SupportsImages:    true,
			ContextTokenLimit: DefaultContextLimit,
		},
		{
			ID:                "gpt-5.6-terra",
			ServerModelName:   "gpt-5.6-terra",
			DisplayName:       "5.6 Terra",
			Aliases:           []string{"terra"},
			SupportsThinking:  true,
			SupportsImages:    true,
			ContextTokenLimit: DefaultContextLimit,
		},
		{
			ID:                "gpt-5.6-luna",
			ServerModelName:   "gpt-5.6-luna",
			DisplayName:       "5.6 Luna",
			Aliases:           []string{"luna"},
			SupportsThinking:  true,
			SupportsImages:    true,
			ContextTokenLimit: DefaultContextLimit,
		},
		{
			ID:                "gpt-5.5",
			ServerModelName:   "gpt-5.5",
			DisplayName:       "5.5",
			Aliases:           []string{"gpt-5.5-codex"},
			SupportsThinking:  true,
			SupportsImages:    true,
			ContextTokenLimit: DefaultContextLimit,
		},
	}
}

// resolveModel 把客户端模型名换成目录里的服务端模型名；未命中回退默认模型。
// 上游会忽略该字段，但仍保持语义：客户端传什么系族，请求体里就写什么。
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
// 上游只接受 low/medium/high/xhigh；max 会被 422 拒绝，这里归一到 xhigh。
// 空档位返回空串（请求体不写 reasoning 字段，使用上游默认）。
func reasoningEffort(nr *adapter.NativeRequest) string {
	effort := strings.TrimSpace(nr.Thinking)
	if effort == "" {
		_, effort = splitModelSuffix(strings.TrimSpace(nr.Model))
	}
	switch strings.ToLower(effort) {
	case "minimal", "low":
		return "low"
	case "medium":
		return "medium"
	case "high":
		return "high"
	case "xhigh", "max", "ultra":
		return "xhigh"
	default:
		return ""
	}
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
