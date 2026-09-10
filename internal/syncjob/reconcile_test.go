package syncjob

import (
	"context"
	"testing"

	"github.com/kubik/bank-system/internal/db"
	"github.com/kubik/bank-system/internal/dbtest"
)

func TestFailStaleRuns_MarksRunningRowsFailed(t *testing.T) {
	pool := dbtest.Pool(t)
	queries := db.New(pool)
	account := seedBankAccountWithToken(t, pool, "9500000005")

	fioRun, err := queries.CreateSyncFioRun(context.Background(), account.ID)
	if err != nil {
		t.Fatalf("CreateSyncFioRun: %v", err)
	}
	orcaRun, err := queries.CreateSyncOrcaRun(context.Background())
	if err != nil {
		t.Fatalf("CreateSyncOrcaRun: %v", err)
	}

	fioCount, orcaCount, err := FailStaleRuns(context.Background(), pool)
	if err != nil {
		t.Fatalf("FailStaleRuns: %v", err)
	}
	if fioCount < 1 {
		t.Errorf("fioCount = %d, want at least 1 (the seeded running row)", fioCount)
	}
	if orcaCount < 1 {
		t.Errorf("orcaCount = %d, want at least 1 (the seeded running row)", orcaCount)
	}

	var fioStatus, orcaStatus string
	if err := pool.QueryRow(context.Background(), `SELECT status FROM sync_fio_runs WHERE id = $1`, fioRun.ID).Scan(&fioStatus); err != nil {
		t.Fatalf("querying sync_fio_runs: %v", err)
	}
	if fioStatus != "failed" {
		t.Errorf("sync_fio_runs status = %q, want failed", fioStatus)
	}
	if err := pool.QueryRow(context.Background(), `SELECT status FROM sync_orca_runs WHERE id = $1`, orcaRun.ID).Scan(&orcaStatus); err != nil {
		t.Fatalf("querying sync_orca_runs: %v", err)
	}
	if orcaStatus != "failed" {
		t.Errorf("sync_orca_runs status = %q, want failed", orcaStatus)
	}
}
