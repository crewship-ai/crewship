package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
)

// nativeAgentHumanHoldsLease admits the fixture's human editor and takes the
// lease, so an agent acquire is refused exactly as on dev3 (#4480 D2).
func nativeAgentHumanHoldsLease(t *testing.T, m *craftNativeAgentManager, human context.Context) {
	t.Helper()
	r := httptest.NewRequest("POST", "/", strings.NewReader(`{}`)).WithContext(human)
	r.SetPathValue("slug", "@native-agent-page")
	w := httptest.NewRecorder()
	m.human.sessions(w, r)
	var session struct {
		Token string `json:"session_token"`
	}
	if w.Code != 201 || json.Unmarshal(w.Body.Bytes(), &session) != nil {
		t.Fatal("human admission", w.Code, w.Body)
	}
	if h := craftNativeLeaseCall(m.human, human, session.Token, "@native-agent-page", "acquire", `{}`); h.Code != 200 {
		t.Fatal("human acquire", h.Code, h.Body)
	}
}

const nativeAgentOutcomeProposal = `{"operation":"propose","base_version":"1","request_id":"outcome-proposal","summary":"Append marker","commands":[{"command":"text.insert","params":{"text":"x"}}]}`

// A proposal made while a human holds the lease is a successful task outcome,
// recorded with its proposal id. Only a proposal created by this run counts:
// replaying another run's request_id returns that run's proposal but does not
// make it this task's outcome.
func TestCraftNativeAgentFinishedRunOutcome(t *testing.T) {
	cases := []struct {
		name     string
		act      func(t *testing.T, m *craftNativeAgentManager, human context.Context, e *craftNativeAgentEntry, call func(string) *httptest.ResponseRecorder)
		wantErr  error
		outcome  string
		proposal bool
	}{
		{
			name: "committed receipt succeeds as saved",
			act: func(t *testing.T, _ *craftNativeAgentManager, _ context.Context, _ *craftNativeAgentEntry, call func(string) *httptest.ResponseRecorder) {
				w := call(`{"operation":"acquire"}`)
				var lease struct {
					Generation string `json:"generation"`
				}
				if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &lease) != nil {
					t.Fatal(w.Code, w.Body)
				}
				if w = call(fmt.Sprintf(`{"operation":"apply","generation":%q,"base_version":"1","request_id":"outcome-save","commands":[{"command":"text.insert","params":{"text":"saved"}}]}`, lease.Generation)); w.Code != 200 {
					t.Fatal(w.Code, w.Body)
				}
			},
			outcome: "saved",
		},
		{
			name: "proposal by this run succeeds as proposal",
			act: func(t *testing.T, m *craftNativeAgentManager, human context.Context, _ *craftNativeAgentEntry, call func(string) *httptest.ResponseRecorder) {
				nativeAgentHumanHoldsLease(t, m, human)
				if w := call(`{"operation":"acquire"}`); w.Code != 409 {
					t.Fatal("human lease not respected", w.Code, w.Body)
				}
				if w := call(nativeAgentOutcomeProposal); w.Code != 201 {
					t.Fatal(w.Code, w.Body)
				}
			},
			outcome:  "proposal",
			proposal: true,
		},
		{
			name: "neither receipt nor proposal fails",
			act: func(t *testing.T, m *craftNativeAgentManager, human context.Context, _ *craftNativeAgentEntry, call func(string) *httptest.ResponseRecorder) {
				nativeAgentHumanHoldsLease(t, m, human)
				if w := call(`{"operation":"acquire"}`); w.Code != 409 {
					t.Fatal("human lease not respected", w.Code, w.Body)
				}
			},
			wantErr: errNativeAgentNoReceipt,
		},
		{
			name: "proposal by another run replayed by request_id fails",
			act: func(t *testing.T, m *craftNativeAgentManager, human context.Context, e *craftNativeAgentEntry, call func(string) *httptest.ResponseRecorder) {
				nativeAgentHumanHoldsLease(t, m, human)
				if w := call(nativeAgentOutcomeProposal); w.Code != 201 {
					t.Fatal(w.Code, w.Body)
				}
				// The earlier run's proposal row stays; this run starts with no
				// tool history of its own and repeats the same request_id.
				e.callMu.Lock()
				e.tool = nil
				e.callMu.Unlock()
				if w := call(nativeAgentOutcomeProposal); w.Code != 201 {
					t.Fatal("replay of an existing proposal", w.Code, w.Body)
				}
			},
			wantErr: errNativeAgentNoReceipt,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, m, human, call, _ := nativeAgentToolFixture(t)
			e := m.runs[f.a.RunID]
			tc.act(t, m, human, e, call)
			nativeAgentRetirementRuntime(m, e, false)
			mustExec(t, f.rig.db, `INSERT INTO journal_entries(id,workspace_id,agent_id,entry_type,actor_type,summary,trace_id) VALUES('native-terminal',?,?,'run.completed','orchestrator','unit confirmed provider exit',?)`, f.a.WorkspaceID, f.a.AgentID, f.a.RunID)
			_, err := m.finishStoppedRun(human, e)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("finishStoppedRun = %v, want %v", err, tc.wantErr)
				}
			} else if err != nil {
				t.Fatalf("finishStoppedRun = %v, want success", err)
			}
			response := nativeAgentStatusCall(m, human, e.workID, "@native-agent-page")
			var status struct {
				Outcome    string `json:"outcome"`
				ProposalID string `json:"proposal_id"`
			}
			if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &status) != nil {
				t.Fatal(response.Code, response.Body)
			}
			if status.Outcome != tc.outcome {
				t.Fatalf("status outcome = %q, want %q: %s", status.Outcome, tc.outcome, response.Body)
			}
			var stored string
			_ = f.rig.db.QueryRow(`SELECT id FROM craft_native_proposals WHERE page_id='native-agent-page'`).Scan(&stored)
			if tc.proposal != (status.ProposalID != "") || (tc.proposal && status.ProposalID != stored) {
				t.Fatalf("status proposal_id = %q, stored %q", status.ProposalID, stored)
			}
		})
	}
}
