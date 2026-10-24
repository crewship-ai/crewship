//go:build linux && mooring_rust_integration

package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/mooring"
	protocol "github.com/crewship-ai/crewship/mooring/protocol/generated/go"
)

// Actual production native FD and FULL SQL, with deliberately synthetic
// authenticated ingress fixtures. No participant output is externally delivered.
func TestReviewActualActorLateOwnerCannotSaveOrRunLaterSubmit(t *testing.T) {
	withActualActorSQLParticipant(t, func(f *actualActorSQLParticipant) {
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()
		request := httptest.NewRequest("POST", "/host-only-review", nil).WithContext(f.x.human)
		ledger, err := newCollaborationSubmissionRegistry(f.humans)
		if err != nil {
			t.Fatal(err)
		}
		binding, err := f.humans.storeBinding()
		if err != nil {
			t.Fatal(err)
		}
		committer, err := newCollaborationSessionCommitter(ctx, f.cell, ledger, f.owner)
		if err != nil {
			t.Fatal(err)
		}
		var snapshot mooring.FullCheckpointSnapshot
		if err = f.humans.runHumanOperation(request, f.joined.admission.participant, func(bound context.Context) error {
			var err error
			snapshot, err = f.cell.store.ReadFullCheckpoint(bound, f.owner)
			return err
		}); err != nil || len(snapshot.Targets) == 0 {
			t.Fatal("original snapshot", err)
		}
		makeChange := func(id string) mooring.BatchChange {
			c := submissionTestChange(t, f.cell.scope, id, "", f.joined.admission.participant)
			op := &c.Operations[0]
			zero := "0"
			op.Reads = []mooring.BatchReadPrecondition{{TargetID: snapshot.Targets[0].TargetID, Revision: "0", StructureRevision: &zero}}
			op.Payload, _ = json.Marshal(map[string]any{"kind": "text.insert", "target_id": snapshot.Targets[0].TargetID, "at": map[string]string{"kind": "end"}, "text": " review"})
			raw, _ := json.Marshal(map[string]any{"domain": "mooring.atomic.v0.4", "scope": committer.scope, "change_id": c.ID, "operations_without_fingerprints": []any{map[string]any{"op_id": op.ID, "participant_ref": op.Participant, "base_revision": op.BaseRevision, "dependencies": op.Dependencies, "read_preconditions": op.Reads, "payload": op.Payload}}})
			hashes, e := mooring.FingerprintAtomicJSON(raw)
			if e != nil {
				t.Fatal(e)
			}
			c.MembershipHash = hashes.MembershipHash
			op.Fingerprint = hashes.Operations[op.ID]
			return c
		}
		a, b := makeChange("review-owner-a"), makeChange("review-owner-b")
		if err = f.humans.runHumanOperation(request, f.joined.admission.participant, func(bound context.Context) error {
			return ledger.run(bound, binding, func(bound context.Context) error { return ledger.sealHuman(bound, []mooring.BatchChange{a, b}) })
		}); err != nil {
			t.Fatal("original seals", err)
		}
		seen := make(chan struct{})
		release := make(chan struct{})
		done := make(chan error, 1)
		var calls, ownerDenied, saved atomic.Int32
		pump, err := newCollaborationSessionPump(f.cell.runtime, collaborationSessionPumpHandlers{
			Submit: func(bound context.Context, submit protocol.SessionSubmitBatch) (protocol.SessionCommitResult, error) {
				if calls.Add(1) != 1 {
					return protocol.SessionCommitResult{}, errors.New("later submit was processed")
				}
				close(seen)
				select {
				case <-release:
				case <-bound.Done():
					return protocol.SessionCommitResult{}, bound.Err()
				}
				if e := f.cell.owners.Release(bound, f.owner); e != nil {
					return protocol.SessionCommitResult{}, e
				}
				result, e := committer.submit(bound, submit)
				if e != nil && len(result.Receipt.Changes) == 0 {
					ownerDenied.Store(1)
				}
				return result, e
			},
			Status: func(context.Context, protocol.SessionStatusRequest) (protocol.SessionStatusResult, error) {
				return protocol.SessionStatusResult{}, errors.New("status not installed")
			},
			Output: func(_ context.Context, out protocol.SessionParticipantOutput) error {
				var m struct {
					Type string `json:"type"`
				}
				if e := json.Unmarshal(out.Message, &m); e != nil {
					return e
				}
				if m.Type == "commit_result" {
					saved.Add(1)
				}
				return nil
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		go func() { done <- pump.run(ctx) }()
		defer func() {
			cancel()
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Error("pump did not stop")
			}
		}()
		send := func(c mooring.BatchChange, id string) {
			raw, _ := json.Marshal([]mooring.BatchChange{c})
			var changes []protocol.AtomicChange
			if e := json.Unmarshal(raw, &changes); e != nil {
				t.Fatal(e)
			}
			inner, _ := json.Marshal(protocol.SubmitChanges{Type: "submit_changes", RequestID: protocol.Identifier(id), Scope: f.opening.Scope, SessionID: f.opening.SessionID, Versions: f.versions, Changes: changes})
			if e := f.cell.runtime.SendParticipantInput(ctx, f.cell.activation, protocol.SessionParticipantInput{Type: "session_participant_input", RequestID: protocol.Identifier("private-" + id), ControlProfile: mooring.PrivateSessionProfile, Scope: f.opening.Scope, SessionID: f.opening.SessionID, RuntimeRef: f.opening.RuntimeRef, Fence: f.opening.Fence, ConnectionRef: f.opening.ConnectionRef, ParticipantRef: protocol.Identifier(f.joined.admission.participant), GateID: protocol.Identifier(f.joined.gate.GateID), PermissionVersion: protocol.Counter(f.joined.gate.PermissionVersion), Message: inner}); e != nil {
				t.Fatal(e)
			}
		}
		send(a, "review-a")
		select {
		case <-seen:
		case <-ctx.Done():
			t.Fatal("no actual A", ctx.Err())
		}
		send(b, "review-b")
		close(release)
		select {
		case e := <-done:
			if e == nil {
				t.Fatal("late owner fabricated success")
			}
			done <- e
		case <-ctx.Done():
			t.Fatal("late owner did not close child", ctx.Err())
		}
		if calls.Load() != 1 || ownerDenied.Load() != 1 || saved.Load() != 0 {
			t.Fatal("wrong uncertainty boundary", calls.Load(), ownerDenied.Load(), saved.Load())
		}
		var rows int
		if e := f.x.f.rig.db.QueryRow(`SELECT count(*) FROM mooring_collab_operations WHERE page_id=?`, f.cell.scope.ArtifactID).Scan(&rows); e != nil || rows != 0 {
			t.Fatal("late owner left durable operations", rows, e)
		}
		select {
		case <-f.cell.runtime.Done():
		case <-ctx.Done():
			t.Fatal("child still running after uncertain owner")
		}
	})
}
