// Package orca calls Orca's (in-house member API) machine-to-machine sync route,
// `GET /sync/bank/members` — static bearer token auth, not Keycloak. See
// docs/orca-sync-members.md for the full API contract.
package orca

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

// OrcaClient calls Orca's bank-sync route for one shared-secret token. Orca has no
// per-account scoping like Fio does — one token covers the whole member list.
type OrcaClient struct {
	httpClient *http.Client
	baseURL    string
	token      string
	debug      bool
}

func NewClient(baseURL, token string, debug bool) *OrcaClient {
	return &OrcaClient{
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
			// This route never legitimately redirects — treat one as an error
			// instead of silently following it (see internal/fio/client.go for
			// the same pattern and why it matters).
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return fmt.Errorf("unexpected redirect to %s", req.URL)
			},
		},
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		debug:   debug,
	}
}

// Member is one parsed row from /sync/bank/members, shaped to map directly
// onto the `members` table.
type Member struct {
	MemberNumber int32
	FeeStartDate *time.Time // nil = not yet liable
	FeeStopDate  *time.Time // nil = fee liability still open
	Active       bool
	Sub          *string // Keycloak UUID; nil if member has no Keycloak account yet
}

type membersResponse struct {
	Members []struct {
		MemberNumber int32   `json:"member_number"`
		FeeStartDate *string `json:"fee_start_date"`
		FeeStopDate  *string `json:"fee_stop_date"`
		Active       bool    `json:"active"`
		Sub          *string `json:"sub"`
	} `json:"members"`
}

// FetchMembers pulls the full member list. Orca has no incremental/cursor
// mode for this route (see docs/orca-sync-members.md) — every call is a full
// pull, upserted idempotently on our side.
func (c *OrcaClient) FetchMembers(ctx context.Context) ([]Member, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/sync/bank/members", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("User-Agent", "bank-system/1.0")
	c.logRequest(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("orca request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading orca response: %w", err)
	}
	c.logResponse(resp, body)

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("orca request: unexpected status %d: %s", resp.StatusCode, body)
	}

	var out membersResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("decoding orca response: %w", err)
	}

	members := make([]Member, 0, len(out.Members))
	for i, m := range out.Members {
		member := Member{MemberNumber: m.MemberNumber, Active: m.Active, Sub: m.Sub}
		if m.FeeStartDate != nil {
			t, err := time.Parse("2006-01-02", *m.FeeStartDate)
			if err != nil {
				return nil, fmt.Errorf("member %d: parsing fee_start_date %q: %w", i, *m.FeeStartDate, err)
			}
			member.FeeStartDate = &t
		}
		if m.FeeStopDate != nil {
			t, err := time.Parse("2006-01-02", *m.FeeStopDate)
			if err != nil {
				return nil, fmt.Errorf("member %d: parsing fee_stop_date %q: %w", i, *m.FeeStopDate, err)
			}
			member.FeeStopDate = &t
		}
		if m.Sub != nil {
			var u pgtype.UUID
			if err := u.Scan(*m.Sub); err != nil {
				return nil, fmt.Errorf("member %d: parsing sub %q: %w", i, *m.Sub, err)
			}
		}
		members = append(members, member)
	}
	return members, nil
}

func (c *OrcaClient) logRequest(req *http.Request) {
	if !c.debug {
		return
	}
	headers := req.Header.Clone()
	if headers.Get("Authorization") != "" {
		headers.Set("Authorization", "Bearer ***REDACTED***")
	}
	log.Printf("orca debug: request %s %s headers=%v", req.Method, req.URL.String(), headers)
}

func (c *OrcaClient) logResponse(resp *http.Response, body []byte) {
	if !c.debug {
		return
	}
	log.Printf("orca debug: response status=%d headers=%v body=%s", resp.StatusCode, resp.Header, body)
}
