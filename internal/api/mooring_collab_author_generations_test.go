package api

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/crewship-ai/crewship/internal/mooring"
)

func TestCollaborationAuthorVectorUnqualifiedSchemaNeverTerminal(t *testing.T) {
	for _, mutation := range []string{
		`CREATE TABLE page_boards(page_id TEXT)`,
		`CREATE VIEW PAGE_ARTIFACTS AS SELECT 'synthetic' AS page_id`,
		`CREATE TABLE page_boards(page_id TEXT); DROP TABLE page_boards`,
		`CREATE TABLE unrelated_schema_change(id TEXT)`,
		`ALTER TABLE mooring_authority_generations RENAME TO hidden_generations`,
	} {
		t.Run(mutation, func(t *testing.T) {
			x, captured := captureCollaborationAgentDecisionFixture(t)
			mustExec(t, x.f.rig.db, mutation)
			// A known relevant grant revocation cannot hide schema uncertainty.
			mustExec(t, x.f.rig.db, `DELETE FROM page_grants WHERE page_id='agent-shared-page'`)
			checkCollaborationAgentDecision(t, x, captured, x.key, collaborationAgentUnqualified)
			x.check(t, x.request(), captured.mandateRef, false)
		})
	}
}

func TestCollaborationAuthorVectorFreshCaptureRefusesOptionalSchemas(t *testing.T) {
	for _, table := range []string{"page_boards", "page_artifacts"} {
		t.Run(table, func(t *testing.T) {
			x := newCollaborationAgentTestFixture(t)
			mustExec(t, x.f.rig.db, `CREATE TABLE `+table+`(page_id TEXT)`)
			x.tx(t, func(tx *sql.Tx) {
				_, _, err := x.store.StartTask(x.human, tx, x.key, "task", x.f.a.MessageID, x.f.receipt)
				if err == nil {
					t.Fatal("fresh task entered unqualified extension schema")
				}
				var count int
				if err = tx.QueryRow(`SELECT count(*) FROM mooring_authority_generations`).Scan(&count); err != nil || count != 0 {
					t.Fatal("refused capture installed tracking", count, err)
				}
			})
		})
	}
}

func TestCollaborationAuthorVectorMissingLegacyCannotInstallOnUse(t *testing.T) {
	x, captured := captureCollaborationAgentDecisionFixture(t)
	forged := captured
	forged.authorVector = collaborationAuthorVector{}
	checkCollaborationAgentDecision(t, x, forged, x.key, collaborationAgentUnqualified)
	x.tx(t, func(tx *sql.Tx) {
		var before, after int
		if err := tx.QueryRow(`SELECT count(*) FROM mooring_authority_generations`).Scan(&before); err != nil {
			t.Fatal(err)
		}
		legacy := collaborationAuthorVector{workspace: x.key.WorkspaceID, user: "never-tracked", page: x.key.ArtifactID, schemaCookie: captured.authorVector.schemaCookie, userRevision: "1", pageRevision: "1"}
		if _, err := collaborationAuthorVectorChanged(t.Context(), tx, legacy); err == nil {
			t.Fatal("missing original tracking accepted")
		}
		if err := tx.QueryRow(`SELECT count(*) FROM mooring_authority_generations`).Scan(&after); err != nil || after != before {
			t.Fatal("revalidation allocated baseline", before, after, err)
		}
	})
}

func TestCollaborationAuthorVectorReadWriteScopeAndOriginalMetadataSurviveDeliveryChange(t *testing.T) {
	x, captured := captureCollaborationAgentDecisionFixture(t)
	original := captured
	// No author dependency changed; delivery gates must still invalidate.
	mustExec(t, x.f.rig.db, `UPDATE workspaces SET authority_revision=authority_revision+1 WHERE id=?`, x.key.WorkspaceID)
	x.tx(t, func(tx *sql.Tx) {
		for i := 0; i < 2; i++ {
			d, err := x.store.DecideCaptured(t.Context(), tx, captured, x.key, "task")
			if err != nil || d.kind != collaborationAgentAllowed {
				t.Fatal("unaffected original author denied", d.kind, err)
			}
			if d.access.permissionVersion != original.permissionVersion || d.access.authorVector != original.authorVector || d.access.memberID != original.memberID || d.access.memberRevision != original.memberRevision || d.access.credentialExpiry.After(original.credentialExpiry) || d.access.currentDeliveryPermission == original.permissionVersion {
				t.Fatal("refreshed original proof or failed to separate delivery epoch")
			}
			captured = d.access
		}
		current, err := x.store.Check(x.request(), tx, original.mandateRef, x.key, "read")
		if err != nil || current.permissionVersion != original.permissionVersion || current.authorVector != original.authorVector {
			t.Fatal("Check refreshed original author metadata", err)
		}
		if d, err := x.store.DecideCaptured(t.Context(), tx, current, x.key, "task"); err == nil || d.kind != collaborationAgentUnqualified {
			t.Fatal("read proof widened after delivery change")
		}
	})
}

func TestCollaborationAuthorVectorGrantABAAndCanonicalCurrentAllow(t *testing.T) {
	x, captured := captureCollaborationAgentDecisionFixture(t)
	mustExec(t, x.f.rig.db, `DELETE FROM page_grants WHERE page_id='agent-shared-page'; INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES('agent-shared-page','agent',?,'write',?)`, x.f.a.AgentID, x.f.a.HumanUserID)
	x.tx(t, func(tx *sql.Tx) {
		ctx := context.WithValue(t.Context(), pageAuthReaderKey{}, tx)
		page, err := x.f.pages.authorizeCollaborationPageSubject(ctx, tx, x.key, "task", x.f.receipt.OriginalCredential())
		if err != nil || !collaborationAgentGrantAllows(ctx, x.f.pages, x.key.WorkspaceID, page.record, x.f.a.AgentID, true) {
			t.Fatal("fixture failed to restore current canonical allow", err)
		}
	})
	checkCollaborationAgentDecision(t, x, captured, x.key, collaborationAgentCanonicalPermissionChanged)
	x.check(t, x.request(), captured.mandateRef, false)
}

func TestCollaborationAuthorVectorUnknownScopeCannotCapture(t *testing.T) {
	x := newCollaborationAgentTestFixture(t)
	x.tx(t, func(tx *sql.Tx) {
		bad := x.key
		bad.ApplicationID = "other"
		if _, err := captureCollaborationAuthorVector(t.Context(), tx, bad, x.f.a.HumanUserID, "", ""); err == nil {
			t.Fatal("foreign application captured")
		}
		if _, err := captureCollaborationAuthorVector(t.Context(), tx, mooring.SessionKey{}, x.f.a.HumanUserID, "", ""); err == nil {
			t.Fatal("empty scope captured")
		}
	})
}

func TestCollaborationAuthorVectorHumanPrecisionAndMissingLegacy(t *testing.T) {
	r, h, owner, ctx, _ := collaborationAdmissionFixture(t)
	mustExec(t, r.db, `INSERT INTO mooring_authority_generations(workspace_id,scope_kind,scope_id,revision) VALUES(?,'user',?,9007199254740993)`, h.scope.WorkspaceID, r.userID)
	_, admission, err := h.issue(ctx, owner, true)
	if err != nil || admission.authorVector.userRevision != "9007199254740993" {
		t.Fatal("original counter lost exact precision", err)
	}
	tx, err := r.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err = h.owners.Check(t.Context(), tx, owner); err != nil {
		t.Fatal(err)
	}
	legacy := admission
	legacy.authorVector = collaborationAuthorVector{}
	if _, err = h.checkCaptured(t.Context(), tx, legacy, true, 0); err == nil {
		t.Fatal("missing legacy vector adopted current authority")
	}
	if changed, err := collaborationHumanCapturedFenceChanged(t.Context(), tx, h.scope, legacy); err == nil || changed {
		t.Fatal("legacy vector became canonical denial")
	}
	if _, err = h.checkCaptured(t.Context(), tx, admission, true, 0); err != nil {
		t.Fatal("precise original vector failed revalidation", err)
	}
}

func TestCollaborationAuthorVectorHumanSchemaUncertaintyPrecedesCanonicalDeny(t *testing.T) {
	r, h, owner, ctx, _ := collaborationAdmissionFixture(t)
	_, admission, err := h.issue(ctx, owner, true)
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, r.db, `CREATE TABLE page_artifacts(page_id TEXT); DROP TABLE page_artifacts`)
	mustExec(t, r.db, `UPDATE user_sessions SET revoked_at='2026-10-24T01:00:00Z' WHERE id='admission-source'`)
	tx, err := r.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err = h.owners.Check(t.Context(), tx, owner); err != nil {
		t.Fatal(err)
	}
	if changed, err := collaborationHumanCapturedFenceChanged(t.Context(), tx, h.scope, admission); err == nil || changed {
		t.Fatal("schema drift was misclassified as canonical revocation")
	}
	if _, err = h.checkCaptured(t.Context(), tx, admission, true, 0); err == nil {
		t.Fatal("schema drift allowed existing author")
	} else if errors.Is(err, errCollaborationAuthorChanged) {
		t.Fatal("SQL/schema uncertainty became successful canonical recipient denial")
	}
}

func TestCollaborationAuthorVectorFreshTrackingSQLFailurePublishesNothing(t *testing.T) {
	x := newCollaborationAgentTestFixture(t)
	mustExec(t, x.f.rig.db, `CREATE TRIGGER author_capture_fault BEFORE INSERT ON mooring_authority_generations WHEN NEW.scope_kind='page' BEGIN SELECT RAISE(ABORT,'synthetic tracking failure'); END`)
	tx, err := x.f.rig.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`UPDATE mooring_session_writer_fence SET seq=seq WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	pending, _, err := x.store.StartTask(x.human, tx, x.key, "task", x.f.a.MessageID, x.f.receipt)
	if err == nil || pending != nil || len(x.store.pending) != 0 {
		t.Fatal("partial tracking published an original pending capability")
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	var keys, work int
	if err = x.f.rig.db.QueryRow(`SELECT (SELECT count(*) FROM mooring_authority_generations),(SELECT count(*) FROM work_items)`).Scan(&keys, &work); err != nil || keys != 0 || work != 0 {
		t.Fatal("failed capture rollback retained partial baseline or work", keys, work, err)
	}
}
