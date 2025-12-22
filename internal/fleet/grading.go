package fleet

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/wojtekmanke/roster/internal/scoring"
	"github.com/wojtekmanke/roster/internal/spec"
	"github.com/wojtekmanke/roster/internal/store"
)

// Grade scores a finished run against its agent's KPIs and, for primary runs,
// reviews the agent's scorecard.
func (s *Service) Grade(ctx context.Context, run *store.Run) error {
	a, err := s.Store.AgentByID(ctx, run.AgentID)
	if err != nil {
		return err
	}
	sp, err := a.Spec()
	if err != nil {
		return fmt.Errorf("fleet: agent %s: %w", a.Name, err)
	}

	in := &scoring.Input{
		Succeeded:    run.Status == store.RunSucceeded,
		Error:        run.Error,
		Input:        run.Input,
		Expected:     run.Expected,
		Output:       run.Output,
		CostUSD:      run.CostUSD,
		ActiveMS:     run.ActiveMS,
		LLMCalls:     run.LLMCalls,
		ToolCalls:    run.ToolCalls,
		Instructions: sp.Instructions,
	}
	approvals, err := s.Store.Approvals(ctx, store.ApprovalFilter{RunID: run.ID})
	if err != nil {
		return err
	}
	for _, ap := range approvals {
		if ap.Status != store.ApprovalPending {
			in.Approvals = append(in.Approvals, ap.Status == store.ApprovalApproved)
		}
	}

	scores, gradeErr := scoring.GradeRun(ctx, s.Graders, sp, in)
	if gradeErr != nil {
		// Keep whatever was scored; a broken grader should not hide the rest.
		s.log().Error("grading incomplete", "run", run.ID, "agent", a.Name, "error", gradeErr)
	}
	rows := make([]store.Score, len(scores))
	for i, sc := range scores {
		rows[i] = store.Score{KPI: sc.KPI, Grader: sc.Grader, Metric: sc.Metric, Value: sc.Value, Detail: sc.Detail}
	}
	if err := s.Store.SaveScores(ctx, run, rows); err != nil {
		return err
	}

	if run.IsShadow() {
		return nil // shadow runs inform comparisons, never employment
	}
	return s.review(ctx, a, sp)
}

// Scorecard computes an agent's standing on its primary model.
func (s *Service) Scorecard(ctx context.Context, a *store.Agent, sp *spec.Spec) (scoring.Scorecard, error) {
	return s.scorecard(ctx, a, sp, a.Provider, a.Model, false)
}

func (s *Service) scorecard(ctx context.Context, a *store.Agent, sp *spec.Spec, provider, model string, shadow bool) (scoring.Scorecard, error) {
	runs, err := s.Store.ScoredRuns(ctx, store.ScoreWindow{
		AgentID:  a.ID,
		Since:    a.ReviewSince,
		Limit:    sp.Review.Window,
		Provider: provider,
		Model:    model,
		Shadow:   shadow,
	})
	if err != nil {
		return scoring.Scorecard{}, err
	}
	samples := make([]map[string]float64, len(runs))
	for i, r := range runs {
		samples[i] = r.Scores
	}
	return scoring.Compute(sp, samples), nil
}

// ModelScorecard is one model's results on an agent's work.
type ModelScorecard struct {
	store.ModelUse
	Scorecard scoring.Scorecard
}

// Comparison returns a scorecard for every model the agent has run on in the
// current review window: its own model first, then its shadows. Shadows
// replay the same inputs, so the columns are comparable.
func (s *Service) Comparison(ctx context.Context, a *store.Agent, sp *spec.Spec) ([]ModelScorecard, error) {
	models, err := s.Store.ModelsUsed(ctx, a.ID, a.ReviewSince)
	if err != nil {
		return nil, err
	}
	out := make([]ModelScorecard, 0, len(models))
	for _, m := range models {
		card, err := s.scorecard(ctx, a, sp, m.Provider, m.Model, m.Shadow)
		if err != nil {
			return nil, err
		}
		out = append(out, ModelScorecard{ModelUse: m, Scorecard: card})
	}
	return out, nil
}

// review applies the agent's policy to its current scorecard: an agent that
// misses a target with enough samples is paused, or flagged if its policy is
// to alert.
func (s *Service) review(ctx context.Context, a *store.Agent, sp *spec.Spec) error {
	if a.Status != store.AgentActive {
		return nil
	}
	card, err := s.Scorecard(ctx, a, sp)
	if err != nil {
		return err
	}
	verdict := scoring.Review(sp, card)
	if verdict.Action == "" {
		return nil
	}

	names := make([]string, len(verdict.Breaches))
	for i, b := range verdict.Breaches {
		names[i] = b.Name
	}
	data, _ := json.Marshal(map[string]any{"kpis": names, "runs": card.Runs})

	switch verdict.Action {
	case spec.OnBreachPause:
		if _, err := s.Store.SetAgentStatus(ctx, a.ID, store.AgentPaused, verdict.Reason, ActorReview); err != nil {
			return err
		}
		s.log().Warn("agent paused by review", "agent", a.Name, "reason", verdict.Reason)
	case spec.OnBreachAlert:
		// One event per distinct set of breached KPIs, not one per run.
		events, err := s.Store.AgentEvents(ctx, a.ID, 1)
		if err != nil {
			return err
		}
		if len(events) > 0 && events[0].Kind == store.EventBreach && sameKPIs(events[0].Data, names) {
			return nil
		}
		if err := s.Store.AddAgentEvent(ctx, a.ID, store.EventBreach, ActorReview, verdict.Reason, data); err != nil {
			return err
		}
		s.log().Warn("agent is missing its targets", "agent", a.Name, "reason", verdict.Reason)
	}
	return nil
}

func sameKPIs(data json.RawMessage, names []string) bool {
	var prev struct {
		KPIs []string `json:"kpis"`
	}
	if json.Unmarshal(data, &prev) != nil || len(prev.KPIs) != len(names) {
		return false
	}
	for i := range names {
		if prev.KPIs[i] != names[i] {
			return false
		}
	}
	return true
}
