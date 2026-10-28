package api

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/mooring"
)

// Canonical SQL admission only: no engine, websocket or checkpoint delivery is
// claimed. Every intent which can precede latest checkpoint bytes is covered.
func TestCollaborationCommentGrantCannotReadLatest(t *testing.T) {
	r := pagesTransferSetup(t)
	r.seedPage(t, "comment-collab", "comment-collab", "Document", r.adminID, "")
	mustExec(t, r.db, `INSERT INTO mooring_collab_documents(page_id,workspace_id,application_id,revision,checkpoint_base64) VALUES('comment-collab',?,'wordcraft','0','e30=')`, r.wsID)
	mustExec(t, r.db, `INSERT INTO user_sessions(id,user_id,expires_at) VALUES('comment-collab-session',?,?)`, r.userID, time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano))
	sourceContext := func() context.Context {
		u := &AuthUser{ID: r.userID, SessionID: "comment-collab-session", CredentialExpiresAt: time.Now().Add(time.Hour)}
		source, err := captureOriginalHumanCredential(t.Context(), r.db, u, AuthKindSession, "")
		if err != nil {
			t.Fatal(err)
		}
		u.OriginalCredential = source
		return auth.WithOriginalCredential(context.WithValue(context.WithValue(t.Context(), ctxUser, u), ctxAuthKind, AuthKindSession), *source)
	}
	h := NewPageHandler(r.db, nil, newTestLogger())
	key := mooring.SessionKey{WorkspaceID: r.wsID, ArtifactID: "comment-collab", ApplicationID: "wordcraft"}
	check := func(ctx context.Context, intent string, want bool) {
		t.Helper()
		tx, err := r.db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		if _, err = tx.Exec(`UPDATE mooring_collab_documents SET revision=revision WHERE page_id=?`, key.ArtifactID); err != nil {
			t.Fatal(err)
		}
		_, err = h.authorizeCollaborationHumanPage(ctx, tx, key, intent)
		if (err == nil) != want {
			t.Fatalf("intent %s allowed=%v want=%v: %v", intent, err == nil, want, err)
		}
	}
	for _, intent := range []string{"read", "read_checkpoint", "outcome", "write"} {
		check(sourceContext(), intent, false)
	}
	mustExec(t, r.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES('comment-collab','user',?,'comment',?)`, r.userID, r.adminID)
	for _, level := range []string{"comment", "read", "produce", "write"} {
		mustExec(t, r.db, `UPDATE page_grants SET level=? WHERE page_id='comment-collab'`, level)
		ctx := sourceContext()
		for _, intent := range []string{"read", "read_checkpoint", "outcome"} {
			check(ctx, intent, level != "comment")
		}
		check(ctx, "write", level == "write")
	}
	// A still-valid original credential does not retain a withdrawn read grant.
	mustExec(t, r.db, `UPDATE page_grants SET level='read' WHERE page_id='comment-collab'`)
	captured := sourceContext()
	check(captured, "read_checkpoint", true)
	mustExec(t, r.db, `UPDATE page_grants SET level='comment' WHERE page_id='comment-collab'`)
	for _, intent := range []string{"read", "read_checkpoint", "outcome"} {
		check(captured, intent, false)
	}
}

func TestCollaborationCommentGrantRetiresCapturedReadAdmission(t *testing.T) {
	r, s, owner, ctx, _ := collaborationAdmissionFixture(t)
	mustExec(t, r.db, `UPDATE pages SET owner_user_id=? WHERE id='admission-page'`, r.adminID)
	mustExec(t, r.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES('admission-page','user',?,'read',?)`, r.userID, r.adminID)
	ticket, admission, err := s.issue(ctx, owner, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.consume(s.runtime, owner, ticket, "comment-revoke-join"); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest("POST", "/", nil).WithContext(ctx)
	if _, err = s.bindHumanRequest(request, admission.participant); err != nil {
		t.Fatal(err)
	}
	check := func(intent string, want bool) {
		t.Helper()
		err := s.runHumanOperation(request, admission.participant, func(bound context.Context) error {
			tx, err := r.db.BeginTx(bound, nil)
			if err != nil {
				return err
			}
			defer tx.Rollback()
			if err = s.owners.Check(bound, tx, owner); err != nil {
				return err
			}
			return s.authorizeBatchAtBinding(bound, tx, mooring.BatchAccess{Intent: intent, Scope: s.scope, Participant: admission.participant}, s.runtime, s.bindingID)
		})
		if (err == nil) != want {
			t.Fatalf("captured %s allowed=%v want=%v: %v", intent, err == nil, want, err)
		}
	}
	check("read_checkpoint", true)
	mustExec(t, r.db, `UPDATE page_grants SET level='comment' WHERE page_id='admission-page'`)
	check("read_checkpoint", false)
	check("outcome", false)
	if _, _, err = s.issue(ctx, owner, false); err == nil {
		t.Fatal("comment-only received fresh checkpoint admission")
	}
	// Regrant permits a fresh read, never resurrection of the captured admission.
	mustExec(t, r.db, `UPDATE page_grants SET level='read' WHERE page_id='admission-page'`)
	check("read_checkpoint", false)
	if _, _, err = s.issue(ctx, owner, false); err != nil {
		t.Fatal("fresh read regrant refused", err)
	}
}
