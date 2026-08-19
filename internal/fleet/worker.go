package fleet

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/wojtekmanke/roster/internal/agent"
	"github.com/wojtekmanke/roster/internal/llm"
	"github.com/wojtekmanke/roster/internal/spec"
	"github.com/wojtekmanke/roster/internal/store"
)

// interrupted is the error recorded on a run whose worker died mid-flight.
// Such runs are failed rather than restarted: restarting could repeat side
// effects the first attempt already carried out.
const interrupted = "interrupted: the worker stopped while this run was executing"

// WorkOne claims and executes one job. It returns false when the queue had
// nothing ready.
func (s *Service) WorkOne(ctx context.Context, workerID string) (bool, error) {
	job, err := s.Store.Dequeue(ctx, workerID)
	if errors.Is(err, store.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}

	failure := ""
	if err := s.handle(ctx, job); err != nil {
		failure = err.Error()
		s.log().Error("job failed", "job", job.ID, "kind", job.Kind, "error", err)
	}
	// The job is finished either way; the run records what happened.
	if err := s.Store.FinishJob(context.WithoutCancel(ctx), job.ID, failure); err != nil {
		return true, err
	}
	return true, nil
}

func (s *Service) handle(ctx context.Context, job *store.Job) error {
	a, err := s.Store.AgentByID(ctx, job.AgentID)
	if err != nil {
		return err
	}
	sp, err := a.Spec()
	if err != nil {
		return fmt.Errorf("agent %s: %w", a.Name, err)
	}

	switch job.Kind {
	case store.JobRun:
		if job.RunID != nil {
			return s.abandon(ctx, *job.RunID)
		}
		run, err := s.Store.CreateRun(ctx, store.NewRun{
			AgentID: a.ID, Trigger: job.Trigger, Input: job.Input, Expected: job.Expected,
			Provider: sp.Model.Provider, Model: sp.Model.Name,
		})
		if err != nil {
			return err
		}
		if err := s.Store.AttachRun(ctx, job.ID, run.ID); err != nil {
			return err
		}
		if err := s.queueShadows(ctx, a, sp, job, run); err != nil {
			return err
		}
		return s.execute(ctx, sp, run, sp.Model, nil, nil)

	case store.JobResume:
		run, err := s.Store.Run(ctx, *job.RunID)
		if err != nil {
			return err
		}
		switch run.Status {
		case store.RunRunning:
			return s.abandon(ctx, run.ID)
		case store.RunAwaitingApproval:
		default:
			return nil // already finished
		}
		var state agent.State
		if err := json.Unmarshal(run.State, &state); err != nil {
			return fmt.Errorf("run %s: stored state is unreadable: %w", run.ID, err)
		}
		decisions, err := s.Store.Decisions(ctx, run.ID)
		if err != nil {
			return err
		}
		if err := s.Store.SetRunRunning(ctx, run.ID); err != nil {
			return err
		}
		// Resume on the model the run started with, even if the spec has
		// since moved to another: a conversation cannot change models midway.
		model := sp.Model
		model.Provider, model.Name = run.Provider, run.Model
		return s.execute(ctx, sp, run, model, &state, decisions)

	case store.JobShadow:
		primary, err := s.Store.Run(ctx, *job.RunID)
		if err != nil {
			return err
		}
		if job.ShadowIndex == nil || *job.ShadowIndex >= len(sp.Shadow) {
			return fmt.Errorf("agent %s no longer has the shadow model this job was queued for", a.Name)
		}
		model := sp.Shadow[*job.ShadowIndex].Model
		if job.Attempts > 1 {
			// A previous attempt may have left its shadow run behind.
			left, err := s.Store.Runs(ctx, store.RunFilter{ShadowOf: primary.ID, Status: store.RunRunning})
			if err != nil {
				return err
			}
			for _, run := range left {
				if run.Provider == model.Provider && run.Model == model.Name {
					return s.abandon(ctx, run.ID)
				}
			}
		}
		run, err := s.Store.CreateRun(ctx, store.NewRun{
			AgentID: a.ID, Trigger: primary.Trigger, Input: primary.Input, Expected: primary.Expected,
			Provider: model.Provider, Model: model.Name, ShadowOf: &primary.ID,
		})
		if err != nil {
			return err
		}
		return s.execute(ctx, sp, run, model, nil, nil)
	}
	return fmt.Errorf("unknown job kind %q", job.Kind)
}

// queueShadows queues a replay of the run on each shadow model, sampled at
// the configured rate.
//
// A shadow takes its primary's place in the queue rather than joining the
// back. Behind a backlog, shadows would otherwise trail their primaries by
// the whole backlog, and a model comparison would be comparing different
// stretches of work.
func (s *Service) queueShadows(ctx context.Context, a *store.Agent, sp *spec.Spec, job *store.Job, run *store.Run) error {
	for i, shadow := range sp.Shadow {
		if s.random() >= shadow.SampleRate {
			continue
		}
		index := i
		if _, err := s.Store.Enqueue(ctx, store.NewJob{
			AgentID: a.ID, Kind: store.JobShadow, RunID: &run.ID, Trigger: run.Trigger, ShadowIndex: &index,
			RunAt: job.RunAt,
		}); err != nil {
			return err
		}
	}
	return nil
}

// execute runs one segment of a run and stores the outcome.
func (s *Service) execute(ctx context.Context, sp *spec.Spec, run *store.Run, model llm.ModelConfig, state *agent.State, decisions map[string]agent.Decision) error {
	var outcome *agent.Outcome

	tools, err := s.Tools.Build(ctx, sp)
	if err != nil {
		// A tool that cannot be set up is the agent's failure, not the queue's.
		outcome = &agent.Outcome{Status: agent.StatusFailed, Error: "tools: " + err.Error()}
		if state != nil {
			outcome.State = *state
		}
	} else {
		defer tools.Close()
		outcome, err = s.Runner.Execute(ctx, agent.Run{
			ID:        run.ID,
			Spec:      sp,
			Model:     model,
			Shadow:    run.IsShadow(),
			Input:     run.Input,
			State:     state,
			Decisions: decisions,
		}, tools)
		if err != nil {
			s.Store.FailRun(context.WithoutCancel(ctx), run.ID, "internal error: "+err.Error())
			return err
		}
	}

	saved, err := s.Store.SaveOutcome(context.WithoutCancel(ctx), run, outcome)
	if err != nil {
		return err
	}
	s.log().Info("run "+saved.Status, "run", saved.ID, "agent", sp.Name, "model", saved.Model,
		"shadow", saved.IsShadow(), "cost_usd", saved.CostUSD, "error", saved.Error)
	if saved.Finished() {
		return s.Grade(context.WithoutCancel(ctx), saved)
	}
	return nil
}

// abandon fails a run left behind by a dead worker and grades it, so the
// failure counts against the agent like any other.
func (s *Service) abandon(ctx context.Context, runID string) error {
	if err := s.Store.FailRun(ctx, runID, interrupted); err != nil {
		return err
	}
	run, err := s.Store.Run(ctx, runID)
	if err != nil {
		return err
	}
	if run.Finished() && run.GradedAt == nil {
		return s.Grade(ctx, run)
	}
	return nil
}

// Drain executes jobs until the queue has nothing ready. It is used by tests,
// by the CLI, and by the HTTP endpoint that lets an external scheduler drive
// a scale-to-zero deployment.
func (s *Service) Drain(ctx context.Context, workerID string, max int) (int, error) {
	done := 0
	for max <= 0 || done < max {
		worked, err := s.WorkOne(ctx, workerID)
		if err != nil {
			return done, err
		}
		if !worked {
			break
		}
		done++
	}
	return done, nil
}

// WorkerOptions configure Run.
type WorkerOptions struct {
	// Concurrency is the number of jobs executed at once. Default 4.
	Concurrency int
	// Poll is how long an idle worker waits before looking again. Default 1s.
	Poll time.Duration
	// TickEvery is how often schedules, stale jobs and ungraded runs are
	// checked. Default 30s.
	TickEvery time.Duration
	// ID distinguishes this process's claims in the jobs table.
	ID string
}

// Run executes jobs and housekeeping until ctx is cancelled, then waits for
// jobs in flight to finish. In-flight jobs are not cancelled with ctx: the
// caller decides how long to wait before exiting.
func (s *Service) Run(ctx context.Context, opts WorkerOptions) {
	if opts.Concurrency <= 0 {
		opts.Concurrency = 4
	}
	if opts.Poll <= 0 {
		opts.Poll = time.Second
	}
	if opts.TickEvery <= 0 {
		opts.TickEvery = 30 * time.Second
	}
	if opts.ID == "" {
		opts.ID = "worker"
	}
	jobCtx := context.WithoutCancel(ctx)

	var wg sync.WaitGroup
	for i := range opts.Concurrency {
		wg.Add(1)
		go func() {
			defer wg.Done()
			workerID := fmt.Sprintf("%s-%d", opts.ID, i)
			for ctx.Err() == nil {
				worked, err := s.WorkOne(jobCtx, workerID)
				if err != nil {
					s.log().Error("worker", "error", err)
				}
				if worked && err == nil {
					continue
				}
				select {
				case <-ctx.Done():
				case <-time.After(opts.Poll):
				}
			}
		}()
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(opts.TickEvery)
		defer ticker.Stop()
		for {
			if err := s.Tick(ctx); err != nil && ctx.Err() == nil {
				s.log().Error("tick", "error", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	wg.Wait()
}
