package api

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
	"github.com/crewship-ai/crewship/internal/mooring"
	"github.com/crewship-ai/crewship/internal/work"
)

type collaborationAgentTestClock struct {
	mu   sync.Mutex
	boot string
	tick time.Duration
}

func (c *collaborationAgentTestClock) Now() (string, time.Duration, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.boot, c.tick, nil
}
func (c *collaborationAgentTestClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.tick += d
}

type collaborationAgentTestFixture struct {
	f      *artifactPrincipalFixture
	store  *collaborationAgentMandateStore
	clock  *collaborationAgentTestClock
	fence  *collaborationIngressFence
	human  context.Context
	key    mooring.SessionKey
	workID string
}

func newCollaborationAgentTestFixture(t *testing.T, grantLevel ...string) *collaborationAgentTestFixture {
	t.Helper()
	f, source := originalSessionFixture(t)
	return newCollaborationAgentTestFixtureForPrincipal(t, f, source, grantLevel...)
}

func newCollaborationAgentTestFixtureForPrincipal(t *testing.T, f *artifactPrincipalFixture, source *auth.OriginalCredential, grantLevel ...string) *collaborationAgentTestFixture {
	t.Helper()
	receipt, e := NewInternalHandler(f.rig.db, "synthetic-mandate-ipc", newTestLogger()).originalHumanChatReceipt(t.Context(), f.a.ChatID, f.a.HumanUserID, nil, source)
	if e != nil || receipt == nil {
		t.Fatal(e)
	}
	f.receipt = receipt
	f.admit(t)
	f.rig.seedPage(t, "agent-shared-page", "agent-shared-page", "Shared", f.a.HumanUserID, "")
	mustExec(t, f.rig.db, `INSERT INTO mooring_collab_documents(page_id,workspace_id,application_id,revision,checkpoint_base64) VALUES('agent-shared-page',?,'wordcraft','0','e30=')`, f.a.WorkspaceID)
	level := "write"
	if len(grantLevel) > 0 {
		level = grantLevel[0]
	}
	mustExec(t, f.rig.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES('agent-shared-page','agent',?,?,?)`, f.a.AgentID, level, f.a.HumanUserID)
	mustExec(t, f.rig.db, `INSERT INTO workspace_artifact_creation_policy(workspace_id,enabled,version,updated_at) VALUES(?,1,1,'2026-10-23T00:00:00Z')`, f.a.WorkspaceID)
	mustExec(t, f.rig.db, `INSERT INTO workspace_artifact_creation_crews(workspace_id,crew_id) VALUES(?,?)`, f.a.WorkspaceID, f.a.CrewID)
	u := &AuthUser{ID: source.UserID, SessionID: source.ID, OriginalCredential: source}
	ctx := context.WithValue(t.Context(), ctxUser, u)
	ctx = context.WithValue(ctx, ctxAuthKind, AuthKindSession)
	ctx = auth.WithOriginalCredential(ctx, *source)
	f.pages.artifactMasterToken = "synthetic-agent-mandate-master"
	clock := &collaborationAgentTestClock{boot: "fixture-boot", tick: time.Second}
	fence, e := newCollaborationIngressFence()
	if e != nil {
		t.Fatal(e)
	}
	ctx = collaborationIngressTestContext(t, fence, ctx)
	store, e := newCollaborationAgentMandateStore(f.pages, true, fence, clock, time.Minute, 4)
	if e != nil {
		t.Fatal(e)
	}
	return &collaborationAgentTestFixture{f: f, store: store, clock: clock, fence: fence, human: ctx, key: mooring.SessionKey{WorkspaceID: f.a.WorkspaceID, ArtifactID: "agent-shared-page", ApplicationID: "wordcraft"}}
}
func (x *collaborationAgentTestFixture) tx(t *testing.T, fn func(*sql.Tx)) {
	t.Helper()
	tx, e := x.f.rig.db.BeginTx(t.Context(), nil)
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback()
	if _, e = tx.Exec(`UPDATE mooring_session_writer_fence SET seq=seq WHERE id=1`); e != nil {
		t.Fatal(e)
	}
	fn(tx)
	if e = tx.Commit(); e != nil {
		t.Fatal(e)
	}
}
func (x *collaborationAgentTestFixture) prepare(t *testing.T) *collaborationAgentPending {
	t.Helper()
	var pending *collaborationAgentPending
	x.tx(t, func(tx *sql.Tx) {
		var e error
		var accepted work.Receipt
		pending, accepted, e = x.store.StartTask(x.human, tx, x.key, "task", x.f.a.MessageID, x.f.receipt)
		x.workID = accepted.WorkID
		if e != nil {
			t.Fatal("prepare", e)
		}
	})
	return pending
}
func (x *collaborationAgentTestFixture) attach(t *testing.T, p *collaborationAgentPending) string {
	t.Helper()
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	future := time.Now().Add(30 * time.Second).UTC().Format(time.RFC3339Nano)
	mustExec(t, x.f.rig.db, `UPDATE work_items SET state='running',generation=1,attempts=1 WHERE id=?`, x.workID)
	mustExec(t, x.f.rig.db, `INSERT INTO work_attempts(run_id,work_id,attempt,generation,lease_owner,lease_expires_at,heartbeat_at,runtime_locator,runtime_phase,started_at) VALUES(?,?,1,1,'fixture-host',?,?,'fixture-runtime','confirmed',?)`, x.f.a.RunID, x.workID, future, stamp, stamp)
	var handle string
	x.tx(t, func(tx *sql.Tx) {
		var e error
		handle, e = x.store.AttachConfirmedAttempt(t.Context(), tx, p, x.f.a.RunID)
		if e != nil {
			t.Fatal("attach", e)
		}
	})
	return handle
}
func (x *collaborationAgentTestFixture) request() *http.Request {
	token := internaltoken.DeriveAgentRunToken(internaltoken.DeriveAgentRunKey(x.f.pages.artifactMasterToken, x.f.a.WorkspaceID, x.f.a.CrewID), x.f.a.WorkspaceID, x.f.a.AgentID, x.f.a.RunID)
	r := httptest.NewRequest("POST", "/unused-mandate-boundary", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	ctx := context.WithValue(r.Context(), ctxInternalTokenWS, x.f.a.WorkspaceID)
	ctx = context.WithValue(ctx, ctxInternalTokenCrew, x.f.a.CrewID)
	r = r.WithContext(ctx)
	var stamped *http.Request
	x.fence.Wrap(http.HandlerFunc(func(_ http.ResponseWriter, v *http.Request) { stamped = v })).ServeHTTP(httptest.NewRecorder(), r)
	return stamped
}
func (x *collaborationAgentTestFixture) check(t *testing.T, r *http.Request, handle string, want bool) {
	t.Helper()
	x.tx(t, func(tx *sql.Tx) {
		proof, e := x.store.Check(r, tx, handle, x.key, "task")
		if (e == nil) != want {
			t.Fatalf("check allowed=%v want=%v err=%v", e == nil, want, e)
		}
		if want && (proof.memberID == "" || proof.permissionVersion == "" || proof.credentialExpiry.After(time.Now().Add(31*time.Second))) {
			t.Fatal("missing identity or lease horizon")
		}
		if want && (proof.agentID != x.f.a.AgentID || proof.runID != x.f.a.RunID || proof.workID != x.workID || proof.attempt != 1 || proof.generation != 1) {
			t.Fatal("untrusted agent identity result")
		}
	})
}

// Real canonical SQL/credential references with synthetic trusted HTTP contexts
// and a synthetic BOOTTIME clock; no dispatcher or real-agent E3 is claimed.
func TestCollaborationAgentMandateRequiresHumanPendingAndFixedAttempt(t *testing.T) {
	x := newCollaborationAgentTestFixture(t)
	x.tx(t, func(tx *sql.Tx) {
		if _, _, e := x.store.StartTask(x.request().Context(), tx, x.key, "task", x.f.a.MessageID, x.f.receipt); e == nil {
			t.Fatal("agent bearer minted human pending consent")
		}
	})
	pending := x.prepare(t)
	handle := x.attach(t, pending)
	request := x.request()
	x.check(t, request, handle, true)
	x.tx(t, func(tx *sql.Tx) {
		if _, e := x.store.AttachConfirmedAttempt(t.Context(), tx, pending, x.f.a.RunID); e == nil {
			t.Fatal("pending attached twice")
		}
	})
	wrong := request.Clone(request.Context())
	wrong.Header.Set("Authorization", "Bearer forged-fixture")
	x.check(t, wrong, handle, false)
	x.check(t, request, handle, true)
	x.check(t, request, "", false)
	projected := request.WithContext(context.WithValue(request.Context(), ctxUser, &AuthUser{ID: x.f.a.HumanUserID}))
	x.check(t, projected, handle, false)
	// Exact restored SQL/old bearer cannot recreate host-memory consent.
	replacement, e := newCollaborationAgentMandateStore(x.f.pages, true, x.fence, x.clock, time.Minute, 4)
	if e != nil {
		t.Fatal(e)
	}
	original := x.store
	x.store = replacement
	x.check(t, request, handle, false)
	x.store = original
	// No origin/bearer-only fallback, and neither expiry nor recovery rebinds it.
	mustExec(t, x.f.rig.db, `UPDATE work_attempts SET runtime_locator='new-runtime'`)
	x.check(t, request, handle, false)
}

func TestCollaborationAgentMandateRevocationAndPrivacy(t *testing.T) {
	for _, tc := range []struct{ name, mutation string }{
		{"private target", `UPDATE pages SET private_audience=1 WHERE id='agent-shared-page'`},
		{"agent grant removed", `DELETE FROM page_grants WHERE page_id='agent-shared-page'`},
		{"source revoked restored", `UPDATE user_sessions SET revoked_at='2026-01-01T00:00:00Z' WHERE id='original-session'`},
		{"principal removed", `DELETE FROM artifact_run_principals WHERE run_id='principal-run'`},
		{"attempt removed", `DELETE FROM work_attempts WHERE run_id='principal-run'`},
		{"terminal work", `UPDATE work_items SET state='cancelled'`},
		{"ceiling removed", `UPDATE workspace_artifact_creation_policy SET enabled=0`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			x := newCollaborationAgentTestFixture(t)
			handle := x.attach(t, x.prepare(t))
			x.check(t, x.request(), handle, true)
			mustExec(t, x.f.rig.db, tc.mutation)
			if tc.name == "source revoked restored" {
				mustExec(t, x.f.rig.db, `UPDATE user_sessions SET revoked_at=NULL WHERE id='original-session'`)
			}
			x.check(t, x.request(), handle, false)
		})
	}
}

func TestCollaborationAgentPendingCapacityClockAndDefaultOff(t *testing.T) {
	x := newCollaborationAgentTestFixture(t)
	x.store.capacity = 1
	pending := x.prepare(t)
	x.tx(t, func(tx *sql.Tx) {
		if _, _, e := x.store.StartTask(x.human, tx, x.key, "task", x.f.a.MessageID, x.f.receipt); e == nil {
			t.Fatal("capacity unbounded")
		}
	})
	x.clock.advance(time.Minute)
	x.tx(t, func(tx *sql.Tx) {
		if _, e := x.store.AttachConfirmedAttempt(t.Context(), tx, pending, x.f.a.RunID); e == nil {
			t.Fatal("expired pending attached")
		}
	})
	x.prepare(t) // Expired entries release capacity; original source stays fixed.
	off, e := newCollaborationAgentMandateStore(nil, false, nil, nil, 0, 0)
	if e != nil {
		t.Fatal(e)
	}
	x.tx(t, func(tx *sql.Tx) {
		if _, _, e := off.StartTask(x.human, tx, x.key, "task", x.f.a.MessageID, x.f.receipt); e == nil {
			t.Fatal("default off admitted")
		}
	})
}

func TestCollaborationAgentOriginalIngressCannotRefreshAfterRestore(t *testing.T) {
	x := newCollaborationAgentTestFixture(t)
	// This actual request context and original source both predate the lifecycle
	// reset. SQL stays byte-for-byte equal, as it can after a physical rollback.
	x.fence.SuspendBeforeRestore()
	if e := x.fence.ResumeAfterRestore(); e != nil {
		t.Fatal(e)
	}
	x.tx(t, func(tx *sql.Tx) {
		if _, _, e := x.store.StartTask(x.human, tx, x.key, "task", x.f.a.MessageID, x.f.receipt); e == nil {
			t.Fatal("old authenticated ingress adopted post-restore incarnation and minted fresh task consent")
		}
	})
}

func TestCollaborationAgentFreshTasksAndRollback(t *testing.T) {
	x := newCollaborationAgentTestFixture(t)
	first := x.prepare(t)
	firstID := x.workID
	second := x.prepare(t)
	if firstID == x.workID || first.id == second.id {
		t.Fatal("fresh human task reused work or consent")
	}
	var before, after int
	if e := x.f.rig.db.QueryRow(`SELECT count(*) FROM work_items`).Scan(&before); e != nil {
		t.Fatal(e)
	}
	tx, e := x.f.rig.db.BeginTx(t.Context(), nil)
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback()
	if _, e = tx.Exec(`UPDATE mooring_session_writer_fence SET seq=seq WHERE id=1`); e != nil {
		t.Fatal(e)
	}
	pending, accepted, e := x.store.StartTask(x.human, tx, x.key, "task", x.f.a.MessageID, x.f.receipt)
	if e != nil || pending == nil || accepted.WorkID == "" {
		t.Fatal("new task", e)
	}
	if e = tx.Rollback(); e != nil {
		t.Fatal(e)
	}
	if e = x.f.rig.db.QueryRow(`SELECT count(*) FROM work_items`).Scan(&after); e != nil || before != after {
		t.Fatal("rollback retained accepted work", e)
	}
	x.tx(t, func(tx *sql.Tx) {
		if _, e := x.store.AttachConfirmedAttempt(t.Context(), tx, pending, x.f.a.RunID); e == nil {
			t.Fatal("rolled-back task acquired mandate")
		}
		if _, e := x.store.prepareAcceptedTask(x.human, tx, x.key, "task", x.f.a.MessageID, x.f.receipt, collaborationAgentFreshWork{workID: firstID}); e == nil {
			t.Fatal("restored queued row bypassed fresh-task seal")
		}
	})
}

func TestCollaborationAgentReadGrantCannotDelegateWrite(t *testing.T) {
	x := newCollaborationAgentTestFixture(t, "read")
	x.tx(t, func(tx *sql.Tx) {
		if _, _, e := x.store.StartTask(x.human, tx, x.key, "task", x.f.a.MessageID, x.f.receipt); e == nil {
			t.Fatal("human owner widened agent read grant")
		}
		var e error
		var accepted work.Receipt
		_, accepted, e = x.store.StartTask(x.human, tx, x.key, "read", x.f.a.MessageID, x.f.receipt)
		if e != nil || accepted.WorkID == "" {
			t.Fatal("explicit read grant rejected", e)
		}
	})
}

func TestCollaborationAgentAttachedMandateCannotRefreshIngress(t *testing.T) {
	x := newCollaborationAgentTestFixture(t)
	handle := x.attach(t, x.prepare(t))
	oldRequest := x.request()
	x.check(t, oldRequest, handle, true)
	x.fence.SuspendBeforeRestore()
	if e := x.fence.ResumeAfterRestore(); e != nil {
		t.Fatal(e)
	}
	// A newly stamped transport and byte-identical SQL cannot upgrade the
	// original human consent retained by the attached mandate.
	x.check(t, x.request(), handle, false)
	x.check(t, oldRequest, handle, false)
}
