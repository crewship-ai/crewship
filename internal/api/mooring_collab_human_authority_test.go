package api

import (
	"context"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/mooring"
)

// Real canonical Page/credential SQL with an opaque fixture checkpoint. No
// engine, transport, ticket or content-delivery qualification is claimed here.
func TestCollaborationHumanAuthorityUsesCanonicalPagesAndOriginalCredential(t *testing.T) {
	r := pagesTransferSetup(t)
	r.seedPage(t, "collab-auth-page", "collab-auth-page", "Document", r.adminID, "")
	mustExec(t, r.db, `INSERT INTO mooring_collab_documents(page_id,workspace_id,application_id,revision,checkpoint_base64) VALUES('collab-auth-page',?,'wordcraft','0','e30=')`, r.wsID)
	h := NewPageHandler(r.db, nil, newTestLogger())
	key := mooring.SessionKey{WorkspaceID: r.wsID, ArtifactID: "collab-auth-page", ApplicationID: "wordcraft"}
	for _, user := range []string{r.userID, r.adminID} {
		mustExec(t, r.db, `INSERT INTO user_sessions(id,user_id,expires_at) VALUES(?,?,?)`, "collab-"+user, user, time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano))
	}
	sourceContext := func(user string) context.Context {
		t.Helper()
		value := &AuthUser{ID: user, SessionID: "collab-" + user, CredentialExpiresAt: time.Now().Add(30 * time.Minute)}
		source, err := captureOriginalHumanCredential(t.Context(), r.db, value, AuthKindSession, "")
		if err != nil {
			t.Fatal(err)
		}
		value.OriginalCredential = source
		ctx := context.WithValue(t.Context(), ctxUser, value)
		ctx = context.WithValue(ctx, ctxRole, "OWNER") // deliberately stale/forged role
		return auth.WithOriginalCredential(ctx, *source)
	}
	check := func(ctx context.Context, intent string, want bool) collaborationHumanPageAccess {
		t.Helper()
		tx, err := r.db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		// Acquire the same SQLite writer before checking policy; no TOCTOU cache.
		if _, err = tx.Exec(`UPDATE mooring_collab_documents SET revision=revision WHERE page_id=?`, key.ArtifactID); err != nil {
			t.Fatal(err)
		}
		proof, err := h.authorizeCollaborationHumanPage(ctx, tx, key, intent)
		if (err == nil) != want {
			t.Fatalf("%s allowed=%v want=%v: %v", intent, err == nil, want, err)
		}
		if want && (proof.record.ID != key.ArtifactID || proof.permissionVersion == "" || proof.memberRevision < 1 || proof.memberID == "") {
			t.Fatal("missing current authority binding", proof)
		}
		return proof
	}
	ctx := sourceContext(r.userID)
	check(ctx, "read", false)
	check(ctx, "write", false)
	mustExec(t, r.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES('collab-auth-page','user',?,'read',?)`, r.userID, r.adminID)
	ctx = sourceContext(r.userID)
	read := check(ctx, "read", true)
	check(ctx, "write", false) // role in request context must not escalate a reader
	mustExec(t, r.db, `UPDATE page_grants SET level='write' WHERE page_id='collab-auth-page'`)
	ctx = sourceContext(r.userID)
	written := check(ctx, "write", true)
	if read.permissionVersion == written.permissionVersion {
		t.Fatal("policy mutation did not change permission binding")
	}
	check(ctx, "outcome", true)
	check(ctx, "unknown-intent", false)
	// Crew grants use canonical Page policy and disappear with membership.
	mustExec(t, r.db, `DELETE FROM page_grants WHERE page_id='collab-auth-page'`)
	r.seedCrew(t, "collab-write-crew", "Writers", "collab-write-crew")
	r.seedCrewMember(t, "collab-write-crew", r.userID)
	mustExec(t, r.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES('collab-auth-page','crew','collab-write-crew','write',?)`, r.adminID)
	check(sourceContext(r.userID), "write", true)
	mustExec(t, r.db, `DELETE FROM crew_members WHERE crew_id='collab-write-crew' AND user_id=?`, r.userID)
	check(sourceContext(r.userID), "write", false)
	// A folder ACL is canonical Page authority and outlives its audit setter.
	mustExec(t, r.db, `DELETE FROM page_grants WHERE page_id='collab-auth-page'`)
	r.seedCrew(t, "collab-folder-crew", "Folder", "collab-folder-crew")
	mustExec(t, r.db, `INSERT INTO page_folders(id,workspace_id,owner_crew_id,name,slug,created_at,updated_at) VALUES('collab-folder',?,'collab-folder-crew','Folder','collab-folder','2026-10-23T00:00:00Z','2026-10-23T00:00:00Z')`, r.wsID)
	mustExec(t, r.db, `UPDATE pages SET folder_id='collab-folder' WHERE id='collab-auth-page'`)
	mustExec(t, r.db, `INSERT INTO page_folder_acl(folder_id,subject_type,subject_id,can_read,can_write,set_by_user_id,set_at) VALUES('collab-folder','user',?,1,1,?,'2026-10-23T00:00:00Z')`, r.userID, r.adminID)
	check(sourceContext(r.userID), "read", true)
	check(sourceContext(r.userID), "write", true)
	// A corrupt cross-workspace folder pointer must not lend its ACL.
	mustExec(t, r.db, `INSERT INTO workspaces(id,name,slug) VALUES('collab-foreign-ws','Foreign','collab-foreign-ws')`)
	mustExec(t, r.db, `UPDATE page_folders SET workspace_id='collab-foreign-ws' WHERE id='collab-folder'`)
	check(sourceContext(r.userID), "read", false)
	check(sourceContext(r.userID), "write", false)
	mustExec(t, r.db, `UPDATE page_folders SET workspace_id=? WHERE id='collab-folder'`, r.wsID)
	mustExec(t, r.db, `DELETE FROM workspace_members WHERE workspace_id=? AND user_id=?`, r.wsID, r.adminID)
	check(sourceContext(r.userID), "write", true)
	// Explicit private audience is never widened by a folder grant.
	mustExec(t, r.db, `UPDATE pages SET private_audience=1 WHERE id='collab-auth-page'`)
	check(sourceContext(r.userID), "read", false)
	check(sourceContext(r.userID), "write", false)
	// Match Docs custody: a trusted workspace OWNER can read and write another
	// human's private output; a restricted custodian can only read their own.
	mustExec(t, r.db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('collab-restored-admin',?,?,'OWNER')`, r.wsID, r.adminID)
	mustExec(t, r.db, `UPDATE pages SET owner_user_id=? WHERE id='collab-auth-page'`, r.userID)
	check(sourceContext(r.adminID), "read", true)
	check(sourceContext(r.adminID), "write", true)
	mustExec(t, r.db, `UPDATE workspace_members SET access_mode='restricted' WHERE workspace_id=? AND user_id=?`, r.wsID, r.userID)
	ctx = sourceContext(r.userID)
	check(ctx, "read", true)
	check(ctx, "write", false)
	// Revoke/restore cannot refresh the immutable original credential snapshot.
	mustExec(t, r.db, `UPDATE user_sessions SET revoked_at='2026-10-23T00:00:00Z' WHERE user_id=?`, r.userID)
	mustExec(t, r.db, `UPDATE user_sessions SET revoked_at=NULL WHERE user_id=?`, r.userID)
	check(ctx, "read", false)
	check(sourceContext(r.userID), "read", true)
	if _, err := h.authorizeCollaborationHumanPage(ctx, nil, key, "read"); err == nil {
		t.Fatal("missing writer accepted")
	}
	check(t.Context(), "read", false)
}

func TestCollaborationHumanInitializationAndScopeAreExplicit(t *testing.T) {
	r := pagesTransferSetup(t)
	h := NewPageHandler(r.db, nil, newTestLogger())
	r.seedPage(t, "collab-init", "collab-init", "New document", r.userID, "")
	raw := "crewship_cli_collab-auth-synthetic"
	mustExec(t, r.db, `INSERT INTO cli_tokens(id,user_id,name,token_hash,tier,scopes) VALUES('collab-cli',?,'fixture',?,'STANDARD','["pages:read"]')`, r.userID, hashStandard(raw))
	makeContext := func() context.Context {
		value := &AuthUser{ID: r.userID, TokenID: "collab-cli"}
		source, err := captureOriginalHumanCredential(t.Context(), r.db, value, AuthKindCLIToken, raw)
		if err != nil {
			t.Fatal(err)
		}
		value.OriginalCredential = source
		return auth.WithOriginalCredential(context.WithValue(t.Context(), ctxUser, value), *source)
	}
	key := mooring.SessionKey{WorkspaceID: r.wsID, ArtifactID: "collab-init", ApplicationID: "wordcraft"}
	invoke := func(intent string) (collaborationHumanPageAccess, error) {
		ctx := makeContext()
		tx, err := r.db.BeginTx(t.Context(), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		if _, err = tx.Exec(`UPDATE pages SET name=name WHERE id=?`, key.ArtifactID); err != nil {
			t.Fatal(err)
		}
		return h.authorizeCollaborationHumanPage(ctx, tx, key, intent)
	}
	if _, err := invoke("initialize"); err == nil {
		t.Fatal("read-scoped credential initialized document")
	}
	mustExec(t, r.db, `UPDATE cli_tokens SET scopes='["pages:read","pages:write"]' WHERE id='collab-cli'`)
	if _, err := invoke("initialize"); err != nil {
		t.Fatal("empty human-owned Page refused", err)
	}
	mustExec(t, r.db, `INSERT INTO documents(workspace_id,page_id,schema_version,snapshot,projection) VALUES(?,'collab-init',1,'{}','{}')`, r.wsID)
	if _, err := invoke("initialize"); err == nil {
		t.Fatal("initialization replaced legacy content")
	}
	key.ApplicationID = "other"
	if _, err := invoke("initialize"); err == nil {
		t.Fatal("unregistered application accepted")
	}
}
