package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"testing"

	"github.com/crewship-ai/crewship/internal/mooring"
)

func submissionTestChange(t *testing.T, key mooring.SessionKey, id, task, participant string) mooring.BatchChange {
	t.Helper()
	op := mooring.BatchOperation{ID: id + "_op", Participant: participant, BaseRevision: "0", Dependencies: []string{}, Reads: []mooring.BatchReadPrecondition{}, Payload: json.RawMessage(`{"test":"original"}`)}
	immutable := map[string]any{"op_id": op.ID, "participant_ref": op.Participant, "base_revision": op.BaseRevision, "dependencies": op.Dependencies, "read_preconditions": op.Reads, "payload": op.Payload}
	preimage := map[string]any{"domain": "mooring.atomic.v0.4", "scope": map[string]string{"application_id": key.ApplicationID, "artifact_id": key.ArtifactID, "workspace_id": key.WorkspaceID}, "change_id": id, "operations_without_fingerprints": []any{immutable}}
	if task != "" {
		preimage["task_id"] = task
	}
	raw, err := json.Marshal(preimage)
	if err != nil {
		t.Fatal(err)
	}
	hashes, err := mooring.FingerprintAtomicJSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	op.Fingerprint = hashes.Operations[op.ID]
	return mooring.BatchChange{ID: id, TaskID: task, MembershipHash: hashes.MembershipHash, Operations: []mooring.BatchOperation{op}}
}
func submissionTestAccess(owner mooring.OwnerLease, c mooring.BatchChange, purpose mooring.SessionBatchPurpose) mooring.SessionBatchAccess {
	intent := "write"
	if purpose == mooring.SessionProcessSealedOutcome {
		intent = "outcome"
	}
	op := c.Operations[0]
	return mooring.SessionBatchAccess{Purpose: purpose, Lease: owner, ChangeID: c.ID, MembershipHash: c.MembershipHash, Access: mooring.BatchAccess{Intent: intent, Scope: owner.Key, Operation: &op, TaskID: c.TaskID, Participant: op.Participant}}
}

func TestCollaborationSubmissionHumanSealPreservesOriginalAfterRevocation(t *testing.T) {
	r, h, owner, ctx, _ := collaborationAdmissionFixture(t)
	_, admission, err := h.issue(ctx, owner, true)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := newCollaborationSubmissionRegistry(h)
	if err != nil {
		t.Fatal(err)
	}
	binding, err := h.storeBinding()
	if err != nil {
		t.Fatal(err)
	}
	c := submissionTestChange(t, h.scope, "human_change", "", admission.participant)
	request := httptest.NewRequest("POST", "/", nil).WithContext(ctx)
	err = h.runHumanOperation(request, admission.participant, func(ctx context.Context) error {
		return registry.run(ctx, binding, func(ctx context.Context) error { return registry.sealHuman(ctx, []mooring.BatchChange{c}) })
	})
	if err != nil {
		t.Fatal(err)
	}
	authority := registry.bind(owner, binding)
	check := func(purpose mooring.SessionBatchPurpose, wantDenied bool) {
		t.Helper()
		err := registry.run(t.Context(), binding, func(ctx context.Context) error {
			tx, err := r.db.BeginTx(ctx, nil)
			if err != nil {
				return err
			}
			defer tx.Rollback()
			return authority(ctx, tx, submissionTestAccess(owner, c, purpose))
		})
		var denied *mooring.BatchDenied
		if wantDenied {
			if !errors.As(err, &denied) || denied.Code != "permission_revoked" {
				t.Fatalf("known revocation not classified: %v", err)
			}
		} else if err != nil {
			t.Fatal(err)
		}
	}
	check(mooring.SessionCurrentWrite, false)
	mustExec(t, r.db, `UPDATE user_sessions SET revoked_at='2026-10-24T01:00:00Z' WHERE id='admission-source'`)
	// Host processing of the already sealed original still succeeds. It is not
	// permission to expose outcomes/content to the now-revoked historical author.
	check(mooring.SessionProcessSealedOutcome, false)
	check(mooring.SessionCurrentWrite, true)
	err = registry.run(t.Context(), binding, func(ctx context.Context) error {
		tx, err := r.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		access := submissionTestAccess(owner, c, mooring.SessionProcessSealedOutcome)
		access.Access.Operation.Payload = []byte(`{"test":"substituted"}`)
		if err := authority(ctx, tx, access); err == nil {
			return errors.New("substituted original accepted")
		}
		access = submissionTestAccess(owner, c, mooring.SessionProcessSealedOutcome)
		access.ChangeID = "other"
		if err := authority(ctx, tx, access); err == nil {
			return errors.New("other atomic membership accepted")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestCollaborationSubmissionRejectsEscapedOperationAndStorageUncertainty(t *testing.T) {
	r, h, owner, ctx, _ := collaborationAdmissionFixture(t)
	_, admission, err := h.issue(ctx, owner, true)
	if err != nil {
		t.Fatal(err)
	}
	registry, _ := newCollaborationSubmissionRegistry(h)
	binding, _ := h.storeBinding()
	c := submissionTestChange(t, h.scope, "original", "", admission.participant)
	request := httptest.NewRequest("POST", "/", nil).WithContext(ctx)
	var escaped context.Context
	err = h.runHumanOperation(request, admission.participant, func(ctx context.Context) error {
		return registry.run(ctx, binding, func(ctx context.Context) error {
			escaped = ctx
			return registry.sealHuman(ctx, []mooring.BatchChange{c})
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	authority := registry.bind(owner, binding)
	tx, err := r.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err = authority(escaped, tx, submissionTestAccess(owner, c, mooring.SessionProcessSealedOutcome)); err == nil {
		t.Fatal("escaped callback accepted")
	}
	tx.Rollback()
	err = registry.run(t.Context(), binding, func(ctx context.Context) error {
		tx, err := r.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		if _, err = tx.Exec(`ALTER TABLE workspaces RENAME TO submission_hidden_workspace`); err != nil {
			return err
		}
		err = authority(ctx, tx, submissionTestAccess(owner, c, mooring.SessionCurrentWrite))
		var denied *mooring.BatchDenied
		if err == nil || errors.As(err, &denied) {
			return errors.New("SQL uncertainty became durable denial")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// A caller cannot upgrade a copied owner or use this committer after rebind.
	err = registry.run(t.Context(), binding, func(ctx context.Context) error {
		tx, err := r.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		access := submissionTestAccess(owner, c, mooring.SessionProcessSealedOutcome)
		access.Lease.Generation++
		if err = authority(ctx, tx, access); err == nil {
			return errors.New("replacement owner adopted")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	h.mu.Lock()
	h.bindingID = "replacement-binding"
	h.mu.Unlock()
	if err = registry.run(t.Context(), binding, func(context.Context) error { t.Error("old binding entered"); return nil }); err == nil {
		t.Fatal("old binding followed replacement")
	}
}

func TestCollaborationSubmissionAgentRequiresOriginalAuthenticatedMandate(t *testing.T) {
	x := newCollaborationAgentTestFixture(t)
	handle := x.attach(t, x.prepare(t))
	stamp, err := collaborationHumanLifecycleStamp(x.fence)
	if err != nil {
		t.Fatal(err)
	}
	// Explicit synthetic runtime identity isolates author binding; no negotiated
	// process, content gate, provider execution or native delta is qualified here.
	h := &collaborationHumanRegistry{lifecycle: stamp, bindingID: "test-binding", runtime: &mooring.Runtime{}, pages: x.f.pages, fence: x.fence, clock: x.clock, scope: x.key}
	registry, err := newCollaborationSubmissionRegistry(h)
	if err != nil {
		t.Fatal(err)
	}
	binding, _ := h.storeBinding()
	owner := mooring.OwnerLease{Key: x.key, OwnerID: "test-owner", Generation: 1, Token: "synthetic-token"}
	var participant *collaborationAgentSubmissionParticipant
	var c mooring.BatchChange
	request := x.request()
	err = registry.run(request.Context(), binding, func(ctx context.Context) error {
		var err error
		participant, err = registry.admitAgent(ctx, request, x.store, handle)
		if err != nil {
			return err
		}
		c = submissionTestChange(t, x.key, "agent_original", x.workID, participant.id)
		return registry.sealAgent(ctx, request, participant, []mooring.BatchChange{c})
	})
	if err != nil {
		t.Fatal(err)
	}
	authority := registry.bind(owner, binding)
	mustExec(t, x.f.rig.db, `DELETE FROM page_grants WHERE page_id='agent-shared-page'`)
	err = registry.run(t.Context(), binding, func(ctx context.Context) error {
		tx, err := x.f.rig.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		if err = authority(ctx, tx, submissionTestAccess(owner, c, mooring.SessionProcessSealedOutcome)); err != nil {
			return err
		}
		err = authority(ctx, tx, submissionTestAccess(owner, c, mooring.SessionCurrentWrite))
		var denied *mooring.BatchDenied
		if !errors.As(err, &denied) || denied.Code != "permission_revoked" {
			return errors.New("agent canonical change not classified")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestCollaborationSubmissionCommitFailurePublishesNoOriginalProof(t *testing.T) {
	r, h, owner, ctx, _ := collaborationAdmissionFixture(t)
	_, admission, err := h.issue(ctx, owner, true)
	if err != nil {
		t.Fatal(err)
	}
	registry, _ := newCollaborationSubmissionRegistry(h)
	binding, _ := h.storeBinding()
	c := submissionTestChange(t, h.scope, "commit_failed", "", admission.participant)
	// Deferred FK permits the authority read and writer update, then fails the
	// actual COMMIT. Publishing a seal before that boundary would retain proof.
	mustExec(t, r.db, `CREATE TABLE submission_commit_failure(user_id TEXT REFERENCES users(id) DEFERRABLE INITIALLY DEFERRED)`)
	mustExec(t, r.db, `CREATE TRIGGER submission_commit_fault AFTER UPDATE OF seq ON mooring_session_writer_fence BEGIN INSERT INTO submission_commit_failure(user_id) VALUES('missing-submission-human'); END`)
	request := httptest.NewRequest("POST", "/", nil).WithContext(ctx)
	seal := func() error {
		return h.runHumanOperation(request, admission.participant, func(ctx context.Context) error {
			return registry.run(ctx, binding, func(ctx context.Context) error { return registry.sealHuman(ctx, []mooring.BatchChange{c}) })
		})
	}
	if err = seal(); err == nil {
		t.Fatal("failed COMMIT accepted")
	}
	if len(registry.entries) != 0 || registry.bytes != 0 {
		t.Fatal("failed COMMIT published original author proof")
	}
	mustExec(t, r.db, `DROP TRIGGER submission_commit_fault`)
	mustExec(t, r.db, `DROP TABLE submission_commit_failure`)
	if err = seal(); err != nil {
		t.Fatal(err)
	}
	if len(registry.entries) != 1 {
		t.Fatal("successful COMMIT did not seal original")
	}
}

func TestCollaborationSubmissionReadAdmissionCannotSealWriterOperation(t *testing.T) {
	_, h, owner, ctx, _ := collaborationAdmissionFixture(t)
	_, admission, err := h.issue(ctx, owner, false)
	if err != nil {
		t.Fatal(err)
	}
	registry, _ := newCollaborationSubmissionRegistry(h)
	binding, _ := h.storeBinding()
	c := submissionTestChange(t, h.scope, "reader_write", "", admission.participant)
	request := httptest.NewRequest("POST", "/", nil).WithContext(ctx)
	err = h.runHumanOperation(request, admission.participant, func(ctx context.Context) error {
		return registry.run(ctx, binding, func(ctx context.Context) error { return registry.sealHuman(ctx, []mooring.BatchChange{c}) })
	})
	if err == nil || len(registry.entries) != 0 {
		t.Fatal("read admission acquired sealed write proof")
	}
}

func TestCollaborationSubmissionCannotMixOriginalIngressFences(t *testing.T) {
	x := newCollaborationAgentTestFixture(t)
	handle := x.attach(t, x.prepare(t))
	otherFence, err := newCollaborationIngressFence()
	if err != nil {
		t.Fatal(err)
	}
	stamp, err := collaborationHumanLifecycleStamp(otherFence)
	if err != nil {
		t.Fatal(err)
	}
	// Same SQL document and authenticated agent, but two distinct restore fences.
	// Holding one must never stand in for the original mandate's other fence.
	h := &collaborationHumanRegistry{lifecycle: stamp, bindingID: "other-fence-runtime", runtime: &mooring.Runtime{}, pages: x.f.pages, fence: otherFence, clock: x.clock, scope: x.key}
	registry, err := newCollaborationSubmissionRegistry(h)
	if err != nil {
		t.Fatal(err)
	}
	binding, _ := h.storeBinding()
	request := x.request()
	var participant *collaborationAgentSubmissionParticipant
	err = registry.run(request.Context(), binding, func(ctx context.Context) error {
		var err error
		participant, err = registry.admitAgent(ctx, request, x.store, handle)
		return err
	})
	if err == nil || participant != nil || len(registry.agents) != 0 {
		t.Fatal("different original restore fence admitted an agent participant")
	}
}
