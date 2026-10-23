package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/crewship-ai/crewship/internal/appengine"
)

func gridcraftHumanRequest(token, userID, kind, sessionID string) *http.Request {
	r := httptest.NewRequest("POST", "/api/v1/pages/x/workbook/operations", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	ctx := context.WithValue(r.Context(), ctxUser, &AuthUser{ID: userID, SessionID: sessionID})
	ctx = context.WithValue(ctx, ctxAuthKind, kind)
	ctx = context.WithValue(ctx, ctxWorkspaceID, "ws-old")
	return r.WithContext(ctx)
}

// Only a credential that is actually revoked or expired is reported as 401.
// A storage failure while checking it (busy, closed transaction) keeps the
// previous hidden refusal, so a transient error never signs the user out.
func TestGridCraftCredentialStorageFailureIsNotUnauthenticated(t *testing.T) {
	db := gridcraftFullDB(t)
	h := &gridcraftHTTP{}
	for _, tc := range []struct{ name, kind, session string }{
		{"cli token", AuthKindCLIToken, ""},
		{"session", AuthKindSession, "session-1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tx, err := db.BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			if err = tx.Rollback(); err != nil { // every read now fails: sql.ErrTxDone
				t.Fatal(err)
			}
			r := gridcraftHumanRequest(cliTokenStandardPrefix+"storage-failure", "boss", tc.kind, tc.session)
			_, err = h.humanAuthority(r, appengine.Key{WorkspaceID: "ws-old", PageID: "p"}, true, nil)(r.Context(), tx)
			if errors.Is(err, appengine.ErrUnauthenticated) || !errors.Is(err, appengine.ErrForbidden) {
				t.Fatalf("storage failure reported as %v", err)
			}
		})
	}
}

// A credential revoked after the middleware admitted it gets the same answer
// whether or not the slug exists, so 401 vs 404 reveals no private Page.
func TestGridCraftRevokedCredentialRevealsNoSlugExistence(t *testing.T) {
	f := gridcraftInstanceFixture(t)
	wmSeedPage(t, f.db, "ws-old", "secret-book", "secret-book", "Workbook", "boss", "")
	mustExec(t, f.db, `UPDATE pages SET private_audience=1 WHERE id='secret-book'`)
	var userID string
	if err := f.db.QueryRow(`SELECT user_id FROM cli_tokens WHERE token_hash=?`, sha256Hex(f.wsAdmin)).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	mustExec(t, f.db, `UPDATE cli_tokens SET revoked_at='2026-01-01T00:00:00Z' WHERE token_hash=?`, sha256Hex(f.wsAdmin))
	h := &gridcraftHTTP{pages: f.r.pages}
	for _, slug := range []string{"secret-book", "no-such-book"} {
		r := gridcraftHumanRequest(f.wsAdmin, userID, AuthKindCLIToken, "")
		r.SetPathValue("slug", slug)
		if _, err := h.humanKey(r, true); !errors.Is(err, appengine.ErrUnauthenticated) {
			t.Fatalf("%s: %v", slug, err)
		}
	}
}

// A caller that went away is reported like an interrupted engine call (503),
// not as an internal failure.
func TestGridCraftInterruptedOperationIsUnavailable(t *testing.T) {
	w := httptest.NewRecorder()
	gridcraftError(w, fmt.Errorf("%w: operation interrupted: %w", context.Canceled, appengine.ErrInvalid))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("got %d", w.Code)
	}
}

// Quota refusal is an actionable terminal outcome, not transient capacity.
func TestGridCraftHistoryQuotaReportsQuotaExceeded(t *testing.T) {
	w := httptest.NewRecorder()
	gridcraftError(w, appengine.ErrQuota)
	if w.Code != http.StatusTooManyRequests || w.Body.String() != "{\"error\":\"quota_exceeded\"}\n" {
		t.Fatalf("quota response: %d %s", w.Code, w.Body.String())
	}
}
