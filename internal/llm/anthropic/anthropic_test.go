package anthropic_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/wojtekmanke/roster/internal/llm"
	"github.com/wojtekmanke/roster/internal/llm/anthropic"
)

// fakeAPI serves canned Messages API responses and records what it was sent.
type fakeAPI struct {
	t         *testing.T
	status    int
	responses []string
	bodies    []map[string]any
	headers   []http.Header
}

func (f *fakeAPI) provider() *anthropic.Provider {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		if err := json.Unmarshal(raw, &body); err != nil {
			f.t.Errorf("request body is not JSON: %v", err)
		}
		f.bodies = append(f.bodies, body)
		f.headers = append(f.headers, r.Header.Clone())

		w.Header().Set("Content-Type", "application/json")
		if f.status != 0 {
			w.WriteHeader(f.status)
			io.WriteString(w, `{"type":"error","error":{"type":"api_error","message":"nope"}}`)
			return
		}
		if len(f.responses) == 0 {
			f.t.Error("unexpected extra request")
			w.WriteHeader(500)
			return
		}
		io.WriteString(w, f.responses[0])
		f.responses = f.responses[1:]
	}))
	f.t.Cleanup(srv.Close)
	return anthropic.New(option.WithBaseURL(srv.URL), option.WithAPIKey("test-key"), option.WithMaxRetries(0))
}

const toolUseResponse = `{
  "id": "msg_1", "type": "message", "role": "assistant", "model": "claude-opus-5-5",
  "content": [
    {"type": "thinking", "thinking": "", "signature": "sig-abc"},
    {"type": "text", "text": "Looking that up."},
    {"type": "tool_use", "id": "toolu_1", "name": "get_order", "input": {"id": "O-1"}}
  ],
  "stop_reason": "tool_use", "stop_sequence": null,
  "usage": {"input_tokens": 120, "output_tokens": 30, "cache_creation_input_tokens": 7, "cache_read_input_tokens": 50}
}`

const finalResponse = `{
  "id": "msg_2", "type": "message", "role": "assistant", "model": "claude-opus-5-5",
  "content": [{"type": "text", "text": "Your order has shipped."}],
  "stop_reason": "end_turn", "stop_sequence": null,
  "usage": {"input_tokens": 10, "output_tokens": 8, "cache_creation_input_tokens": 0, "cache_read_input_tokens": 150}
}`

var orderTool = llm.ToolDef{
	Name:        "get_order",
	Description: "Look up an order",
	InputSchema: json.RawMessage(`{"type":"object","properties":{"id":{"type":"string"}},"required":["id"],"additionalProperties":false}`),
}

func TestCompleteMapsRequestAndResponse(t *testing.T) {
	api := &fakeAPI{t: t, responses: []string{toolUseResponse}}
	p := api.provider()

	resp, err := p.Complete(context.Background(), &llm.Request{
		Model:    llm.ModelConfig{Provider: "anthropic", Name: "claude-opus-5-5", Effort: "low"},
		System:   "You are a support agent.",
		Messages: []llm.Message{llm.UserText("Where is order O-1?")},
		Tools:    []llm.ToolDef{orderTool},
	})
	if err != nil {
		t.Fatal(err)
	}

	if resp.StopReason != llm.StopToolUse {
		t.Errorf("StopReason = %q, want tool_use", resp.StopReason)
	}
	if resp.Model != "claude-opus-5-5" {
		t.Errorf("Model = %q", resp.Model)
	}
	want := llm.Usage{InputTokens: 120, OutputTokens: 30, CacheReadTokens: 50, CacheWriteTokens: 7}
	if resp.Usage != want {
		t.Errorf("Usage = %+v, want %+v", resp.Usage, want)
	}
	if got := resp.Message.Text(); got != "Looking that up." {
		t.Errorf("text = %q", got)
	}
	calls := resp.Message.ToolUses()
	if len(calls) != 1 || calls[0].ToolUseID != "toolu_1" || calls[0].ToolName != "get_order" {
		t.Fatalf("tool calls = %+v", calls)
	}
	var input struct{ ID string }
	if err := json.Unmarshal(calls[0].Input, &input); err != nil || input.ID != "O-1" {
		t.Errorf("tool input = %s (%v)", calls[0].Input, err)
	}
	if resp.Message.Native == nil || resp.Message.Native.Provider != "anthropic" {
		t.Fatal("assistant turn has no native payload to replay")
	}

	body := api.bodies[0]
	if body["model"] != "claude-opus-5-5" {
		t.Errorf("model = %v", body["model"])
	}
	if body["max_tokens"] != float64(16000) {
		t.Errorf("max_tokens = %v, want the 16000 default", body["max_tokens"])
	}
	if _, ok := body["temperature"]; ok {
		t.Error("temperature was sent although the spec did not set it")
	}
	if effort := dig(body, "output_config", "effort"); effort != "low" {
		t.Errorf("output_config.effort = %v", effort)
	}
	if cc := dig(body, "cache_control", "type"); cc != "ephemeral" {
		t.Errorf("cache_control.type = %v, want ephemeral", cc)
	}
	if _, ok := body["fallbacks"]; ok {
		t.Error("fallbacks were sent although none are configured")
	}
	tools := body["tools"].([]any)
	schema := tools[0].(map[string]any)["input_schema"].(map[string]any)
	if schema["type"] != "object" || schema["additionalProperties"] != false {
		t.Errorf("input_schema = %v", schema)
	}
	if req := schema["required"].([]any); len(req) != 1 || req[0] != "id" {
		t.Errorf("input_schema.required = %v", req)
	}
}

func TestCompleteReplaysNativeTurnUnchanged(t *testing.T) {
	api := &fakeAPI{t: t, responses: []string{toolUseResponse, finalResponse}}
	p := api.provider()
	ctx := context.Background()

	req := &llm.Request{
		Model:    llm.ModelConfig{Name: "claude-opus-5-5"},
		Messages: []llm.Message{llm.UserText("Where is order O-1?")},
		Tools:    []llm.ToolDef{orderTool},
	}
	first, err := p.Complete(ctx, req)
	if err != nil {
		t.Fatal(err)
	}

	// Persist and reload the turn, as the worker does between approval steps.
	stored, _ := json.Marshal(first.Message)
	var reloaded llm.Message
	if err := json.Unmarshal(stored, &reloaded); err != nil {
		t.Fatal(err)
	}
	req.Messages = append(req.Messages, reloaded, llm.Message{
		Role:    llm.RoleUser,
		Content: []llm.Block{llm.ToolResult("toolu_1", `{"status":"shipped"}`, false)},
	})

	second, err := p.Complete(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if second.StopReason != llm.StopEndTurn || second.Message.Text() != "Your order has shipped." {
		t.Fatalf("second = %+v", second)
	}

	msgs := api.bodies[1]["messages"].([]any)
	if len(msgs) != 3 {
		t.Fatalf("sent %d messages, want 3", len(msgs))
	}
	assistant := msgs[1].(map[string]any)["content"].([]any)
	if len(assistant) != 3 {
		t.Fatalf("assistant turn replayed %d blocks, want 3 (thinking, text, tool_use)", len(assistant))
	}
	thinking := assistant[0].(map[string]any)
	if thinking["type"] != "thinking" || thinking["signature"] != "sig-abc" {
		t.Errorf("thinking block not replayed verbatim: %v", thinking)
	}
	result := msgs[2].(map[string]any)["content"].([]any)[0].(map[string]any)
	if result["type"] != "tool_result" || result["tool_use_id"] != "toolu_1" {
		t.Errorf("tool result = %v", result)
	}
}

func TestCompleteSendsFallbacks(t *testing.T) {
	tests := []struct {
		name      string
		fallbacks llm.Fallbacks
		wantBody  string
		wantBeta  string
	}{
		{"default routing", llm.Fallbacks{Default: true}, `"default"`, "server-side-fallback-2026-07-01"},
		{"explicit models", llm.Fallbacks{Models: []string{"claude-opus-5"}}, `[{"model":"claude-opus-5"}]`, "server-side-fallback-2026-06-01"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api := &fakeAPI{t: t, responses: []string{finalResponse}}
			_, err := api.provider().Complete(context.Background(), &llm.Request{
				Model:    llm.ModelConfig{Name: "claude-opus-5-5", Fallbacks: tt.fallbacks},
				Messages: []llm.Message{llm.UserText("hi")},
			})
			if err != nil {
				t.Fatal(err)
			}
			got, _ := json.Marshal(api.bodies[0]["fallbacks"])
			if string(got) != tt.wantBody {
				t.Errorf("fallbacks = %s, want %s", got, tt.wantBody)
			}
			if beta := strings.Join(api.headers[0].Values("anthropic-beta"), ","); !strings.Contains(beta, tt.wantBeta) {
				t.Errorf("anthropic-beta = %q, want it to contain %q", beta, tt.wantBeta)
			}
		})
	}
}

func TestCompleteReportsRefusal(t *testing.T) {
	api := &fakeAPI{t: t, responses: []string{`{
	  "id": "msg_3", "type": "message", "role": "assistant", "model": "claude-opus-5-5",
	  "content": [], "stop_reason": "refusal", "stop_sequence": null,
	  "stop_details": {"type": "refusal", "category": "cyber", "explanation": "declined"},
	  "usage": {"input_tokens": 5, "output_tokens": 0, "cache_creation_input_tokens": 0, "cache_read_input_tokens": 0}
	}`}}
	resp, err := api.provider().Complete(context.Background(), &llm.Request{
		Model:    llm.ModelConfig{Name: "claude-opus-5-5"},
		Messages: []llm.Message{llm.UserText("hi")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.StopReason != llm.StopRefusal || resp.RefusalCategory != "cyber" {
		t.Fatalf("StopReason = %q, category = %q", resp.StopReason, resp.RefusalCategory)
	}
	if len(resp.Message.Content) != 0 {
		t.Errorf("refusal carried content: %+v", resp.Message.Content)
	}
}

func TestCompleteClassifiesErrors(t *testing.T) {
	tests := []struct {
		status    int
		retryable bool
	}{
		{400, false},
		{404, false},
		{429, true},
		{529, true},
	}
	for _, tt := range tests {
		api := &fakeAPI{t: t, status: tt.status}
		_, err := api.provider().Complete(context.Background(), &llm.Request{
			Model:    llm.ModelConfig{Name: "claude-opus-5-5"},
			Messages: []llm.Message{llm.UserText("hi")},
		})
		var llmErr *llm.Error
		if !errors.As(err, &llmErr) {
			t.Fatalf("status %d: err = %v, want *llm.Error", tt.status, err)
		}
		if llmErr.Status != tt.status || llmErr.Retryable != tt.retryable {
			t.Errorf("status %d: got Status=%d Retryable=%v, want Retryable=%v", tt.status, llmErr.Status, llmErr.Retryable, tt.retryable)
		}
	}
}

func dig(m map[string]any, path ...string) any {
	var cur any = m
	for _, key := range path {
		obj, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = obj[key]
	}
	return cur
}
