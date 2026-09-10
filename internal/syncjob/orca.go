package syncjob

import (
	"context"
	"fmt"
	"log"
	"strconv"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kubik/bank-system/internal/db"
	"github.com/kubik/bank-system/internal/orca"
)

// OrcaResult summarizes one Orca member sync attempt.
type OrcaResult struct {
	MembersFetched  int
	MembersUpserted int
}

// RunOrcaSync pulls the full member list from Orca and upserts it into
// `members`, recording the attempt in sync_orca_runs. Same idempotent-pull
// shape as RunFioSync — see docs/orca-sync-members.md for why this is a full
// pull every time rather than incremental.
func RunOrcaSync(requestContext context.Context, pool *pgxpool.Pool, client *orca.OrcaClient) (result OrcaResult, err error) {
	queries := db.New(pool)

	run, err := queries.CreateSyncOrcaRun(requestContext)
	if err != nil {
		return OrcaResult{}, fmt.Errorf("creating sync_orca_runs row: %w", err)
	}

	// A panic anywhere below would otherwise crash the whole process (an
	// unrecovered panic kills the program, not just this goroutine) and leave
	// this row stuck at status='running' forever — recover it into a normal
	// failed run instead.
	var fetched, upserted int
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic: %v", r)
			finishOrcaRun(requestContext, queries, run.ID, "failed", fetched, upserted, err)
			result = OrcaResult{}
			log.Printf("orca sync: recovered from panic: %v", r)
		}
	}()

	members, err := client.FetchMembers(requestContext)
	if err != nil {
		finishOrcaRun(requestContext, queries, run.ID, "failed", 0, 0, err)
		return OrcaResult{}, fmt.Errorf("fetching from orca: %w", err)
	}
	fetched = len(members)

	for _, m := range members {
		var feeStartDate pgtype.Date
		if m.FeeStartDate != nil {
			feeStartDate = pgtype.Date{Time: *m.FeeStartDate, Valid: true}
		}
		var feeStopDate pgtype.Date
		if m.FeeStopDate != nil {
			feeStopDate = pgtype.Date{Time: *m.FeeStopDate, Valid: true}
		}
		var sub pgtype.UUID
		if m.Sub != nil {
			if err := sub.Scan(*m.Sub); err != nil {
				finishOrcaRun(requestContext, queries, run.ID, "failed", len(members), upserted, err)
				return OrcaResult{}, fmt.Errorf("member_number=%d: parsing sub %q: %w", m.MemberNumber, *m.Sub, err)
			}
		}
		if err := queries.UpsertMember(requestContext, db.UpsertMemberParams{
			MemberNumber: m.MemberNumber,
			FeeStartDate: feeStartDate,
			FeeStopDate:  feeStopDate,
			Active:       m.Active,
			Sub:          sub,
		}); err != nil {
			finishOrcaRun(requestContext, queries, run.ID, "failed", len(members), upserted, err)
			return OrcaResult{}, fmt.Errorf("upserting member_number=%d: %w", m.MemberNumber, err)
		}

		// Seed the member's default payment identifier (variable_symbol ==
		// member_number, valid from fee_start_date). DO NOTHING on conflict, so
		// re-running is a no-op. Skipped when fee_start_date is null — the member
		// isn't liable yet and valid_from is NOT NULL.
		vs := strconv.Itoa(int(m.MemberNumber))
		if m.FeeStartDate != nil {
			if err := queries.EnsureDefaultPaymentIdentifier(requestContext, db.EnsureDefaultPaymentIdentifierParams{
				MemberNumber:   m.MemberNumber,
				VariableSymbol: vs,
				ValidFrom:      *m.FeeStartDate,
			}); err != nil {
				finishOrcaRun(requestContext, queries, run.ID, "failed", len(members), upserted, err)
				return OrcaResult{}, fmt.Errorf("seeding payment identifier for member_number=%d: %w", m.MemberNumber, err)
			}
		}

		// Mirror fee_stop_date onto that default identifier's valid_to: fee
		// liability ended -> row closed with that date; fee_stop_date cleared ->
		// row reopened (valid_to = NULL). No-op for members with no default row.
		if err := queries.SyncDefaultPaymentIdentifierValidTo(requestContext, db.SyncDefaultPaymentIdentifierValidToParams{
			MemberNumber:   m.MemberNumber,
			VariableSymbol: vs,
			ValidTo:        feeStopDate,
		}); err != nil {
			finishOrcaRun(requestContext, queries, run.ID, "failed", len(members), upserted, err)
			return OrcaResult{}, fmt.Errorf("updating payment identifier validity for member_number=%d: %w", m.MemberNumber, err)
		}
		upserted++
	}

	finishOrcaRun(requestContext, queries, run.ID, "success", len(members), upserted, nil)
	return OrcaResult{MembersFetched: len(members), MembersUpserted: upserted}, nil
}

func finishOrcaRun(requestContext context.Context, queries *db.Queries, runID int64, status string, fetched, upserted int, runErr error) {
	var errorMessage *string
	if runErr != nil {
		message := runErr.Error()
		errorMessage = &message
	}
	fetched32 := int32(fetched)
	upserted32 := int32(upserted)
	if err := queries.FinishSyncOrcaRun(requestContext, db.FinishSyncOrcaRunParams{
		ID:              runID,
		Status:          status,
		MembersFetched:  &fetched32,
		MembersUpserted: &upserted32,
		ErrorMessage:    errorMessage,
	}); err != nil {
		log.Printf("orca sync: recording sync_orca_runs id=%d failed: %v", runID, err)
	}
}
