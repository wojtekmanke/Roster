package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/wojtekmanke/roster/internal/agent"
	"github.com/wojtekmanke/roster/internal/id"
)

// Run statuses.
const (
	RunRunning          = "running"
	RunAwaitingApproval = "awaiting_approval"
	RunSucceeded        = "succeeded"
	RunFailed           = "failed"
)

// Approval statuses.
const (
	ApprovalPending  = "pending"
	ApprovalApproved = "approved"
	ApprovalRejected = "rejected"
)

// Run is one execution of an agent.
type Run struct {
	ID               string          `db:"id"`
	AgentID          string          `db:"agent_id"`
	Status           string          `db:"status"`
	Trigger          string          `db:"trigger"`
	Input            json.RawMessage `db:"input"`
	Expected         json.RawMessage `db:"expected"`
	Output           json.RawMessage `db:"output"`
	Error            string          `db:"error"`
	Provider         string          `db:"provider"`
	Model            string          `db:"model"`
	ShadowOf         *string         `db:"shadow_of"`
	State            json.RawMessage `db:"state"`
	InputTokens      int64           `db:"input_tokens"`
	OutputTokens     int64           `db:"output_tokens"`
	CacheReadTokens  int64           `db:"cache_read_tokens"`
	CacheWriteTokens int64           `db:"cache_write_tokens"`
	CostUSD          float64         `db:"cost_usd"`
	LLMCalls         int             `db:"llm_calls"`
	ToolCalls        int             `db:"tool_calls"`
	ActiveMS         int64           `db:"active_ms"`
	StartedAt        time.Time       `db:"started_at"`
	FinishedAt       *time.Time      `db:"finished_at"`
	GradedAt         *time.Time      `db:"graded_at"`
}

// IsShadow reports whether the run replays another run's input on a different model.
func (r *Run) IsShadow() bool { return r.ShadowOf != nil }

// Finished reports whether the run has reached a final status.
func (r *Run) Finished() bool { return r.Status == RunSucceeded || r.Status == RunFailed }

// Step is one stored trace entry.
type Step struct {
	RunID            string          `db:"run_id"`
	Seq              int             `db:"seq"`
	Kind             string          `db:"kind"`
	Name             string          `db:"name"`
	Input            json.RawMessage `db:"input"`
	Output           json.RawMessage `db:"output"`
	IsError          bool            `db:"is_error"`
	Simulated        bool            `db:"simulated"`
	InputTokens      int64           `db:"input_tokens"`
	OutputTokens     int64           `db:"output_tokens"`
	CacheReadTokens  int64           `db:"cache_read_tokens"`
	CacheWriteTokens int64           `db:"cache_write_tokens"`
	CostUSD          float64         `db:"cost_usd"`
	LatencyMS        int64           `db:"latency_ms"`
	CreatedAt        time.Time       `db:"created_at"`
}

// Approval is a tool call that needs, or has had, a person's decision.
type Approval struct {
	ID        string          `db:"id"`
	RunID     string          `db:"run_id"`
	AgentID   string          `db:"agent_id"`
	ToolUseID string          `db:"tool_use_id"`
	Tool      string          `db:"tool"`
	Input     json.RawMessage `db:"input"`
	Status    string          `db:"status"`
	DecidedBy string          `db:"decided_by"`
	Reason    string          `db:"reason"`
	CreatedAt time.Time       `db:"created_at"`
	DecidedAt *time.Time      `db:"decided_at"`
}

const runColumns = `id, agent_id, status, trigger, input, expected, output, error, provider, model,
	shadow_of, state, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens,
	cost_usd, llm_calls, tool_calls, active_ms, started_at, finished_at, graded_at`

// NewRun describes a run to create.
type NewRun struct {
	AgentID  string
	Trigger  string
	Input    json.RawMessage
	Expected json.RawMessage
	Provider string
	Model    string
	ShadowOf *string
}

// CreateRun inserts a run in the running status.
func (s *Store) CreateRun(ctx context.Context, n NewRun) (*Run, error) {
	return queryOne[Run](ctx, s.Pool, `
		insert into runs (id, agent_id, status, trigger, input, expected, provider, model, shadow_of)
		values ($1, $2, 'running', $3, $4, $5, $6, $7, $8)
		returning `+runColumns,
		id.New("run"), n.AgentID, n.Trigger, jsonArg(n.Input), jsonArg(n.Expected), n.Provider, n.Model, n.ShadowOf)
}

// Run returns the run with the given ID.
func (s *Store) Run(ctx context.Context, runID string) (*Run, error) {
	return queryOne[Run](ctx, s.Pool, `select `+runColumns+` from runs where id = $1`, runID)
}

// RunFilter narrows Runs. Zero values mean no restriction.
type RunFilter struct {
	AgentID string
	Status  string
	// ShadowOf lists the shadow runs of one primary run.
	ShadowOf string
	// PrimaryOnly excludes shadow runs.
	PrimaryOnly bool
	Limit       int
}

// Runs lists runs, newest first.
func (s *Store) Runs(ctx context.Context, f RunFilter) ([]Run, error) {
	var where []string
	var args []any
	add := func(cond string, arg any) {
		args = append(args, arg)
		where = append(where, fmt.Sprintf(cond, len(args)))
	}
	if f.AgentID != "" {
		add("agent_id = $%d", f.AgentID)
	}
	if f.Status != "" {
		add("status = $%d", f.Status)
	}
	if f.ShadowOf != "" {
		add("shadow_of = $%d", f.ShadowOf)
	}
	if f.PrimaryOnly {
		where = append(where, "shadow_of is null")
	}
	sql := `select ` + runColumns + ` from runs`
	if len(where) > 0 {
		sql += ` where ` + strings.Join(where, " and ")
	}
	limit := f.Limit
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	args = append(args, limit)
	sql += fmt.Sprintf(` order by started_at desc, id desc limit $%d`, len(args))

	rows, err := s.Pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[Run])
}

// SetRunRunning marks a suspended run as executing again.
func (s *Store) SetRunRunning(ctx context.Context, runID string) error {
	_, err := s.Pool.Exec(ctx, `update runs set status = 'running' where id = $1`, runID)
	return err
}

// SaveOutcome stores the result of an execution segment: the run's new
// status, output, totals and conversation state, plus any approvals it is now
// waiting on. Everything is written in one transaction.
func (s *Store) SaveOutcome(ctx context.Context, run *Run, out *agent.Outcome) (*Run, error) {
	state, err := json.Marshal(out.State)
	if err != nil {
		return nil, err
	}
	finished := out.Status == agent.StatusSucceeded || out.Status == agent.StatusFailed

	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	t := out.State.Totals
	updated, err := queryOne[Run](ctx, tx, `
		update runs set
			status = $2, output = $3, error = $4,
			-- A finished run has no use for its conversation state.
			state = case when $5 then null else $6::jsonb end,
			input_tokens = $7, output_tokens = $8, cache_read_tokens = $9, cache_write_tokens = $10,
			cost_usd = $11, llm_calls = $12, tool_calls = $13, active_ms = $14,
			finished_at = case when $5 then now() else null end
		where id = $1
		returning `+runColumns,
		run.ID, string(out.Status), jsonArg(out.Output), out.Error, finished, string(state),
		t.Usage.InputTokens, t.Usage.OutputTokens, t.Usage.CacheReadTokens, t.Usage.CacheWriteTokens,
		t.CostUSD, t.LLMCalls, t.ToolCalls, t.ActiveMS)
	if err != nil {
		return nil, err
	}

	for _, req := range out.Approvals {
		// A resumed run reports approvals it is still waiting on; those rows exist already.
		if _, err := tx.Exec(ctx, `
			insert into approvals (id, run_id, agent_id, tool_use_id, tool, input)
			values ($1, $2, $3, $4, $5, $6)
			on conflict (run_id, tool_use_id) do nothing`,
			id.New("apr"), run.ID, run.AgentID, req.ToolUseID, req.Tool, jsonArg(req.Input)); err != nil {
			return nil, err
		}
	}
	return updated, tx.Commit(ctx)
}

// FailRun marks a run failed outside the normal execution path, for example
// when the worker that was executing it died.
func (s *Store) FailRun(ctx context.Context, runID, reason string) error {
	_, err := s.Pool.Exec(ctx, `
		update runs set status = 'failed', error = $2, state = null, finished_at = now()
		where id = $1 and status in ('running', 'awaiting_approval')`, runID, reason)
	return err
}

// SetExpected attaches ground truth to a run and clears its grade so it is
// scored again.
func (s *Store) SetExpected(ctx context.Context, runID string, expected json.RawMessage) (*Run, error) {
	return queryOne[Run](ctx, s.Pool, `
		update runs set expected = $2, graded_at = null where id = $1
		returning `+runColumns, runID, jsonArg(expected))
}

// RecordStep implements agent.Recorder.
func (s *Store) RecordStep(ctx context.Context, runID string, step agent.Step) error {
	_, err := s.Pool.Exec(ctx, `
		insert into steps (run_id, seq, kind, name, input, output, is_error, simulated,
			input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, cost_usd, latency_ms, created_at)
		values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)`,
		runID, step.Seq, step.Kind, step.Name, jsonArg(step.Input), jsonArg(step.Output), step.IsError, step.Simulated,
		step.Usage.InputTokens, step.Usage.OutputTokens, step.Usage.CacheReadTokens, step.Usage.CacheWriteTokens,
		step.CostUSD, step.Latency.Milliseconds(), step.At)
	return err
}

// Steps returns a run's trace in order.
func (s *Store) Steps(ctx context.Context, runID string) ([]Step, error) {
	rows, err := s.Pool.Query(ctx, `
		select run_id, seq, kind, name, input, output, is_error, simulated,
			input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, cost_usd, latency_ms, created_at
		from steps where run_id = $1 order by seq`, runID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[Step])
}

const approvalColumns = `id, run_id, agent_id, tool_use_id, tool, input, status, decided_by, reason, created_at, decided_at`

// ApprovalFilter narrows Approvals. Zero values mean no restriction.
type ApprovalFilter struct {
	RunID  string
	Status string
	Limit  int
}

// Approvals lists approvals, oldest first so the inbox is first come, first served.
func (s *Store) Approvals(ctx context.Context, f ApprovalFilter) ([]Approval, error) {
	limit := f.Limit
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.Pool.Query(ctx, `
		select `+approvalColumns+` from approvals
		where ($1 = '' or run_id = $1) and ($2 = '' or status = $2)
		order by created_at, id limit $3`, f.RunID, f.Status, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[Approval])
}

// DecideApproval records a decision. When it was the last pending approval of
// its run, a job to resume the run is queued in the same transaction, so a
// decision can never be recorded without the run being picked up again.
func (s *Store) DecideApproval(ctx context.Context, approvalID string, approve bool, decidedBy, reason string) (*Approval, error) {
	status := ApprovalRejected
	if approve {
		status = ApprovalApproved
	}

	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	decided, err := queryOne[Approval](ctx, tx, `
		update approvals set status = $2, decided_by = $3, reason = $4, decided_at = now()
		where id = $1 and status = 'pending'
		returning `+approvalColumns, approvalID, status, decidedBy, reason)
	if err == ErrNotFound {
		// Distinguish "no such approval" from "already decided".
		if _, lookupErr := queryOne[Approval](ctx, tx, `select `+approvalColumns+` from approvals where id = $1`, approvalID); lookupErr == nil {
			return nil, fmt.Errorf("store: approval %s has already been decided", approvalID)
		}
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}

	var pending int
	if err := tx.QueryRow(ctx, `select count(*) from approvals where run_id = $1 and status = 'pending'`, decided.RunID).Scan(&pending); err != nil {
		return nil, err
	}
	if pending == 0 {
		if _, err := tx.Exec(ctx, `
			insert into jobs (agent_id, kind, run_id, trigger)
			values ($1, 'resume', $2, 'approval')`, decided.AgentID, decided.RunID); err != nil {
			return nil, err
		}
	}
	return decided, tx.Commit(ctx)
}

// Decisions returns the decided approvals of a run, keyed by tool call ID.
func (s *Store) Decisions(ctx context.Context, runID string) (map[string]agent.Decision, error) {
	rows, err := s.Pool.Query(ctx, `
		select tool_use_id, status, reason from approvals
		where run_id = $1 and status <> 'pending'`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	decisions := map[string]agent.Decision{}
	for rows.Next() {
		var toolUseID, status, reason string
		if err := rows.Scan(&toolUseID, &status, &reason); err != nil {
			return nil, err
		}
		decisions[toolUseID] = agent.Decision{Approved: status == ApprovalApproved, Reason: reason}
	}
	return decisions, rows.Err()
}
