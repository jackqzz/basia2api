package bps

import (
	"encoding/json"
	"fmt"
	"strings"

	"bps-2api/internal/adapter"
)

// ============================================================
// 内核请求 → bps Responses 请求体。
// 关键点：上游要求 metadata.task_id / metadata.turn_id（缺失 422），
// instructions 由上游注入，客户端 system 消息作为 input item 传递。
// ============================================================

// mapChat 组装站点原生请求；task/turn 在这一次逻辑请求内固定（重试复用）。
func mapChat(req adapter.ChatRequest) (*adapter.NativeRequest, error) {
	nr := &adapter.NativeRequest{
		Model:        req.Model,
		Messages:     req.Messages,
		Tools:        req.Tools,
		Stream:       req.Stream,
		Temperature:  req.Temperature,
		MaxTokens:    req.MaxTokens,
		Thinking:     req.ReasoningEffort,
		SystemPrompt: req.SystemPrompt,
		Extra: map[string]any{
			"task_id": newID(),
			"turn_id": newID(),
		},
	}
	return nr, nil
}

// buildRequestBody 生成 POST /responses 的 JSON body。
func buildRequestBody(nr *adapter.NativeRequest) map[string]any {
	body := map[string]any{
		"model":  resolveModel(nr.Model),
		"input":  buildInput(nr),
		"stream": true,
		// 上游是 Excel 插件 agent，store 固定 false（与官方客户端一致）。
		"store": false,
		"metadata": map[string]any{
			"task_id": extraString(nr, "task_id"),
			"turn_id": extraString(nr, "turn_id"),
		},
	}
	if effort := reasoningEffort(nr); effort != "" {
		body["reasoning"] = map[string]any{"effort": effort}
	}
	if tools := buildTools(nr.Tools); len(tools) > 0 {
		body["tools"] = tools
	}
	return body
}

// extraString 读取 Extra 里的字符串字段。
func extraString(nr *adapter.NativeRequest, key string) string {
	if nr == nil || nr.Extra == nil {
		return ""
	}
	if s, ok := nr.Extra[key].(string); ok {
		return s
	}
	return ""
}

// buildInput 把消息列表摊成 Responses input items：
// system 合并成一条放最前；assistant 文本 + function_call；tool 结果 → function_call_output。
func buildInput(nr *adapter.NativeRequest) []any {
	items := make([]any, 0, len(nr.Messages)+2)
	sys := make([]string, 0, 2)
	if s := strings.TrimSpace(nr.SystemPrompt); s != "" {
		sys = append(sys, s)
	}
	for _, m := range nr.Messages {
		if isSystemRole(m.Role) {
			if t := strings.TrimSpace(m.Content); t != "" {
				sys = append(sys, t)
			}
		}
	}
	if len(sys) > 0 {
		items = append(items, textMessageItem("system", strings.Join(sys, "\n\n")))
	}
	for _, m := range nr.Messages {
		switch {
		case isSystemRole(m.Role):
			// 已合并到最前
		default:
			items = append(items, messageInputItems(m)...)
		}
	}
	if len(items) == 0 {
		items = append(items, textMessageItem("user", "(empty)"))
	}
	return items
}

// messageInputItems 把一条内核消息转成 1~N 个 input item。
func messageInputItems(m adapter.ChatMessage) []any {
	switch {
	case isAssistantRole(m.Role):
		out := []any{}
		if strings.TrimSpace(m.Content) != "" || len(m.Files) > 0 {
			out = append(out, assistantMessageItem(m))
		}
		for _, tc := range m.ToolCalls {
			out = append(out, functionCallItem(tc))
		}
		return out
	case isToolRole(m.Role):
		id := strings.TrimSpace(m.ToolCallID)
		if id == "" {
			id = "call_" + newID()
		}
		return []any{map[string]any{
			"type":    "function_call_output",
			"call_id": id,
			"output":  m.Content,
		}}
	default: // user / function
		return []any{userMessageItem(m)}
	}
}

// userMessageItem 构造 user 消息（文本 + 图片 data URL + 文本附件内联）。
func userMessageItem(m adapter.ChatMessage) map[string]any {
	parts := make([]any, 0, 4)
	if t := strings.TrimSpace(m.Content); t != "" {
		parts = append(parts, map[string]any{"type": "input_text", "text": t})
	}
	for _, f := range m.Files {
		if strings.HasPrefix(strings.ToLower(f.Mime), "image/") && len(f.Data) > 0 {
			parts = append(parts, map[string]any{
				"type":      "input_image",
				"image_url": fmt.Sprintf("data:%s;base64,%s", f.Mime, encodeBase64(f.Data)),
			})
			continue
		}
		marker := fileTextBlock(f)
		if marker != "" {
			parts = append(parts, map[string]any{"type": "input_text", "text": marker})
		}
	}
	if len(parts) == 0 {
		parts = append(parts, map[string]any{"type": "input_text", "text": "(empty)"})
	}
	return map[string]any{"type": "message", "role": "user", "content": parts}
}

// fileTextBlock 把附件折成文本块：文本类内联，二进制给标记。
func fileTextBlock(f adapter.File) string {
	name := strings.TrimSpace(f.Name)
	if name == "" {
		name = "attachment"
	}
	if t := strings.TrimSpace(f.Text); t != "" {
		const maxInline = 120 << 10
		if len(t) > maxInline {
			t = t[:maxInline] + "\n...[附件过长已截断]"
		}
		return fmt.Sprintf("[附件 %s]\n%s", name, t)
	}
	if len(f.Data) == 0 {
		return fmt.Sprintf("[附件 %s 无内容]", name)
	}
	return fmt.Sprintf("[附件 %s（%s，%d 字节，未内联）]", name, f.Mime, len(f.Data))
}

// assistantMessageItem 构造 assistant 历史消息（content 用 output_text）。
func assistantMessageItem(m adapter.ChatMessage) map[string]any {
	parts := make([]any, 0, 2)
	if t := strings.TrimSpace(m.Content); t != "" {
		parts = append(parts, map[string]any{"type": "output_text", "text": t})
	}
	if r := strings.TrimSpace(m.Reasoning); r != "" {
		parts = append(parts, map[string]any{"type": "output_text", "text": "[思考摘要]\n" + r})
	}
	if len(parts) == 0 {
		parts = append(parts, map[string]any{"type": "output_text", "text": ""})
	}
	return map[string]any{"type": "message", "role": "assistant", "content": parts}
}

// textMessageItem 构造纯文本消息 item（system 用）。
func textMessageItem(role, text string) map[string]any {
	return map[string]any{
		"type":    "message",
		"role":    role,
		"content": []any{map[string]any{"type": "input_text", "text": text}},
	}
}

// functionCallItem 构造历史里的 function_call item。
func functionCallItem(tc adapter.ToolCall) map[string]any {
	id := strings.TrimSpace(tc.ID)
	if id == "" {
		id = "call_" + newID()
	}
	args := strings.TrimSpace(tc.Arguments)
	if args == "" {
		args = "{}"
	}
	return map[string]any{
		"type":      "function_call",
		"call_id":   id,
		"name":      tc.Name,
		"arguments": args,
	}
}

// buildTools 把内核工具定义转成 Responses function tools。
func buildTools(tools []adapter.ToolDef) []any {
	out := make([]any, 0, len(tools))
	for _, t := range tools {
		name := strings.TrimSpace(t.Name)
		if name == "" {
			continue
		}
		params := json.RawMessage(t.Parameters)
		if len(params) == 0 || !json.Valid(params) {
			params = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		out = append(out, map[string]any{
			"type":        "function",
			"name":        name,
			"description": t.Description,
			"parameters":  params,
		})
	}
	return out
}

// encodeBase64 标准 base64（data URL 用）。
func encodeBase64(data []byte) string {
	return base64Std.EncodeToString(data)
}

// isSystemRole 判断 system/developer 角色。
func isSystemRole(role string) bool {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "system", "developer":
		return true
	}
	return false
}

// isAssistantRole 判断 assistant 角色。
func isAssistantRole(role string) bool {
	return strings.ToLower(strings.TrimSpace(role)) == "assistant"
}

// isToolRole 判断 tool/function 角色。
func isToolRole(role string) bool {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "tool", "function":
		return true
	}
	return false
}
