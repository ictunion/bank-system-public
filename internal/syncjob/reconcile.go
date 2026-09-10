package syncjob

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kubik/bank-system/internal/db"
)

// FailStaleRuns marks any sync_fio_runs/sync_orca_runs row still 'running' as
// 'failed'. Call once at server startup, before the scheduler starts: a
// single instance drives all syncs, so a 'running' row at boot can only be
// left over from the previous process dying (crash, panic, kill) between
// creating the row and finishing it — see RunOrcaSync/syncAccount's
// defer/recover, which covers panics within a live process but not a kill
// or OOM.
func FailStaleRuns(requestContext context.Context, pool *pgxpool.Pool) (fioCount, orcaCount int64, err error) {
	queries := db.New(pool)

	fioCount, err = queries.FailStaleSyncFioRuns(requestContext)
	if err != nil {
		return 0, 0, fmt.Errorf("failing stale sync_fio_runs: %w", err)
	}

	orcaCount, err = queries.FailStaleSyncOrcaRuns(requestContext)
	if err != nil {
		return fioCount, 0, fmt.Errorf("failing stale sync_orca_runs: %w", err)
	}

	return fioCount, orcaCount, nil
}
