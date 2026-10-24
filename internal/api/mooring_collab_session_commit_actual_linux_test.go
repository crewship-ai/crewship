//go:build linux && mooring_rust_integration

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/mooring"
	protocol "github.com/crewship-ai/crewship/mooring/protocol/generated/go"
)

// Actual activated Rust preparation, FULL SQL and original source checks. The
// structured text command and seal are synthetic trusted-bridge fixtures, not
// proof of raw human/agent ingress, issued origins or participant output.
func TestCollaborationSessionActualSealedFullCommit(t *testing.T) {
	runCollaborationSessionActualStartup(t, false, func(cell *collaborationSessionStore, humans *collaborationHumanRegistry, owner mooring.OwnerLease, request *http.Request) {
		ctx := request.Context()
		// Install fault machinery before original admission captures the schema cookie.
		mustExec(t, cell.pages.db, `CREATE TABLE test_session_commit_fault(enabled INTEGER NOT NULL)`)
		mustExec(t, cell.pages.db, `INSERT INTO test_session_commit_fault VALUES(0)`)
		mustExec(t, cell.pages.db, `CREATE TRIGGER test_session_commit_abort BEFORE INSERT ON mooring_collab_operations WHEN (SELECT enabled FROM test_session_commit_fault)=1 BEGIN SELECT RAISE(ABORT,'synthetic commit failure'); END`)
		ticket, admission, err := humans.issue(ctx, owner, true)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = humans.join(ctx, cell.runtime, owner, ticket, "commit-human-join"); err != nil {
			t.Fatal(err)
		}
		submissions, err := newCollaborationSubmissionRegistry(humans)
		if err != nil {
			t.Fatal(err)
		}
		binding, err := humans.storeBinding()
		if err != nil {
			t.Fatal(err)
		}
		handler, err := newCollaborationSessionCommitter(ctx, cell, submissions, owner)
		if err != nil {
			t.Fatal("actual factory", err)
		}
		var snapshot mooring.FullCheckpointSnapshot
		err = humans.runHumanOperation(request, admission.participant, func(bound context.Context) error {
			var err error
			snapshot, err = cell.store.ReadFullCheckpoint(bound, owner)
			return err
		})
		if err != nil || len(snapshot.Targets) == 0 {
			t.Fatal("retained full inventory", err)
		}
		zero := "0"
		target := snapshot.Targets[0].TargetID
		payload, _ := json.Marshal(map[string]any{"kind": "text.insert", "target_id": target, "at": map[string]string{"kind": "end"}, "text": " concrete SQL Č😀", "properties": map[string]bool{"bold": true}})
		change := submissionTestChange(t, cell.scope, "concrete-commit", "", admission.participant)
		change.Operations[0].Payload = payload
		change.Operations[0].Reads = []mooring.BatchReadPrecondition{{TargetID: target, Revision: "0", StructureRevision: &zero}}
		// Recompute fingerprints over the exact synthetic original command.
		op := change.Operations[0]
		preimage, _ := json.Marshal(map[string]any{"domain": "mooring.atomic.v0.4", "scope": handler.scope, "change_id": change.ID,
			"operations_without_fingerprints": []any{map[string]any{"op_id": op.ID, "participant_ref": op.Participant, "base_revision": op.BaseRevision, "dependencies": op.Dependencies, "read_preconditions": op.Reads, "payload": op.Payload}}})
		hashes, err := mooring.FingerprintAtomicJSON(preimage)
		if err != nil {
			t.Fatal(err)
		}
		change.MembershipHash, change.Operations[0].Fingerprint = hashes.MembershipHash, hashes.Operations[op.ID]
		seal := func(c mooring.BatchChange) error {
			return humans.runHumanOperation(request, admission.participant, func(bound context.Context) error {
				return submissions.run(bound, binding, func(bound context.Context) error { return submissions.sealHuman(bound, []mooring.BatchChange{c}) })
			})
		}
		if err = seal(change); err != nil {
			t.Fatal("original seal", err)
		}
		changesRaw, _ := json.Marshal([]mooring.BatchChange{change})
		var changes []protocol.AtomicChange
		if err = json.Unmarshal(changesRaw, &changes); err != nil {
			t.Fatal(err)
		}
		submit := protocol.SessionSubmitBatch{Type: "session_submit_batch", RequestID: "concrete-submit", ControlProfile: mooring.PrivateSessionProfile,
			Scope: handler.scope, SessionID: handler.session, RuntimeRef: handler.runtime, Fence: handler.fence, Versions: handler.versions,
			ConfirmedBasis: protocol.ConfirmedCheckpointBasis{Revision: snapshot.Checkpoint.Revision, Digest: snapshot.Checkpoint.Digest}, Changes: changes}
		count := func() int {
			var count int
			if err := cell.pages.db.QueryRow(`SELECT COUNT(*) FROM mooring_collab_operations WHERE page_id=?`, cell.scope.ArtifactID).Scan(&count); err != nil {
				t.Fatal(err)
			}
			return count
		}
		negatives := []struct {
			name   string
			change func(*protocol.SessionSubmitBatch)
		}{
			{"scope", func(r *protocol.SessionSubmitBatch) { r.Scope.ArtifactID = "other" }},
			{"session", func(r *protocol.SessionSubmitBatch) { r.SessionID = "other" }},
			{"runtime", func(r *protocol.SessionSubmitBatch) { r.RuntimeRef = "other" }},
			{"fence", func(r *protocol.SessionSubmitBatch) { r.Fence.Token = strings.Repeat("f", 64) }},
			{"generation", func(r *protocol.SessionSubmitBatch) {
				r.Fence.Generation = protocol.Counter(strconv.FormatInt(owner.Generation+1, 10))
			}},
			{"version", func(r *protocol.SessionSubmitBatch) { r.Versions.ModelVersion = "9.0.0" }},
			{"profile", func(r *protocol.SessionSubmitBatch) { r.ControlProfile = "other" }},
			{"full basis", func(r *protocol.SessionSubmitBatch) {
				r.ConfirmedBasis.Digest = protocol.Digest("sha256:" + strings.Repeat("0", 64))
			}},
		}
		for _, negative := range negatives {
			bad := submit
			negative.change(&bad)
			result, err := handler.submit(ctx, bad)
			if err == nil || result.Checkpoint.State != "" || result.Receipt.Changes != nil || count() != 0 {
				t.Fatal("unqualified request returned output or wrote", negative.name, err)
			}
		}
		// An actual SQL failure must not release a candidate receipt/checkpoint.
		mustExec(t, cell.pages.db, `UPDATE test_session_commit_fault SET enabled=1`)
		if result, err := handler.submit(ctx, submit); err == nil || !strings.Contains(err.Error(), "synthetic commit failure") || result.Checkpoint.State != "" || result.Receipt.Changes != nil || count() != 0 {
			t.Fatal("failed writer released output or partial operation", err)
		}
		mustExec(t, cell.pages.db, `UPDATE test_session_commit_fault SET enabled=0`)
		// A matching fingerprint alone is not an original author seal.
		unsealed := submissionTestChange(t, cell.scope, "unsealed", "", admission.participant)
		unsealedRaw, _ := json.Marshal([]mooring.BatchChange{unsealed})
		unknown := submit
		unknown.Changes = nil // Do not reuse the original request backing slices/maps.
		if err = json.Unmarshal(unsealedRaw, &unknown.Changes); err != nil {
			t.Fatal(err)
		}
		if result, err := handler.submit(ctx, unknown); err == nil || result.Checkpoint.State != "" || count() != 0 {
			t.Fatal("unsealed operation borrowed original authority", err)
		}
		result, err := handler.submit(ctx, submit)
		if err != nil || result.Receipt.Revision != "1" || len(result.Receipt.Changes) != 1 || result.Receipt.Changes[0].Status != "saved" || result.Checkpoint.State == snapshot.Checkpoint.State || count() != 1 {
			t.Fatal("concrete durable commit", err)
		}
		retry, err := handler.submit(ctx, submit)
		if err != nil || retry.Receipt.Revision != "1" || retry.Checkpoint.Digest != result.Checkpoint.Digest || count() != 1 {
			t.Fatal("exact original retry", err)
		}
		// Seal another exact synthetic original while its author is still allowed.
		// Revocation must be classified before application preparation is attempted.
		blocked := submissionTestChange(t, cell.scope, "sealed-before-revocation", "", admission.participant)
		if err = seal(blocked); err != nil {
			t.Fatal("seal before revocation", err)
		}
		blockedRaw, _ := json.Marshal([]mooring.BatchChange{blocked})
		blockedRequest := submit
		blockedRequest.RequestID = "revoked-original"
		blockedRequest.Changes = nil
		if err = json.Unmarshal(blockedRaw, &blockedRequest.Changes); err != nil {
			t.Fatal(err)
		}
		// No later source recapture can refresh an already sealed original.
		mustExec(t, cell.pages.db, `UPDATE user_sessions SET revoked_at='2026-10-24T00:00:00Z' WHERE id='admission-source'`)
		// Exact known outcome is host processing, not permission to deliver it.
		if _, err = handler.submit(ctx, submit); err != nil {
			t.Fatal("known sealed retry after revocation", err)
		}
		rejected, err := handler.submit(ctx, blockedRequest)
		if err != nil || rejected.Checkpoint.Digest != result.Checkpoint.Digest || len(rejected.Receipt.Changes) != 1 || rejected.Receipt.Changes[0].Status != "rejected" || len(rejected.Receipt.Changes[0].Operations) != 1 || count() != 2 {
			t.Fatal("original revocation did not produce one durable rejection", err)
		}
		var denied struct {
			Reason struct {
				Code string `json:"code"`
			} `json:"reason"`
		}
		if json.Unmarshal(rejected.Receipt.Changes[0].Operations[0], &denied) != nil || denied.Reason.Code != "permission_revoked" {
			t.Fatal("revocation classification lost original source")
		}
		_ = cell.runtime.Close()
		if result, err = handler.submit(ctx, submit); err == nil || result.Checkpoint.State != "" {
			t.Fatal("dead process retained commit authority")
		}
	})
}
