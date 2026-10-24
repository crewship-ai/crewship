package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/mooring"
)

// Actual canonical source/owner/mandate SQL, synthetic runtime identity and
// trusted request fixtures. No inventory, origin reservation or native ingress.
func TestCollaborationOriginHumanAuthorOriginalScopeAndRevocation(t *testing.T) {
	r, h, owner, ctx, _ := collaborationAdmissionFixture(t)
	// Stable schema before original generation capture; real deferred COMMIT.
	mustExec(t, r.db, `CREATE TABLE origin_author_commit_switch(enabled INTEGER)`)
	mustExec(t, r.db, `INSERT INTO origin_author_commit_switch VALUES(0)`)
	mustExec(t, r.db, `CREATE TABLE origin_author_commit_failure(user_id TEXT REFERENCES users(id) DEFERRABLE INITIALLY DEFERRED)`)
	mustExec(t, r.db, `CREATE TRIGGER origin_author_commit_fault AFTER UPDATE OF seq ON mooring_session_writer_fence WHEN (SELECT enabled FROM origin_author_commit_switch)=1 BEGIN INSERT INTO origin_author_commit_failure VALUES('missing-original-human'); END`)
	_, writable, err := h.issue(ctx, owner, true)
	if err != nil {
		t.Fatal(err)
	}
	_, readable, err := h.issue(ctx, owner, false)
	if err != nil {
		t.Fatal(err)
	}
	s, err := newCollaborationSubmissionRegistry(h)
	if err != nil {
		t.Fatal(err)
	}
	binding, _ := h.storeBinding()
	request := httptest.NewRequest("POST", "/unused-origin-author", nil).WithContext(ctx)
	run := func(participant string, fn func(context.Context) error) error {
		return h.runHumanOperation(request, participant, func(ctx context.Context) error { return s.run(ctx, binding, fn) })
	}
	capture := func(participant string, lease mooring.OwnerLease) (*collaborationOriginAuthor, error) {
		var a *collaborationOriginAuthor
		err := run(participant, func(ctx context.Context) error { var err error; a, err = s.captureOriginHuman(ctx, lease); return err })
		return a, err
	}
	check := func(a *collaborationOriginAuthor, participant string, write, want bool) {
		t.Helper()
		err := run(participant, func(ctx context.Context) error {
			return s.withWriter(ctx, func(tx *sql.Tx) error { return a.check(ctx, tx, write) })
		})
		if (err == nil) != want {
			t.Fatalf("write=%v want=%v: %v", write, want, err)
		}
		var terminal *mooring.BatchDenied
		if errors.As(err, &terminal) {
			t.Fatal("author-only refusal became durable denial", err)
		}
	}
	if a, e := s.captureOriginHuman(ctx, owner); e == nil || a != nil {
		t.Fatal("unbound human context constructed reference")
	}
	badOwner := owner
	badOwner.Token = strings.Repeat("f", 64)
	if a, e := capture(writable.participant, badOwner); e == nil || a != nil {
		t.Fatal("foreign owner constructed author reference")
	}
	mustExec(t, r.db, `UPDATE origin_author_commit_switch SET enabled=1`)
	if a, e := capture(writable.participant, owner); e == nil || !strings.Contains(e.Error(), "FOREIGN KEY") || a != nil {
		t.Fatal("failed actual COMMIT published reference", e)
	}
	mustExec(t, r.db, `UPDATE origin_author_commit_switch SET enabled=0`)
	writer, err := capture(writable.participant, owner)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := capture(readable.participant, owner)
	if err != nil {
		t.Fatal(err)
	}
	if writer.human == nil || *writer.human != writable || reader.human == nil || *reader.human != readable {
		t.Fatal("reference refreshed original admission")
	}
	check(writer, writable.participant, true, true)
	check(writer, writable.participant, false, true)
	check(reader, readable.participant, false, true)
	check(reader, readable.participant, true, false)
	check(writer, readable.participant, true, false)
	copied := *writer
	check(&copied, writable.participant, true, false)
	var deserialized collaborationOriginAuthor
	//nolint:staticcheck // SA9005: hostile JSON must not initialize any private proof field.
	if err := json.Unmarshal([]byte(`{"write":true,"owner":{},"human":{}}`), &deserialized); err != nil {
		t.Fatal(err)
	}
	check(&deserialized, writable.participant, true, false)
	check(nil, writable.participant, true, false)
	var escaped context.Context
	if err := run(writable.participant, func(ctx context.Context) error { escaped = ctx; return nil }); err != nil {
		t.Fatal(err)
	}
	tx, e := r.db.BeginTx(ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	if e := writer.check(escaped, tx, true); e == nil {
		t.Fatal("escaped operation authenticated author")
	}
	_ = tx.Rollback()
	mustExec(t, r.db, `UPDATE user_sessions SET revoked_at='2026-10-24T00:00:00Z' WHERE id='admission-source'; UPDATE user_sessions SET revoked_at=NULL WHERE id='admission-source'`)
	check(writer, writable.participant, true, false)
	if a, e := capture(writable.participant, owner); e == nil || a != nil {
		t.Fatal("old original admission recaptured after source ABA")
	}
}

func TestCollaborationOriginAgentAuthorRetainsFixedProofAndReadScope(t *testing.T) {
	x := newCollaborationAgentTestFixture(t)
	// Unit fixture has exactly one pooled connection. Make its actual handle
	// FULL before owner transactions; no credential/proof is recaptured.
	x.f.rig.db.SetMaxOpenConns(1)
	mustExec(t, x.f.rig.db, `PRAGMA synchronous=FULL`)
	owners := mooring.NewOwnerStore(x.f.rig.db, true, x.clock, time.Minute)
	owner, err := owners.Acquire(t.Context(), x.key, "synthetic-origin-owner")
	if err != nil {
		t.Fatal(err)
	}
	handle := x.attach(t, x.prepare(t))
	stamp, err := collaborationHumanLifecycleStamp(x.fence)
	if err != nil {
		t.Fatal(err)
	}
	h := &collaborationHumanRegistry{lifecycle: stamp, bindingID: "origin-author-unit-binding", runtime: &mooring.Runtime{}, pages: x.f.pages, owners: owners, fence: x.fence, clock: x.clock, scope: x.key, ownerGeneration: owner.Generation}
	s, err := newCollaborationSubmissionRegistry(h)
	if err != nil {
		t.Fatal(err)
	}
	binding, _ := h.storeBinding()
	request := x.request()
	var writable, readable collaborationAgentPageAccess
	x.tx(t, func(tx *sql.Tx) {
		var err error
		writable, err = x.store.Check(request, tx, handle, x.key, "task")
		if err != nil {
			t.Fatal(err)
		}
		readable, err = x.store.Check(request, tx, handle, x.key, "read")
		if err != nil {
			t.Fatal(err)
		}
	})
	capture := func(r *http.Request, p collaborationAgentPageAccess) (*collaborationOriginAuthor, error) {
		var a *collaborationOriginAuthor
		err := s.run(request.Context(), binding, func(ctx context.Context) error {
			var err error
			a, err = s.captureOriginAgent(ctx, r, owner, p)
			return err
		})
		return a, err
	}
	check := func(a *collaborationOriginAuthor, write, want bool) {
		t.Helper()
		err := s.run(request.Context(), binding, func(ctx context.Context) error {
			return s.withWriter(ctx, func(tx *sql.Tx) error { return a.check(ctx, tx, write) })
		})
		if (err == nil) != want {
			t.Fatalf("write=%v want=%v: %v", write, want, err)
		}
		var terminal *mooring.BatchDenied
		if errors.As(err, &terminal) {
			t.Fatal("author evidence created durable denial")
		}
	}
	if a, e := capture(request, collaborationAgentPageAccess{}); e == nil || a != nil {
		t.Fatal("missing original proof constructed author")
	}
	wrong := request.Clone(request.Context())
	wrong.Header.Set("Authorization", "Bearer wrong-synthetic-token")
	if a, e := capture(wrong, writable); e == nil || a != nil {
		t.Fatal("wrong credential adopted original proof")
	}
	alias := collaborationAgentMandateStore{pages: NewPageHandler(x.f.rig.db, nil, newTestLogger()), fence: x.fence}
	aliased := writable
	aliased.store = &alias
	if a, e := capture(request, aliased); e == nil || a != nil {
		t.Fatal("same DB foreign handler qualified original reference")
	}
	writer, err := capture(request, writable)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := capture(request, readable)
	if err != nil {
		t.Fatal(err)
	}
	if writer.agent.intent != writable.intent || writer.agent.workID != writable.workID || writer.agent.credentialExpiry != writable.credentialExpiry || writer.agent.credentialBinding != writable.credentialBinding || writer.agent.authorVector != writable.authorVector {
		t.Fatal("capture adopted new intent, horizon or author identity")
	}
	check(writer, true, true)
	check(writer, false, true)
	check(reader, false, true)
	check(reader, true, false)
	copied := *writer
	check(&copied, true, false)
	forged := writable
	forged.credentialBinding[0] ^= 1
	if a, e := capture(request, forged); e == nil || a != nil {
		t.Fatal("forged original credential constructed author")
	}
	mustExec(t, x.f.rig.db, `DELETE FROM page_grants WHERE page_id='agent-shared-page'; INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES('agent-shared-page','agent',?,'write',?)`, x.f.a.AgentID, x.f.a.HumanUserID)
	check(writer, true, false)
	check(reader, false, false)
	if a, e := capture(request, writable); e == nil || a != nil {
		t.Fatal("old fixed task adopted grant ABA")
	}
}
