package syncjob

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kubik/bank-system/internal/dbtest"
	"github.com/kubik/bank-system/internal/orca"
)

func fakeOrcaServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	return server
}

func memberRow(t *testing.T, pool *pgxpool.Pool, memberNumber int32) (active bool, feeStartDateSet bool) {
	t.Helper()
	var feeStartDateValid bool
	if err := pool.QueryRow(context.Background(),
		`SELECT active, fee_start_date IS NOT NULL FROM members WHERE member_number = $1`, memberNumber,
	).Scan(&active, &feeStartDateValid); err != nil {
		t.Fatalf("querying members row for %d: %v", memberNumber, err)
	}
	return active, feeStartDateValid
}

func paymentIdentifierCount(t *testing.T, pool *pgxpool.Pool, memberNumber int32) int {
	t.Helper()
	var count int
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM member_payment_identifiers WHERE member_number = $1`, memberNumber,
	).Scan(&count); err != nil {
		t.Fatalf("counting member_payment_identifiers for %d: %v", memberNumber, err)
	}
	return count
}

func latestOrcaRunStatus(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	var status string
	if err := pool.QueryRow(context.Background(),
		`SELECT status FROM sync_orca_runs ORDER BY id DESC LIMIT 1`,
	).Scan(&status); err != nil {
		t.Fatalf("querying latest sync_orca_runs row: %v", err)
	}
	return status
}

func TestRunOrcaSync_UpsertsMembersAndPaymentIdentifier(t *testing.T) {
	pool := dbtest.Pool(t)
	server := fakeOrcaServer(t, `{
		"members": [
			{"member_number": 900801, "fee_start_date": "2020-01-01", "fee_stop_date": null, "active": true, "sub": null, "workplace_executive_committee_sub": null}
		]
	}`)
	client := orca.NewClient(server.URL, "token", false)

	result, err := RunOrcaSync(context.Background(), pool, client)
	if err != nil {
		t.Fatalf("RunOrcaSync: %v", err)
	}
	if result.MembersFetched != 1 || result.MembersUpserted != 1 {
		t.Errorf("result = %+v, want {1 1}", result)
	}

	active, hasFeeStart := memberRow(t, pool, 900801)
	if !active {
		t.Error("member 900801: active = false, want true")
	}
	if !hasFeeStart {
		t.Error("member 900801: fee_start_date not set")
	}
	if n := paymentIdentifierCount(t, pool, 900801); n != 1 {
		t.Errorf("member_payment_identifiers rows for 900801 = %d, want 1 (default identifier seeded from fee_start_date)", n)
	}
	if status := latestOrcaRunStatus(t, pool); status != "success" {
		t.Errorf("sync_orca_runs status = %q, want success", status)
	}
}

// defaultIdentifierWindow returns the valid_from/valid_to of a member's single
// default payment identifier (variable_symbol == member_number), formatted
// YYYY-MM-DD, valid_to "" when open. Fails unless exactly one such row exists.
func defaultIdentifierWindow(t *testing.T, pool *pgxpool.Pool, memberNumber int32) (validFrom, validTo string) {
	t.Helper()
	var count int
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*), min(valid_from)::text, COALESCE(min(valid_to)::text, '')
		 FROM member_payment_identifiers
		 WHERE member_number = $1 AND variable_symbol = member_number::text`, memberNumber,
	).Scan(&count, &validFrom, &validTo); err != nil {
		t.Fatalf("querying default payment identifier for %d: %v", memberNumber, err)
	}
	if count != 1 {
		t.Fatalf("default payment identifier rows for %d = %d, want exactly 1", memberNumber, count)
	}
	return validFrom, validTo
}

// A fee_start_date corrected in Orca (here moved earlier, the real production
// case: onboarding recorded months after the member actually joined) must
// move the existing default identifier's valid_from, not add a second row.
func TestRunOrcaSync_CorrectedDatesMoveDefaultIdentifierWindow(t *testing.T) {
	pool := dbtest.Pool(t)
	const memberNumber = int32(900803)

	runs := []struct {
		name          string
		body          string
		wantValidFrom string
		wantValidTo   string
	}{
		{
			name:          "initial, wrong start date",
			body:          `{"members": [{"member_number": 900803, "fee_start_date": "2024-02-25", "fee_stop_date": null, "active": true, "sub": null, "workplace_executive_committee_sub": null}]}`,
			wantValidFrom: "2024-02-25",
		},
		{
			name:          "start date corrected earlier",
			body:          `{"members": [{"member_number": 900803, "fee_start_date": "2023-11-14", "fee_stop_date": null, "active": true, "sub": null, "workplace_executive_committee_sub": null}]}`,
			wantValidFrom: "2023-11-14",
		},
		{
			name:          "member left",
			body:          `{"members": [{"member_number": 900803, "fee_start_date": "2023-11-14", "fee_stop_date": "2026-06-30", "active": false, "sub": null, "workplace_executive_committee_sub": null}]}`,
			wantValidFrom: "2023-11-14",
			wantValidTo:   "2026-06-30",
		},
	}
	for _, run := range runs {
		client := orca.NewClient(fakeOrcaServer(t, run.body).URL, "token", false)
		if _, err := RunOrcaSync(context.Background(), pool, client); err != nil {
			t.Fatalf("%s: RunOrcaSync: %v", run.name, err)
		}
		validFrom, validTo := defaultIdentifierWindow(t, pool, memberNumber)
		if validFrom != run.wantValidFrom || validTo != run.wantValidTo {
			t.Errorf("%s: window = [%s, %q], want [%s, %q]", run.name, validFrom, validTo, run.wantValidFrom, run.wantValidTo)
		}
	}
}

func TestRunOrcaSync_MemberNotYetLiableGetsNoPaymentIdentifier(t *testing.T) {
	pool := dbtest.Pool(t)
	server := fakeOrcaServer(t, `{
		"members": [
			{"member_number": 900802, "fee_start_date": null, "fee_stop_date": null, "active": true, "sub": null, "workplace_executive_committee_sub": null}
		]
	}`)
	client := orca.NewClient(server.URL, "token", false)

	if _, err := RunOrcaSync(context.Background(), pool, client); err != nil {
		t.Fatalf("RunOrcaSync: %v", err)
	}

	if n := paymentIdentifierCount(t, pool, 900802); n != 0 {
		t.Errorf("member_payment_identifiers rows for 900802 = %d, want 0 — no fee_start_date means not yet liable", n)
	}
}

func TestRunOrcaSync_FetchFailureRecordsFailedRun(t *testing.T) {
	pool := dbtest.Pool(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	client := orca.NewClient(server.URL, "token", false)

	if _, err := RunOrcaSync(context.Background(), pool, client); err == nil {
		t.Error("RunOrcaSync returned nil error on an Orca 500, want an error")
	}
	if status := latestOrcaRunStatus(t, pool); status != "failed" {
		t.Errorf("sync_orca_runs status = %q, want failed", status)
	}
}
