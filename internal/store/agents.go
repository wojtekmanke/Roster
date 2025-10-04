package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/wojtekmanke/roster/internal/id"
	"github.com/wojtekmanke/roster/internal/spec"
)

// Agent statuses.
const (
	AgentActive  = "active"
	AgentPaused  = "paused"
	AgentRetired = "retired"
)

// Agent event kinds.
const (
	EventCreated     = "created"
	EventSpecUpdated = "spec_updated"
	EventPaused      = "paused"
	EventResumed     = "resumed"
	EventRetired     = "retired"
	// EventBreach records a missed KPI on an agent whose policy is to alert
	// rather than pause.
	EventBreach = "breach"
)

// Agent is a stored agent spec and its employment status.
type Agent struct {
	ID           string    `db:"id"`
	Name         string    `db:"name"`
	Description  string    `db:"description"`
	SpecYAML     string    `db:"spec_yaml"`
	SpecHash     string    `db:"spec_hash"`
	Provider     string    `db:"provider"`
	Model        string    `db:"model"`
	Status       string    `db:"status"`
	StatusReason string    `db:"status_reason"`
	ReviewSince  time.Time `db:"review_since"`
	CreatedAt    time.Time `db:"created_at"`
	UpdatedAt    time.Time `db:"updated_at"`
}

// Spec parses the agent's stored YAML.
func (a *Agent) Spec() (*spec.Spec, error) {
	return spec.Parse([]byte(a.SpecYAML))
}

// AgentEvent is one entry in an agent's audit trail.
type AgentEvent struct {
	ID        int64           `db:"id"`
	AgentID   string          `db:"agent_id"`
	Kind      string          `db:"kind"`
	Actor     string          `db:"actor"`
	Reason    string          `db:"reason"`
	Data      json.RawMessage `db:"data"`
	CreatedAt time.Time       `db:"created_at"`
}

const agentColumns = `id, name, description, spec_yaml, spec_hash, provider, model,
	status, status_reason, review_since, created_at, updated_at`

// ApplyAgent creates the agent described by the YAML, or updates the agent of
// the same name. It reports whether anything changed. The spec must be valid.
func (s *Store) ApplyAgent(ctx context.Context, yaml []byte, actor string) (*Agent, bool, error) {
	parsed, err := spec.Parse(yaml)
	if err != nil {
		return nil, false, err
	}
	hash := spec.Hash(yaml)

	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback(ctx)

	existing, err := queryAgent(ctx, tx, `select `+agentColumns+` from agents where name = $1 for update`, parsed.Name)
	switch {
	case err == nil && existing.SpecHash == hash:
		return existing, false, tx.Commit(ctx)
	case err == nil:
		updated, err := queryAgent(ctx, tx, `
			update agents
			set description = $2, spec_yaml = $3, spec_hash = $4, provider = $5, model = $6, updated_at = now()
			where id = $1
			returning `+agentColumns,
			existing.ID, parsed.Description, string(yaml), hash, parsed.Model.Provider, parsed.Model.Name)
		if err != nil {
			return nil, false, err
		}
		if err := addEvent(ctx, tx, updated.ID, EventSpecUpdated, actor, "", nil); err != nil {
			return nil, false, err
		}
		return updated, true, tx.Commit(ctx)
	case err != ErrNotFound:
		return nil, false, err
	}

	created, err := queryAgent(ctx, tx, `
		insert into agents (id, name, description, spec_yaml, spec_hash, provider, model)
		values ($1, $2, $3, $4, $5, $6, $7)
		returning `+agentColumns,
		id.New("agt"), parsed.Name, parsed.Description, string(yaml), hash, parsed.Model.Provider, parsed.Model.Name)
	if err != nil {
		return nil, false, err
	}
	if err := addEvent(ctx, tx, created.ID, EventCreated, actor, "", nil); err != nil {
		return nil, false, err
	}
	return created, true, tx.Commit(ctx)
}

// Agent returns the agent with the given name.
func (s *Store) Agent(ctx context.Context, name string) (*Agent, error) {
	return queryAgent(ctx, s.Pool, `select `+agentColumns+` from agents where name = $1`, name)
}

// AgentByID returns the agent with the given ID.
func (s *Store) AgentByID(ctx context.Context, agentID string) (*Agent, error) {
	return queryAgent(ctx, s.Pool, `select `+agentColumns+` from agents where id = $1`, agentID)
}

// Agents lists every agent by name.
func (s *Store) Agents(ctx context.Context) ([]Agent, error) {
	rows, err := s.Pool.Query(ctx, `select `+agentColumns+` from agents order by name`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[Agent])
}

// SetAgentStatus moves an agent to a new status and records why. Returning an
// agent to active restarts its review window, so the runs that got it paused
// do not immediately pause it again. It is a no-op if the status is unchanged.
func (s *Store) SetAgentStatus(ctx context.Context, agentID, status, reason, actor string) (*Agent, error) {
	kind := map[string]string{AgentActive: EventResumed, AgentPaused: EventPaused, AgentRetired: EventRetired}[status]
	if kind == "" {
		return nil, fmt.Errorf("store: unknown agent status %q", status)
	}

	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	current, err := queryAgent(ctx, tx, `select `+agentColumns+` from agents where id = $1 for update`, agentID)
	if err != nil {
		return nil, err
	}
	if current.Status == status {
		return current, tx.Commit(ctx)
	}
	if current.Status == AgentRetired {
		return nil, fmt.Errorf("store: agent %q is retired and cannot be changed", current.Name)
	}

	updated, err := queryAgent(ctx, tx, `
		update agents
		set status = $2, status_reason = $3, updated_at = now(),
		    review_since = case when $2 = 'active' then now() else review_since end
		where id = $1
		returning `+agentColumns, agentID, status, reason)
	if err != nil {
		return nil, err
	}
	if err := addEvent(ctx, tx, agentID, kind, actor, reason, nil); err != nil {
		return nil, err
	}
	if status == AgentRetired {
		// A retired agent never works again; drop whatever it had queued.
		if _, err := tx.Exec(ctx, `
			update jobs set status = 'failed', last_error = 'agent retired', finished_at = now()
			where agent_id = $1 and status = 'queued'`, agentID); err != nil {
			return nil, err
		}
	}
	return updated, tx.Commit(ctx)
}

// AddAgentEvent appends to an agent's audit trail.
func (s *Store) AddAgentEvent(ctx context.Context, agentID, kind, actor, reason string, data json.RawMessage) error {
	return addEvent(ctx, s.Pool, agentID, kind, actor, reason, data)
}

// AgentEvents returns an agent's most recent events, newest first.
func (s *Store) AgentEvents(ctx context.Context, agentID string, limit int) ([]AgentEvent, error) {
	rows, err := s.Pool.Query(ctx, `
		select id, agent_id, kind, actor, reason, data, created_at
		from agent_events where agent_id = $1
		order by id desc limit $2`, agentID, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[AgentEvent])
}

// querier is satisfied by both the pool and a transaction.
type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

type execer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

func queryAgent(ctx context.Context, q querier, sql string, args ...any) (*Agent, error) {
	return queryOne[Agent](ctx, q, sql, args...)
}

// queryOne runs a query expected to return exactly one row.
func queryOne[T any](ctx context.Context, q querier, sql string, args ...any) (*T, error) {
	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	row, err := pgx.CollectExactlyOneRow(rows, pgx.RowToStructByName[T])
	if err != nil {
		return nil, notFound(err)
	}
	return &row, nil
}

func addEvent(ctx context.Context, e execer, agentID, kind, actor, reason string, data json.RawMessage) error {
	_, err := e.Exec(ctx, `
		insert into agent_events (agent_id, kind, actor, reason, data)
		values ($1, $2, $3, $4, $5)`, agentID, kind, actor, reason, jsonArg(data))
	return err
}
