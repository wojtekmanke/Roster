package openaicompat_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/openai/openai-go/v3/option"

	"github.com/wojtekmanke/roster/internal/llm"
	"github.com/wojtekmanke/roster/internal/llm/openaicompat"
)

type fakeAPI struct {
	t        *testing.T
	status   int
	response string
	body     map[string]any
	path     string
}

func (f *fakeAPI) server() *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &f.body); err != nil {
			f.t.Errorf("request body is not JSON: %v", err)
		}
		f.path = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		if f.status != 0 {
			w.WriteHeader(f.status)
			io.WriteString(w, `{"error":{"message":"nope","type":"invalid_request_error"}}`)
			return
		}
		io.WriteString(w, f.response)
	}))
	f.t.Cleanup(srv.Close)
	return srv
}

const toolCallResponse = `{
  "id": "chatcmpl-1", "object": "chat.completion", "created": 1, "model": "gpt-6.1-sol",
  "choices": [{
    "index": 0, "finish_reason": "tool_calls",
    "message": {"role": "assistant", "content": null, "tool_calls": [
      {"id": "call_1", "type": "function", "function": {"name": "get_order", "arguments": "{\"id\":\"O-1\"}"}},
      {"id": "call_2", "type": "function", "function": {"name": "list_orders", "arguments": ""}}
    ]}
  }],
  "usage": {"prompt_tokens": 200, "completion_tokens": 25, "total_tokens": 225, "prompt_tokens_details": {"cached_tokens": 150}}
}`

const textResponse = `{
  "id": "chatcmpl-2", "object": "chat.completion", "created": 1, "model": "llama3.2",
  "choices": [{"index": 0, "finish_reason": "stop", "message": {"role": "assistant", "content": "Shipped."}}],
  "usage": {"prompt_tokens": 12, "completion_tokens": 3, "total_tokens": 15}
}`

var conversation = []llm.Message{
	llm.UserText("Where is order O-1?"),
	{Role: llm.RoleAssistant, Content: []llm.Block{
		llm.Text("Checking."),
		llm.ToolUse("call_0", "get_order", json.RawMessage(`{"id":"O-1"}`)),
	}},
	{Role: llm.RoleUser, Content: []llm.Block{llm.ToolResult("call_0", `{"status":"shipped"}`, false)}},
}

var orderTool = llm.ToolDef{
	Name:        "get_order",
	Description: "Look up an order",
	InputSchema: json.RawMessage(`{"type":"object","properties":{"id":{"type":"string"}},"required":["id"]}`),
}

func TestCompleteMapsRequestAndResponse(t *testing.T) {
	api := &fakeAPI{t: t, response: toolCallResponse}
	p := openaicompat.NewOpenAI(option.WithBaseURL(api.server().URL), option.WithAPIKey("k"), option.WithMaxRetries(0))

	resp, err := p.Complete(context.Background(), &llm.Request{
		Model:    llm.ModelConfig{Provider: "openai", Name: "gpt-6.1-sol", MaxTokens: 900, Effort: "low"},
		System:   "You are a support agent.",
		Messages: conversation,
		Tools:    []llm.ToolDef{orderTool},
	})
	if err != nil {
		t.Fatal(err)
	}

	if resp.StopReason != llm.StopToolUse {
		t.Errorf("StopReason = %q, want tool_use", resp.StopReason)
	}
	want := llm.Usage{InputTokens: 50, OutputTokens: 25, CacheReadTokens: 150}
	if resp.Usage != want {
		t.Errorf("Usage = %+v, want %+v (cached tokens split out of the prompt count)", resp.Usage, want)
	}
	calls := resp.Message.ToolUses()
	if len(calls) != 2 || calls[0].ToolUseID != "call_1" || string(calls[0].Input) != `{"id":"O-1"}` {
		t.Fatalf("tool calls = %+v", calls)
	}
	if string(calls[1].Input) != `{}` {
		t.Errorf("empty arguments became %s, want {}", calls[1].Input)
	}

	body := api.body
	if body["max_completion_tokens"] != float64(900) {
		t.Errorf("max_completion_tokens = %v", body["max_completion_tokens"])
	}
	if _, ok := body["max_tokens"]; ok {
		t.Error("OpenAI request used the legacy max_tokens field")
	}
	if body["reasoning_effort"] != "low" {
		t.Errorf("reasoning_effort = %v", body["reasoning_effort"])
	}

	msgs := body["messages"].([]any)
	var roles []string
	for _, m := range msgs {
		roles = append(roles, m.(map[string]any)["role"].(string))
	}
	if got, want := roles, []string{"system", "user", "assistant", "tool"}; !equal(got, want) {
		t.Fatalf("roles = %v, want %v", got, want)
	}
	assistant := msgs[2].(map[string]any)
	call := assistant["tool_calls"].([]any)[0].(map[string]any)
	if call["id"] != "call_0" || call["function"].(map[string]any)["arguments"] != `{"id":"O-1"}` {
		t.Errorf("assistant tool call = %v", call)
	}
	if tool := msgs[3].(map[string]any); tool["tool_call_id"] != "call_0" || tool["content"] != `{"status":"shipped"}` {
		t.Errorf("tool message = %v", tool)
	}
	fn := body["tools"].([]any)[0].(map[string]any)["function"].(map[string]any)
	if fn["name"] != "get_order" || fn["parameters"].(map[string]any)["type"] != "object" {
		t.Errorf("tool definition = %v", fn)
	}
}

func TestOllamaUsesLegacyMaxTokens(t *testing.T) {
	api := &fakeAPI{t: t, response: textResponse}
	p := openaicompat.NewOllama(api.server().URL, option.WithMaxRetries(0))

	resp, err := p.Complete(context.Background(), &llm.Request{
		Model:    llm.ModelConfig{Provider: "ollama", Name: "llama3.2", MaxTokens: 256},
		Messages: []llm.Message{llm.UserText("hi")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.StopReason != llm.StopEndTurn || resp.Message.Text() != "Shipped." {
		t.Fatalf("resp = %+v", resp)
	}
	if resp.Usage != (llm.Usage{InputTokens: 12, OutputTokens: 3}) {
		t.Errorf("Usage = %+v", resp.Usage)
	}
	if api.body["max_tokens"] != float64(256) {
		t.Errorf("max_tokens = %v", api.body["max_tokens"])
	}
	if _, ok := api.body["max_completion_tokens"]; ok {
		t.Error("Ollama request used max_completion_tokens")
	}
	if api.path != "/chat/completions" {
		t.Errorf("path = %q", api.path)
	}
}

func TestMaxTokensOmittedWhenUnset(t *testing.T) {
	api := &fakeAPI{t: t, response: textResponse}
	p := openaicompat.NewXAI("k", option.WithBaseURL(api.server().URL), option.WithMaxRetries(0))
	if _, err := p.Complete(context.Background(), &llm.Request{
		Model:    llm.ModelConfig{Provider: "xai", Name: "grok-4.7"},
		Messages: []llm.Message{llm.UserText("hi")},
	}); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"max_tokens", "max_completion_tokens", "temperature", "reasoning_effort"} {
		if _, ok := api.body[field]; ok {
			t.Errorf("%s was sent although the spec did not set it", field)
		}
	}
}

func TestStopReasons(t *testing.T) {
	tests := []struct {
		name    string
		message string
		finish  string
		want    llm.StopReason
	}{
		{"stop", `{"role":"assistant","content":"ok"}`, "stop", llm.StopEndTurn},
		{"length", `{"role":"assistant","content":"ok"}`, "length", llm.StopMaxTokens},
		{"content filter", `{"role":"assistant","content":""}`, "content_filter", llm.StopRefusal},
		{"refusal", `{"role":"assistant","content":null,"refusal":"I can't help with that."}`, "stop", llm.StopRefusal},
		{"tool calls reported as stop", `{"role":"assistant","content":"","tool_calls":[{"id":"c","type":"function","function":{"name":"f","arguments":"{}"}}]}`, "stop", llm.StopToolUse},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api := &fakeAPI{t: t, response: `{"id":"x","object":"chat.completion","created":1,"model":"m","choices":[{"index":0,"finish_reason":"` +
				tt.finish + `","message":` + tt.message + `}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`}
			p := openaicompat.NewOpenAI(option.WithBaseURL(api.server().URL), option.WithAPIKey("k"), option.WithMaxRetries(0))
			resp, err := p.Complete(context.Background(), &llm.Request{
				Model:    llm.ModelConfig{Name: "m"},
				Messages: []llm.Message{llm.UserText("hi")},
			})
			if err != nil {
				t.Fatal(err)
			}
			if resp.StopReason != tt.want {
				t.Errorf("StopReason = %q, want %q", resp.StopReason, tt.want)
			}
		})
	}
}

func TestInvalidArgumentsAreKeptAsString(t *testing.T) {
	api := &fakeAPI{t: t, response: `{"id":"x","object":"chat.completion","created":1,"model":"m","choices":[{"index":0,"finish_reason":"tool_calls","message":{"role":"assistant","content":"","tool_calls":[{"id":"c","type":"function","function":{"name":"f","arguments":"{not json"}}]}}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`}
	p := openaicompat.NewOllama(api.server().URL, option.WithMaxRetries(0))
	resp, err := p.Complete(context.Background(), &llm.Request{
		Model:    llm.ModelConfig{Name: "m"},
		Messages: []llm.Message{llm.UserText("hi")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := string(resp.Message.ToolUses()[0].Input); got != `"{not json"` {
		t.Errorf("input = %s, want the raw text as a JSON string", got)
	}
}

func TestErrorsAndUnsupportedOptions(t *testing.T) {
	api := &fakeAPI{t: t, status: 429}
	p := openaicompat.NewOpenAI(option.WithBaseURL(api.server().URL), option.WithAPIKey("k"), option.WithMaxRetries(0))
	req := &llm.Request{Model: llm.ModelConfig{Name: "m"}, Messages: []llm.Message{llm.UserText("hi")}}

	_, err := p.Complete(context.Background(), req)
	var llmErr *llm.Error
	if !errors.As(err, &llmErr) || llmErr.Status != 429 || !llmErr.Retryable || llmErr.Provider != "openai" {
		t.Fatalf("err = %#v, want a retryable 429 from openai", err)
	}

	req.Model.Fallbacks = llm.Fallbacks{Default: true}
	if _, err := p.Complete(context.Background(), req); err == nil {
		t.Fatal("expected an error for refusal fallbacks on a provider without them")
	}
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
