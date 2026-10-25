//go:build linux && mooring_rust_integration

package api

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/database"
	"github.com/crewship-ai/crewship/internal/mooring"
	"github.com/crewship-ai/crewship/internal/testutil"
	protocol "github.com/crewship-ai/crewship/mooring/protocol/generated/go"
)

// Production bootstrap/pump/commit and Rust/SQL are real. Original human ingress
// and confirmed runtime identity remain controlled fixtures: no model/container,
// network authentication or two real human browser acceptance is claimed.
func testMooringAgentActualProductionSave(t *testing.T) {
	checkpoint, err := os.ReadFile(os.Getenv("MOORING_SESSION_CHECKPOINT"))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(checkpoint)
	if hex.EncodeToString(sum[:]) != mooringActualFixtureSHA(t, "MOORING_SESSION_CHECKPOINT_SHA256") {
		t.Fatal("checkpoint pin mismatch")
	}
	// Use the production FULL handle before capturing any original proof.
	copy := testutil.MigratedDB(t)
	path := copy.Path()
	if err := copy.Close(); err != nil {
		t.Fatal(err)
	}
	full, err := database.Open("file:" + path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { full.Close() })
	f := newArtifactPrincipalFixtureWithRig(t, pagesTransferSetupWithDB(t, full))
	expiry := time.Now().Add(time.Hour).UTC()
	mustExec(t, f.rig.db, `INSERT INTO user_sessions(id,user_id,expires_at) VALUES('original-session',?,?)`, f.a.HumanUserID, expiry.Format(time.RFC3339Nano))
	source, err := captureOriginalHumanCredential(t.Context(), f.rig.db, &AuthUser{ID: f.a.HumanUserID, SessionID: "original-session", CredentialExpiresAt: expiry.Add(-time.Minute)}, AuthKindSession, "")
	if err != nil {
		t.Fatal(err)
	}
	x := newCollaborationAgentTestFixtureForPrincipal(t, f, source, "read")
	mustExec(t, x.f.rig.db, `DELETE FROM mooring_collab_documents WHERE page_id=?`, x.key.ArtifactID)

	m, err := newCollaborationHTTPManager(f.pages, MooringHTTPConfig{Enabled: true, Binary: os.Getenv("MOORING_SESSION_BINARY"), BinarySHA256: mooringActualFixtureSHA(t, "MOORING_SESSION_BINARY_SHA256"), SDKPrefix: mooringActualSDKPrefix(t)})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	// Reuse the fixture's original fence before bootstrap or any manager operation.
	m.fence = x.fence
	d, err := m.document(httptest.NewRequest(http.MethodPost, "/host-initialize", nil).WithContext(x.human), x.key, checkpoint)
	if err != nil {
		t.Fatalf("production bootstrap: %v", err)
	}
	mustExec(t, x.f.rig.db, `UPDATE page_grants SET level='write' WHERE page_id=? AND subject_type='agent'`, x.key.ArtifactID)
	handle := x.attach(t, x.prepare(t))
	a := &collaborationLiveAgent{document: d, ref: "actual-production-agent-save", confirmed: collaborationAgentConfirmed{store: x.store, key: x.key, mandate: handle, workID: x.workID, runID: x.f.a.RunID, agentID: x.f.a.AgentID, crewID: x.f.a.CrewID, attempt: 1, generation: 1, execID: "synthetic-confirmed-exec"}, ready: make(chan struct{})}
	d.agents = map[string]*collaborationLiveAgent{a.ref: a}
	m.agentMandates = x.store
	m.agentTasks = &collaborationAgentTaskController{manager: m, confirmed: map[string]*collaborationLiveAgent{x.f.a.RunID: a}}
	read := x.request()
	read.Method = http.MethodGet
	reply := newAgentToolBufferedWriter(false)
	m.agentTool(reply, read)
	var view struct {
		BlockIDs []string                     `json:"block_ids"`
		Revision string                       `json:"revision"`
		Targets  []mooring.BatchTargetVersion `json:"targets"`
	}
	if reply.Code != 200 || json.Unmarshal(reply.Body.Bytes(), &view) != nil || len(view.BlockIDs) == 0 {
		t.Fatalf("actual agent read: %d %s", reply.Code, reply.Body.String())
	}
	change := submissionTestChange(t, x.key, "actual-production-save", x.workID, a.participant.id)
	op := &change.Operations[0]
	op.BaseRevision = view.Revision
	for _, target := range view.Targets {
		if target.TargetID == view.BlockIDs[0] {
			op.Reads = []mooring.BatchReadPrecondition{{TargetID: target.TargetID, Revision: target.Revision}}
		}
	}
	op.Payload, _ = json.Marshal(map[string]any{"kind": "text.insert", "target_id": view.BlockIDs[0], "at": map[string]string{"kind": "end"}, "text": " actual registered agent Save"})
	preimage, _ := json.Marshal(map[string]any{"domain": "mooring.atomic.v0.4", "scope": d.wireScope(), "change_id": change.ID, "task_id": change.TaskID, "operations_without_fingerprints": []any{map[string]any{"op_id": op.ID, "participant_ref": op.Participant, "base_revision": op.BaseRevision, "dependencies": op.Dependencies, "read_preconditions": op.Reads, "payload": op.Payload}}})
	hashes, err := mooring.FingerprintAtomicJSON(preimage)
	if err != nil {
		t.Fatal(err)
	}
	change.MembershipHash = hashes.MembershipHash
	op.Fingerprint = hashes.Operations[op.ID]
	encoded, _ := json.Marshal([]mooring.BatchChange{change})
	var changes []protocol.AtomicChange
	if err := json.Unmarshal(encoded, &changes); err != nil {
		t.Fatal(err)
	}
	task := protocol.Identifier(x.workID)
	packet, _ := json.Marshal(protocol.SubmitChanges{Type: "submit_changes", RequestID: "actual-production-save-request", Scope: d.wireScope(), SessionID: protocol.Identifier(d.activation.SessionID()), Versions: d.wireVersions(), TaskID: &task, Changes: changes})
	request := x.request()
	request.Method = http.MethodPost
	request.Body = http.NoBody
	request.Body = io.NopCloser(bytes.NewReader(packet))
	saved := newAgentToolBufferedWriter(false)
	m.agentTool(saved, request)
	var receipt protocol.CommitResult
	if saved.Code != http.StatusOK || json.Unmarshal(saved.Body.Bytes(), &receipt) != nil || receipt.Type != "commit_result" || receipt.RequestID != "actual-production-save-request" || receipt.Scope != d.wireScope() || receipt.Revision != "1" || len(receipt.Changes) != 1 || receipt.Changes[0].Status != "saved" {
		t.Fatalf("actual production agent Save failed: %d %s runtime=%v", saved.Code, saved.Body.String(), d.cell.runtime.Err())
	}
	var resultJSON, participant string
	if err := x.f.rig.db.QueryRow(`SELECT result_json,participant_ref FROM mooring_collab_operations WHERE page_id=? AND op_id=?`, x.key.ArtifactID, op.ID).Scan(&resultJSON, &participant); err != nil {
		t.Fatal(err)
	}
	var result mooring.BatchOperationResult
	if err := json.Unmarshal([]byte(resultJSON), &result); err != nil {
		t.Fatal(err)
	}
	if result.Revision != "1" || result.Status != "saved" || participant != a.participant.id {
		t.Fatalf("unexpected durable result: %s %s %s", result.Revision, result.Status, participant)
	}
	var actor, human, agent, crew, taskID, run, hash string
	if err := x.f.rig.db.QueryRow(`SELECT actor_kind,human_user_id,agent_id,crew_id,work_id,run_id,membership_hash FROM mooring_collab_artifact_edits WHERE page_id=? AND change_id=?`, x.key.ArtifactID, change.ID).Scan(&actor, &human, &agent, &crew, &taskID, &run, &hash); err != nil {
		t.Fatal(err)
	}
	if actor != "agent" || human != f.a.HumanUserID || agent != f.a.AgentID || crew != f.a.CrewID || taskID != x.workID || run != f.a.RunID || hash != change.MembershipHash {
		t.Fatal("durable attribution lost original task identity")
	}
	reread := x.request()
	reread.Method = http.MethodGet
	after := newAgentToolBufferedWriter(false)
	m.agentTool(after, reread)
	if after.Code != 200 || !strings.Contains(after.Body.String(), "actual registered agent Save") {
		t.Fatalf("durable checkpoint lost inserted text: %d", after.Code)
	}
	retryRequest := x.request()
	retryRequest.Method = http.MethodPost
	retryRequest.Body = io.NopCloser(bytes.NewReader(packet))
	retry := newAgentToolBufferedWriter(false)
	m.agentTool(retry, retryRequest)
	if retry.Code != 200 || !bytes.Equal(retry.Body.Bytes(), saved.Body.Bytes()) {
		t.Fatalf("exact original retry lost same receipt: %d %s", retry.Code, retry.Body.String())
	}
	var count int
	if err := x.f.rig.db.QueryRow(`SELECT count(*) FROM mooring_collab_operations WHERE page_id=?`, x.key.ArtifactID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("retry duplicated SQL operations: %d %v", count, err)
	}

}
