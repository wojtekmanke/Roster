package store

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"
)

// Score is the value of one KPI for one run.
type Score struct {
	RunID     string          `db:"run_id"`
	AgentID   string          `db:"agent_id"`
	KPI       string          `db:"kpi"`
	Grader    string          `db:"grader"`
	Metric    string          `db:"metric"`
	Value     float64         `db:"value"`
	Detail    json.RawMessage `db:"detail"`
	CreatedAt time.Time       `db:"created_at"`
}

// SaveScores replaces a run's scores and marks it graded. Replacing rather
// than appending lets a run be graded again when its ground truth arrives late.
func (s *Store) SaveScores(ctx context.Context, run *Run, scores []Score) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `delete from scores where run_id = $1`, run.ID); err != nil {
		return err
	}
	for _, sc := range scores {
		if _, err := tx.Exec(ctx, `
			insert into scores (run_id, agent_id, kpi, grader, metric, value, detail)
			values ($1, $2, $3, $4, $5, $6, $7)`,
			run.ID, run.AgentID, sc.KPI, sc.Grader, sc.Metric, sc.Value, jsonArg(sc.Detail)); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `update runs set graded_at = now() where id = $1`, run.ID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Scores returns a run's scores.
func (s *Store) Scores(ctx context.Context, runID string) ([]Score, error) {
	rows, err := s.Pool.Query(ctx, `
		select run_id, agent_id, kpi, grader, metric, value, detail, created_at
		from scores where run_id = $1 order by kpi`, runID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[Score])
}

// UngradedRuns returns finished runs that have no grade yet, oldest first.
func (s *Store) UngradedRuns(ctx context.Context, limit int) ([]Run, error) {
	rows, err := s.Pool.Query(ctx, `
		select `+runColumns+` from runs
		where finished_at is not null and graded_at is null
		order by finished_at limit $1`, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[Run])
}

// ScoredRun is a graded run with its KPI values, keyed by KPI name.
type ScoredRun struct {
	RunID      string
	FinishedAt time.Time
	Scores     map[string]float64
}

// ScoreWindow selects the runs a scorecard is computed from.
type ScoreWindow struct {
	AgentID string
	// Since excludes runs finished before this instant.
	Since time.Time
	// Limit is the number of most recent runs to include.
	Limit    int
	Provider string
	Model    string
	// Shadow selects shadow runs instead of primary ones.
	Shadow bool
}

// ScoredRuns returns the most recent graded runs matching the window, newest
// first, each with all of its scores.
func (s *Store) ScoredRuns(ctx context.Context, w ScoreWindow) ([]ScoredRun, error) {
	rows, err := s.Pool.Query(ctx, `
		with recent as (
			select id, finished_at from runs
			where agent_id = $1 and graded_at is not null and finished_at >= $2
			  and provider = $3 and model = $4 and (shadow_of is not null) = $5
			order by finished_at desc, id desc
			limit $6
		)
		select r.id, r.finished_at, s.kpi, s.value
		from recent r
		left join scores s on s.run_id = r.id
		order by r.finished_at desc, r.id desc`,
		w.AgentID, w.Since, w.Provider, w.Model, w.Shadow, w.Limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var runs []ScoredRun
	for rows.Next() {
		var (
			runID      string
			finishedAt time.Time
			kpi        *string
			value      *float64
		)
		if err := rows.Scan(&runID, &finishedAt, &kpi, &value); err != nil {
			return nil, err
		}
		if len(runs) == 0 || runs[len(runs)-1].RunID != runID {
			runs = append(runs, ScoredRun{RunID: runID, FinishedAt: finishedAt, Scores: map[string]float64{}})
		}
		if kpi != nil {
			runs[len(runs)-1].Scores[*kpi] = *value
		}
	}
	return runs, rows.Err()
}

// ModelUse says how many finished runs an agent has on one model.
type ModelUse struct {
	Provider string `db:"provider"`
	Model    string `db:"model"`
	Shadow   bool   `db:"shadow"`
	Runs     int    `db:"runs"`
}

// ModelsUsed lists the models an agent has finished runs on since the given
// instant, primary model first.
func (s *Store) ModelsUsed(ctx context.Context, agentID string, since time.Time) ([]ModelUse, error) {
	rows, err := s.Pool.Query(ctx, `
		select provider, model, shadow_of is not null as shadow, count(*)::int as runs
		from runs
		where agent_id = $1 and finished_at >= $2
		group by 1, 2, 3
		order by shadow, provider, model`, agentID, since)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[ModelUse])
}

// AgentStats summarises an agent's primary runs over a period.
type AgentStats struct {
	AgentID          string     `db:"agent_id"`
	Runs             int        `db:"runs"`
	Succeeded        int        `db:"succeeded"`
	Failed           int        `db:"failed"`
	AwaitingApproval int        `db:"awaiting_approval"`
	CostUSD          float64    `db:"cost_usd"`
	LastRunAt        *time.Time `db:"last_run_at"`
}

// StatsSince summarises every agent's primary runs started since the given
// instant, keyed by agent ID. Agents with no runs are absent.
func (s *Store) StatsSince(ctx context.Context, since time.Time) (map[string]AgentStats, error) {
	rows, err := s.Pool.Query(ctx, `
		select agent_id,
			count(*)::int as runs,
			(count(*) filter (where status = 'succeeded'))::int as succeeded,
			(count(*) filter (where status = 'failed'))::int as failed,
			(count(*) filter (where status = 'awaiting_approval'))::int as awaiting_approval,
			coalesce(sum(cost_usd), 0)::float8 as cost_usd,
			max(started_at) as last_run_at
		from runs
		where shadow_of is null and started_at >= $1
		group by agent_id`, since)
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, pgx.RowToStructByName[AgentStats])
	if err != nil {
		return nil, err
	}
	stats := make(map[string]AgentStats, len(list))
	for _, st := range list {
		stats[st.AgentID] = st
	}
	return stats, nil
}

// SchedulerTick records now as the latest schedule evaluation and returns the
// previous one. On the very first tick it returns now.
func (s *Store) SchedulerTick(ctx context.Context, now time.Time) (time.Time, error) {
	var previous *time.Time
	// The subquery in RETURNING sees the row as it was before this statement.
	err := s.Pool.QueryRow(ctx, `
		insert into scheduler_state (id, last_tick) values (true, $1)
		on conflict (id) do update set last_tick = greatest(scheduler_state.last_tick, excluded.last_tick)
		returning (select last_tick from scheduler_state where id)`, now).Scan(&previous)
	if err != nil {
		return time.Time{}, err
	}
	if previous == nil {
		return now, nil
	}
	return *previous, nil
}
