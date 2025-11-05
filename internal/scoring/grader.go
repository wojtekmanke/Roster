// Package scoring turns finished runs into numbers and numbers into
// decisions: graders score a run, a scorecard aggregates scores over a
// window, and a review compares the scorecard with the agent's targets.
package scoring

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
)

// Input is everything a grader may look at for one run.
type Input struct {
	// Succeeded is false when the run failed.
	Succeeded bool
	Error     string
	Input     json.RawMessage
	// Expected is the ground truth attached to the run, if any.
	Expected json.RawMessage
	Output   json.RawMessage
	CostUSD  float64
	ActiveMS int64
	LLMCalls int
	// ToolCalls counts executed and simulated tool calls.
	ToolCalls int
	// Approvals holds one entry per decided approval: true if approved.
	Approvals []bool
	// Instructions is the agent's system prompt, for graders that judge
	// whether the output did what was asked.
	Instructions string
}

// Result is what a grader produces for one run.
type Result struct {
	// Metrics maps metric names to values. A metric that does not apply to
	// this run is simply absent; it then contributes no sample.
	Metrics map[string]float64
	// Detail is optional context for a person reading the score.
	Detail any
}

// Grader scores a run.
type Grader interface {
	// Primary names the metric a KPI gets when it does not choose one.
	Primary() string
	Grade(ctx context.Context, in *Input, params map[string]any) (Result, error)
}

// Registry maps grader names to graders.
type Registry map[string]Grader

// Names returns the registered grader names, sorted.
func (r Registry) Names() []string {
	names := make([]string, 0, len(r))
	for name := range r {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Builtin returns the graders that need nothing but the run itself.
func Builtin() Registry {
	return Registry{
		"outcome":        outcomeGrader{},
		"cost":           costGrader{},
		"latency":        latencyGrader{},
		"steps":          stepsGrader{},
		"approvals":      approvalsGrader{},
		"expected":       expectedGrader{},
		"classification": classificationGrader{},
	}
}

// GraderFunc adapts a function to a single-metric Grader.
type GraderFunc struct {
	Metric string
	Func   func(ctx context.Context, in *Input, params map[string]any) (Result, error)
}

// Primary implements Grader.
func (g GraderFunc) Primary() string { return g.Metric }

// Grade implements Grader.
func (g GraderFunc) Grade(ctx context.Context, in *Input, params map[string]any) (Result, error) {
	return g.Func(ctx, in, params)
}

func bool01(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

// outcomeGrader reports whether the run finished without failing.
type outcomeGrader struct{}

func (outcomeGrader) Primary() string { return "success" }

func (outcomeGrader) Grade(_ context.Context, in *Input, _ map[string]any) (Result, error) {
	return Result{Metrics: map[string]float64{"success": bool01(in.Succeeded)}}, nil
}

// costGrader reports the run's model spend.
type costGrader struct{}

func (costGrader) Primary() string { return "usd" }

func (costGrader) Grade(_ context.Context, in *Input, _ map[string]any) (Result, error) {
	return Result{Metrics: map[string]float64{"usd": in.CostUSD}}, nil
}

// latencyGrader reports how long the run spent executing.
type latencyGrader struct{}

func (latencyGrader) Primary() string { return "seconds" }

func (latencyGrader) Grade(_ context.Context, in *Input, _ map[string]any) (Result, error) {
	return Result{Metrics: map[string]float64{"seconds": float64(in.ActiveMS) / 1000}}, nil
}

// stepsGrader reports how much work the run took.
type stepsGrader struct{}

func (stepsGrader) Primary() string { return "llm_calls" }

func (stepsGrader) Grade(_ context.Context, in *Input, _ map[string]any) (Result, error) {
	return Result{Metrics: map[string]float64{
		"llm_calls":  float64(in.LLMCalls),
		"tool_calls": float64(in.ToolCalls),
	}}, nil
}

// approvalsGrader reports the share of the run's approval requests that a
// person approved. A low value means reviewers keep overriding the agent.
type approvalsGrader struct{}

func (approvalsGrader) Primary() string { return "approved" }

func (approvalsGrader) Grade(_ context.Context, in *Input, _ map[string]any) (Result, error) {
	if len(in.Approvals) == 0 {
		return Result{}, nil
	}
	approved := 0
	for _, ok := range in.Approvals {
		if ok {
			approved++
		}
	}
	return Result{Metrics: map[string]float64{"approved": float64(approved) / float64(len(in.Approvals))}}, nil
}

// expectedGrader compares fields of the run's output with its ground truth.
//
// Params:
//
//	fields:    which fields to compare (default: every field in the ground truth)
//	tolerance: relative tolerance for numbers (default 0, exact)
//
// Metrics: match (1 when every compared field agrees) and field_accuracy (the
// share of compared fields that agree). A failed run scores 0 on both: it was
// asked a question that has an answer and did not give one.
type expectedGrader struct{}

func (expectedGrader) Primary() string { return "match" }

func (expectedGrader) Grade(_ context.Context, in *Input, params map[string]any) (Result, error) {
	expected, ok := decodeObject(in.Expected)
	if !ok {
		return Result{}, nil // nothing to compare against
	}
	fields, err := stringList(params, "fields")
	if err != nil {
		return Result{}, err
	}
	if fields == nil {
		for name := range expected {
			fields = append(fields, name)
		}
		sort.Strings(fields)
	}
	tolerance, err := floatParam(params, "tolerance", 0)
	if err != nil {
		return Result{}, err
	}

	output, _ := decodeObject(in.Output)
	var compared, agreed int
	mismatches := map[string]any{}
	for _, field := range fields {
		want, has := expected[field]
		if !has {
			continue // the ground truth says nothing about this field
		}
		compared++
		got, present := output[field]
		if in.Succeeded && present && valuesEqual(want, got, tolerance) {
			agreed++
			continue
		}
		mismatches[field] = map[string]any{"expected": want, "got": got}
	}
	if compared == 0 {
		return Result{}, nil
	}
	res := Result{Metrics: map[string]float64{
		"match":          bool01(agreed == compared),
		"field_accuracy": float64(agreed) / float64(compared),
	}}
	if len(mismatches) > 0 {
		res.Detail = map[string]any{"mismatches": mismatches}
	}
	return res, nil
}

// classificationGrader scores a yes/no decision against ground truth.
//
// Params:
//
//	field:    the boolean field to compare (required)
//	positive: the value that counts as a positive (default true)
//
// Metrics: correct, plus precision and recall "hits". precision is emitted
// only when the agent said yes, and is 1 when that was right; its mean over a
// window is therefore TP/(TP+FP). recall is emitted only when the truth was
// yes, and is 1 when the agent caught it; its mean is TP/(TP+FN). A failed
// run counts as a no: nothing was flagged.
type classificationGrader struct{}

func (classificationGrader) Primary() string { return "correct" }

func (classificationGrader) Grade(_ context.Context, in *Input, params map[string]any) (Result, error) {
	field, _ := params["field"].(string)
	if field == "" {
		return Result{}, fmt.Errorf("classification grader: params.field is required")
	}
	var positive any = true
	if p, ok := params["positive"]; ok {
		positive = p
	}

	expected, ok := decodeObject(in.Expected)
	if !ok {
		return Result{}, nil
	}
	want, has := expected[field]
	if !has {
		return Result{}, nil
	}
	output, _ := decodeObject(in.Output)
	actual := valuesEqual(positive, want, 0)
	predicted := in.Succeeded && valuesEqual(positive, output[field], 0)

	metrics := map[string]float64{"correct": bool01(actual == predicted)}
	if predicted {
		metrics["precision"] = bool01(actual)
	}
	if actual {
		metrics["recall"] = bool01(predicted)
	}
	return Result{
		Metrics: metrics,
		Detail:  map[string]any{"expected": actual, "predicted": predicted},
	}, nil
}

func decodeObject(raw json.RawMessage) (map[string]any, bool) {
	if len(raw) == 0 {
		return nil, false
	}
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil || obj == nil {
		return nil, false
	}
	return obj, true
}

// valuesEqual compares two decoded JSON values. Numbers agree within a
// relative tolerance, strings ignore case and surrounding space, and
// everything else must be structurally identical.
func valuesEqual(want, got any, tolerance float64) bool {
	switch w := want.(type) {
	case float64:
		g, ok := toFloat(got)
		if !ok {
			return false
		}
		if w == g {
			return true
		}
		return math.Abs(w-g) <= tolerance*math.Abs(w)
	case int:
		return valuesEqual(float64(w), got, tolerance)
	case string:
		g, ok := got.(string)
		return ok && strings.EqualFold(strings.TrimSpace(w), strings.TrimSpace(g))
	case bool:
		g, ok := got.(bool)
		return ok && w == g
	case nil:
		return got == nil
	}
	a, errA := json.Marshal(want)
	b, errB := json.Marshal(got)
	return errA == nil && errB == nil && string(a) == string(b)
}

func toFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	}
	return 0, false
}

func stringList(params map[string]any, key string) ([]string, error) {
	raw, ok := params[key]
	if !ok {
		return nil, nil
	}
	list, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("params.%s must be a list of strings", key)
	}
	out := make([]string, 0, len(list))
	for _, item := range list {
		s, ok := item.(string)
		if !ok {
			return nil, fmt.Errorf("params.%s must be a list of strings", key)
		}
		out = append(out, s)
	}
	return out, nil
}

func floatParam(params map[string]any, key string, fallback float64) (float64, error) {
	raw, ok := params[key]
	if !ok {
		return fallback, nil
	}
	f, ok := toFloat(raw)
	if !ok {
		return 0, fmt.Errorf("params.%s must be a number", key)
	}
	return f, nil
}
