package llm_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/wojtekmanke/roster/internal/llm"
	"github.com/wojtekmanke/roster/internal/llm/llmtest"
)

func TestFallbacksYAML(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		want    llm.Fallbacks
		wantErr bool
	}{
		{"default", "fallbacks: default", llm.Fallbacks{Default: true}, false},
		{"list", "fallbacks: [a, b]", llm.Fallbacks{Models: []string{"a", "b"}}, false},
		{"absent", "name: x", llm.Fallbacks{}, false},
		{"bad scalar", "fallbacks: always", llm.Fallbacks{}, true},
		{"mapping", "fallbacks: {a: b}", llm.Fallbacks{}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var cfg llm.ModelConfig
			err := yaml.Unmarshal([]byte(tt.yaml), &cfg)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if err != nil {
				return
			}
			got := cfg.Fallbacks
			if got.Default != tt.want.Default || strings.Join(got.Models, ",") != strings.Join(tt.want.Models, ",") {
				t.Fatalf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestMessageHelpers(t *testing.T) {
	m := llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{
		llm.Text("one"),
		llm.ToolUse("c1", "lookup", json.RawMessage(`{"id":1}`)),
		llm.Text("two"),
	}}
	if got := m.Text(); got != "one\ntwo" {
		t.Errorf("Text() = %q", got)
	}
	if calls := m.ToolUses(); len(calls) != 1 || calls[0].ToolName != "lookup" {
		t.Errorf("ToolUses() = %+v", calls)
	}
}

func TestUsageAdd(t *testing.T) {
	u := llm.Usage{InputTokens: 1, OutputTokens: 2, CacheReadTokens: 3, CacheWriteTokens: 4}
	u.Add(llm.Usage{InputTokens: 10, OutputTokens: 20, CacheReadTokens: 30, CacheWriteTokens: 40})
	want := llm.Usage{InputTokens: 11, OutputTokens: 22, CacheReadTokens: 33, CacheWriteTokens: 44}
	if u != want {
		t.Fatalf("got %+v, want %+v", u, want)
	}
}

func TestRegistryGet(t *testing.T) {
	r := llm.Registry{"scripted": llmtest.NewScripted()}
	if _, err := r.Get("scripted"); err != nil {
		t.Fatal(err)
	}
	_, err := r.Get("missing")
	if err == nil || !strings.Contains(err.Error(), "have: scripted") {
		t.Fatalf("err = %v, want it to list configured providers", err)
	}
}

func TestErrorUnwrap(t *testing.T) {
	inner := errors.New("boom")
	err := error(&llm.Error{Provider: "p", Status: 429, Retryable: true, Err: inner})
	if !errors.Is(err, inner) {
		t.Fatal("errors.Is did not find the wrapped error")
	}
	if got := err.Error(); got != "p: HTTP 429: boom" {
		t.Fatalf("Error() = %q", got)
	}
}

func TestScriptedReplaysInOrder(t *testing.T) {
	p := llmtest.NewScripted(llmtest.Call("lookup", map[string]any{"id": 1}), llmtest.Say("done"))
	ctx := context.Background()
	req := &llm.Request{Model: llm.ModelConfig{Name: "m"}, Messages: []llm.Message{llm.UserText("hi")}}

	first, err := p.Complete(ctx, req)
	if err != nil || first.StopReason != llm.StopToolUse || first.Model != "m" {
		t.Fatalf("first = %+v, %v", first, err)
	}
	second, err := p.Complete(ctx, req)
	if err != nil || second.Message.Text() != "done" {
		t.Fatalf("second = %+v, %v", second, err)
	}
	if _, err := p.Complete(ctx, req); err == nil {
		t.Fatal("expected an error once the script is exhausted")
	}
	if got := len(p.Requests()); got != 3 {
		t.Fatalf("recorded %d requests, want 3", got)
	}
}
