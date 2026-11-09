package plugin

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"opencode-go-cliproxyapi/internal/catalog"
)

func TestIsOpenCodeFreeModel(t *testing.T) {
	if !isOpenCodeFreeModel("inkling-2-small-free") {
		t.Error("inkling-2-small-free should be free")
	}
	if !isOpenCodeFreeModel("GLM-5.3-flash-FREE") {
		t.Error("case-insensitive -free suffix should be free")
	}
	if isOpenCodeFreeModel("gpt-6-luna") {
		t.Error("gpt-6-luna should not be free")
	}
}

func TestShapeOpencodeSessionChat(t *testing.T) {
	body := []byte(`{"model":"inkling-2-small-free","messages":[{"role":"user","content":"hi"}]}`)
	shaped, forced := shapeOpencodeSession(catalog.RouteChatCompletions, body)
	if !forced {
		t.Fatal("stream should be forced")
	}
	var doc map[string]any
	if err := json.Unmarshal(shaped, &doc); err != nil {
		t.Fatal(err)
	}
	if doc["stream"] != true {
		t.Error("stream not true")
	}
	tools, _ := doc["tools"].([]any)
	if len(tools) == 0 {
		t.Error("tools not added")
	}
	msgs := doc["messages"].([]any)
	sys, _ := msgs[0].(map[string]any)
	if sys["role"] != "system" || !strings.Contains(sys["content"].(string), "<env>") {
		t.Errorf("system preamble not prepended: %v", msgs[0])
	}
	// original user message preserved
	if len(msgs) != 2 || !strings.Contains(msgs[1].(map[string]any)["content"].(string), "hi") {
		t.Error("original user message not preserved")
	}
}

func TestShapeOpencodeSessionIdempotent(t *testing.T) {
	body := []byte(`{"model":"x","messages":[{"role":"system","content":"<env>\n  Platform: linux\n</env> already here"}],"tools":[{"type":"function"}],"stream":true}`)
	shaped, forced := shapeOpencodeSession(catalog.RouteChatCompletions, body)
	if forced {
		t.Error("stream already true, should not be forced")
	}
	if !bytes.Contains(shaped, []byte("already here")) {
		t.Error("original system content lost")
	}
	if bytes.Count(shaped, []byte("<env>")) != 1 {
		t.Errorf("env block duplicated: %s", shaped)
	}
}

func TestAggregateChatCompletionsSSE(t *testing.T) {
	sse := "data: {\"id\":\"c1\",\"model\":\"inkling\",\"choices\":[{\"delta\":{\"content\":\"pon\"},\"finish_reason\":null}]}\n\n" +
		"data: {\"id\":\"c1\",\"model\":\"inkling\",\"choices\":[{\"delta\":{\"content\":\"g\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":2,\"total_tokens\":3}}\n\n" +
		"data: [DONE]\n\n"
	out := aggregateChatCompletionsSSE([]byte(sse))
	var doc map[string]any
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatalf("not JSON: %s", out)
	}
	choice := doc["choices"].([]any)[0].(map[string]any)
	msg := choice["message"].(map[string]any)
	if msg["content"] != "pong" {
		t.Errorf("content = %v, want pong", msg["content"])
	}
	if choice["finish_reason"] != "stop" {
		t.Errorf("finish_reason = %v", choice["finish_reason"])
	}
	if _, ok := doc["usage"]; !ok {
		t.Error("usage not captured")
	}
}

func TestIsSSEBody(t *testing.T) {
	if !isSSEBody([]byte("data: {\"a\":1}\n")) {
		t.Error("should detect SSE")
	}
	if isSSEBody([]byte(`{"choices":[]}`)) {
		t.Error("should not detect plain JSON as SSE")
	}
}
