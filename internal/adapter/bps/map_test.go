package bps

import (
	"encoding/json"
	"strings"
	"testing"

	"bps-2api/internal/adapter"
)

func TestMapChatGeneratesTaskTurn(t *testing.T) {
	nr, err := mapChat(adapter.ChatRequest{Model: "gpt-5.6-sol", Messages: []adapter.ChatMessage{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Fatalf("mapChat: %v", err)
	}
	body := buildRequestBody(nr)
	meta, _ := body["metadata"].(map[string]any)
	if meta["task_id"] == "" || meta["turn_id"] == "" || meta["agent_iteration"] == "" {
		t.Fatalf("metadata task/turn/iter missing: %+v", meta)
	}
	if body["stream"] != true || body["store"] != false {
		t.Fatalf("stream/store wrong: %+v", body)
	}
	if body["model_selection"] != "explicit" {
		t.Fatalf("model_selection: %+v", body["model_selection"])
	}
	// 同一输入派生同一 task_id（确定性）；Extra 显式值优先
	body2 := buildRequestBody(nr)
	if body2["metadata"].(map[string]any)["task_id"] != meta["task_id"] {
		t.Fatal("task_id not deterministic")
	}
	nr2 := &adapter.NativeRequest{Messages: nr.Messages, Extra: map[string]any{"task_id": "t1", "turn_id": "u1"}}
	meta2 := buildRequestBody(nr2)["metadata"].(map[string]any)
	if meta2["task_id"] != "t1" || meta2["turn_id"] != "u1" {
		t.Fatalf("extra override lost: %+v", meta2)
	}
}

func TestBuildInputRolesAndTools(t *testing.T) {
	nr := &adapter.NativeRequest{
		Model:        "gpt-5.6-sol",
		SystemPrompt: "sys",
		Messages: []adapter.ChatMessage{
			{Role: "system", Content: "sys2"},
			{Role: "user", Content: "u"},
			{Role: "assistant", Content: "a", ToolCalls: []adapter.ToolCall{{ID: "call_1", Name: "Read", Arguments: `{"f":"x"}`}}},
			{Role: "tool", ToolCallID: "call_1", Content: "result"},
			{Role: "user", Content: "next"},
		},
		Extra: map[string]any{"task_id": "t", "turn_id": "u"},
	}
	raw, err := json.Marshal(buildInput(nr))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	text := string(raw)
	for _, want := range []string{`"role":"system"`, `"role":"user"`, `"role":"assistant"`, `"type":"function_call"`, `"type":"function_call_output"`, `"call_id":"call_1"`, "sys2"} {
		if !strings.Contains(text, want) {
			t.Fatalf("input missing %s: %s", want, text)
		}
	}
}

func TestBuildRequestBodyEffortAndTools(t *testing.T) {
	nr := &adapter.NativeRequest{
		Model:    "sol-max",
		Thinking: "",
		Tools: []adapter.ToolDef{{
			Name:        "Read",
			Description: "read file",
			Parameters:  json.RawMessage(`{"type":"object"}`),
		}},
		Extra: map[string]any{"task_id": "t", "turn_id": "u"},
	}
	body := buildRequestBody(nr)
	reasoning, _ := body["reasoning"].(map[string]any)
	if reasoning["effort"] != "xhigh" {
		t.Fatalf("max should map to xhigh: %+v", reasoning)
	}
	// 客户端 tools 不进上游（白名单 422），改走 run_officejs 提示词目录
	if _, present := body["tools"]; present {
		t.Fatalf("client tools must not be forwarded: %+v", body["tools"])
	}
	raw, _ := json.Marshal(body["input"])
	if !strings.Contains(string(raw), "Read") || !strings.Contains(string(raw), "run_officejs") {
		t.Fatalf("tool catalog not injected into prompt: %s", raw)
	}
}

func TestResolveModel(t *testing.T) {
	cases := map[string]string{
		"":                   DefaultModel,
		"sol":                "gpt-5.6-sol",
		"default":            "gpt-5.6-sol",
		"gpt-5.6-sol-high":   "gpt-5.6-sol",
		"gpt-6-astra":        "gpt-6-astra",
		"astra":              "gpt-6-astra",
		"unknown-model-name": DefaultModel,
	}
	for in, want := range cases {
		if got := resolveModel(in); got != want {
			t.Fatalf("resolveModel(%q)=%q want %q", in, got, want)
		}
	}
}
