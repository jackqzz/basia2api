package bps

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"strings"

	"bps-2api/internal/adapter"
)

// base64Std 是 data URL 编码用的标准 base64。
var base64Std = base64.StdEncoding

// ============================================================
// Responses SSE → adapter.Event。
// 上游事件（实测）：
//   response.created / response.in_progress
//   response.output_text.delta / .done
//   response.reasoning_summary_text.delta（推理摘要，若开启）
//   response.output_item.done（function_call）
//   response.completed（含 usage）
//   response.failed / error
// ============================================================

// sseFrame 是 data 行 JSON 的公共字段。
type sseFrame struct {
	Type     string          `json:"type"`
	Delta    string          `json:"delta"`
	Item     json.RawMessage `json:"item"`
	Response json.RawMessage `json:"response"`
	Error    *struct {
		Type    string `json:"type"`
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
	Message string `json:"message"`
}

// responseUsage 是 response.completed 里的用量。
type responseUsage struct {
	InputTokens  int32 `json:"input_tokens"`
	OutputTokens int32 `json:"output_tokens"`
}

// responseObject 是 response.completed / response.failed 里的 response 对象。
type responseObject struct {
	Model  string         `json:"model"`
	Status string         `json:"status"`
	Usage  *responseUsage `json:"usage"`
	Error  *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// outputItem 是 response.output_item.done 里的 item。
type outputItem struct {
	Type      string `json:"type"`
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// consumeSSE 解析上游 SSE 流并映射成事件；emit 返回 false 表示客户端已断开。
// tools 是本请求声明的客户端工具集：run_officejs 传输调用在这里被还原成
// 客户端工具调用（call_id 直通），未声明的原生调用（office 工具）不下发。
func consumeSSE(r io.Reader, emit func(adapter.Event) bool, tools []adapter.ToolDef) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 8<<20)
	var (
		eventName string
		dataBuf   strings.Builder
		done      bool
	)
	dispatch := func() error {
		payload := strings.TrimSpace(dataBuf.String())
		eventName = ""
		dataBuf.Reset()
		if payload == "" || payload == "[DONE]" {
			return nil
		}
		return handleFrame(eventName, payload, emit, tools)
	}
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		switch {
		case line == "":
			if err := dispatch(); err != nil {
				return err
			}
			if done {
				return nil
			}
		case strings.HasPrefix(line, "event:"):
			eventName = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			if dataBuf.Len() > 0 {
				dataBuf.WriteByte('\n')
			}
			dataBuf.WriteString(strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		default:
			// 注释帧（: ping）等忽略
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	return dispatch()
}

// handleFrame 处理一个 SSE 帧；返回错误表示流内失败（交由内核换号）。
func handleFrame(eventName, payload string, emit func(adapter.Event) bool, tools []adapter.ToolDef) error {
	var f sseFrame
	if err := json.Unmarshal([]byte(payload), &f); err != nil {
		// 坏帧不致命：忽略继续读流。
		return nil
	}
	typ := f.Type
	if typ == "" {
		typ = eventName
	}
	switch typ {
	case "response.output_text.delta":
		if f.Delta != "" && !emit(adapter.Event{Text: f.Delta}) {
			return errClientGone
		}
	case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
		if f.Delta != "" && !emit(adapter.Event{Thinking: &adapter.Thinking{Text: f.Delta}}) {
			return errClientGone
		}
	case "response.output_item.done":
		var item outputItem
		if json.Unmarshal(f.Item, &item) != nil || item.Type != "function_call" {
			break
		}
		call := mapNativeToolCall(item, tools)
		if call == nil {
			log.Printf("bps: dropped native function_call name=%q (not a declared client tool)", item.Name)
			break
		}
		if !emit(adapter.Event{ToolCall: call}) {
			return errClientGone
		}
	case "response.completed":
		var obj responseObject
		_ = json.Unmarshal(f.Response, &obj)
		ev := adapter.Event{Ended: true}
		if obj.Usage != nil {
			ev.InputTokens = obj.Usage.InputTokens
			ev.OutputTokens = obj.Usage.OutputTokens
			ev.ContextWindow = &adapter.ContextWindowStatus{TokensUsed: int64(obj.Usage.InputTokens)}
		}
		emit(ev)
	case "response.failed", "response.incomplete":
		var obj responseObject
		_ = json.Unmarshal(f.Response, &obj)
		msg := "response not completed"
		if obj.Error != nil && strings.TrimSpace(obj.Error.Message) != "" {
			msg = obj.Error.Code + ": " + obj.Error.Message
		}
		return &upstreamError{Status: 500, Msg: truncate(msg, 400)}
	case "error":
		msg := strings.TrimSpace(f.Message)
		if f.Error != nil {
			if s := strings.TrimSpace(f.Error.Message); s != "" {
				msg = s
			}
			if c := strings.TrimSpace(f.Error.Code); c != "" {
				msg = c + ": " + msg
			}
		}
		return &upstreamError{Status: 500, Msg: "stream error: " + truncate(msg, 400)}
	}
	return nil
}

// errClientGone 是 emit 返回 false 时的哨兵错误（客户端断开，内核不换号）。
var errClientGone = fmt.Errorf("bps: client disconnected")

// mapNativeToolCall 把上游 function_call 还原成客户端工具调用。
//   - run_officejs → 解 code envelope，取内层 name/arguments（call_id 直通）
//   - 原生 update_plan → 参数归一到客户端 schema
//   - 直呼客户端工具名 → 校验 schema 后放行
//   - 其它（office 原生工具/畸形 envelope/未声明名）→ 返回 nil 不下发
func mapNativeToolCall(item outputItem, tools []adapter.ToolDef) *adapter.StreamedToolCall {
	if len(tools) == 0 {
		log.Printf("bps: drop %q: no client tools declared", item.Name)
		return nil
	}
	specs := make(map[string]json.RawMessage, len(tools))
	customSet := make(map[string]bool, len(tools))
	for _, t := range tools {
		if n := strings.TrimSpace(t.Name); n != "" {
			specs[n] = t.Parameters
			customSet[n] = t.Custom
		}
	}

	name, rawArgs := item.Name, item.Arguments
	if isTransportName(name) {
		env := transportEnvelope(name, rawArgs)
		if env == nil {
			log.Printf("bps: drop run_officejs: malformed envelope")
			return nil
		}
		inner, _ := env["name"].(string)
		if inner == "" {
			log.Printf("bps: drop run_officejs: envelope missing inner name")
			return nil
		}
		name = inner
		switch a := env["arguments"].(type) {
		case string:
			rawArgs = a
		case map[string]any:
			if b, err := json.Marshal(a); err == nil {
				rawArgs = string(b)
			}
		default:
			// custom 形状 {"name":..., "input":...}: 包回 {input:...} 参数形态
			if in, ok := env["input"].(string); ok {
				if b, err := json.Marshal(map[string]any{"input": in}); err == nil {
					rawArgs = string(b)
				}
			}
		}
	}
	if _, ok := specs[name]; !ok {
		// 命名空间兜底：模型可能丢前缀（catalog 里是 functions.exec，
		// 模型只写 exec）。裸名唯一命中一个声明工具时用它。
		matched := ""
		for k := range specs {
			if strings.HasSuffix(k, "."+name) {
				if matched != "" {
					matched = "" // 歧义：多个命名空间同名，放弃
					break
				}
				matched = k
			}
		}
		if matched == "" {
			log.Printf("bps: drop %q: inner tool not in client catalog", name)
			return nil
		}
		name = matched
	}
	var parsed map[string]any
	if json.Unmarshal([]byte(rawArgs), &parsed) != nil || parsed == nil {
		log.Printf("bps: drop %q: arguments not a JSON object", name)
		return nil
	}
	if customSet[name] {
		// freeform 工具：模型可能给 arguments 对象而非 {input}，
		// 统一包成 {"input":...}——API 层 customInput() 会取 input 或原样透传。
		if _, has := parsed["input"]; !has {
			parsed = map[string]any{"input": rawArgs}
		}
	}
	if name == "update_plan" {
		parsed = normalizeNativeUpdatePlanArgs(parsed)
	}
	if !customSet[name] && len(specs[name]) > 0 && json.Valid(specs[name]) {
		var schema any
		if json.Unmarshal(specs[name], &schema) == nil && !valueMatchesSchema(parsed, schema) {
			log.Printf("bps: drop %q: arguments fail declared schema", name)
			return nil
		}
	}
	outArgs, _ := json.Marshal(parsed)
	return &adapter.StreamedToolCall{
		Tool:       "function",
		ToolCallID: item.CallID,
		Name:       name,
		RawArgs:    string(outArgs),
		Kind:       "function",
	}
}
