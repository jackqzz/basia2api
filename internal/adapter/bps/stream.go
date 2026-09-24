package bps

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
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
func consumeSSE(r io.Reader, emit func(adapter.Event) bool) error {
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
		return handleFrame(eventName, payload, emit)
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
func handleFrame(eventName, payload string, emit func(adapter.Event) bool) error {
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
		if json.Unmarshal(f.Item, &item) == nil && item.Type == "function_call" {
			if !emit(adapter.Event{ToolCall: &adapter.StreamedToolCall{
				Tool:       "function",
				ToolCallID: item.CallID,
				Name:       item.Name,
				RawArgs:    item.Arguments,
				Kind:       "function",
			}}) {
				return errClientGone
			}
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
