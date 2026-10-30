package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/auth"
)

// craftNativeGrantedWriter admits a plain MEMBER whose only path to the native
// Page is an explicit user write grant, so grant changes can actually remove it.
func craftNativeGrantedWriter(t *testing.T, f *pagesTransferRig) context.Context {
	t.Helper()
	user := "native-scope-writer"
	mustExec(t, f.db, `INSERT INTO users(id,email) VALUES(?,'native-scope-writer@example.test')`, user)
	mustExec(t, f.db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('native-scope-member',?,?,'MEMBER')`, f.wsID, user)
	mustExec(t, f.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES('native-page','user',?,'write',?)`, user, f.adminID)
	expiry := time.Now().Add(time.Hour).UTC()
	mustExec(t, f.db, `INSERT INTO user_sessions(id,user_id,expires_at) VALUES('native-scope-session',?,?)`, user, expiry.Format(time.RFC3339Nano))
	u := &AuthUser{ID: user, SessionID: "native-scope-session", CredentialExpiresAt: expiry}
	source, err := captureOriginalHumanCredential(t.Context(), f.db, u, AuthKindSession, "")
	if err != nil {
		t.Fatal(err)
	}
	u.OriginalCredential = source
	return auth.WithOriginalCredential(context.WithValue(context.WithValue(context.WithValue(t.Context(), ctxUser, u), ctxAuthKind, AuthKindSession), ctxWorkspaceID, f.wsID), *source)
}

// craftNativeStreamBound runs exactly the check the SSE loop repeats every five
// seconds before each lease delivery (events → bound). An error is what makes
// the stream send session_retired.
func craftNativeStreamBound(m *craftNativeHTTP, ctx context.Context, token, id string) error {
	r := httptest.NewRequest(http.MethodGet, "/api/v1/pages/native-page/craft/events", nil)
	r = r.WithContext(context.WithValue(ctx, craftNativeStreamKey{}, id))
	r.SetPathValue("slug", "native-page")
	r.Header.Set("X-Craft-Session", token)
	_, err := m.bound(r)
	return err
}

// dev3 F1: an unrelated workspace mutation (another Page, a grant on another
// Page, a new member) bumps the broad workspace delivery epoch. It must not
// retire a native editor whose own rights on its own Page are unchanged. A
// real loss of read/write on this Page, membership loss and credential
// revocation must still retire it, including revoke-then-restore (ABA).
func TestCraftNativeSessionSurvivesUnrelatedWorkspaceAuthorityChanges(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(t *testing.T, f *pagesTransferRig)
		retire bool
		// sameEpoch marks a control row that does not touch the workspace epoch.
		sameEpoch bool
	}{
		{name: "create another page", mutate: func(t *testing.T, f *pagesTransferRig) {
			f.seedPage(t, "scope-other", "scope-other", "Other", f.adminID, "")
		}},
		{name: "grant on another page to another member", mutate: func(t *testing.T, f *pagesTransferRig) {
			f.seedPage(t, "scope-other", "scope-other", "Other", f.adminID, "")
			mustExec(t, f.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES('scope-other','user',?,'read',?)`, f.userID, f.adminID)
		}},
		{name: "revoke grant on another page from another member", mutate: func(t *testing.T, f *pagesTransferRig) {
			f.seedPage(t, "scope-other", "scope-other", "Other", f.adminID, "")
			mustExec(t, f.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES('scope-other','user',?,'read',?)`, f.userID, f.adminID)
			mustExec(t, f.db, `DELETE FROM page_grants WHERE page_id='scope-other'`)
		}},
		{name: "grant on this page to another member", mutate: func(t *testing.T, f *pagesTransferRig) {
			mustExec(t, f.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES('native-page','user',?,'read',?)`, f.userID, f.adminID)
		}},
		{name: "add workspace member", mutate: func(t *testing.T, f *pagesTransferRig) {
			mustExec(t, f.db, `INSERT INTO users(id,email) VALUES('scope-new','scope-new@example.test')`)
			mustExec(t, f.db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('scope-new-member',?,'scope-new','MEMBER')`, f.wsID)
		}},
		{name: "another member logs in", sameEpoch: true, mutate: func(t *testing.T, f *pagesTransferRig) {
			mustExec(t, f.db, `INSERT INTO user_sessions(id,user_id,expires_at) VALUES('scope-other-login',?,?)`, f.userID, time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano))
		}},
		{name: "revoke this page grant", retire: true, mutate: func(t *testing.T, f *pagesTransferRig) {
			mustExec(t, f.db, `DELETE FROM page_grants WHERE page_id='native-page' AND subject_id='native-scope-writer'`)
		}},
		{name: "downgrade this page grant to comment", retire: true, mutate: func(t *testing.T, f *pagesTransferRig) {
			mustExec(t, f.db, `UPDATE page_grants SET level='comment' WHERE page_id='native-page' AND subject_id='native-scope-writer'`)
		}},
		{name: "revoke and restore this page grant", retire: true, mutate: func(t *testing.T, f *pagesTransferRig) {
			mustExec(t, f.db, `DELETE FROM page_grants WHERE page_id='native-page' AND subject_id='native-scope-writer'`)
			mustExec(t, f.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES('native-page','user','native-scope-writer','write',?)`, f.adminID)
		}},
		{name: "transfer this page owner away and back", retire: true, mutate: func(t *testing.T, f *pagesTransferRig) {
			mustExec(t, f.db, `UPDATE pages SET owner_user_id=? WHERE id='native-page'`, f.adminID)
			mustExec(t, f.db, `UPDATE pages SET owner_user_id=? WHERE id='native-page'`, f.userID)
		}},
		{name: "remove workspace membership", retire: true, mutate: func(t *testing.T, f *pagesTransferRig) {
			mustExec(t, f.db, `DELETE FROM workspace_members WHERE id='native-scope-member'`)
		}},
		{name: "revoke and restore credential", retire: true, mutate: func(t *testing.T, f *pagesTransferRig) {
			mustExec(t, f.db, `UPDATE user_sessions SET revoked_at='2026-10-27T00:00:00Z' WHERE id='native-scope-session'`)
			mustExec(t, f.db, `UPDATE user_sessions SET revoked_at=NULL WHERE id='native-scope-session'`)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			f, m, _ := craftNativeHTTPFixture(t)
			ctx := craftNativeGrantedWriter(t, f)
			token, id := craftNativeIssued(t, m, ctx)
			acquired := craftNativeLeaseCall(m, ctx, token, "native-page", "acquire", `{}`)
			if acquired.Code != 200 {
				t.Fatalf("calibration acquire: %d %s", acquired.Code, acquired.Body)
			}
			var lease struct {
				Generation string `json:"generation"`
			}
			if err := json.Unmarshal(acquired.Body.Bytes(), &lease); err != nil {
				t.Fatal(err)
			}
			if err := craftNativeStreamBound(m, ctx, token, id); err != nil {
				t.Fatalf("calibration stream bound: %v", err)
			}
			var before string
			if err := f.db.QueryRow(`SELECT CAST(authority_revision AS TEXT) FROM workspaces WHERE id=?`, f.wsID).Scan(&before); err != nil {
				t.Fatal(err)
			}
			test.mutate(t, f)
			var after string
			if err := f.db.QueryRow(`SELECT CAST(authority_revision AS TEXT) FROM workspaces WHERE id=?`, f.wsID).Scan(&after); err != nil && err != sql.ErrNoRows {
				t.Fatal(err)
			}
			if (before == after) != test.sameEpoch {
				t.Fatalf("fixture workspace delivery epoch %s -> %s, sameEpoch=%v", before, after, test.sameEpoch)
			}
			err := craftNativeStreamBound(m, ctx, token, id)
			renew := craftNativeLeaseCall(m, ctx, token, "native-page", "renew", `{"generation":"`+lease.Generation+`"}`)
			if test.retire {
				if err == nil {
					t.Fatal("stream stayed bound after this user's rights on this page changed")
				}
				if renew.Code != 403 {
					t.Fatalf("retired session renewed: %d %s", renew.Code, renew.Body)
				}
				return
			}
			if err != nil {
				t.Fatalf("unrelated change retired the native session: %v", err)
			}
			if renew.Code != 200 {
				t.Fatalf("unrelated change blocked the holder's renewal: %d %s", renew.Code, renew.Body)
			}
		})
	}
}
