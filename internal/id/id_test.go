package id

import (
	"strings"
	"testing"
	"time"
)

func TestNewIsPrefixedAndUnique(t *testing.T) {
	seen := map[string]bool{}
	for range 1000 {
		got := New("run")
		if !strings.HasPrefix(got, "run_") {
			t.Fatalf("New(%q) = %q, want run_ prefix", "run", got)
		}
		if len(got) != len("run_")+26 {
			t.Fatalf("len(%q) = %d, want %d", got, len(got), len("run_")+26)
		}
		if seen[got] {
			t.Fatalf("duplicate id %q", got)
		}
		seen[got] = true
	}
}

func TestNewSortsByTime(t *testing.T) {
	a := New("x")
	time.Sleep(2 * time.Millisecond)
	b := New("x")
	if a >= b {
		t.Fatalf("ids not time-ordered: %q >= %q", a, b)
	}
}
