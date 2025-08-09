package spec_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/wojtekmanke/roster/internal/spec"
)

const full = `
name: support-triage
description: Answers order-status tickets.
model:
  provider: anthropic
  name: claude-opus-5-5
  effort: low
  fallbacks: default
instructions: |
  You answer order-status tickets.
prompt: "Handle ticket {{.ticket_id}}."
triggers:
  - event: ticket.created
  - schedule: every 15m
    input: {sweep: true}
  - webhook: true
tools:
  - name: get_ticket
    description: Fetch a support ticket.
    input_schema: {type: object, properties: {id: {type: string}}, required: [id]}
    graphql:
      endpoint: ${SHOP_URL}
      query: "query($id: ID!) { ticket(id: $id) { id } }"
  - name: refund_order
    description: Refund an order.
    requires_approval: true
    graphql:
      endpoint: ${SHOP_URL}
      query: "mutation($id: ID!) { refundOrder(id: $id) { id } }"
output:
  schema: {type: object, properties: {action: {type: string}}, required: [action]}
limits:
  max_steps: 8
  max_cost_usd: 0.25
  timeout: 90s
kpis:
  - name: accuracy
    grader: expected
    target: ">= 0.85"
  - name: cost
    grader: cost
    aggregate: p95
    target: "<= 0.05"
review:
  window: 40
  min_runs: 10
shadow:
  - provider: ollama
    name: llama3.2
`

func TestParseFullSpec(t *testing.T) {
	s, err := spec.Parse([]byte(full))
	if err != nil {
		t.Fatal(err)
	}
	if s.Name != "support-triage" || s.Model.Name != "claude-opus-5-5" || !s.Model.Fallbacks.Default {
		t.Errorf("header = %+v", s.Model)
	}
	if got := time.Duration(s.Limits.Timeout); got != 90*time.Second {
		t.Errorf("timeout = %s", got)
	}
	if got := time.Duration(s.Limits.ToolTimeout); got != spec.DefaultToolTimeout {
		t.Errorf("tool timeout = %s, want the default", got)
	}
	if !s.Tools[1].SideEffect {
		t.Error("requires_approval did not imply side_effect")
	}
	if s.Tools[0].SideEffect {
		t.Error("read-only tool was marked as a side effect")
	}
	if s.KPIs[0].Aggregate != "mean" || s.KPIs[1].Aggregate != "p95" {
		t.Errorf("aggregates = %q, %q", s.KPIs[0].Aggregate, s.KPIs[1].Aggregate)
	}
	if tgt := s.KPIs[0].Target; tgt == nil || tgt.Op != ">=" || tgt.Value != 0.85 {
		t.Errorf("target = %+v", tgt)
	}
	if s.Review.OnBreach != spec.OnBreachPause || s.Review.Window != 40 || s.Review.MinRuns != 10 {
		t.Errorf("review = %+v", s.Review)
	}
	if s.Shadow[0].SampleRate != 1 {
		t.Errorf("shadow sample rate = %v, want the default 1", s.Shadow[0].SampleRate)
	}
	if !s.HasEvent("ticket.created") || s.HasEvent("order.placed") || !s.HasWebhook() {
		t.Error("trigger lookups are wrong")
	}
}

func TestParseDefaults(t *testing.T) {
	s, err := spec.Parse([]byte("name: tiny\nmodel: {provider: ollama, name: llama3.2}\ninstructions: Do the thing.\n"))
	if err != nil {
		t.Fatal(err)
	}
	if s.Limits.MaxSteps != spec.DefaultMaxSteps || s.Review.Window != spec.DefaultWindow || s.Review.MinRuns != spec.DefaultMinRuns {
		t.Errorf("defaults not applied: %+v %+v", s.Limits, s.Review)
	}
}

func TestParseRejectsUnknownFields(t *testing.T) {
	_, err := spec.Parse([]byte("name: tiny\nmodel: {provider: ollama, name: llama3.2}\ninstructions: x\nlimit: {max_steps: 2}\n"))
	if err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("err = %v, want it to name the unknown field", err)
	}
}

func TestValidateReportsEveryProblem(t *testing.T) {
	bad := `
name: Bad Name
model: {provider: anthropic}
instructions: " "
prompt: "{{.unclosed"
triggers:
  - event: a
    schedule: every 5m
  - schedule: every 5s
  - event: b
    input: {x: 1}
tools:
  - name: submit_result
    builtin: x
  - name: dup
    builtin: x
  - name: dup
    builtin: x
  - name: neither
  - name: gql
    graphql: {endpoint: "", query: ""}
  - name: schema
    builtin: x
    input_schema: {type: string}
mcp_servers:
  - name: both
    url: http://x
    command: [x]
    tools: [{name: t}]
  - name: empty
    url: http://x
    tools: []
output:
  schema: {type: array}
limits: {max_cost_usd: -1}
kpis:
  - name: k
    grader: ""
    aggregate: median
  - name: k
    grader: cost
review: {window: 5, min_runs: 9, on_breach: fire}
shadow:
  - {provider: anthropic, name: ""}
  - {provider: ollama, name: m, sample_rate: 2}
`
	_, err := spec.Parse([]byte(bad))
	if err == nil {
		t.Fatal("expected validation errors")
	}
	msg := err.Error()
	for _, want := range []string{
		`name "Bad Name"`,
		"model.provider and model.name are required",
		"instructions are required",
		"prompt:",
		"triggers[0] must set exactly one",
		"interval must be at least",
		"triggers[2]: input is only valid on a schedule trigger",
		`"submit_result" is reserved`,
		`duplicate tool name "dup"`,
		"tools[3] (neither) must set one of graphql or builtin",
		"graphql.endpoint and graphql.query are required",
		`input_schema: must have type "object"`,
		"mcp_servers[0] (both) must set exactly one of url or command",
		"mcp_servers[1] (empty): list the tools",
		`output.schema: must have type "object"`,
		"limits.max_cost_usd must not be negative",
		"kpis[0] (k): grader is required",
		`aggregate "median"`,
		"kpis[1]: name is required and must be unique",
		"review.min_runs must be between 1 and review.window (5)",
		"review.on_breach must be",
		"shadow[0]: provider and name are required",
		"shadow[1]: sample_rate must be in (0, 1]",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("missing error %q in:\n%s", want, msg)
		}
	}
}

func TestShadowOfPrimaryModelIsRejected(t *testing.T) {
	_, err := spec.Parse([]byte("name: tiny\nmodel: {provider: ollama, name: m}\ninstructions: x\nshadow: [{provider: ollama, name: m}]\n"))
	if err == nil || !strings.Contains(err.Error(), "compares it with itself") {
		t.Fatalf("err = %v", err)
	}
}

func TestTarget(t *testing.T) {
	tests := []struct {
		expr  string
		value float64
		met   bool
	}{
		{">= 0.85", 0.85, true},
		{">= 0.85", 0.8499, false},
		{"<=0.05", 0.05, true},
		{"< 3", 3, false},
		{"> -1", 0, true},
		{"== 1", 1, true},
	}
	for _, tt := range tests {
		target, err := spec.ParseTarget(tt.expr)
		if err != nil {
			t.Fatalf("ParseTarget(%q): %v", tt.expr, err)
		}
		if got := target.Met(tt.value); got != tt.met {
			t.Errorf("%q.Met(%v) = %v, want %v", tt.expr, tt.value, got, tt.met)
		}
	}
	for _, expr := range []string{"", "0.85", "=> 1", ">= high", ">= 1 2"} {
		if _, err := spec.ParseTarget(expr); err == nil {
			t.Errorf("ParseTarget(%q) succeeded, want an error", expr)
		}
	}
	if got := (spec.Target{Op: ">=", Value: 0.85}).String(); got != ">= 0.85" {
		t.Errorf("String() = %q", got)
	}
}

func TestRenderPrompt(t *testing.T) {
	s, err := spec.Parse([]byte(full))
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.RenderPrompt(json.RawMessage(`{"ticket_id":"T-7"}`))
	if err != nil || got != "Handle ticket T-7." {
		t.Fatalf("RenderPrompt = %q, %v", got, err)
	}
	if _, err := s.RenderPrompt(json.RawMessage(`{"other":1}`)); err == nil {
		t.Error("a missing template key should be an error, not an empty string")
	}

	s.Prompt = ""
	got, err = s.RenderPrompt(json.RawMessage(`{"ticket_id":"T-7"}`))
	if err != nil || !strings.Contains(got, `"ticket_id": "T-7"`) {
		t.Fatalf("default prompt = %q, %v", got, err)
	}
	if got, err := s.RenderPrompt(nil); err != nil || !strings.Contains(got, "{}") {
		t.Fatalf("empty input = %q, %v", got, err)
	}
}

func TestHashIsStable(t *testing.T) {
	if spec.Hash([]byte("a")) != spec.Hash([]byte("a")) || spec.Hash([]byte("a")) == spec.Hash([]byte("b")) {
		t.Fatal("Hash is not a function of the content")
	}
}
