package scoring_test

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/wojtekmanke/roster/internal/llm"
	"github.com/wojtekmanke/roster/internal/llm/llmtest"
	"github.com/wojtekmanke/roster/internal/scoring"
	"github.com/wojtekmanke/roster/internal/spec"
)

func grade(t *testing.T, grader string, in scoring.Input, params map[string]any) map[string]float64 {
	t.Helper()
	res, err := scoring.Builtin()[grader].Grade(context.Background(), &in, params)
	if err != nil {
		t.Fatalf("%s: %v", grader, err)
	}
	return res.Metrics
}

func ok(output, expected string) scoring.Input {
	return scoring.Input{Succeeded: true, Output: json.RawMessage(output), Expected: json.RawMessage(expected)}
}

func TestRunFactGraders(t *testing.T) {
	in := scoring.Input{Succeeded: true, CostUSD: 0.042, ActiveMS: 2500, LLMCalls: 3, ToolCalls: 4, Approvals: []bool{true, false, true, true}}

	if m := grade(t, "outcome", in, nil); m["success"] != 1 {
		t.Errorf("outcome = %v", m)
	}
	if m := grade(t, "outcome", scoring.Input{}, nil); m["success"] != 0 {
		t.Errorf("outcome of a failed run = %v", m)
	}
	if m := grade(t, "cost", in, nil); m["usd"] != 0.042 {
		t.Errorf("cost = %v", m)
	}
	if m := grade(t, "latency", in, nil); m["seconds"] != 2.5 {
		t.Errorf("latency = %v", m)
	}
	if m := grade(t, "steps", in, nil); m["llm_calls"] != 3 || m["tool_calls"] != 4 {
		t.Errorf("steps = %v", m)
	}
	if m := grade(t, "approvals", in, nil); m["approved"] != 0.75 {
		t.Errorf("approvals = %v", m)
	}
	// A run that asked for no approval says nothing about the approval rate.
	if m := grade(t, "approvals", scoring.Input{Succeeded: true}, nil); len(m) != 0 {
		t.Errorf("approvals with no requests = %v, want no metric", m)
	}
}

func TestExpectedGrader(t *testing.T) {
	tests := []struct {
		name     string
		in       scoring.Input
		params   map[string]any
		match    float64
		accuracy float64
		none     bool
	}{
		{"all fields agree", ok(`{"action":"reply","status":"shipped","extra":1}`, `{"action":"reply","status":"shipped"}`), nil, 1, 1, false},
		{"one field differs", ok(`{"action":"reply","status":"lost"}`, `{"action":"reply","status":"shipped"}`), nil, 0, 0.5, false},
		{"strings ignore case and space", ok(`{"color":" Blue "}`, `{"color":"blue"}`), nil, 1, 1, false},
		{"missing output field", ok(`{}`, `{"action":"reply"}`), nil, 0, 0, false},
		{"only listed fields count", ok(`{"action":"reply","status":"lost"}`, `{"action":"reply","status":"shipped"}`), map[string]any{"fields": []any{"action"}}, 1, 1, false},
		{"number within tolerance", ok(`{"quantity":104}`, `{"quantity":100}`), map[string]any{"tolerance": 0.05}, 1, 1, false},
		{"number outside tolerance", ok(`{"quantity":106}`, `{"quantity":100}`), map[string]any{"tolerance": 0.05}, 0, 0, false},
		{"numbers are exact by default", ok(`{"quantity":101}`, `{"quantity":100}`), nil, 0, 0, false},
		{"booleans", ok(`{"reorder":false}`, `{"reorder":false}`), nil, 1, 1, false},
		{"wrong type", ok(`{"reorder":"false"}`, `{"reorder":false}`), nil, 0, 0, false},
		{"nested values", ok(`{"tags":["a","b"]}`, `{"tags":["a","b"]}`), nil, 1, 1, false},
		{"failed run scores zero", scoring.Input{Succeeded: false, Expected: json.RawMessage(`{"action":"reply"}`)}, nil, 0, 0, false},
		{"no ground truth", ok(`{"action":"reply"}`, ``), nil, 0, 0, true},
		{"ground truth lacks the listed field", ok(`{"action":"reply"}`, `{"other":1}`), map[string]any{"fields": []any{"action"}}, 0, 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := grade(t, "expected", tt.in, tt.params)
			if tt.none {
				if len(m) != 0 {
					t.Fatalf("metrics = %v, want none", m)
				}
				return
			}
			if m["match"] != tt.match || m["field_accuracy"] != tt.accuracy {
				t.Fatalf("metrics = %v, want match=%v field_accuracy=%v", m, tt.match, tt.accuracy)
			}
		})
	}

	_, err := scoring.Builtin()["expected"].Grade(context.Background(), &scoring.Input{Expected: json.RawMessage(`{"a":1}`)}, map[string]any{"fields": "a"})
	if err == nil {
		t.Error("a non-list fields parameter was accepted")
	}
}

func TestExpectedGraderExplainsMismatches(t *testing.T) {
	in := ok(`{"status":"lost"}`, `{"status":"shipped"}`)
	res, err := scoring.Builtin()["expected"].Grade(context.Background(), &in, nil)
	if err != nil {
		t.Fatal(err)
	}
	detail, _ := json.Marshal(res.Detail)
	if string(detail) != `{"mismatches":{"status":{"expected":"shipped","got":"lost"}}}` {
		t.Errorf("detail = %s", detail)
	}
}

func TestClassificationGrader(t *testing.T) {
	params := map[string]any{"field": "flag"}
	tests := []struct {
		name    string
		in      scoring.Input
		metrics map[string]float64
	}{
		{"true positive", ok(`{"flag":true}`, `{"flag":true}`), map[string]float64{"correct": 1, "precision": 1, "recall": 1}},
		{"false positive", ok(`{"flag":true}`, `{"flag":false}`), map[string]float64{"correct": 0, "precision": 0}},
		{"false negative", ok(`{"flag":false}`, `{"flag":true}`), map[string]float64{"correct": 0, "recall": 0}},
		{"true negative", ok(`{"flag":false}`, `{"flag":false}`), map[string]float64{"correct": 1}},
		{"failed run missed a positive", scoring.Input{Expected: json.RawMessage(`{"flag":true}`)}, map[string]float64{"correct": 0, "recall": 0}},
		{"no ground truth", ok(`{"flag":true}`, ``), map[string]float64{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := grade(t, "classification", tt.in, params)
			if len(got) != len(tt.metrics) {
				t.Fatalf("metrics = %v, want %v", got, tt.metrics)
			}
			for name, want := range tt.metrics {
				if v, ok := got[name]; !ok || v != want {
					t.Errorf("%s = %v (present %v), want %v", name, v, ok, want)
				}
			}
		})
	}

	// A custom positive value.
	m := grade(t, "classification", ok(`{"action":"escalate"}`, `{"action":"escalate"}`), map[string]any{"field": "action", "positive": "escalate"})
	if m["precision"] != 1 || m["recall"] != 1 {
		t.Errorf("custom positive = %v", m)
	}
	if _, err := scoring.Builtin()["classification"].Grade(context.Background(), &scoring.Input{}, nil); err == nil {
		t.Error("missing field parameter was accepted")
	}
}

const scoredSpec = `
name: scored
model: {provider: scripted, name: m}
instructions: Work.
kpis:
  - {name: accuracy, grader: expected, target: ">= 0.8"}
  - {name: field_accuracy, grader: expected, metric: field_accuracy}
  - {name: precision, grader: classification, metric: precision, params: {field: flag}, target: ">= 0.9"}
  - {name: p95_cost, grader: cost, aggregate: p95, target: "<= 0.05"}
  - {name: total_cost, grader: cost, aggregate: sum}
review: {window: 10, min_runs: 4}
`

func mustSpec(t *testing.T, yaml string) *spec.Spec {
	t.Helper()
	s, err := spec.Parse([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestGradeRun(t *testing.T) {
	s := mustSpec(t, scoredSpec)
	calls := 0
	reg := scoring.Builtin()
	inner := reg["expected"]
	reg["expected"] = scoring.GraderFunc{Metric: "match", Func: func(ctx context.Context, in *scoring.Input, p map[string]any) (scoring.Result, error) {
		calls++
		return inner.Grade(ctx, in, p)
	}}

	in := ok(`{"flag":false,"status":"lost"}`, `{"flag":false,"status":"shipped"}`)
	in.CostUSD = 0.03
	scores, err := scoring.GradeRun(context.Background(), reg, s, &in)
	if err != nil {
		t.Fatal(err)
	}

	got := map[string]float64{}
	for _, sc := range scores {
		got[sc.KPI] = sc.Value
	}
	want := map[string]float64{"accuracy": 0, "field_accuracy": 0.5, "p95_cost": 0.03, "total_cost": 0.03}
	if len(got) != len(want) {
		t.Fatalf("scores = %v, want %v (precision does not apply: nothing was flagged)", got, want)
	}
	for kpi, v := range want {
		if got[kpi] != v {
			t.Errorf("%s = %v, want %v", kpi, got[kpi], v)
		}
	}
	if calls != 1 {
		t.Errorf("expected grader ran %d times for two KPIs with the same parameters, want 1", calls)
	}
	for _, sc := range scores {
		if sc.KPI == "accuracy" && !strings.Contains(string(sc.Detail), "mismatches") {
			t.Errorf("accuracy detail = %s", sc.Detail)
		}
	}
}

func TestGradeRunKeepsGoodScoresWhenAGraderFails(t *testing.T) {
	s := mustSpec(t, `
name: scored
model: {provider: scripted, name: m}
instructions: Work.
kpis:
  - {name: cost, grader: cost}
  - {name: quality, grader: nonexistent}
  - {name: broken, grader: broken}
`)
	reg := scoring.Builtin()
	reg["broken"] = scoring.GraderFunc{Metric: "x", Func: func(context.Context, *scoring.Input, map[string]any) (scoring.Result, error) {
		return scoring.Result{}, errors.New("grader exploded")
	}}
	scores, err := scoring.GradeRun(context.Background(), reg, s, &scoring.Input{Succeeded: true, CostUSD: 1})
	if len(scores) != 1 || scores[0].KPI != "cost" {
		t.Errorf("scores = %+v, want the cost score to survive", scores)
	}
	if err == nil || !strings.Contains(err.Error(), `no grader named "nonexistent"`) || !strings.Contains(err.Error(), "grader exploded") {
		t.Errorf("err = %v, want both problems reported", err)
	}
}

func TestAggregate(t *testing.T) {
	values := []float64{5, 1, 4, 2, 3, 10, 9, 8, 7, 6}
	tests := map[string]float64{"mean": 5.5, "sum": 55, "min": 1, "max": 10, "p50": 5, "p95": 10}
	for kind, want := range tests {
		if got := scoring.Aggregate(kind, values); got != want {
			t.Errorf("%s = %v, want %v", kind, got, want)
		}
	}
	if got := scoring.Aggregate("p95", []float64{0.2}); got != 0.2 {
		t.Errorf("p95 of one value = %v", got)
	}
	if values[0] != 5 {
		t.Error("Aggregate reordered its input")
	}
}

func runsWith(n int, values map[string]float64) []map[string]float64 {
	runs := make([]map[string]float64, n)
	for i := range runs {
		runs[i] = values
	}
	return runs
}

func TestComputeAndReview(t *testing.T) {
	s := mustSpec(t, scoredSpec)

	// Six runs: accuracy 4/6, precision present on only three of them.
	runs := append(
		runsWith(3, map[string]float64{"accuracy": 1, "precision": 1, "p95_cost": 0.01, "total_cost": 0.01}),
		runsWith(1, map[string]float64{"accuracy": 1, "p95_cost": 0.02, "total_cost": 0.02})...,
	)
	runs = append(runs, runsWith(2, map[string]float64{"accuracy": 0, "p95_cost": 0.04, "total_cost": 0.04})...)

	card := scoring.Compute(s, runs)
	if card.Runs != 6 {
		t.Fatalf("Runs = %d", card.Runs)
	}
	byName := map[string]scoring.KPIResult{}
	for _, k := range card.KPIs {
		byName[k.Name] = k
	}

	if k := byName["accuracy"]; k.Status != scoring.StatusBreach || k.Samples != 6 || math.Abs(k.Value-4.0/6) > 1e-9 {
		t.Errorf("accuracy = %+v", k)
	}
	if k := byName["precision"]; k.Status != scoring.StatusInsufficient || k.Samples != 3 || k.Value != 1 {
		t.Errorf("precision = %+v, want insufficient data with 3 of 4 samples", k)
	}
	if k := byName["p95_cost"]; k.Status != scoring.StatusOK || k.Value != 0.04 {
		t.Errorf("p95_cost = %+v", k)
	}
	if k := byName["total_cost"]; k.Status != scoring.StatusNoTarget || math.Abs(k.Value-0.13) > 1e-9 {
		t.Errorf("total_cost = %+v", k)
	}
	if k := byName["field_accuracy"]; k.Samples != 0 || k.Value != 0 || k.Status != scoring.StatusNoTarget {
		t.Errorf("field_accuracy = %+v", k)
	}

	verdict := scoring.Review(s, card)
	if verdict.Action != spec.OnBreachPause || len(verdict.Breaches) != 1 {
		t.Fatalf("verdict = %+v", verdict)
	}
	if want := "accuracy is 0.6667 over 6 runs (target >= 0.8)"; verdict.Reason != want {
		t.Errorf("reason = %q, want %q", verdict.Reason, want)
	}

	// With every target met there is nothing to do.
	healthy := scoring.Compute(s, runsWith(6, map[string]float64{"accuracy": 1, "precision": 1, "p95_cost": 0.01}))
	if v := scoring.Review(s, healthy); v.Action != "" || v.Reason != "" {
		t.Errorf("healthy verdict = %+v", v)
	}

	// Too few runs never breach, however bad they are.
	young := scoring.Compute(s, runsWith(3, map[string]float64{"accuracy": 0, "p95_cost": 9}))
	if v := scoring.Review(s, young); v.Action != "" {
		t.Errorf("verdict on 3 runs = %+v, want none below min_runs", v)
	}
}

func TestReviewRespectsAlertPolicy(t *testing.T) {
	s := mustSpec(t, strings.Replace(scoredSpec, "min_runs: 4", "min_runs: 4, on_breach: alert", 1))
	card := scoring.Compute(s, runsWith(5, map[string]float64{"accuracy": 0}))
	if v := scoring.Review(s, card); v.Action != spec.OnBreachAlert {
		t.Fatalf("action = %q, want alert", v.Action)
	}
}

func TestJudge(t *testing.T) {
	params := map[string]any{
		"rubric": "5 if the description names the material and colour.",
		"model":  map[string]any{"provider": "scripted", "name": "judge-model", "effort": "low"},
	}
	in := scoring.Input{
		Succeeded:    true,
		Instructions: "Write product descriptions.",
		Input:        json.RawMessage(`{"sku":"TEE-1"}`),
		Output:       json.RawMessage(`{"description":"A blue cotton tee."}`),
	}

	model := llmtest.NewScripted(llmtest.Call("grade", map[string]any{"score": 4, "reasoning": "Names both."}))
	judge := scoring.Judge{Providers: llm.Registry{"scripted": model}}

	res, err := judge.Grade(context.Background(), &in, params)
	if err != nil {
		t.Fatal(err)
	}
	if res.Metrics["score"] != 4 {
		t.Errorf("score = %v", res.Metrics)
	}
	detail, _ := json.Marshal(res.Detail)
	if !strings.Contains(string(detail), "Names both.") || !strings.Contains(string(detail), "scripted/judge-model") {
		t.Errorf("detail = %s", detail)
	}

	req := model.Requests()[0]
	if req.Model.Name != "judge-model" || req.Model.Effort != "low" {
		t.Errorf("judge model = %+v", req.Model)
	}
	prompt := req.Messages[0].Text()
	for _, want := range []string{"Write product descriptions.", `{"sku":"TEE-1"}`, "A blue cotton tee.", "names the material and colour"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("judge prompt is missing %q", want)
		}
	}
	if len(req.Tools) != 1 || req.Tools[0].Name != "grade" {
		t.Errorf("judge tools = %+v", req.Tools)
	}

	// Failed runs are not judged, and cost no model call.
	if res, err := judge.Grade(context.Background(), &scoring.Input{}, params); err != nil || len(res.Metrics) != 0 {
		t.Errorf("failed run: %v, %v", res.Metrics, err)
	}
	if got := len(model.Requests()); got != 1 {
		t.Errorf("judge made %d model calls, want 1", got)
	}

	failures := map[string]*llm.Response{
		"did not call the grade tool": llmtest.Say("Looks fine to me."),
		"outside 1-5":                 llmtest.Call("grade", map[string]any{"score": 9, "reasoning": "!"}),
		"declined":                    {StopReason: llm.StopRefusal, Message: llm.Message{Role: llm.RoleAssistant}},
	}
	for want, resp := range failures {
		judge := scoring.Judge{Providers: llm.Registry{"scripted": llmtest.NewScripted(resp)}}
		if _, err := judge.Grade(context.Background(), &in, params); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("err = %v, want it to mention %q", err, want)
		}
	}

	for name, bad := range map[string]map[string]any{
		"no rubric": {"model": params["model"]},
		"no model":  {"rubric": "x"},
		"bad model": {"rubric": "x", "model": map[string]any{"provider": "scripted"}},
	} {
		if _, err := judge.Grade(context.Background(), &in, bad); err == nil {
			t.Errorf("%s: params were accepted", name)
		}
	}
}
