package api

import (
	"net/http/httptest"
	"testing"
)

// Authentication may predate suspension. The mutation must recheck the live
// account inside its transaction, including non-NULL empty legacy values.
func TestHumanIssueOperationRechecksAccountInsideTransaction(t *testing.T) {
	for _, suspended := range []any{nil, "", "2026-10-15T00:00:00Z"} {
		name := "active"
		if suspended != nil {
			name = "suspended_" + suspended.(string)
		}
		t.Run(name, func(t *testing.T) {
			h, user, ws, crew, lead, _ := newTestIssueHandler(t)
			issue := seedIssue(t, h.db, ws, crew, lead, "ENG-1", "BACKLOG")
			execOrFatal(t, h.db, `UPDATE users SET suspended_at=? WHERE id=?`, suspended, user)
			req := httptest.NewRequest("POST", "/", nil)
			req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: user}), ws, "OWNER"))
			tx, err := h.db.BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			rr := httptest.NewRecorder()
			_, handled := h.beginHumanIssueOperation(rr, req, tx, "commented", issue, map[string]string{"body": "Pending comment"})
			if suspended == nil {
				if handled {
					t.Fatalf("active account refused: %d", rr.Code)
				}
			} else if !handled || rr.Code != 403 {
				t.Fatalf("stale authenticated account accepted: handled=%v HTTP %d", handled, rr.Code)
			}
		})
	}
}
