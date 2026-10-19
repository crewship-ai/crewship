//go:build linux

package orchestrator

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/provider/bbolt"
)

func TestProtectedCompletionCallbackUsesPersistedExit(t *testing.T) {
	requireProtectedHostTestUID(t)
	state, err := bbolt.New(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	completionPath := filepath.Join(t.TempDir(), "capture-complete")
	provider := &runAgentHostProvider{covContainer: covNewRunContainer(covRunOpts{}), completionPath: completionPath}
	orchestrator := New(provider, state, covQuietLogger())
	orchestrator.ipcToken = "fixture-key"
	orchestrator.SetHostOutputRoot(runAgentHostOutputRoot(t))
	request := covRunReq()
	request.RunID = "completion-callback-fixture"
	calls := 0
	captured := ""
	request.CapturedExit = func(value string) { calls++; captured = value }
	if err := orchestrator.RunAgent(t.Context(), request, nil); err != nil {
		t.Fatal(err)
	}
	awaitRunAgentHostFixtureCompletion(t, completionPath, time.Now().Add(3*time.Second))
	raw, err := state.Get(t.Context(), "agent_runs", request.RunID)
	if err != nil {
		t.Fatal(err)
	}
	var run RunState
	if err := json.Unmarshal(raw, &run); err != nil {
		t.Fatal(err)
	}
	snapshot, err := orchestrator.ReadRetainedRunResult(t.Context(), run)
	if err != nil || !snapshot.Complete || snapshot.Result == nil {
		t.Fatalf("persisted exit unavailable: %+v %v", snapshot, err)
	}
	if calls != 1 || captured == "" || captured != snapshot.Result.CompletedAt {
		t.Fatalf("completion callback did not use exact protected exit: calls=%d got=%q stored=%q", calls, captured, snapshot.Result.CompletedAt)
	}
}
