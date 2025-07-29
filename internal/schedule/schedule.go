// Package schedule parses the schedule expressions used by agent triggers.
package schedule

import (
	"fmt"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
)

// Schedule yields firing times.
type Schedule interface {
	// Next returns the first firing time strictly after t.
	Next(t time.Time) time.Time
}

// minInterval keeps "every" schedules from firing faster than the scheduler ticks.
const minInterval = time.Minute

// Parse accepts either "every <duration>" (for example "every 15m") or a
// standard five-field cron expression, including descriptors such as @hourly.
func Parse(expr string) (Schedule, error) {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return nil, fmt.Errorf("schedule is empty")
	}
	if rest, ok := strings.CutPrefix(expr, "every "); ok {
		d, err := time.ParseDuration(strings.TrimSpace(rest))
		if err != nil {
			return nil, fmt.Errorf("schedule %q: %w", expr, err)
		}
		if d < minInterval {
			return nil, fmt.Errorf("schedule %q: interval must be at least %s", expr, minInterval)
		}
		return every(d), nil
	}
	if strings.HasPrefix(expr, "@every") {
		return nil, fmt.Errorf("schedule %q: write it as \"every <duration>\"", expr)
	}
	s, err := cron.ParseStandard(expr)
	if err != nil {
		return nil, fmt.Errorf("schedule %q: %w", expr, err)
	}
	return s, nil
}

// every fires at multiples of the interval since the Unix epoch. Aligning to
// the clock rather than to process start makes firing times identical across
// restarts and instances, which is what lets the scheduler deduplicate them.
type every time.Duration

func (e every) Next(t time.Time) time.Time {
	d := time.Duration(e)
	return t.Truncate(d).Add(d)
}
