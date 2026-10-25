//go:build linux && mooring_rust_integration

package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"testing"

	"github.com/crewship-ai/crewship/internal/mooring"
	protocol "github.com/crewship-ai/crewship/mooring/protocol/generated/go"
)

// Actual activated native process, FULL SQL and original credential rows.
// The private connection/origin are synthetic host fixtures; the raw updates
// remain unqualified DATA and never reach native preparation or Saved here.
func TestMooringOriginalPublicationPrecedesEarlyOutcomesAndKnownRetry(t *testing.T) {
	withActualActorSQLParticipant(t, func(f *actualActorSQLParticipant) {
		registry, err := newCollaborationSubmissionRegistry(f.humans)
		if err != nil {
			t.Fatal(err)
		}
		binding, err := f.humans.storeBinding()
		if err != nil {
			t.Fatal(err)
		}
		request := httptest.NewRequest("POST", "/host-original-fixture", nil).WithContext(f.x.human)
		d := &collaborationHTTPDocument{key: f.cell.scope, cell: f.cell, owner: f.owner, humans: f.humans, submissions: registry, activation: f.cell.activation, connections: make(map[string]*collaborationHTTPConnection)}
		m := &collaborationHTTPManager{docs: map[mooring.SessionKey]*collaborationHTTPDocument{d.key: d}, usedOrigins: map[mooring.SessionKey]map[uint64]struct{}{d.key: {17: {}}}}
		d.manager = m
		d.originals = newCollaborationLiveOriginals(d)
		c := &collaborationHTTPConnection{document: d, request: request, admission: f.joined.admission, joined: f.joined, origin: "17", ref: "original-connection", used: true, joinedDelivered: true}
		d.connections[c.ref] = c
		var snapshot mooring.FullCheckpointSnapshot
		err = f.humans.runHumanOperation(request, c.admission.participant, func(ctx context.Context) error {
			var e error
			snapshot, e = f.cell.store.ReadFullCheckpoint(ctx, f.owner)
			return e
		})
		if err != nil {
			t.Fatal(err)
		}
		if err = c.recordObservedCheckpoint(snapshot.Checkpoint); err != nil {
			t.Fatal(err)
		}
		a := originalTestChange(t, c, "published-a", "0", "absent", "AQ==")
		b := originalTestChange(t, c, "published-b", "0", a.Operations[0].ID, "Ag==")
		denied := originalTestChange(t, c, "published-denied", "0", "", "Aw==")
		missing := originalTestChange(t, c, "missing-original", "0", "absent", "BA==")
		uncertain := originalTestChange(t, c, "uncertain-original", "0", "absent", "BQ==")
		// Inject the fault after real authenticated sealing but before raw-slot
		// publication. Registry identity alone is deliberately insufficient.
		err = f.humans.runHumanOperation(request, c.admission.participant, func(ctx context.Context) error {
			return registry.run(ctx, binding, func(ctx context.Context) error {
				return registry.sealHuman(ctx, []mooring.BatchChange{a, b, denied, missing, uncertain})
			})
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = d.originals.capture(t.Context(), c, []mooring.BatchChange{a, b, denied}); err != nil {
			t.Fatal("original authenticated capture", err)
		}
		committer, err := newCollaborationSessionCommitterWithOriginals(t.Context(), f.cell, registry, f.owner, d.originals)
		if err != nil {
			t.Fatal(err)
		}
		packet := func(id string, change mooring.BatchChange) protocol.SessionSubmitBatch {
			raw, _ := json.Marshal([]mooring.BatchChange{change})
			var changes []protocol.AtomicChange
			if json.Unmarshal(raw, &changes) != nil {
				t.Fatal("wire change")
			}
			return protocol.SessionSubmitBatch{Type: "session_submit_batch", RequestID: protocol.Identifier(id), ControlProfile: mooring.PrivateSessionProfile, Scope: committer.scope, SessionID: committer.session, RuntimeRef: committer.runtime, Fence: committer.fence, Versions: committer.versions, ConfirmedBasis: protocol.ConfirmedCheckpointBasis{Revision: snapshot.Checkpoint.Revision, Digest: snapshot.Checkpoint.Digest}, Changes: changes}
		}
		noOutcome := func(id string, change mooring.BatchChange) {
			t.Helper()
			result, cuts, e := committer.submitWithCuts(f.x.human, packet(id, change))
			if e == nil || len(result.Receipt.Changes) != 0 || result.Checkpoint.State != "" || len(cuts) != 0 {
				t.Fatal("unpublished original produced terminal receipt/content", e)
			}
		}
		noOutcome("missing-request", missing)
		_, fresh, err := d.originals.reserve(c, []mooring.BatchChange{uncertain})
		if err != nil {
			t.Fatal(err)
		}
		d.originals.finishCapture(fresh, errors.New("injected uncertain publication"))
		noOutcome("uncertain-request", uncertain)
		var rows int
		if err = f.x.f.rig.db.QueryRow(`SELECT count(*) FROM mooring_collab_operations WHERE page_id=?`, d.key.ArtifactID).Scan(&rows); err != nil || rows != 0 {
			t.Fatal("failed source proof wrote outcome", rows, err)
		}
		terminal := func(id string, change mooring.BatchChange, code string) {
			t.Helper()
			r, cuts, e := committer.submitWithCuts(f.x.human, packet(id, change))
			if e != nil || len(cuts) != 0 || len(r.Receipt.Changes) != 1 || len(r.Receipt.Changes[0].Operations) != 1 || r.Receipt.Changes[0].Status != "rejected" {
				t.Fatal("valid original terminal outcome", e)
			}
			raw, _ := json.Marshal(r.Receipt.Changes[0])
			var outcome mooring.BatchChangeResult
			if json.Unmarshal(raw, &outcome) != nil || outcome.Operations[0].Reason == nil || outcome.Operations[0].Reason.Code != code {
				t.Fatal("wrong terminal reason", string(raw))
			}
		}
		// Already captured A/B retain immutable processing proof after retirement.
		terminal("known-a", a, "unknown_dependency")
		terminal("known-b", b, "dependency_rejected")
		d.originals.mu.Lock()
		delete(d.originals.changes, a.ID)
		d.originals.mu.Unlock()
		noOutcome("known-a-with-lost-proof", a)
		terminal("known-b-retry", b, "dependency_rejected")
		mustExec(t, f.x.f.rig.db, `UPDATE user_sessions SET revoked_at='2026-10-24T00:00:00Z' WHERE id='original-session'`)
		terminal("known-canonical-denial", denied, "permission_revoked")
	})
}
