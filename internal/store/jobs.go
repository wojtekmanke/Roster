package store

import (
	"context"
	"encoding/json"
	"time"
)

// Job kinds.
const (
	JobRun    = "run"
	JobResume = "resume"
	JobShadow = "shadow"
)

// Job is one unit of queued work.
type Job struct {
	ID          int64           `db:"id"`
	AgentID     string          `db:"agent_id"`
	Kind        string          `db:"kind"`
	RunID       *string         `db:"run_id"`
	Trigger     string          `db:"trigger"`
	Input       json.RawMessage `db:"input"`
	Expected    json.RawMessage `db:"expected"`
	ShadowIndex *int            `db:"shadow_index"`
	Status      string          `db:"status"`
	Attempts    int             `db:"attempts"`
	RunAt       time.Time       `db:"run_at"`
	LastError   string          `db:"last_error"`
	CreatedAt   time.Time       `db:"created_at"`
}

const jobColumns = `id, agent_id, kind, run_id, trigger, input, expected, shadow_index,
	status, attempts, run_at, last_error, created_at`

// NewJob describes a job to enqueue.
type NewJob struct {
	AgentID     string
	Kind        string
	RunID       *string
	Trigger     string
	Input       json.RawMessage
	Expected    json.RawMessage
	ShadowIndex *int
	// DedupeKey, when set, makes a second enqueue with the same key a no-op.
	DedupeKey string
	// RunAt is the job's place in the queue. Zero means now, the back of it.
	RunAt time.Time
}

// Enqueue adds a job. It returns false, with no error, when a job with the
// same dedupe key already exists.
func (s *Store) Enqueue(ctx context.Context, n NewJob) (bool, error) {
	var dedupe *string
	if n.DedupeKey != "" {
		dedupe = &n.DedupeKey
	}
	var runAt *time.Time
	if !n.RunAt.IsZero() {
		runAt = &n.RunAt
	}
	tag, err := s.Pool.Exec(ctx, `
		insert into jobs (agent_id, kind, run_id, trigger, input, expected, shadow_index, dedupe_key, run_at)
		values ($1, $2, $3, $4, $5, $6, $7, $8, coalesce($9, now()))
		on conflict (dedupe_key) do nothing`,
		n.AgentID, n.Kind, n.RunID, n.Trigger, jsonArg(n.Input), jsonArg(n.Expected), n.ShadowIndex, dedupe, runAt)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// Dequeue claims the oldest ready job of an active agent, or returns
// ErrNotFound when there is none. Jobs of paused agents stay queued: a paused
// agent does nothing, including finishing runs that were waiting on approval.
func (s *Store) Dequeue(ctx context.Context, workerID string) (*Job, error) {
	return queryOne[Job](ctx, s.Pool, `
		update jobs set status = 'running', attempts = attempts + 1, locked_by = $1, locked_at = now()
		where id = (
			select j.id from jobs j
			join agents a on a.id = j.agent_id
			where j.status = 'queued' and j.run_at <= now() and a.status = 'active'
			order by j.run_at, j.id
			for update of j skip locked
			limit 1
		)
		returning `+jobColumns, workerID)
}

// AttachRun links a run job to the run it created, so a redelivered job can
// find the run it already started.
func (s *Store) AttachRun(ctx context.Context, jobID int64, runID string) error {
	_, err := s.Pool.Exec(ctx, `update jobs set run_id = $2 where id = $1`, jobID, runID)
	return err
}

// FinishJob marks a claimed job done, or failed with the given reason.
func (s *Store) FinishJob(ctx context.Context, jobID int64, failure string) error {
	status := "done"
	if failure != "" {
		status = "failed"
	}
	_, err := s.Pool.Exec(ctx, `
		update jobs set status = $2, last_error = $3, finished_at = now(), locked_by = null, locked_at = null
		where id = $1`, jobID, status, failure)
	return err
}

// RequeueStale returns jobs to the queue whose worker claimed them longer
// than olderThan ago and never finished, which means the worker died.
func (s *Store) RequeueStale(ctx context.Context, olderThan time.Duration) (int64, error) {
	tag, err := s.Pool.Exec(ctx, `
		update jobs set status = 'queued', locked_by = null, locked_at = null
		where status = 'running' and locked_at < now() - make_interval(secs => $1)`, olderThan.Seconds())
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// QueueDepth counts jobs waiting to run.
func (s *Store) QueueDepth(ctx context.Context) (int, error) {
	var n int
	err := s.Pool.QueryRow(ctx, `select count(*) from jobs where status = 'queued'`).Scan(&n)
	return n, err
}
