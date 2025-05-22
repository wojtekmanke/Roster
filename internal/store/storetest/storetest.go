// Package storetest gives each test its own migrated Postgres database.
package storetest

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/wojtekmanke/roster/internal/id"
	"github.com/wojtekmanke/roster/internal/store"
)

// EnvURL names the variable holding a Postgres URL whose role may create databases.
const EnvURL = "ROSTER_TEST_DATABASE_URL"

// New creates a fresh database, applies all migrations and returns a Store
// connected to it. The database is dropped when the test ends. Tests are
// skipped when EnvURL is unset.
func New(t testing.TB) *store.Store {
	t.Helper()
	s, _ := NewWithURL(t)
	return s
}

// NewWithURL is New, also returning the URL of the fresh database.
func NewWithURL(t testing.TB) (*store.Store, string) {
	t.Helper()
	base := os.Getenv(EnvURL)
	if base == "" {
		t.Skipf("%s not set; skipping database test", EnvURL)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	admin, err := pgx.Connect(ctx, base)
	if err != nil {
		t.Fatalf("storetest: connect: %v", err)
	}
	defer admin.Close(ctx)

	name := "t_" + strings.TrimPrefix(id.New("t"), "t_")
	if _, err := admin.Exec(ctx, fmt.Sprintf(`create database %q`, name)); err != nil {
		t.Fatalf("storetest: create database: %v", err)
	}

	u, err := url.Parse(base)
	if err != nil {
		t.Fatalf("storetest: parse %s: %v", EnvURL, err)
	}
	u.Path = "/" + name
	dbURL := u.String()

	s, err := store.Open(ctx, dbURL)
	if err != nil {
		t.Fatalf("storetest: open: %v", err)
	}
	if _, err := s.Migrate(ctx); err != nil {
		t.Fatalf("storetest: migrate: %v", err)
	}

	t.Cleanup(func() {
		s.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		admin, err := pgx.Connect(ctx, base)
		if err != nil {
			t.Logf("storetest: cleanup connect: %v", err)
			return
		}
		defer admin.Close(ctx)
		if _, err := admin.Exec(ctx, fmt.Sprintf(`drop database if exists %q with (force)`, name)); err != nil {
			t.Logf("storetest: drop database: %v", err)
		}
	})
	return s, dbURL
}
