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

// mapChat 组装站点原生请求；task/turn 由消息内容派生（重试/多轮稳定）。
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
		Extra:        map[string]any{},
	}
	return nr, nil
}

// buildRequestBody 生成 POST /responses 的 JSON body。
// 上游白名单契约：客户端 tools 一律 422，改走 run_officejs 传输层（见 toolrelay.go）。
// model_selection=explicit 让请求模型真正生效（否则上游静默兜底默认模型）。
func buildRequestBody(nr *adapter.NativeRequest) map[string]any {
	conversation := conversationFingerprint(nr.Messages, nr.SystemPrompt)
	turnFp, iteration := turnState(nr.Messages)
	metadata := map[string]any{
		"task_id":         extraString(nr, "task_id"),
		"turn_id":         extraString(nr, "turn_id"),
		"agent_iteration": iteration,
	}
	if metadata["task_id"] == "" {
		metadata["task_id"] = uuid5("bps-2api/gpt-excel/" + conversation)
	}
	if metadata["turn_id"] == "" {
		metadata["turn_id"] = uuid5("bps-2api/gpt-excel/" + conversation + "/turn/" + turnFp)
	}
	body := map[string]any{
		"model":           resolveModel(nr.Model),
		"model_selection": "explicit",
		"input":           buildInput(nr),
		"stream":          true,
		// 上游是 Excel 插件 agent，store 固定 false（与官方客户端一致）。
		"store":    false,
		"metadata": metadata,
	}
	if effort := reasoningEffort(nr); effort != "" {
		body["reasoning"] = map[string]any{"effort": effort}
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
// system 合并成一条放最前；随后是工具目录/提醒 developer 消息（run_officejs
// 传输层协议，稳定前缀利于上游 prompt cache）；assistant 文本 + function_call
// 历史改写成 run_officejs envelope；tool 结果 → function_call_output。
func buildInput(nr *adapter.NativeRequest) []any {
	items := make([]any, 0, len(nr.Messages)+4)
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
	// 工具传输协议：有客户端工具时注入目录+提醒；无工具时注入禁用 office 工具指令。
	items = append(items, textMessageItem("developer", clientToolInstructions(nr.Tools)))
	if r := clientToolReminder(nr.Tools); r != "" {
		items = append(items, textMessageItem("developer", r))
	}
	for _, m := range nr.Messages {
		switch {
		case isSystemRole(m.Role):
			// 已合并到最前
		default:
			items = append(items, messageInputItems(m)...)
		}
	}
	// 畸形传输回灌：模型写了坏 envelope，重放其 function_call + 纠正回执，
	// 让它重出一轮合法调用（见 toolrelay.go TransportRetryItems）。
	if s := extraString(nr, "bps_transport_items"); s != "" {
		var extra []any
		if json.Unmarshal([]byte(s), &extra) == nil {
			items = append(items, extra...)
		}
	}
	if len(items) == 0 {
		items = append(items, textMessageItem("user", "(empty)"))
	}
	return items
}

// messageInputItems 把一条内核消息转成 1~N 个 input item。
// assistant 历史里的 function_call 需按 run_officejs envelope 重放（模型视角
// 里它调的是 run_officejs），update_plan 例外——它是上游原生工具，参数恢复成
// 原生 schema。
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
		output := m.Content
		if strings.TrimSpace(output) == "" {
			// 空输出在上游读作失败，会诱发重试；显式标记成功。
			output = "(tool call succeeded with no output)"
		}
		return []any{map[string]any{
			"type":    "function_call_output",
			"id":      functionItemID(id),
			"call_id": id,
			"output":  output,
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
		isImage := strings.HasPrefix(strings.ToLower(f.Mime), "image/") || f.URL != ""
		if isImage {
			// 上游 input_image 只认 http(s) URL 并由服务端代抓；data: base64 一律 422。
			// f.URL 的来源：客户端给的公网 URL 原样透传；本地图由 prepareImages
			// 先走 ChatGPT 文件通道上传换 oaiusercontent SAS 地址（无需公网网关）。
			// 都没成 → 公网基址暂存（若配置）→ 占位文本兜底，整轮请求不被图拖死。
			url := strings.TrimSpace(f.URL)
			if url == "" && len(f.Data) > 0 {
				url = stashImage(f.Data, firstNonEmpty(f.Mime, "image/png"))
			}
			if url != "" {
				parts = append(parts, map[string]any{"type": "input_image", "image_url": url})
				continue
			}
			name := strings.TrimSpace(f.Name)
			if name == "" {
				name = "image"
			}
			parts = append(parts, map[string]any{"type": "input_text", "text": fmt.Sprintf(
				"[image %s (%s, %d bytes): upstream does not accept image input; content omitted]", name, f.Mime, len(f.Data))})
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
// 客户端工具调用在模型视角是 run_officejs 传输调用（见 toolrelay.go），
// 历史回放必须还原成那个形态，否则上游看到的 call/结果对不上；
// update_plan 是上游原生工具，参数恢复成原生 schema。
func functionCallItem(tc adapter.ToolCall) map[string]any {
	id := strings.TrimSpace(tc.ID)
	if id == "" {
		id = "call_" + newID()
	}
	args := strings.TrimSpace(tc.Arguments)
	if args == "" {
		args = "{}"
	}
	if tc.Name == "update_plan" {
		return map[string]any{
			"type":      "function_call",
			"id":        functionItemID(id),
			"call_id":   id,
			"name":      "update_plan",
			"arguments": restoreNativeUpdatePlanArgs(args),
		}
	}
	return fallbackTransportCall(map[string]any{
		"type": "function_call", "call_id": id,
		"name": tc.Name, "arguments": args,
	})
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
