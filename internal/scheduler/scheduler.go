// Package scheduler runs a job immediately and then repeatedly at a fixed
// clock time every day, using only the standard library — no cron dependency
// needed for a single daily job (see db-design.md, "Sync Job (Go)").
package scheduler

import (
	"context"
	"time"
)

// RunImmediatellyAndThenDaily calls fn once immediately, then again every day at hour:min in the
// local timezone, until ctx is canceled. fn is responsible for its own error
// handling/logging — a failed run doesn't stop future runs.
func RunImmediatellyAndThenDaily(ctx context.Context, hour, min int, fn func(context.Context)) {
	fn(ctx)

	for {
		timer := time.NewTimer(time.Until(nextRun(time.Now(), hour, min)))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
			fn(ctx)
		}
	}
}

func nextRun(from time.Time, hour, min int) time.Time {
	next := time.Date(from.Year(), from.Month(), from.Day(), hour, min, 0, 0, from.Location())
	if !next.After(from) {
		next = next.AddDate(0, 0, 1)
	}
	return next
}
