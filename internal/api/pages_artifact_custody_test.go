package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Both departure doors must refuse before crew fallback, even when another
// regular page in the same batch has a valid transfer target.
func TestStaticPagesHumanCustodyRefusesDepartureAndErasure(t *testing.T) {
	for _, content := range []string{"empty", "artifact", "board"} {
		for _, route := range []string{"membership", "erasure"} {
			t.Run(content+"/"+route, func(t *testing.T) {
				r := pagesTransferSetup(t)
				r.seedCrew(t, "crewA", "Crew A", "crew-a")
				r.seedCrewMember(t, "crewA", r.userID)
				r.seedPage(t, "regular-page", "regular", "Regular", r.userID, "")
				if _, err := r.db.Exec(`INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json) VALUES('private-page',?,'secret-slug','Secret private title',?,'{}')`, r.wsID, r.userID); err != nil {
					t.Fatal(err)
				}
				switch content {
				case "artifact":
					if _, err := r.db.Exec(`INSERT INTO page_artifacts(page_id,head_rev) VALUES('private-page',1)`); err != nil {
						t.Fatal(err)
					}
				case "board":
					if _, err := r.db.Exec(`INSERT INTO page_boards(page_id,seq,format) VALUES('private-page',0,'crewship-board-excalidraw-0.18.1-v1')`); err != nil {
						t.Fatal(err)
					}
				}
				rec := httptest.NewRecorder()
				if route == "erasure" {
					r.h.DeleteUserData(rec, r.deleteReq(t, "custody-test"))
				} else {
					req := httptest.NewRequest(http.MethodDelete, "/", nil)
					req.SetPathValue("memberId", "ptm2")
					ctx := context.WithValue(req.Context(), ctxWorkspaceID, r.wsID)
					ctx = context.WithValue(ctx, ctxUser, &AuthUser{ID: r.adminID})
					ctx = context.WithValue(ctx, ctxRole, "OWNER")
					NewWorkspaceHandler(r.db, newTestLogger()).RemoveMember(rec, req.WithContext(ctx))
				}
				if rec.Code != 409 {
					t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
				}
				body := rec.Body.String()
				if !strings.Contains(body, "page_ownership_transfer_required") || !strings.Contains(body, "human successor") {
					t.Fatalf("missing actionable typed refusal: %s", body)
				}
				for _, secret := range []string{"private-page", "secret-slug", "Secret private title", "regular-page"} {
					if strings.Contains(body, secret) {
						t.Fatalf("private metadata leaked: %s", body)
					}
				}
				var retained int
				if err := r.db.QueryRow(`SELECT COUNT(*) FROM pages WHERE owner_user_id=?`, r.userID).Scan(&retained); err != nil || retained != 2 {
					t.Fatalf("batch transferred: count=%d err=%v", retained, err)
				}
				if err := r.db.QueryRow(`SELECT COUNT(*) FROM workspace_members WHERE id='ptm2'`).Scan(&retained); err != nil || retained != 1 {
					t.Fatalf("membership removed: count=%d err=%v", retained, err)
				}
				if err := r.db.QueryRow(`SELECT COUNT(*) FROM crew_members WHERE user_id=?`, r.userID).Scan(&retained); err != nil || retained != 1 {
					t.Fatalf("crew membership removed: count=%d err=%v", retained, err)
				}
			})
		}
	}
}
