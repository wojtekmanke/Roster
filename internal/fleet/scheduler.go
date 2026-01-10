package fleet

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/wojtekmanke/roster/internal/schedule"
	"github.com/wojtekmanke/roster/internal/store"
)

const (
	// staleAfter is how long a job may stay claimed before its worker is
	// presumed dead. It must exceed the longest run timeout in use.
	staleAfter = 30 * time.Minute
	// maxCatchUp bounds how far back a tick looks for missed firing times.
	maxCatchUp = 24 * time.Hour
	// gradeBatch is how many ungraded runs one tick will grade.
	gradeBatch = 25
)

// Tick does the periodic housekeeping: it fires due schedules, returns jobs
// held by dead workers to the queue, and grades any finished run that was
// left ungraded. It is safe to call from several instances at once.
func (s *Service) Tick(ctx context.Context) error {
	if _, err := s.FireSchedules(ctx); err != nil {
		return err
	}
	if n, err := s.Store.RequeueStale(ctx, staleAfter); err != nil {
		return err
	} else if n > 0 {
		s.log().Warn("requeued jobs from workers that stopped responding", "jobs", n)
	}
	runs, err := s.Store.UngradedRuns(ctx, gradeBatch)
	if err != nil {
		return err
	}
	for i := range runs {
		if err := s.Grade(ctx, &runs[i]); err != nil {
			s.log().Error("grading failed", "run", runs[i].ID, "error", err)
		}
	}
	return nil
}

// FireSchedules queues a run for every schedule trigger that came due since
// the previous call, and returns how many runs it queued.
//
// A trigger that came due several times while nothing was ticking fires once,
// for its most recent time: an agent that summarises yesterday does not need
// to do so three times because the service was down for three days.
func (s *Service) FireSchedules(ctx context.Context) (int, error) {
	now := s.now()
	last, err := s.Store.SchedulerTick(ctx, now)
	if err != nil {
		return 0, err
	}
	if now.Sub(last) > maxCatchUp {
		last = now.Add(-maxCatchUp)
	}

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
		for i, trigger := range sp.Triggers {
			if trigger.Schedule == "" {
				continue
			}
			sched, err := schedule.Parse(trigger.Schedule)
			if err != nil {
				continue // validated when the spec was applied
			}
			var due time.Time
			for t := sched.Next(last); !t.After(now); t = sched.Next(t) {
				due = t
			}
			if due.IsZero() {
				continue
			}
			var input json.RawMessage
			if trigger.Input != nil {
				if input, err = json.Marshal(trigger.Input); err != nil {
					return queued, err
				}
			}
			// The key makes the enqueue idempotent across instances that
			// tick at the same moment.
			added, err := s.Store.Enqueue(ctx, store.NewJob{
				AgentID:   a.ID,
				Kind:      store.JobRun,
				Trigger:   TriggerSchedule,
				Input:     input,
				DedupeKey: fmt.Sprintf("schedule:%s:%d:%d", a.ID, i, due.Unix()),
			})
			if err != nil {
				return queued, err
			}
			if added {
				queued++
			}
		}
	}
	return queued, nil
}
