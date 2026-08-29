package demo_test

import (
	"context"
	"strings"
	"testing"

	"github.com/wojtekmanke/roster/agents"
	"github.com/wojtekmanke/roster/internal/demo"
	"github.com/wojtekmanke/roster/internal/llm"
	"github.com/wojtekmanke/roster/internal/spec"
)

func TestRewriteLeavesNoRealProvider(t *testing.T) {
	examples, err := agents.Examples()
	if err != nil {
		t.Fatal(err)
	}
	for file, data := range examples {
		for _, careless := range []bool{false, true} {
			out, err := demo.Rewrite(data, careless)
			if err != nil {
				t.Fatalf("%s: %v", file, err)
			}
			s, err := spec.Parse(out)
			if err != nil {
				t.Fatalf("%s: rewritten spec does not parse: %v\n%s", file, err, out)
			}
			for _, real := range []string{"anthropic", "openai", "xai", "ollama", "claude", "grok", "gpt-", "llama"} {
				if strings.Contains(string(out), "provider: "+real) || strings.Contains(string(out), "name: "+real) {
					t.Errorf("%s: rewritten spec still names %q", file, real)
				}
			}
			want := demo.Careful
			if careless {
				want = demo.Careless
			}
			if s.Model.Provider != demo.Name || s.Model.Name != want {
				t.Errorf("%s: model = %s, want demo/%s", file, s.Model, want)
			}
			if !s.Model.Fallbacks.IsZero() {
				t.Errorf("%s: refusal fallbacks survived the rewrite", file)
			}
			for _, sh := range s.Shadow {
				if sh.Model.Provider != demo.Name || sh.Model.Name == want || sh.SampleRate != 1 {
					t.Errorf("%s: shadow = %+v", file, sh)
				}
			}
		}

		// Everything but the models is untouched.
		original, _ := spec.Parse(data)
		rewritten, _ := demo.Rewrite(data, false)
		got, _ := spec.Parse(rewritten)
		if got.Instructions != original.Instructions || len(got.Tools) != len(original.Tools) ||
			len(got.KPIs) != len(original.KPIs) || got.Review != original.Review || got.Limits != original.Limits {
			t.Errorf("%s: the rewrite changed more than the models", file)
		}
		for i, kpi := range got.KPIs {
			if original.KPIs[i].Target != nil && (kpi.Target == nil || *kpi.Target != *original.KPIs[i].Target) {
				t.Errorf("%s: kpi %s lost its target in the rewrite", file, kpi.Name)
			}
		}
	}
}

func TestProviderKnowsOnlyItsTwoModels(t *testing.T) {
	_, err := demo.Provider{}.Complete(context.Background(), &llm.Request{
		Model:    llm.ModelConfig{Provider: demo.Name, Name: "gpt-9"},
		Messages: []llm.Message{llm.UserText("hi")},
	})
	if err == nil || !strings.Contains(err.Error(), "no scripted model named") {
		t.Fatalf("err = %v", err)
	}

	// An agent that is not one of the bundled examples gets a clear refusal.
	_, err = demo.Provider{}.Complete(context.Background(), &llm.Request{
		Model:    llm.ModelConfig{Provider: demo.Name, Name: demo.Careful},
		Messages: []llm.Message{llm.UserText("hi")},
		Tools:    []llm.ToolDef{{Name: "launch_rocket"}},
	})
	if err == nil || !strings.Contains(err.Error(), "only know the bundled example agents") {
		t.Fatalf("err = %v", err)
	}
}

func TestProviderReportsUsage(t *testing.T) {
	resp, err := demo.Provider{}.Complete(context.Background(), &llm.Request{
		Model:    llm.ModelConfig{Provider: demo.Name, Name: demo.Careful},
		System:   strings.Repeat("x", 400),
		Messages: []llm.Message{llm.UserText("Screen order O-5001.")},
		Tools:    []llm.ToolDef{{Name: "get_order"}, {Name: "hold_order"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Model != demo.Careful || resp.Usage.InputTokens < 100 || resp.Usage.OutputTokens < 1 {
		t.Errorf("resp = %+v", resp)
	}
	if calls := resp.Message.ToolUses(); len(calls) != 1 || calls[0].ToolName != "get_order" || string(calls[0].Input) != `{"id":"O-5001"}` {
		t.Errorf("first move = %+v", resp.Message.Content)
	}
}
