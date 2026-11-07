package api

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/auth"
)

// craftNativeFreshRequestContext is what the auth middleware builds for the
// writer's next request: the credential row read again, with the revision the
// user has now. craftNativeGrantedWriter captured the one at admission.
func craftNativeFreshRequestContext(t *testing.T, f *pagesTransferRig, user, session string) context.Context {
	t.Helper()
	u := &AuthUser{ID: user, SessionID: session, CredentialExpiresAt: time.Now().Add(time.Hour).UTC()}
	source, err := captureOriginalHumanCredential(t.Context(), f.db, u, AuthKindSession, "")
	if err != nil {
		t.Fatal(err)
	}
	u.OriginalCredential = source
	return auth.WithOriginalCredential(context.WithValue(context.WithValue(context.WithValue(t.Context(), ctxUser, u), ctxAuthKind, AuthKindSession), ctxWorkspaceID, f.wsID), *source)
}

// memberElsewhere seeds the holder's row in another workspace before admission.
func memberElsewhere(role, mode string) func(*testing.T, *pagesTransferRig) {
	return func(t *testing.T, f *pagesTransferRig) {
		t.Helper()
		mustExec(t, f.db, `INSERT INTO workspaces(id,name,slug) VALUES('native-other-ws','Other','native-other-ws')`)
		mustExec(t, f.db, `INSERT INTO workspace_members(id,workspace_id,user_id,role,access_mode) VALUES('native-other-member','native-other-ws','native-scope-writer',?,?)`, role, mode)
	}
}

// RC f3035b06c, Word soak: save 1004 of 2,000 was refused with 403 "native
// Craft access denied" while the same owner created a workspace elsewhere.
// Membership of ANOTHER workspace changes nothing on this Page, in the same
// sense as the dev3 F1 rows of TestCraftNativeSessionSurvivesUnrelatedWorkspaceAuthorityChanges,
// so the holder's open editor must keep its session and lease.
func TestCraftNativeSessionSurvivesHolderMembershipElsewhere(t *testing.T) {
	const user = "native-scope-writer"
	for _, test := range []struct {
		name    string
		prepare func(t *testing.T, f *pagesTransferRig)
		mutate  func(t *testing.T, f *pagesTransferRig)
		retire  bool
	}{
		{name: "control: the holder signs in again", mutate: func(t *testing.T, f *pagesTransferRig) {
			mustExec(t, f.db, `INSERT INTO user_sessions(id,user_id,expires_at) VALUES('native-scope-login-2',?,?)`, user, time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano))
		}},
		{name: "the holder creates another workspace", mutate: func(t *testing.T, f *pagesTransferRig) {
			mustExec(t, f.db, `INSERT INTO workspaces(id,name,slug) VALUES('native-other-ws','Other','native-other-ws')`)
			mustExec(t, f.db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('native-other-owner','native-other-ws',?,'OWNER')`, user)
		}},
		{name: "the holder is invited to another workspace", mutate: func(t *testing.T, f *pagesTransferRig) {
			mustExec(t, f.db, `INSERT INTO workspaces(id,name,slug) VALUES('native-other-ws','Other','native-other-ws')`)
			mustExec(t, f.db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('native-other-member','native-other-ws',?,'MEMBER')`, user)
		}},
		{name: "the holder leaves another workspace", prepare: memberElsewhere("MEMBER", "trusted"), mutate: func(t *testing.T, f *pagesTransferRig) {
			mustExec(t, f.db, `DELETE FROM workspace_members WHERE id='native-other-member'`)
		}},
		{name: "another workspace is deleted", prepare: memberElsewhere("OWNER", "trusted"), mutate: func(t *testing.T, f *pagesTransferRig) {
			mustExec(t, f.db, `UPDATE workspaces SET deleted_at='2026-11-06T00:00:00Z' WHERE id='native-other-ws'`)
			mustExec(t, f.db, `DELETE FROM workspace_members WHERE workspace_id='native-other-ws'`)
		}},
		{name: "the holder's role changes elsewhere", prepare: memberElsewhere("MEMBER", "trusted"), mutate: func(t *testing.T, f *pagesTransferRig) {
			mustExec(t, f.db, `UPDATE workspace_members SET role='ADMIN' WHERE id='native-other-member'`)
		}},
		{name: "the holder's capabilities change elsewhere", prepare: memberElsewhere("MEMBER", "trusted"), mutate: func(t *testing.T, f *pagesTransferRig) {
			mustExec(t, f.db, `UPDATE workspace_members SET capabilities='["pages:read"]' WHERE id='native-other-member'`)
		}},
		{name: "own workspace: role change retires", retire: true, mutate: func(t *testing.T, f *pagesTransferRig) {
			mustExec(t, f.db, `UPDATE workspace_members SET role='ADMIN' WHERE id='native-scope-member'`)
		}},
		{name: "own workspace: role down and back retires", retire: true, mutate: func(t *testing.T, f *pagesTransferRig) {
			mustExec(t, f.db, `UPDATE workspace_members SET role='VIEWER' WHERE id='native-scope-member'`)
			mustExec(t, f.db, `UPDATE workspace_members SET role='MEMBER' WHERE id='native-scope-member'`)
		}},
		{name: "own workspace: capability change retires", retire: true, mutate: func(t *testing.T, f *pagesTransferRig) {
			mustExec(t, f.db, `UPDATE workspace_members SET capabilities='["pages:read"]' WHERE id='native-scope-member'`)
		}},
		{name: "own workspace: restriction retires", retire: true, mutate: func(t *testing.T, f *pagesTransferRig) {
			mustExec(t, f.db, `UPDATE workspace_members SET access_mode='restricted' WHERE id='native-scope-member'`)
		}},
		{name: "own workspace: removal and rejoin retires", retire: true, mutate: func(t *testing.T, f *pagesTransferRig) {
			mustExec(t, f.db, `DELETE FROM workspace_members WHERE id='native-scope-member'`)
			mustExec(t, f.db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('native-scope-member',?,?,'MEMBER')`, f.wsID, user)
		}},
		{name: "restriction elsewhere retires", retire: true, prepare: memberElsewhere("MEMBER", "trusted"), mutate: func(t *testing.T, f *pagesTransferRig) {
			mustExec(t, f.db, `UPDATE workspace_members SET access_mode='restricted' WHERE id='native-other-member'`)
		}},
		{name: "a restricted membership elsewhere is a global ceiling and still retires", retire: true, mutate: func(t *testing.T, f *pagesTransferRig) {
			mustExec(t, f.db, `INSERT INTO workspaces(id,name,slug) VALUES('native-other-ws','Other','native-other-ws')`)
			mustExec(t, f.db, `INSERT INTO workspace_members(id,workspace_id,user_id,role,access_mode) VALUES('native-other-member','native-other-ws',?,'MEMBER','restricted')`, user)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			f, m, _ := craftNativeHTTPFixture(t)
			ctx := craftNativeGrantedWriter(t, f)
			if test.prepare != nil {
				test.prepare(t, f)
				ctx = craftNativeFreshRequestContext(t, f, user, "native-scope-session")
			}
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
			test.mutate(t, f)
			next := craftNativeFreshRequestContext(t, f, user, "native-scope-session")
			err := craftNativeStreamBound(m, next, token, id)
			renew := craftNativeLeaseCall(m, next, token, "native-page", "renew", `{"generation":"`+lease.Generation+`"}`)
			if test.retire {
				if err == nil || renew.Code != 403 {
					t.Fatalf("a restriction elsewhere left the session live: bound %v, renew %d", err, renew.Code)
				}
				return
			}
			if err != nil {
				t.Errorf("membership elsewhere retired the native session: %v", err)
			}
			if renew.Code != 200 {
				t.Errorf("membership elsewhere blocked the holder's renewal: %d %s", renew.Code, renew.Body)
			}
		})
	}
}
