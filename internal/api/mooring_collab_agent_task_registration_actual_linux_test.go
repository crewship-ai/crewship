//go:build linux && mooring_rust_integration

package api

import (
	"context"

	"github.com/crewship-ai/crewship/internal/mooring"
	"net/http"
	"testing"
)

// Native admission and the ordinary document-tool read are real; the confirmed
// task identity is the existing controlled fixture. This checks registration,
// not a model/container dispatch or a successful mutation/Save.
func TestMooringAgentToolActualOriginalTaskRegistration(t *testing.T) {
	t.Run("proof-isolation", testMooringAgentToolActualTaskRegistrationProofIsolation)
	t.Run("production-save", testMooringAgentActualProductionSave)
	t.Run("writer-original-task", func(t *testing.T) {
		withMooringAgentToolHostFixture(t, func(x *collaborationAgentTestFixture, m *collaborationHTTPManager, a *collaborationLiveAgent) {
			response := newAgentToolBufferedWriter(false)
			request := x.request()
			request.Method = http.MethodGet
			m.agentTool(response, request)
			if response.Code != http.StatusOK {
				t.Fatalf("ordinary native task admission/read failed: %d", response.Code)
			}
			a.mu.Lock()
			participant := a.participant.id
			a.mu.Unlock()
			var registeredParticipant, workspace, status string
			var partial int
			err := x.f.rig.db.QueryRow(`SELECT workspace_id,participant_ref,allow_partial,status FROM mooring_collab_tasks WHERE page_id=? AND task_id=?`, x.key.ArtifactID, x.workID).Scan(&workspace, &registeredParticipant, &partial, &status)
			if err != nil {
				t.Fatalf("original admitted writer task was not registered before content: %v", err)
			}
			if workspace != x.key.WorkspaceID || registeredParticipant != participant || partial != 0 || status != "running" {
				t.Fatalf("registration lost original scope/participant/default atomic policy: %q %q %d %q", workspace, registeredParticipant, partial, status)
			}
			var count int
			if err := x.f.rig.db.QueryRow(`SELECT count(*) FROM mooring_collab_tasks WHERE page_id=?`, x.key.ArtifactID).Scan(&count); err != nil || count != 1 {
				t.Fatalf("unexpected additional task registration: %d %v", count, err)
			}
		})
	})
	t.Run("declared-partial-policy", func(t *testing.T) {
		withMooringAgentToolHostFixture(t, func(x *collaborationAgentTestFixture, m *collaborationHTTPManager, a *collaborationLiveAgent) {
			response := newAgentToolBufferedWriter(false)
			request := x.request()
			request.Method = http.MethodGet
			m.agentTool(response, request)
			if response.Code != http.StatusOK {
				t.Fatalf("declared partial task admission failed: %d", response.Code)
			}
			var participant string
			var partial int
			if err := x.f.rig.db.QueryRow(`SELECT participant_ref,allow_partial FROM mooring_collab_tasks WHERE page_id=? AND task_id=?`, x.key.ArtifactID, x.workID).Scan(&participant, &partial); err != nil || participant != a.participant.id || partial != 1 {
				t.Fatalf("original partial policy was not registered: %q %d %v", participant, partial, err)
			}
		}, "task-partial")
	})
	t.Run("read-task-has-no-writer-registration", func(t *testing.T) {
		withMooringAgentToolHostFixture(t, func(x *collaborationAgentTestFixture, m *collaborationHTTPManager, a *collaborationLiveAgent) {
			response := newAgentToolBufferedWriter(false)
			request := x.request()
			request.Method = http.MethodGet
			m.agentTool(response, request)
			if response.Code != http.StatusOK {
				t.Fatalf("ordinary read task failed: %d", response.Code)
			}
			var count int
			if err := x.f.rig.db.QueryRow(`SELECT count(*) FROM mooring_collab_tasks WHERE page_id=?`, x.key.ArtifactID).Scan(&count); err != nil || count != 0 {
				t.Fatalf("read task acquired writer registration: %d %v", count, err)
			}
		}, "read")
	})
}

// These are same-package adversarial proof constructions, never caller input.
// The allowed control and each rejection use the real FULL RegisterTask writer.
func testMooringAgentToolActualTaskRegistrationProofIsolation(t *testing.T) {
	withMooringAgentToolHostFixture(t, func(x *collaborationAgentTestFixture, m *collaborationHTTPManager, a *collaborationLiveAgent) {
		read := x.request()
		read.Method = http.MethodGet
		response := newAgentToolBufferedWriter(false)
		m.agentTool(response, read)
		if response.Code != http.StatusOK {
			t.Fatalf("admission failed: %d", response.Code)
		}
		d, p := a.document, a.participant
		binding, err := d.humans.storeBinding()
		if err != nil {
			t.Fatal(err)
		}
		var escaped context.Context
		err = d.submissions.run(x.request().Context(), binding, func(bound context.Context) error {
			original := func() *collaborationAgentTaskRegistration {
				proof := &collaborationAgentTaskRegistration{registry: d.submissions, participant: p, owner: d.owner}
				proof.active.Store(true)
				return proof
			}
			register := func(ctx context.Context, owner mooring.OwnerLease, task, participant string) error {
				return d.cell.store.RegisterTask(ctx, owner, task, participant, p.captured.allowPartial)
			}
			proof := original()
			control := context.WithValue(bound, collaborationAgentTaskRegistrationKey{}, proof)
			if err := register(control, d.owner, x.workID, p.id); err != nil {
				t.Fatalf("allowed original retry: %v", err)
			}
			proof.active.Store(false)
			cases := []struct {
				name  string
				alter func(*collaborationAgentTaskRegistration)
			}{
				{"inactive", func(v *collaborationAgentTaskRegistration) { v.active.Store(false) }},
				{"unknown-participant", func(v *collaborationAgentTaskRegistration) {
					v.participant = &collaborationAgentSubmissionParticipant{}
				}},
				{"missing-registry", func(v *collaborationAgentTaskRegistration) { v.registry = nil }},
				{"wrong-owner-generation", func(v *collaborationAgentTaskRegistration) { v.owner.Generation++ }},
				{"wrong-owner-token", func(v *collaborationAgentTaskRegistration) { v.owner.Token = "other-owner-token" }},
				{"wrong-scope", func(v *collaborationAgentTaskRegistration) { v.owner.Key.ArtifactID = "other-page" }},
			}
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					v := original()
					tc.alter(v)
					if err := register(context.WithValue(bound, collaborationAgentTaskRegistrationKey{}, v), d.owner, x.workID, p.id); err == nil {
						t.Fatal("foreign/inactive proof registered task")
					}
					v.active.Store(false)
				})
			}
			if err := register(bound, d.owner, x.workID, p.id); err == nil {
				t.Fatal("missing proof registered task")
			}
			live := original()
			defer live.active.Store(false)
			authorized := context.WithValue(bound, collaborationAgentTaskRegistrationKey{}, live)
			if err := register(authorized, d.owner, "different-task", p.id); err == nil {
				t.Fatal("foreign task accepted")
			}
			if err := register(authorized, d.owner, x.workID, "different-participant"); err == nil {
				t.Fatal("foreign participant accepted")
			}
			// Keep the inner proof active deliberately: the outer operation must expire.
			escapedProof := original()
			escaped = context.WithValue(bound, collaborationAgentTaskRegistrationKey{}, escapedProof)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := d.cell.store.RegisterTask(escaped, d.owner, x.workID, p.id, p.captured.allowPartial); err == nil {
			t.Fatal("escaped outer operation registered task")
		}
		var count int
		if err := x.f.rig.db.QueryRow(`SELECT count(*) FROM mooring_collab_tasks WHERE page_id=?`, x.key.ArtifactID).Scan(&count); err != nil || count != 1 {
			t.Fatalf("rejected proofs changed task rows: %d %v", count, err)
		}
	})
}
