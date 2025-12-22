// Package fleet is Roster's control plane: it accepts agent specs, turns
// triggers into queued work, executes that work, grades the results and acts
// on the scorecards.
package fleet

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"strings"
	"time"

	"github.com/wojtekmanke/roster/internal/agent"
	"github.com/wojtekmanke/roster/internal/scoring"
	"github.com/wojtekmanke/roster/internal/spec"
	"github.com/wojtekmanke/roster/internal/store"
	"github.com/wojtekmanke/roster/internal/tool"
)

// Actor names recorded in the audit trail for things Roster does itself.
const (
	ActorReview = "review"
)

// Trigger labels stored on runs.
const (
	TriggerManual   = "manual"
	TriggerWebhook  = "webhook"
	TriggerSchedule = "schedule"
	triggerEvent    = "event:"
)

// ErrAgentNotActive is returned when work is requested from a paused or
// retired agent.
var ErrAgentNotActive = errors.New("agent is not active")

// Service wires the store, the agent runner and the graders together.
type Service struct {
	Store   *store.Store
	Runner  *agent.Runner
	Tools   *tool.Builder
	Graders scoring.Registry
	Log     *slog.Logger
	// Now is the clock. Nil means time.Now.
	Now func() time.Time
	// Rand samples shadow runs. Nil means math/rand.
	Rand func() float64
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Service) random() float64 {
	if s.Rand != nil {
		return s.Rand()
	}
	return rand.Float64()
}

func (s *Service) log() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// Apply validates and stores an agent spec. Beyond the spec's own rules it
// checks that every KPI names a grader that exists, so a typo cannot leave an
// agent unjudged.
func (s *Service) Apply(ctx context.Context, yaml []byte, actor string) (*store.Agent, bool, error) {
	parsed, err := spec.Parse(yaml)
	if err != nil {
		return nil, false, err
	}
	var unknown []string
	for _, kpi := range parsed.KPIs {
		if _, ok := s.Graders[kpi.Grader]; !ok {
			unknown = append(unknown, fmt.Sprintf("kpi %q uses unknown grader %q", kpi.Name, kpi.Grader))
		}
	}
	if len(unknown) > 0 {
		return nil, false, fmt.Errorf("spec %q is invalid: %s (graders: %s)",
			parsed.Name, strings.Join(unknown, "; "), strings.Join(s.Graders.Names(), ", "))
	}
	return s.Store.ApplyAgent(ctx, yaml, actor)
}

// Trigger queues one run of the named agent.
func (s *Service) Trigger(ctx context.Context, name, trigger string, input, expected json.RawMessage) error {
	a, err := s.Store.Agent(ctx, name)
	if err != nil {
		return err
	}
	if a.Status != store.AgentActive {
		return fmt.Errorf("%w: %s is %s", ErrAgentNotActive, a.Name, a.Status)
	}
	_, err = s.Store.Enqueue(ctx, store.NewJob{
		AgentID: a.ID, Kind: store.JobRun, Trigger: trigger, Input: input, Expected: expected,
	})
	return err
}

// Emit delivers an event to every active agent that subscribes to it and
// returns how many runs were queued. expected, when given, is ground truth
// for grading and is never shown to the agents.
func (s *Service) Emit(ctx context.Context, event string, payload, expected json.RawMessage) (int, error) {
	agents, err := s.Store.Agents(ctx)
	if err != nil {
		return 0, err
	}
	queued := 0
	for _, a := range agents {
		if a.Status != store.AgentActive {
			continue
		}
		sp, err := a.Spec()
		if err != nil {
			s.log().Error("stored spec no longer parses", "agent", a.Name, "error", err)
			continue
		}
		if !sp.HasEvent(event) {
			continue
		}
		if _, err := s.Store.Enqueue(ctx, store.NewJob{
			AgentID: a.ID, Kind: store.JobRun, Trigger: triggerEvent + event, Input: payload, Expected: expected,
		}); err != nil {
			return queued, err
		}
		queued++
	}
	return queued, nil
}

// SetStatus pauses, resumes or retires the named agent.
func (s *Service) SetStatus(ctx context.Context, name, status, reason, actor string) (*store.Agent, error) {
	a, err := s.Store.Agent(ctx, name)
	if err != nil {
		return nil, err
	}
	return s.Store.SetAgentStatus(ctx, a.ID, status, reason, actor)
}

// Decide records a person's decision on an approval. Deciding the last
// pending approval of a run queues the run to continue.
func (s *Service) Decide(ctx context.Context, approvalID string, approve bool, decidedBy, reason string) (*store.Approval, error) {
	return s.Store.DecideApproval(ctx, approvalID, approve, decidedBy, reason)
}

// Label attaches ground truth to a finished run and grades it again. This is
// how truth that arrives late (a chargeback, a reopened ticket) reaches the
// scorecard.
func (s *Service) Label(ctx context.Context, runID string, expected json.RawMessage) (*store.Run, error) {
	run, err := s.Store.SetExpected(ctx, runID, expected)
	if err != nil {
		return nil, err
	}
	if run.Finished() {
		if err := s.Grade(ctx, run); err != nil {
			return nil, err
		}
	}
	return s.Store.Run(ctx, runID)
}
