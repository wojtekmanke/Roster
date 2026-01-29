package graph

import (
	"context"
	"errors"
	"fmt"

	"github.com/wojtekmanke/roster/internal/scoring"
	"github.com/wojtekmanke/roster/internal/store"
)

func (r *mutationResolver) setStatus(ctx context.Context, name, status, reason string) (*store.Agent, error) {
	a, err := r.Svc.SetStatus(ctx, name, status, reason, actor(ctx))
	if errors.Is(err, store.ErrNotFound) {
		return nil, fmt.Errorf("no agent named %q", name)
	}
	return a, err
}

// toScorecard converts a computed scorecard to its GraphQL shape. A KPI with
// no samples has a null value rather than a misleading zero.
func toScorecard(card scoring.Scorecard) *Scorecard {
	out := &Scorecard{Runs: card.Runs, Kpis: make([]*Kpi, len(card.KPIs))}
	for i, k := range card.KPIs {
		kpi := &Kpi{Name: k.Name, Grader: k.Grader, Aggregate: k.Aggregate, Samples: k.Samples, Status: string(k.Status)}
		if k.Samples > 0 {
			value := k.Value
			kpi.Value = &value
		}
		if k.Target != nil {
			target := k.Target.String()
			kpi.Target = &target
		}
		out.Kpis[i] = kpi
	}
	return out
}
