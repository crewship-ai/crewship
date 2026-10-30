package api

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/crewship-ai/crewship/internal/orchestrator"
	"github.com/crewship-ai/crewship/internal/provider"
)

// Provider and run records here are explicit unit stand-ins, not a real model
// invocation or public acceptance. The native version/lease paths are real.
type nativeAgentStoppedProvider struct {
	provider.ContainerProvider
	running bool
}

func (p *nativeAgentStoppedProvider) ExecInspect(context.Context, string) (bool, int, error) {
	return p.running, 0, nil
}

func nativeAgentRetirementRuntime(m *craftNativeAgentManager, e *craftNativeAgentEntry, running bool) *craftNativeAgentRuntime {
	h := &WebhookHandler{db: m.human.pages.db, container: &nativeAgentStoppedProvider{running: running}, logger: newTestLogger()}
	rt := &craftNativeAgentRuntime{WebhookRuntime: NewWebhookRuntime(h), manager: m}
	location := orchestrator.RunLocation{ContainerID: "unit-native-container", AgentSlug: "principal-agent", RunID: e.attempt.runID}
	rt.launches.Store(e.attempt.runID, &webhookLaunch{requested: true, confirmed: true, nativeExecID: "unit-original-exec", location: &location, cancel: func() {}})
	m.runtime = rt
	return rt
}

func TestCraftNativeAgentStoppedTerminalRunRetiresOwnLease(t *testing.T) {
	f, m, human, call, _ := nativeAgentToolFixture(t)
	w := call(`{"operation":"acquire"}`)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	var lease struct {
		Generation string `json:"generation"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &lease); err != nil {
		t.Fatal(err)
	}
	w = call(fmt.Sprintf(`{"operation":"apply","generation":%q,"base_version":"1","request_id":"retirement","commands":[{"command":"text.insert","params":{"text":"saved"}}]}`, lease.Generation))
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	e := m.runs[f.a.RunID]
	nativeAgentRetirementRuntime(m, e, false)
	// This is the canonical terminal vocabulary, not audit_log's different
	// agent.run.completed spelling. Tool authority must stop immediately.
	mustExec(t, f.rig.db, `INSERT INTO journal_entries(id,workspace_id,agent_id,entry_type,actor_type,summary,trace_id) VALUES('native-terminal',?,?,'run.completed','orchestrator','unit confirmed provider exit',?)`, f.a.WorkspaceID, f.a.AgentID, f.a.RunID)
	if denied := call(`{"operation":"read"}`); denied.Code != 403 {
		t.Fatal("terminal run retained tool authority", denied.Code, denied.Body)
	}
	if _, err := m.finishStoppedRun(human, e); err != nil {
		t.Fatalf("actual stopped original exec could not retire after terminal journal: %v", err)
	}
	token := nativeAgentHumanIssued(t, m.human, human)
	if next := craftNativeLeaseCall(m.human, human, token, "@native-agent-page", "acquire", `{}`); next.Code != 200 {
		t.Fatal("retirement did not free original agent lease", next.Code, next.Body)
	}
}

func TestCraftNativeAgentRunningOriginalExecCannotUseRetirement(t *testing.T) {
	f, m, human, call, _ := nativeAgentToolFixture(t)
	if w := call(`{"operation":"acquire"}`); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	e := m.runs[f.a.RunID]
	nativeAgentRetirementRuntime(m, e, true)
	if _, err := m.finishStoppedRun(human, e); err == nil {
		t.Fatal("running original provider obtained retirement proof")
	}
	m.mu.Lock()
	closed := e.closed
	m.mu.Unlock()
	if closed {
		t.Fatal("retirement closed a still-running original execution")
	}
}
