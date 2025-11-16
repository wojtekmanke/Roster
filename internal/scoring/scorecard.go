package scoring

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/wojtekmanke/roster/internal/spec"
)

// Score is the value of one KPI for one run.
type Score struct {
	KPI    string
	Grader string
	Metric string
	Value  float64
	Detail json.RawMessage
}

// GradeRun evaluates every KPI of the spec against one run. A KPI whose
// metric does not apply to the run produces no score. Grader failures are
// collected and returned alongside the scores that did succeed, so one broken
// grader does not hide the others.
func GradeRun(ctx context.Context, reg Registry, s *spec.Spec, in *Input) ([]Score, error) {
	type memoKey struct{ grader, params string }
	memo := map[memoKey]Result{}
	var scores []Score
	var problems []string

	for _, kpi := range s.KPIs {
		grader, ok := reg[kpi.Grader]
		if !ok {
			problems = append(problems, fmt.Sprintf("kpi %q: no grader named %q (have: %s)", kpi.Name, kpi.Grader, strings.Join(reg.Names(), ", ")))
			continue
		}
		// KPIs that share a grader and parameters share one evaluation,
		// which matters when the grader is itself a model call.
		params, _ := json.Marshal(kpi.Params)
		key := memoKey{kpi.Grader, string(params)}
		result, done := memo[key]
		if !done {
			var err error
			if result, err = grader.Grade(ctx, in, kpi.Params); err != nil {
				problems = append(problems, fmt.Sprintf("kpi %q: %v", kpi.Name, err))
				continue
			}
			memo[key] = result
		}

		metric := kpi.Metric
		if metric == "" {
			metric = grader.Primary()
		}
		value, ok := result.Metrics[metric]
		if !ok {
			continue
		}
		score := Score{KPI: kpi.Name, Grader: kpi.Grader, Metric: metric, Value: value}
		if result.Detail != nil {
			score.Detail, _ = json.Marshal(result.Detail)
		}
		scores = append(scores, score)
	}

	if len(problems) > 0 {
		return scores, fmt.Errorf("scoring: %s", strings.Join(problems, "; "))
	}
	return scores, nil
}

// KPIStatus is how a KPI stands against its target.
type KPIStatus string

const (
	// StatusOK means the target is met.
	StatusOK KPIStatus = "ok"
	// StatusBreach means the target is missed with enough samples to act on.
	StatusBreach KPIStatus = "breach"
	// StatusInsufficient means there are too few samples to judge.
	StatusInsufficient KPIStatus = "insufficient_data"
	// StatusNoTarget means the KPI is tracked but has no target.
	StatusNoTarget KPIStatus = "no_target"
)

// KPIResult is one line of a scorecard.
type KPIResult struct {
	Name      string
	Grader    string
	Aggregate string
	// Value is the aggregate over Samples runs. It is zero when Samples is zero.
	Value   float64
	Samples int
	Target  *spec.Target
	Status  KPIStatus
}

// Scorecard is an agent's standing over a window of runs.
type Scorecard struct {
	// Runs is the number of graded runs in the window.
	Runs int
	KPIs []KPIResult
}

// Compute builds a scorecard from per-run KPI values. Each element of runs
// maps KPI names to that run's values; a run may lack some KPIs.
func Compute(s *spec.Spec, runs []map[string]float64) Scorecard {
	card := Scorecard{Runs: len(runs)}
	for _, kpi := range s.KPIs {
		var values []float64
		for _, run := range runs {
			if v, ok := run[kpi.Name]; ok {
				values = append(values, v)
			}
		}
		result := KPIResult{
			Name:      kpi.Name,
			Grader:    kpi.Grader,
			Aggregate: kpi.Aggregate,
			Samples:   len(values),
			Target:    kpi.Target,
		}
		if len(values) > 0 {
			result.Value = Aggregate(kpi.Aggregate, values)
		}
		switch {
		case kpi.Target == nil:
			result.Status = StatusNoTarget
		case len(values) < s.Review.MinRuns:
			result.Status = StatusInsufficient
		case kpi.Target.Met(result.Value):
			result.Status = StatusOK
		default:
			result.Status = StatusBreach
		}
		card.KPIs = append(card.KPIs, result)
	}
	return card
}

// Aggregate combines values. kind is one of spec.Aggregates; unknown kinds
// fall back to the mean. It panics on an empty slice.
func Aggregate(kind string, values []float64) float64 {
	switch kind {
	case "sum":
		return sum(values)
	case "min":
		m := values[0]
		for _, v := range values[1:] {
			m = math.Min(m, v)
		}
		return m
	case "max":
		m := values[0]
		for _, v := range values[1:] {
			m = math.Max(m, v)
		}
		return m
	case "p50":
		return percentile(values, 0.50)
	case "p95":
		return percentile(values, 0.95)
	}
	return sum(values) / float64(len(values))
}

func sum(values []float64) float64 {
	var total float64
	for _, v := range values {
		total += v
	}
	return total
}

// percentile uses the nearest-rank method: the smallest value with at least
// p of the samples at or below it.
func percentile(values []float64, p float64) float64 {
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	rank := int(math.Ceil(p * float64(len(sorted))))
	if rank < 1 {
		rank = 1
	}
	return sorted[rank-1]
}

// Breaches returns the KPIs that miss their targets.
func (c Scorecard) Breaches() []KPIResult {
	var out []KPIResult
	for _, kpi := range c.KPIs {
		if kpi.Status == StatusBreach {
			out = append(out, kpi)
		}
	}
	return out
}

// Verdict is the outcome of reviewing a scorecard against the agent's policy.
type Verdict struct {
	Breaches []KPIResult
	// Action is spec.OnBreachPause, spec.OnBreachAlert, or empty when
	// nothing needs doing.
	Action string
	// Reason is a sentence fit for the audit trail.
	Reason string
}

// Review applies the agent's review policy to its scorecard.
func Review(s *spec.Spec, card Scorecard) Verdict {
	breaches := card.Breaches()
	if len(breaches) == 0 {
		return Verdict{}
	}
	parts := make([]string, len(breaches))
	for i, b := range breaches {
		parts[i] = fmt.Sprintf("%s is %s over %d runs (target %s)", b.Name, formatValue(b.Value), b.Samples, b.Target)
	}
	return Verdict{
		Breaches: breaches,
		Action:   s.Review.OnBreach,
		Reason:   strings.Join(parts, "; "),
	}
}

// formatValue prints a KPI value with enough digits to compare with a target
// and no trailing noise.
func formatValue(v float64) string {
	return strconv.FormatFloat(math.Round(v*10000)/10000, 'f', -1, 64)
}
