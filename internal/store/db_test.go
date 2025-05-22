package store_test

import (
	"context"
	"testing"

	"github.com/wojtekmanke/roster/internal/store/storetest"
)

func TestMigrateIsIdempotent(t *testing.T) {
	s := storetest.New(t) // already migrated once
	ctx := context.Background()

	applied, err := s.Migrate(ctx)
	if err != nil {
		t.Fatalf("second Migrate: %v", err)
	}
	if len(applied) != 0 {
		t.Fatalf("second Migrate applied %v, want nothing", applied)
	}

	var n int
	if err := s.Pool.QueryRow(ctx, `select count(*) from schema_migrations`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Fatal("schema_migrations is empty after Migrate")
	}
}
