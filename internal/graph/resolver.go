// Package graph is Roster's GraphQL API. The executable schema is generated
// by gqlgen from schema.graphqls; this package supplies the resolvers.
package graph

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/99designs/gqlgen/graphql"
	"github.com/99designs/gqlgen/graphql/handler"
	"github.com/99designs/gqlgen/graphql/handler/extension"
	"github.com/99designs/gqlgen/graphql/handler/transport"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/vektah/gqlparser/v2/gqlerror"

	"github.com/wojtekmanke/roster/internal/fleet"
	"github.com/wojtekmanke/roster/internal/spec"
	"github.com/wojtekmanke/roster/internal/store"
)

// Resolver holds the resolvers' dependencies.
type Resolver struct {
	Svc *fleet.Service
}

// statsWindow is the period covered by the dashboard's activity counters.
const statsWindow = 24 * time.Hour

// maxListLimit caps list arguments so one query cannot pull a whole table.
const maxListLimit = 200

// NewHandler returns the HTTP handler serving the GraphQL API.
func NewHandler(svc *fleet.Service, log *slog.Logger) http.Handler {
	srv := handler.New(NewExecutableSchema(Config{Resolvers: &Resolver{Svc: svc}}))
	srv.AddTransport(transport.Options{})
	// GET is accepted for queries because common clients send short queries
	// that way by default. Mutations over GET are refused by the transport.
	srv.AddTransport(transport.GET{})
	srv.AddTransport(transport.POST{})
	srv.Use(extension.Introspection{})

	// Share parsed specs and activity counters between the resolvers of one request.
	srv.AroundOperations(func(ctx context.Context, next graphql.OperationHandler) graphql.ResponseHandler {
		return next(context.WithValue(ctx, cacheKey{}, &requestCache{}))
	})

	// Validation and not-found errors are meant for the caller. Database and
	// context errors are not: log them and answer with something generic.
	srv.SetErrorPresenter(func(ctx context.Context, err error) *gqlerror.Error {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			log.Error("graphql: internal error", "path", graphql.GetPath(ctx).String(), "error", err)
			return &gqlerror.Error{Message: "internal error", Path: graphql.GetPath(ctx)}
		}
		return graphql.DefaultErrorPresenter(ctx, err)
	})
	return srv
}

type actorKey struct{}

// WithActor records who is making the request, for the audit trail.
func WithActor(ctx context.Context, actor string) context.Context {
	return context.WithValue(ctx, actorKey{}, actor)
}

func actor(ctx context.Context) string {
	if a, _ := ctx.Value(actorKey{}).(string); a != "" {
		return a
	}
	return "api"
}

type cacheKey struct{}

// requestCache memoises work that several fields of one response repeat.
type requestCache struct {
	mu          sync.Mutex
	specs       map[string]*spec.Spec
	stats       map[string]store.AgentStats
	statsLoaded bool
}

func cache(ctx context.Context) *requestCache {
	if c, ok := ctx.Value(cacheKey{}).(*requestCache); ok {
		return c
	}
	return &requestCache{}
}

// specOf parses an agent's spec once per request.
func (r *Resolver) specOf(ctx context.Context, a *store.Agent) (*spec.Spec, error) {
	c := cache(ctx)
	c.mu.Lock()
	defer c.mu.Unlock()
	if sp, ok := c.specs[a.ID]; ok {
		return sp, nil
	}
	sp, err := a.Spec()
	if err != nil {
		return nil, err
	}
	if c.specs == nil {
		c.specs = map[string]*spec.Spec{}
	}
	c.specs[a.ID] = sp
	return sp, nil
}

// statsOf loads every agent's activity counters with one query per request.
func (r *Resolver) statsOf(ctx context.Context, agentID string) (store.AgentStats, error) {
	c := cache(ctx)
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.statsLoaded {
		stats, err := r.Svc.Store.StatsSince(ctx, time.Now().Add(-statsWindow))
		if err != nil {
			return store.AgentStats{}, err
		}
		c.stats, c.statsLoaded = stats, true
	}
	return c.stats[agentID], nil
}

// clampLimit applies a default and an upper bound to a list argument.
func clampLimit(limit *int, fallback int) int {
	if limit == nil || *limit <= 0 {
		return fallback
	}
	return min(*limit, maxListLimit)
}

// optional maps "not found" to a null result for nullable lookups.
func optional[T any](v *T, err error) (*T, error) {
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	return v, err
}

// pointers adapts a slice of values to the slice of pointers gqlgen expects.
func pointers[T any](items []T) []*T {
	out := make([]*T, len(items))
	for i := range items {
		out[i] = &items[i]
	}
	return out
}
