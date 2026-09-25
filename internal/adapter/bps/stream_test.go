package bps

import (
	"encoding/json"
	"strings"
	"testing"

	"bps-2api/internal/adapter"
)

const sampleSSE = `event: response.created
data: {"type":"response.created","response":{"id":"resp_1","model":"gpt-5.6-sol","status":"in_progress"}}

event: response.output_text.delta
data: {"type":"response.output_text.delta","delta":"Hel"}

event: response.output_text.delta
data: {"type":"response.output_text.delta","delta":"lo"}

event: response.output_item.done
data: {"type":"response.output_item.done","item":{"type":"function_call","call_id":"call_9","name":"Read","arguments":"{\"f\":\"a\"}"}}

event: response.completed
data: {"type":"response.completed","response":{"id":"resp_1","model":"gpt-5.6-sol","status":"completed","usage":{"input_tokens":17,"output_tokens":3}}}

`

func TestConsumeSSEEvents(t *testing.T) {
	var events []adapter.Event
	err := consumeSSE(strings.NewReader(sampleSSE), func(ev adapter.Event) bool {
		events = append(events, ev)
		return true
	}, []adapter.ToolDef{{Name: "Read", Parameters: json.RawMessage(`{"type":"object"}`)}})
	if err != nil {
		t.Fatalf("consumeSSE: %v", err)
	}
	var text string
	var toolCalled bool
	var ctxSeen bool
	for _, ev := range events {
		text += ev.Text
		if ev.ToolCall != nil {
			toolCalled = true
			if ev.ToolCall.ToolCallID != "call_9" || ev.ToolCall.Name != "Read" || ev.ToolCall.RawArgs != `{"f":"a"}` {
				t.Fatalf("tool call mismatch: %+v", ev.ToolCall)
			}
		}
		if ev.ContextWindow != nil {
			ctxSeen = true
			if ev.ContextWindow.TokensUsed != 17 {
				t.Fatalf("context window mismatch: %+v", ev.ContextWindow)
			}
		}
	}
	if text != "Hello" {
		t.Fatalf("text=%q want Hello", text)
	}
	if !toolCalled || !ctxSeen {
		t.Fatalf("tool/context events missing: %+v", events)
	}
}

func TestConsumeSSEFailedEvent(t *testing.T) {
	sse := `event: response.failed
data: {"type":"response.failed","response":{"status":"failed","error":{"code":"server_error","message":"boom"}}}

`
	err := consumeSSE(strings.NewReader(sse), func(adapter.Event) bool { return true }, nil)
	if err == nil {
		t.Fatalf("expected error")
	}
	up, ok := err.(*upstreamError)
	if !ok || up.StatusCode() != 500 || !strings.Contains(up.Error(), "boom") {
		t.Fatalf("unexpected err: %#v", err)
	}
}

func TestConsumeSSEClientGone(t *testing.T) {
	err := consumeSSE(strings.NewReader(sampleSSE), func(adapter.Event) bool { return false }, nil)
	if err != errClientGone {
		t.Fatalf("want errClientGone, got %v", err)
	}
}

func TestConsumeSSEIgnoresBadFrames(t *testing.T) {
	sse := "data: {bad json}\n\nevent: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\n"
	var text string
	if err := consumeSSE(strings.NewReader(sse), func(ev adapter.Event) bool {
		text += ev.Text
		return true
	}, nil); err != nil {
		t.Fatalf("consumeSSE: %v", err)
	}
	if text != "ok" {
		t.Fatalf("text=%q", text)
	}
}
