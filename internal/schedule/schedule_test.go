package schedule_test

import (
	"testing"
	"time"

	"github.com/wojtekmanke/roster/internal/schedule"
)

func at(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

func TestNext(t *testing.T) {
	tests := []struct {
		expr  string
		after string
		want  string
	}{
		{"every 15m", "2026-10-12T10:07:30Z", "2026-10-12T10:15:00Z"},
		// Strictly after: a time on the boundary moves to the next one.
		{"every 15m", "2026-10-12T10:15:00Z", "2026-10-12T10:30:00Z"},
		{"every 1h", "2026-10-12T23:59:59Z", "2026-10-13T00:00:00Z"},
		{"0 8 * * *", "2026-10-12T08:00:00Z", "2026-10-13T08:00:00Z"},
		{"*/10 * * * *", "2026-10-12T10:07:30Z", "2026-10-12T10:10:00Z"},
		{"@hourly", "2026-10-12T10:07:30Z", "2026-10-12T11:00:00Z"},
	}
	for _, tt := range tests {
		t.Run(tt.expr, func(t *testing.T) {
			s, err := schedule.Parse(tt.expr)
			if err != nil {
				t.Fatal(err)
			}
			if got := s.Next(at(tt.after)); !got.Equal(at(tt.want)) {
				t.Fatalf("Next(%s) = %s, want %s", tt.after, got.Format(time.RFC3339), tt.want)
			}
		})
	}
}

func TestParseErrors(t *testing.T) {
	for _, expr := range []string{"", "every", "every soon", "every 10s", "@every 5m", "not a cron", "* * *"} {
		if _, err := schedule.Parse(expr); err == nil {
			t.Errorf("Parse(%q) succeeded, want an error", expr)
		}
	}
}
