package api

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/auth"
)

func TestCraftNativeCommentOnlyNeverGetsLatestOrLease(t *testing.T) {
	f, native, _ := craftNativeHTTPFixture(t)
	user := "native-comment-person"
	mustExec(t, f.db, `INSERT INTO users(id,email,full_name) VALUES(?,'native-comment@example.test','Commenter')`, user)
	mustExec(t, f.db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES(?,?,?,'MEMBER')`, generateCUID(), f.wsID, user)
	expiry := time.Now().Add(time.Hour).UTC()
	mustExec(t, f.db, `INSERT INTO user_sessions(id,user_id,expires_at) VALUES('native-comment-session',?,?)`, user, expiry.Format(time.RFC3339Nano))
	u := &AuthUser{ID: user, SessionID: "native-comment-session", CredentialExpiresAt: expiry}
	source, err := captureOriginalHumanCredential(t.Context(), f.db, u, AuthKindSession, "")
	if err != nil {
		t.Fatal(err)
	}
	u.OriginalCredential = source
	ctx := auth.WithOriginalCredential(context.WithValue(context.WithValue(context.WithValue(t.Context(), ctxUser, u), ctxAuthKind, AuthKindSession), ctxWorkspaceID, f.wsID), *source)
	mustExec(t, f.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES('native-page','user',?,'comment',?)`, user, f.userID)
	if got := craftNativeSessionCall(native, ctx); got.Code != 403 {
		t.Fatalf("comment-only opened latest observer: %d %s", got.Code, got.Body)
	}
	// Pre-existing explicit read still opens the observer, without granting edit.
	mustExec(t, f.db, `UPDATE page_grants SET level='read' WHERE page_id='native-page' AND subject_id=?`, user)
	read := craftNativeSessionCall(native, ctx)
	if read.Code != 201 {
		t.Fatalf("existing read path changed: %d %s", read.Code, read.Body)
	}
	var admitted struct {
		Token string `json:"session_token"`
	}
	if err := json.Unmarshal(read.Body.Bytes(), &admitted); err != nil {
		t.Fatal(err)
	}
	if got := craftNativeLeaseCall(native, ctx, admitted.Token, "native-page", "acquire", `{}`); got.Code != 403 {
		t.Fatalf("reader acquired edit: %d %s", got.Code, got.Body)
	}
	// A writer remains admitted, but downgrade revokes the established lease
	// immediately; neither its old token nor an unexpired TTL keeps write rights.
	mustExec(t, f.db, `UPDATE page_grants SET level='write' WHERE page_id='native-page' AND subject_id=?`, user)
	token, _ := craftNativeIssued(t, native, ctx)
	acquired := craftNativeLeaseCall(native, ctx, token, "native-page", "acquire", `{}`)
	if acquired.Code != 200 {
		t.Fatalf("writer calibration %d %s", acquired.Code, acquired.Body)
	}
	var lease struct {
		Generation string `json:"generation"`
	}
	if err := json.Unmarshal(acquired.Body.Bytes(), &lease); err != nil {
		t.Fatal(err)
	}
	mustExec(t, f.db, `UPDATE page_grants SET level='comment' WHERE page_id='native-page' AND subject_id=?`, user)
	if got := craftNativeLeaseCall(native, ctx, token, "native-page", "renew", fmt.Sprintf(`{"generation":%q}`, lease.Generation)); got.Code != 403 {
		t.Fatalf("downgraded writer renewed: %d %s", got.Code, got.Body)
	}
}

func TestCraftNativeLeaseReportsCurrentEditCapability(t *testing.T) {
	f, native, owner := craftNativeHTTPFixture(t)
	ownerToken, _ := craftNativeIssued(t, native, owner)
	read := func(ctx context.Context, token string, wantEdit bool) {
		t.Helper()
		got := craftNativeLeaseCall(native, ctx, token, "native-page", "read", `{}`)
		var view map[string]any
		if got.Code != 200 || json.Unmarshal(got.Body.Bytes(), &view) != nil || view["can_edit"] != wantEdit {
			t.Fatalf("edit capability want %v: %d %s", wantEdit, got.Code, got.Body)
		}
	}
	read(owner, ownerToken, true)
	mustExec(t, f.db, `INSERT INTO users(id,email) VALUES('native-preview-user','native-preview@example.test')`)
	mustExec(t, f.db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES(?,?,'native-preview-user','MEMBER')`, generateCUID(), f.wsID)
	expiry := time.Now().Add(time.Hour).UTC()
	mustExec(t, f.db, `INSERT INTO user_sessions(id,user_id,expires_at) VALUES('native-preview-source','native-preview-user',?)`, expiry.Format(time.RFC3339Nano))
	mustExec(t, f.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES('native-page','user','native-preview-user','read',?)`, f.userID)
	u := &AuthUser{ID: "native-preview-user", SessionID: "native-preview-source", CredentialExpiresAt: expiry}
	source, err := captureOriginalHumanCredential(t.Context(), f.db, u, AuthKindSession, "")
	if err != nil {
		t.Fatal(err)
	}
	u.OriginalCredential = source
	ctx := auth.WithOriginalCredential(context.WithValue(context.WithValue(context.WithValue(t.Context(), ctxUser, u), ctxAuthKind, AuthKindSession), ctxWorkspaceID, f.wsID), *source)
	token, _ := craftNativeIssued(t, native, ctx)
	read(ctx, token, false)
	ownerToken, _ = craftNativeIssued(t, native, owner)
	if got := craftNativeLeaseCall(native, owner, ownerToken, "native-page", "acquire", `{}`); got.Code != 200 {
		t.Fatal(got.Code, got.Body)
	}
	read(ctx, token, false)
	// A fresh editor admission can request custody even while someone else holds it.
	mustExec(t, f.db, `UPDATE page_grants SET level='write' WHERE page_id='native-page' AND subject_id='native-preview-user'`)
	token, _ = craftNativeIssued(t, native, ctx)
	read(ctx, token, true)
}
