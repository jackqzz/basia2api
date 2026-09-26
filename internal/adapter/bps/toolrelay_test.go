package bps

import (
	"encoding/json"
	"strings"
	"testing"

	"bps-2api/internal/adapter"
)

var shellTool = adapter.ToolDef{
	Name:        "shell",
	Description: "run a command",
	Parameters:  json.RawMessage(`{"type":"object","properties":{"command":{"type":"array","items":{"type":"string"}}},"required":["command"]}`),
}

func sseWithItem(item string) string {
	return "event: response.output_item.done\n" +
		"data: {\"type\":\"response.output_item.done\",\"item\":" + item + "}\n\n" +
		"event: response.completed\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n"
}

func TestTransportEnvelopeDecode(t *testing.T) {
	args := `{"summary":"x","code":"{\"name\":\"shell\",\"arguments\":{\"command\":[\"echo\",\"hi\"]}}"}`
	env := transportEnvelope("run_officejs", args)
	if env == nil || env["name"] != "shell" {
		t.Fatalf("env=%v", env)
	}
	// functions. 前缀别名
	if transportEnvelope("functions.run_officejs", args) == nil {
		t.Fatal("alias not accepted")
	}
	// 非 transport 名
	if transportEnvelope("shell", args) != nil {
		t.Fatal("non-transport accepted")
	}
	// 坏 envelope
	if transportEnvelope("run_officejs", `{"code":"not json"}`) != nil {
		t.Fatal("bad code accepted")
	}
	// 嵌套 envelope 剥一层
	nested := `{"code":"{\"name\":\"run_officejs\",\"arguments\":{\"code\":\"{\\\"name\\\":\\\"shell\\\",\\\"arguments\\\":{}}\"}}"}`
	env = transportEnvelope("run_officejs", nested)
	if env == nil || env["name"] != "shell" {
		t.Fatalf("nested env=%v", env)
	}
}

func TestMapNativeToolCall(t *testing.T) {
	tools := []adapter.ToolDef{shellTool}
	// run_officejs 传输 → shell
	item := outputItem{Type: "function_call", CallID: "call_1", Name: "run_officejs",
		Arguments: `{"summary":"x","extended_summary":"y","destructive":false,"references":[],"code":"{\"name\":\"shell\",\"arguments\":{\"command\":[\"echo\",\"ok\"]}}"}`}
	call := mapNativeToolCall(item, tools)
	if call == nil || call.Name != "shell" || call.ToolCallID != "call_1" {
		t.Fatalf("call=%+v", call)
	}
	var parsed map[string]any
	if json.Unmarshal([]byte(call.RawArgs), &parsed) != nil {
		t.Fatalf("args not json: %s", call.RawArgs)
	}
	if _, ok := parsed["command"].([]any); !ok {
		t.Fatalf("missing command: %s", call.RawArgs)
	}

	// 直呼客户端工具名（模型偶尔不走 envelope）
	direct := outputItem{Type: "function_call", CallID: "call_2", Name: "shell",
		Arguments: `{"command":["ls"]}`}
	call = mapNativeToolCall(direct, tools)
	if call == nil || call.Name != "shell" {
		t.Fatalf("direct call=%+v", call)
	}

	// 未声明的工具名（office 原生工具）→ 丢弃
	office := outputItem{Type: "function_call", CallID: "call_3", Name: "read_workbook",
		Arguments: `{}`}
	if mapNativeToolCall(office, tools) != nil {
		t.Fatal("office tool leaked to client")
	}

	// 无客户端工具 → 全丢
	if mapNativeToolCall(item, nil) != nil {
		t.Fatal("emitted without declared tools")
	}

	// schema required 不满足 → 丢弃
	bad := outputItem{Type: "function_call", CallID: "call_4", Name: "shell",
		Arguments: `{"wrong":1}`}
	if mapNativeToolCall(bad, tools) != nil {
		t.Fatal("schema-mismatched call emitted")
	}
}

func TestMapNativeToolCallCustomEnvelope(t *testing.T) {
	customTool := adapter.ToolDef{Name: "apply_patch",
		Parameters: json.RawMessage(`{"type":"object","properties":{"input":{"type":"string"}},"required":["input"]}`)}
	tools := []adapter.ToolDef{customTool}
	// custom 形状 envelope {"name":..., "input":...} → 包回 {input:...}
	item := outputItem{Type: "function_call", CallID: "call_9", Name: "run_officejs",
		Arguments: `{"code":"{\"name\":\"apply_patch\",\"input\":\"*** Begin Patch\\n*** End Patch\"}"}`}
	call := mapNativeToolCall(item, tools)
	if call == nil || call.Name != "apply_patch" {
		t.Fatalf("custom envelope dropped: %+v", call)
	}
	var parsed map[string]any
	_ = json.Unmarshal([]byte(call.RawArgs), &parsed)
	if !strings.HasPrefix(parsed["input"].(string), "*** Begin Patch") {
		t.Fatalf("input=%v", parsed)
	}
}

func TestFallbackTransportCall(t *testing.T) {
	item := fallbackTransportCall(map[string]any{
		"type": "function_call", "call_id": "call_x",
		"name": "shell", "arguments": `{"command":["ls"]}`,
	})
	if item["name"] != "run_officejs" || item["call_id"] != "call_x" {
		t.Fatalf("item=%v", item)
	}
	if item["id"] != "fc_call_x" {
		t.Fatalf("id=%v", item["id"])
	}
	var args map[string]any
	_ = json.Unmarshal([]byte(item["arguments"].(string)), &args)
	env := decodeTransportCode(args["code"])
	if env["name"] != "shell" {
		t.Fatalf("env=%v", env)
	}
}

func TestUpdatePlanRoundTrip(t *testing.T) {
	native := map[string]any{
		"summary": "do it",
		"plan": []any{
			map[string]any{"id": "step1", "description": "探查", "status": "in_progress", "result": ""},
		},
	}
	n := normalizeNativeUpdatePlanArgs(native)
	plan := n["plan"].([]map[string]any)
	if plan[0]["step"] != "探查" || plan[0]["status"] != "in_progress" {
		t.Fatalf("normalized=%v", n)
	}
	if n["explanation"] != "do it" {
		t.Fatalf("explanation=%v", n["explanation"])
	}
	restored := restoreNativeUpdatePlanArgs(`{"plan":[{"step":"探查","status":"completed"}],"explanation":"x"}`)
	var rm map[string]any
	_ = json.Unmarshal([]byte(restored), &rm)
	rp := rm["plan"].([]any)[0].(map[string]any)
	if rp["id"] != "step1" || rp["description"] != "探查" || rp["status"] != "completed" {
		t.Fatalf("restored=%v", rm)
	}
}

func TestTurnState(t *testing.T) {
	msgs := []adapter.ChatMessage{
		{Role: "user", Content: "run ls"},
		{Role: "assistant", ToolCalls: []adapter.ToolCall{{ID: "c1", Name: "shell"}}},
		{Role: "tool", ToolCallID: "c1", Content: "files"},
		{Role: "assistant", ToolCalls: []adapter.ToolCall{{ID: "c2", Name: "shell"}}},
		{Role: "tool", ToolCallID: "c2", Content: "more"},
	}
	fp, it := turnState(msgs)
	if fp == "" || it != "3" {
		t.Fatalf("fp=%q iter=%q", fp, it)
	}
	// 同一 user turn 里累加 tool 结果指纹不变（turn_id 稳定，iteration 递增）
	fp2, it2 := turnState(msgs[:1])
	if fp2 != fp || it2 != "1" {
		t.Fatalf("same-turn prefix must keep fingerprint: fp2=%q it2=%q", fp2, it2)
	}
	// 追加新的 user 消息后进入新 turn，指纹变、iteration 归零
	msgs = append(msgs, adapter.ChatMessage{Role: "user", Content: "next"})
	fp4, it4 := turnState(msgs)
	if fp4 == fp || it4 != "1" {
		t.Fatal("new user msg must start new turn")
	}
}

func TestUUID5Stable(t *testing.T) {
	a := uuid5("bps-2api/gpt-excel/conv1")
	b := uuid5("bps-2api/gpt-excel/conv1")
	if a != b || len(a) != 36 || a[14] != '5' {
		t.Fatalf("uuid5=%q", a)
	}
}

func TestConsumeSSERelayedCall(t *testing.T) {
	sse := sseWithItem(`{"type":"function_call","call_id":"call_relay","name":"run_officejs","arguments":"{\"code\":\"{\\\"name\\\":\\\"shell\\\",\\\"arguments\\\":{\\\"command\\\":[\\\"echo\\\",\\\"hi\\\"]}}\"}"}`)
	var got *adapter.StreamedToolCall
	err := consumeSSE(strings.NewReader(sse), func(ev adapter.Event) bool {
		if ev.ToolCall != nil {
			got = ev.ToolCall
		}
		return true
	}, []adapter.ToolDef{shellTool})
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.Name != "shell" || got.ToolCallID != "call_relay" {
		t.Fatalf("got=%+v", got)
	}
}

func TestMapNativeToolCallPassthroughRealOfficeJS(t *testing.T) {
	tools := []adapter.ToolDef{shellTool}
	// code 是真实 OfficeJS（非 JSON envelope）→ 标记 transport_malformed，
	// 由 api 层决定：客户端声明了 run_officejs 则透传执行，否则回灌重试。
	item := outputItem{Type: "function_call", Name: "run_officejs", CallID: "call_1",
		Arguments: `{"summary":"read A1","code":"await Excel.run(async (ctx)=>{...})"}`}
	call := mapNativeToolCall(item, tools)
	if call == nil || call.Name != "run_officejs" || call.ToolCallID != "call_1" {
		t.Fatalf("undecodable transport should return marker, got %+v", call)
	}
	if call.Kind != "transport_malformed" {
		t.Fatalf("expected transport_malformed kind, got %q", call.Kind)
	}
	if !strings.Contains(call.RawArgs, "Excel.run") {
		t.Fatalf("args should be preserved raw, got %s", call.RawArgs)
	}
	// envelope 缺 inner name → 同样标记回灌
	item2 := outputItem{Type: "function_call", Name: "run_officejs", CallID: "call_2",
		Arguments: `{"summary":"x","code":"{\"foo\":1}"}`}
	call2 := mapNativeToolCall(item2, tools)
	if call2 == nil || call2.Name != "run_officejs" || call2.Kind != "transport_malformed" {
		t.Fatalf("missing inner name should be transport_malformed, got %+v", call2)
	}
}

func TestTransportEnvelopeSalvageUnescapedInner(t *testing.T) {
	// 线上实锤形态：内层 envelope JSON 未转义嵌进 code 字符串，
	// 外层整体非法 JSON，但内层裸露可取。
	args := `{"summary":"安装隔离建模依赖","extended_summary":"x","code":"{"name":"functions__exec","arguments":{"input":"const r=await tools.exec_command({cmd:\"$py='C:/Users/a.py'\"})"}}"}`
	env := transportEnvelope("run_officejs", args)
	if env == nil || env["name"] != "functions__exec" {
		t.Fatalf("salvaged env=%v", env)
	}
	in, _ := env["arguments"].(map[string]any)
	if !strings.Contains(in["input"].(string), "exec_command") {
		t.Fatalf("input lost: %v", in)
	}
	// 大 heredoc + 单引号混合的嵌套命令同样打捞出
	args2 := `{"summary":"s","code":"{"name":"shell","arguments":{"command":["python","-c","import json;print({'a':1})"]}}"}`
	env2 := transportEnvelope("run_officejs", args2)
	if env2 == nil || env2["name"] != "shell" {
		t.Fatalf("salvaged env2=%v", env2)
	}
	// 真 OfficeJS（code 里是 JS 不是 JSON）打不出 envelope → 仍判畸形
	args3 := `{"summary":"s","code":"await Excel.run(async (ctx)=>{ctx.workbook.worksheets;})"}`
	if env3 := transportEnvelope("run_officejs", args3); env3 != nil {
		t.Fatalf("real OfficeJS should not salvage, got %v", env3)
	}
}

func TestTransportEnvelopeSalvageJSCall(t *testing.T) {
	// 模型把 run_officejs 当 JS 执行器：code 里是裸 JS 调用。
	args := `{"summary":"s","code":"const r=await tools.exec_command({cmd:\"python a.py\"});console.log(r)"}`
	env := transportEnvelope("run_officejs", args)
	if env == nil || env["name"] != "exec_command" {
		t.Fatalf("js salvaged env=%v", env)
	}
	m, _ := env["arguments"].(map[string]any)
	if m["cmd"] != "python a.py" {
		t.Fatalf("args=%v", m)
	}
	// 外层转义崩 + 内层是 JS 的双重坏法也能捞
	args2 := `{"summary":"s","code":"await tools.shell({cmd:\"ls\",timeout:30})"}`
	env2 := transportEnvelope("run_officejs", args2)
	if env2 == nil || env2["name"] != "shell" {
		t.Fatalf("js salvaged env2=%v", env2)
	}
	if m2, _ := env2["arguments"].(map[string]any); m2["timeout"] != float64(30) {
		t.Fatalf("args2=%v", m2)
	}
	// code 纯文本无 tools. 调用 → 不捞
	args3 := `{"summary":"s","code":"just some text, no call"}`
	if env3 := transportEnvelope("run_officejs", args3); env3 != nil {
		t.Fatalf("plain text should not salvage, got %v", env3)
	}
}

func TestTransportEnvelopeDirectShape(t *testing.T) {
	// 模型把 envelope 当顶层 args 直写（少套 code）——兼容放行。
	args := `{"name":"shell","arguments":{"command":["ls","-l"]}}`
	env := transportEnvelope("run_officejs", args)
	if env == nil || env["name"] != "shell" {
		t.Fatalf("env=%v", env)
	}
}

func TestTransportRetryItems(t *testing.T) {
	items := TransportRetryItems("call_x", "run_officejs", `{"summary":"s","code":"bad json ("}`, []string{"exec_command", "shell"})
	if len(items) != 2 {
		t.Fatalf("expected call+output pair, got %d items", len(items))
	}
	fc, _ := items[0].(map[string]any)
	out, _ := items[1].(map[string]any)
	if fc["type"] != "function_call" || fc["call_id"] != "call_x" || fc["name"] != "run_officejs" {
		t.Fatalf("bad function_call item: %+v", fc)
	}
	if out["type"] != "function_call_output" || out["call_id"] != "call_x" {
		t.Fatalf("bad function_call_output item: %+v", out)
	}
	o, _ := out["output"].(string)
	if !strings.Contains(o, "envelope was malformed") || !strings.Contains(o, "exec_command, shell") {
		t.Fatalf("output should carry guidance+catalog, got %q", o)
	}
	// callID 为空不可回放
	if TransportRetryItems("", "run_officejs", "{}", nil) != nil {
		t.Fatal("empty call_id should return nil")
	}
}
