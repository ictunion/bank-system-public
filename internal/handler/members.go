package handler

import (
	"net/http"

	"github.com/kubik/bank-system/internal/db"
)

type memberResponse struct {
	MemberNumber int32   `json:"member_number"`
	FeeStartDate *string `json:"fee_start_date"`
	Active       bool    `json:"active"`
}

// ListMembers handles GET /members — debug endpoint dumping every member row.
// Not meant to survive to production; the first prototype of a
// Keycloak-authenticated read endpoint (see RequireRole).
func ListMembers(queries *db.Queries) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		members, err := queries.ListMembers(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to list members")
			return
		}

		out := make([]memberResponse, 0, len(members))
		for _, m := range members {
			resp := memberResponse{MemberNumber: m.MemberNumber, Active: m.Active}
			if m.FeeStartDate.Valid {
				s := m.FeeStartDate.Time.Format("2006-01-02")
				resp.FeeStartDate = &s
			}
			out = append(out, resp)
		}

		writeJSON(w, http.StatusOK, out)
	}
}
