package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/crewship-ai/crewship/internal/artifacts"
	"github.com/crewship-ai/crewship/internal/dispatch"
	"github.com/crewship-ai/crewship/internal/mooring"
)

const nativeRunControlProposal = `{"operation":"propose","base_version":"1","request_id":"run-control-proposal","summary":"Review","commands":[{"command":"text.insert","params":{"text":"x"}}]}`

func TestCraftNativeAgentProposalRespectsUnknownSave(t *testing.T) {
	f, m, _, call, calls := nativeAgentToolFixture(t)
	acquire := call(`{"operation":"acquire"}`)
	var lease struct {
		Generation string `json:"generation"`
	}
	if acquire.Code != 200 || json.Unmarshal(acquire.Body.Bytes(), &lease) != nil {
		t.Fatal(acquire.Code, acquire.Body)
	}
	// Exercise the actual unknown-outcome branch. The injected SQL rollback
	// is not claimed as evidence of an ambiguous successful COMMIT.
	mustExec(t, f.rig.db, `CREATE TRIGGER proposal_unknown_save BEFORE INSERT ON artifact_versions WHEN NEW.version=2 BEGIN SELECT RAISE(ABORT,'unclassified save failure'); END`)
	failed := call(fmt.Sprintf(`{"operation":"apply","generation":%q,"base_version":"1","request_id":"uncertain-save","commands":[{"command":"text.insert","params":{"text":"save"}}]}`, lease.Generation))
	entry := m.runs[f.a.RunID]
	if failed.Code == 200 || !entry.tool.uncertain {
		t.Fatalf("unknown branch not calibrated: %d %s", failed.Code, failed.Body)
	}
	before := *calls
	refused := call(nativeRunControlProposal)
	if refused.Code != 503 || *calls != before {
		t.Errorf("proposal bypassed unknown save: %d engine=%d/%d %s", refused.Code, *calls, before, refused.Body)
	}
	var count int
	if err := f.rig.db.QueryRow(`SELECT count(*) FROM craft_native_proposals`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("unknown run persisted a new proposal: %d %v", count, err)
	}
}

func TestCraftNativeAgentProposalSharesMutationBudgetAndExactRetry(t *testing.T) {
	f, m, _, call, calls := nativeAgentToolFixture(t)
	acquire := call(`{"operation":"acquire"}`)
	var lease struct {
		Generation string `json:"generation"`
	}
	if acquire.Code != 200 || json.Unmarshal(acquire.Body.Bytes(), &lease) != nil {
		t.Fatal(acquire.Code, acquire.Body)
	}
	entry := m.runs[f.a.RunID]
	// Calibrate one remaining slot without running 63 external engines. These
	// prior admitted attempts share the same run state as real apply calls.
	for i := range 63 {
		entry.tool.requests[fmt.Sprintf("earlier-%d", i)] = &craftNativeAgentToolAttempt{}
	}
	first := call(nativeRunControlProposal)
	var proposal struct {
		ID string `json:"proposal_id"`
	}
	if first.Code != 201 || json.Unmarshal(first.Body.Bytes(), &proposal) != nil || proposal.ID == "" {
		t.Fatal(first.Code, first.Body)
	}
	before := *calls
	retry := call(nativeRunControlProposal)
	var replay struct {
		ID string `json:"proposal_id"`
	}
	if retry.Code != 201 || json.Unmarshal(retry.Body.Bytes(), &replay) != nil || replay.ID != proposal.ID || *calls != before {
		t.Errorf("exact proposal retry reran engine/changed result: %d engine=%d/%d %s", retry.Code, *calls, before, retry.Body)
	}
	changed := call(`{"operation":"propose","base_version":"1","request_id":"run-control-proposal","summary":"Review","commands":[{"command":"text.insert","params":{"text":"different command"}}]}`)
	if changed.Code != 409 || *calls != before {
		t.Errorf("changed commands reused request ID: %d engine=%d/%d %s", changed.Code, *calls, before, changed.Body)
	}
	refused := call(fmt.Sprintf(`{"operation":"apply","generation":%q,"base_version":"1","request_id":"over-shared-budget","commands":[{"command":"text.insert","params":{"text":"save"}}]}`, lease.Generation))
	if refused.Code != 429 || *calls != before {
		t.Errorf("proposal did not consume shared run budget: %d engine=%d/%d %s", refused.Code, *calls, before, refused.Body)
	}
}

func TestCraftNativeAgentProposalRetryAfterHeadMovesDoesNotRerunEngine(t *testing.T) {
	f, m, human, call, calls := nativeAgentToolFixture(t)
	first := call(nativeRunControlProposal)
	if first.Code != 201 {
		t.Fatal(first.Code, first.Body)
	}
	var proposal struct {
		ID string `json:"proposal_id"`
	}
	if err := json.Unmarshal(first.Body.Bytes(), &proposal); err != nil {
		t.Fatal(err)
	}
	// Advance the fixture's version store, independently of this tool request.
	entry := m.runs[f.a.RunID]
	store := artifacts.NewVersionStore(f.rig.db, artifacts.VersionOptions{Enabled: true, ContentType: "wordcraft"})
	_, err := store.Apply(human, artifacts.VersionKey{WorkspaceID: f.rig.wsID, PageID: entry.grant.Scope.ArtifactID}, artifacts.VersionRequest{BaseVersion: 1, EntryPoint: "document.json", Files: map[string]artifacts.VersionFile{"document.json": {MediaType: "application/json", Content: []byte(`{"body":[{"kind":"para","text":"other writer"}]}`)}}}, func(context.Context, *sql.Tx) (string, error) { return "user:" + f.a.HumanUserID, nil })
	if err != nil {
		t.Fatal(err)
	}
	before := *calls
	retry := call(nativeRunControlProposal)
	var replay struct {
		ID string `json:"proposal_id"`
	}
	if retry.Code != 201 || json.Unmarshal(retry.Body.Bytes(), &replay) != nil || replay.ID != proposal.ID || *calls != before {
		t.Fatalf("retry after head advance failed/reran engine: %d engine=%d/%d %s", retry.Code, *calls, before, retry.Body)
	}
}

func TestCraftNativeAgentProposalUnknownCommitReconcilesOnlyOriginalCandidate(t *testing.T) {
	f, m, _, call, calls := nativeAgentToolFixture(t)
	mustExec(t, f.rig.db, `CREATE TRIGGER proposal_unknown_commit BEFORE INSERT ON craft_native_proposals BEGIN SELECT RAISE(ABORT,'unclassified proposal persistence failure'); END`)
	failed := call(nativeRunControlProposal)
	entry := m.runs[f.a.RunID]
	attempt := entry.tool.requests["run-control-proposal"]
	if failed.Code != 503 || !entry.tool.uncertain || attempt == nil || len(attempt.proposalCandidate) == 0 {
		t.Fatalf("unknown proposal was not retained for exact reconciliation: %d %s", failed.Code, failed.Body)
	}
	before := *calls
	other := call(`{"operation":"propose","base_version":"1","request_id":"another-proposal","commands":[{"command":"text.insert","params":{"text":"other"}}]}`)
	if other.Code != 503 || *calls != before {
		t.Fatal("unknown proposal admitted another engine", other.Code, *calls, before)
	}
	// As above, this trigger is an injected rollback, not an actual COMMIT
	// acknowledgement loss. The retry still must reuse the frozen candidate.
	mustExec(t, f.rig.db, `DROP TRIGGER proposal_unknown_commit`)
	retry := call(nativeRunControlProposal)
	if retry.Code != 201 || *calls != before || entry.tool.uncertain || len(attempt.proposalCandidate) != 0 {
		t.Fatalf("proposal reconciliation reran engine or retained uncertainty: %d %s", retry.Code, retry.Body)
	}
	outcome, unknown := craftNativeAgentProposalOutcome(entry)
	if unknown || outcome == nil || outcome.ID != attempt.proposalID {
		t.Fatalf("reconciled own proposal missing: %+v %v", outcome, unknown)
	}
}

func TestCraftNativeAgentProposalReplayKeepsAnotherUnknownSave(t *testing.T) {
	f, m, _, call, calls := nativeAgentToolFixture(t)
	if w := call(nativeRunControlProposal); w.Code != 201 {
		t.Fatal(w.Code, w.Body)
	}
	acquire := call(`{"operation":"acquire"}`)
	var lease struct {
		Generation string `json:"generation"`
	}
	if acquire.Code != 200 || json.Unmarshal(acquire.Body.Bytes(), &lease) != nil {
		t.Fatal(acquire.Code, acquire.Body)
	}
	mustExec(t, f.rig.db, `CREATE TRIGGER proposal_replay_unknown_save BEFORE INSERT ON artifact_versions WHEN NEW.version=2 BEGIN SELECT RAISE(ABORT,'unclassified later save failure'); END`)
	failed := call(fmt.Sprintf(`{"operation":"apply","generation":%q,"base_version":"1","request_id":"later-save","commands":[{"command":"text.insert","params":{"text":"later"}}]}`, lease.Generation))
	entry := m.runs[f.a.RunID]
	if failed.Code != 503 || !entry.tool.uncertain {
		t.Fatal("unknown later save not calibrated", failed.Code, failed.Body)
	}
	unknown := entry.tool.uncertainAttempt
	before := *calls
	if w := call(nativeRunControlProposal); w.Code != 201 || *calls != before || !entry.tool.uncertain || entry.tool.uncertainAttempt != unknown {
		t.Fatalf("old proposal replay erased newer uncertainty: %d %s", w.Code, w.Body)
	}
}

func TestCraftNativeAgentProposalKnownRefusalReleasesCandidateMemory(t *testing.T) {
	f, m, _, call, calls := nativeAgentToolFixture(t)
	app := m.documents.applications["wordcraft"]
	app.Store.Validate = func(context.Context, []byte) error { return mooring.ErrInvalid }
	m.documents.applications["wordcraft"] = app
	if w := call(nativeRunControlProposal); w.Code != 422 {
		t.Fatal(w.Code, w.Body)
	}
	entry := m.runs[f.a.RunID]
	attempt := entry.tool.requests["run-control-proposal"]
	if entry.tool.uncertain || attempt == nil || len(attempt.proposalCandidate) != 0 {
		t.Fatal("definitely refused candidate retained bytes/uncertainty")
	}
	before := *calls
	if w := call(nativeRunControlProposal); w.Code != 422 || *calls != before {
		t.Fatalf("known refusal reran engine: %d %s", w.Code, w.Body)
	}
}

func TestCraftNativeAgentProposalRetiredRunReleasesPendingCandidate(t *testing.T) {
	for _, mode := range []string{"close", "retire", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			f, m, human, call, calls := nativeAgentToolFixture(t)
			entry := m.runs[f.a.RunID]
			nativeAgentRetirementRuntime(m, entry, false)
			m.dispatcher = &dispatch.Dispatcher{} // unit readiness, no provider invocation
			if w := call(`{"operation":"acquire"}`); w.Code != 200 {
				t.Fatal(w.Code, w.Body)
			}
			mustExec(t, f.rig.db, `CREATE TRIGGER proposal_retirement_unknown BEFORE INSERT ON craft_native_proposals BEGIN SELECT RAISE(ABORT,'unclassified proposal failure'); END`)
			if w := call(nativeRunControlProposal); w.Code != 503 {
				t.Fatal(w.Code, w.Body)
			}
			attempt := entry.tool.requests["run-control-proposal"]
			if attempt == nil || len(attempt.proposalCandidate) == 0 || !entry.tool.uncertain {
				t.Fatal("unknown candidate not calibrated")
			}
			switch mode {
			case "close":
				m.Close()
			case "cancel":
				if w := nativeAgentCancelCall(m, human); w.Code != 200 && w.Code != 202 {
					t.Fatal(w.Code, w.Body)
				}
			case "retire":
				mustExec(t, f.rig.db, `INSERT INTO journal_entries(id,workspace_id,agent_id,entry_type,actor_type,summary,trace_id) VALUES('proposal-terminal',?,?,'run.completed','orchestrator','unit stopped provider',?)`, f.a.WorkspaceID, f.a.AgentID, f.a.RunID)
				if _, err := m.finishStoppedRun(human, entry); err == nil {
					t.Fatal("unknown result was claimed as success")
				}
			}
			if len(attempt.proposalCandidate) != 0 {
				t.Errorf("closed run retained %d candidate bytes", len(attempt.proposalCandidate))
			}
			if !entry.tool.uncertain {
				t.Error("cleanup erased outcome uncertainty")
			}
			before := *calls
			if w := call(nativeRunControlProposal); w.Code != 403 || *calls != before {
				t.Fatalf("closed run retained tool authority: %d %s", w.Code, w.Body)
			}
		})
	}
}

func TestCraftNativeAgentProposalRefusalKeepsPriorSaveReceipt(t *testing.T) {
	f, m, _, call, _ := nativeAgentToolFixture(t)
	acquire := call(`{"operation":"acquire"}`)
	var lease struct {
		Generation string `json:"generation"`
	}
	if acquire.Code != 200 || json.Unmarshal(acquire.Body.Bytes(), &lease) != nil {
		t.Fatal(acquire.Code, acquire.Body)
	}
	if w := call(fmt.Sprintf(`{"operation":"apply","generation":%q,"base_version":"1","request_id":"confirmed-save","commands":[{"command":"text.insert","params":{"text":"saved"}}]}`, lease.Generation)); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	m.config.AgentApply = func(context.Context, CraftNativeDocumentProfile, []byte, json.RawMessage) ([]byte, error) {
		return nil, ErrCraftNativeAgentEngine
	}
	if w := call(`{"operation":"propose","base_version":"2","request_id":"refused-after-save","commands":[{"command":"text.insert","params":{"text":"candidate"}}]}`); w.Code != 422 {
		t.Fatal(w.Code, w.Body)
	}
	receipt, unknown, _ := craftNativeAgentCompletion(m.runs[f.a.RunID])
	if unknown || receipt == nil || receipt.Version != "2" {
		t.Fatalf("non-writing proposal refusal hid the confirmed save: %+v %v", receipt, unknown)
	}
}
