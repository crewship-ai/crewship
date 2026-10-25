package api

import (
	"context"
	"database/sql"
	"errors"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/database"
	"github.com/crewship-ai/crewship/internal/mooring"
)

// Real canonical SQL, source credentials, admission and sealed author callbacks.
// The runtime identity is explicitly synthetic: no Rust delta/transport, FIFO,
// Saved, content delivery or end-to-end D3 acceptance is qualified by this test.
func TestCollaborationAuthorD3SealedJanaDeniedIndependentHumanAndAgentAllowed(t *testing.T) {
	x := newCollaborationAgentTestFixture(t)
	// Use the production FULL handle for actual owner acquisition/checks.
	var seq int
	var name, path string
	if err := x.f.rig.db.QueryRow(`PRAGMA database_list`).Scan(&seq, &name, &path); err != nil || name != "main" || path == "" {
		t.Fatal("fixture database path", err)
	}
	full, err := database.Open("file:" + path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { full.Close() })
	x.f.rig.db, x.f.pages.db = full.DB, full.DB
	mustExec(t, full.DB, `INSERT INTO users(id,email) VALUES('sealed-jana','sealed-jana@synthetic.invalid')`)
	mustExec(t, full.DB, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('sealed-jana-member',?,'sealed-jana','MEMBER')`, x.key.WorkspaceID)
	mustExec(t, full.DB, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES(?,'user','sealed-jana','write',?)`, x.key.ArtifactID, x.f.a.HumanUserID)
	expiry := time.Now().Add(time.Hour)
	mustExec(t, full.DB, `INSERT INTO user_sessions(id,user_id,expires_at) VALUES('sealed-jana-session','sealed-jana',?)`, expiry.UTC().Format(time.RFC3339Nano))
	source, err := captureOriginalHumanCredential(t.Context(), full, &AuthUser{ID: "sealed-jana", SessionID: "sealed-jana-session", CredentialExpiresAt: expiry}, AuthKindSession, "")
	if err != nil {
		t.Fatal(err)
	}
	jana := context.WithValue(t.Context(), ctxUser, &AuthUser{ID: source.UserID, SessionID: source.ID, OriginalCredential: source})
	jana = auth.WithOriginalCredential(context.WithValue(jana, ctxAuthKind, AuthKindSession), *source)
	jana = collaborationIngressTestContext(t, x.fence, jana)
	owners := mooring.NewOwnerStore(full.DB, true, x.clock, time.Minute)
	owner, err := owners.Acquire(t.Context(), x.key, "sealed-d3-owner")
	if err != nil {
		t.Fatal(err)
	}
	stamp, err := collaborationHumanLifecycleStamp(x.fence)
	if err != nil {
		t.Fatal(err)
	}
	h := &collaborationHumanRegistry{lifecycle: stamp, bindingID: "synthetic-d3-runtime", runtime: &mooring.Runtime{}, pages: x.f.pages, owners: owners, fence: x.fence, clock: x.clock, scope: x.key, sessionID: "synthetic-d3-session", limit: 4, admissions: make(map[string]collaborationHumanAdmission), tickets: make(map[string]collaborationHumanJoinTicket)}
	_, ownerAdmission, err := h.issue(x.human, owner, true)
	if err != nil {
		t.Fatal("original independent human", err)
	}
	_, janaAdmission, err := h.issue(jana, owner, true)
	if err != nil {
		t.Fatal("original Jana", err)
	}
	registry, err := newCollaborationSubmissionRegistry(h)
	if err != nil {
		t.Fatal(err)
	}
	binding, err := h.storeBinding()
	if err != nil {
		t.Fatal(err)
	}
	ownerChange := submissionTestChange(t, x.key, "d3_independent_human", "", ownerAdmission.participant)
	janaChange := submissionTestChange(t, x.key, "d3_jana", "", janaAdmission.participant)
	sealHuman := func(ctx context.Context, admission collaborationHumanAdmission, change mooring.BatchChange) {
		t.Helper()
		r := httptest.NewRequest("POST", "/", nil).WithContext(ctx)
		if err := h.runHumanOperation(r, admission.participant, func(ctx context.Context) error {
			return registry.run(ctx, binding, func(ctx context.Context) error { return registry.sealHuman(ctx, []mooring.BatchChange{change}) })
		}); err != nil {
			t.Fatal("seal actual original human", err)
		}
	}
	sealHuman(x.human, ownerAdmission, ownerChange)
	sealHuman(jana, janaAdmission, janaChange)
	handle := x.attach(t, x.prepare(t))
	r := x.request()
	var agentChange mooring.BatchChange
	var agentOriginal collaborationAgentPageAccess
	err = registry.run(r.Context(), binding, func(ctx context.Context) error {
		p, err := registry.admitAgent(ctx, r, x.store, handle)
		if err != nil {
			return err
		}
		agentChange = submissionTestChange(t, x.key, "d3_independent_agent", x.workID, p.id)
		return registry.sealAgent(ctx, r, p, []mooring.BatchChange{agentChange})
	})
	if err != nil {
		t.Fatal("seal actual authenticated agent", err)
	}
	x.tx(t, func(tx *sql.Tx) {
		agentOriginal, err = x.store.Check(r, tx, handle, x.key, "task")
		if err != nil {
			t.Fatal(err)
		}
	})
	authority := registry.bind(owner, binding)
	check := func(change mooring.BatchChange, purpose mooring.SessionBatchPurpose, wantDenied bool) {
		t.Helper()
		err := registry.run(t.Context(), binding, func(ctx context.Context) error {
			return registry.withWriter(ctx, func(tx *sql.Tx) error {
				if err := owners.Check(ctx, tx, owner); err != nil {
					return err
				}
				access := submissionTestAccess(owner, change, purpose)
				if purpose == mooring.SessionCurrentTask {
					access.Access.Intent = "task"
				}
				return authority(ctx, tx, access)
			})
		})
		var denied *mooring.BatchDenied
		if wantDenied {
			if !errors.As(err, &denied) || denied.Code != "permission_revoked" {
				t.Fatal("expected Jana canonical revocation", err)
			}
		} else if err != nil {
			t.Fatal("independent sealed original denied", err)
		}
	}
	check(ownerChange, mooring.SessionCurrentWrite, false)
	check(janaChange, mooring.SessionCurrentWrite, false)
	check(agentChange, mooring.SessionCurrentTask, false)
	mustExec(t, full.DB, `DELETE FROM page_grants WHERE page_id=? AND subject_type='user' AND subject_id='sealed-jana'`, x.key.ArtifactID)
	check(janaChange, mooring.SessionCurrentWrite, true)
	check(ownerChange, mooring.SessionCurrentWrite, false)
	check(agentChange, mooring.SessionCurrentTask, false)
	check(janaChange, mooring.SessionProcessSealedOutcome, false)
	x.tx(t, func(tx *sql.Tx) {
		permission, err := h.checkCaptured(t.Context(), tx, ownerAdmission, true, 0)
		if err != nil || permission == ownerAdmission.permission {
			t.Fatal("delivery did not advance independently of original author", err)
		}
		d, err := x.store.DecideCaptured(t.Context(), tx, agentOriginal, x.key, "task")
		if err != nil || d.kind != collaborationAgentAllowed || d.access.authorVector != agentOriginal.authorVector || d.access.permissionVersion != agentOriginal.permissionVersion || d.access.credentialExpiry.After(agentOriginal.credentialExpiry) {
			t.Fatal("original agent identity/vector/horizon changed", err)
		}
		if h.admissions[ownerAdmission.participant] != ownerAdmission || h.admissions[janaAdmission.participant] != janaAdmission {
			t.Fatal("revalidation refreshed original human admission")
		}
	})
}
