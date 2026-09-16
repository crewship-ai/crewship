package pipeline

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// TestExecutor_StepOutput_ScrubbedBeforePersistAndBroadcast pins #1416
// item 5: a step whose output happens to echo a credential-shaped string
// (e.g. `{{ inputs.token }}` printed to stdout, or an agent quoting a key
// it was given) must NOT persist or broadcast that secret verbatim.
// internal/pipeline/executor.go's step_outputs_json write and the
// pipeline.step.completed journal/broadcast event both run the shared
// internal/scrubber (the same one the notify path's scrubPreview and the
// script runner's argv audit already use) over the output before it
// leaves the process.
//
// The IN-MEMORY value used for downstream template chaining
// ({{ steps.X.output }}) is deliberately left UNSCRUBBED — a legitimate
// routine that produces a secret in one step and consumes it in the next
// (e.g. a provisioning flow) must keep working; only the persist/broadcast
// copies are redacted.
func TestExecutor_StepOutput_ScrubbedBeforePersistAndBroadcast(t *testing.T) {
	db := openStoreTestDB(t)
	defer db.Close()
	if _, err := db.Exec(runsProjectionDDL); err != nil {
		t.Fatalf("runs ddl: %v", err)
	}
	store := NewStore(db)
	runStore := NewRunStore(db)
	runner := newMockRunner()
	const secret = "sk-ant-abcdefghijklmnopqrstuvwxyz0123456789"
	runner.outputsBySlug["agent_lead"] = []string{"here is the key: " + secret}

	emitter := &captureEmitter{}
	exec := NewExecutor(store, NewResolver(db), runner, emitter).WithRunStore(runStore)
	ctx := context.Background()

	in := validSaveInput("scrub-outputs")
	in.DefinitionJSON = agentStepDef // single agent_run step "s1" / agent_lead
	p, err := store.Save(ctx, in)
	if err != nil {
		t.Fatalf("save: %v", err)
	}

	res, err := exec.Run(ctx, RunInput{PipelineID: p.ID, WorkspaceID: "ws_test", Mode: ModeRun})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Status != "COMPLETED" {
		t.Fatalf("status: %q", res.Status)
	}

	// In-memory chaining value: unscrubbed (downstream steps need the
	// real value).
	if !strings.Contains(res.StepOutputs["s1"], secret) {
		t.Errorf("in-memory StepOutputs must keep the raw value for template chaining, got %q", res.StepOutputs["s1"])
	}

	// Persisted projection: scrubbed. Step outputs live in the normalized
	// pipeline_run_step_outputs table since #1411 (RunRecord.StepOutputsJSON
	// is no longer written on the hot path), so read the persisted copy via
	// GetStepOutputs.
	persisted, err := runStore.GetStepOutputs(ctx, res.RunID)
	if err != nil {
		t.Fatalf("get step outputs: %v", err)
	}
	if strings.Contains(persisted["s1"], secret) {
		t.Errorf("persisted step output leaked the secret: %q", persisted["s1"])
	}
	if !strings.Contains(persisted["s1"], "[REDACTED") {
		t.Errorf("persisted step output should carry a [REDACTED...] marker, got %q", persisted["s1"])
	}

	// Broadcast/journal event: scrubbed.
	found := false
	for _, e := range emitter.entries {
		if e.Type != "pipeline.step.completed" {
			continue
		}
		found = true
		preview, _ := e.Payload["output_preview"].(string)
		if strings.Contains(preview, secret) {
			t.Errorf("journal output_preview leaked the secret: %q", preview)
		}
		if !strings.Contains(preview, "[REDACTED") {
			t.Errorf("journal output_preview should carry a [REDACTED...] marker, got %q", preview)
		}
	}
	if !found {
		t.Fatal("expected a pipeline.step.completed journal entry")
	}
}

func TestExecutor_ScriptFailure_ScrubbedBeforePersistAndBroadcast(t *testing.T) {
	db := openStoreTestDB(t)
	defer db.Close()
	if _, err := db.Exec(runsProjectionDDL); err != nil {
		t.Fatal(err)
	}
	store, runStore := NewStore(db), NewRunStore(db)
	secrets := []string{"sk-proj-exampleSecret1234567890", "opaqueToken123456789", "example-password-123"}
	stderr := secrets[0] + " Authorization: Bearer " + secrets[1] + " PASSWORD=" + secrets[2]
	runner := &fakeScriptRunner{result: ScriptRunResult{Stderr: stderr, ExitCode: 1}}
	emitter, ws := &captureEmitter{}, &captureWS{}
	exec := NewExecutor(store, NewResolver(db), nil, emitter).WithRunStore(runStore).WithScriptRunner(runner).WithWSBroadcaster(ws)
	in := validSaveInput("scrub-script-failure")
	in.DefinitionJSON = `{"name":"scrub-script-failure","steps":[{"id":"fail","type":"script","script":{"path":"scripts/fail.sh"}}]}`
	p, err := store.Save(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	res, err := exec.Run(context.Background(), RunInput{PipelineID: p.ID, WorkspaceID: "ws_test", Mode: ModeRun})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "FAILED" {
		t.Fatalf("status = %s", res.Status)
	}
	persisted, err := runStore.Get(context.Background(), res.RunID)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(emitter.entries)
	if err != nil {
		t.Fatal(err)
	}
	ws.mu.Lock()
	defer ws.mu.Unlock()
	surfaces := []string{persisted.ErrorMessage, string(encoded)}
	for _, event := range ws.events {
		b, err := json.Marshal(event.payload)
		if err != nil {
			t.Fatal(err)
		}
		surfaces = append(surfaces, string(b))
	}
	if len(emitter.entries) == 0 || len(ws.events) == 0 {
		t.Fatal("missing journal or broadcast evidence")
	}
	for _, surface := range surfaces {
		for _, secret := range secrets {
			if strings.Contains(surface, secret) {
				t.Errorf("secret leaked: %s", surface)
			}
		}
	}
	if !strings.Contains(persisted.ErrorMessage, "[REDACTED") {
		t.Fatalf("missing redaction: %s", persisted.ErrorMessage)
	}
}

// The browser refetches run-records as soon as it receives run.started.
// Reading synchronously in the broadcast callback makes the ordering race
// deterministic instead of relying on network/SQLite timing.
type runStartReadProbe struct {
	t     *testing.T
	store *RunStore
	seen  bool
}

func (p *runStartReadProbe) BroadcastWorkspace(_, event string, payload any) {
	if event != "pipeline.run.started" {
		return
	}
	p.seen = true
	id := payload.(map[string]any)["run_id"].(string)
	if _, err := p.store.Get(context.Background(), id); err != nil {
		p.t.Errorf("run.started preceded readable projection: %v", err)
	}
}
func TestExecutor_RunStartedAfterProjection(t *testing.T) {
	db := openStoreTestDB(t)
	defer db.Close()
	if _, err := db.Exec(runsProjectionDDL); err != nil {
		t.Fatal(err)
	}
	store, runs := NewStore(db), NewRunStore(db)
	probe := &runStartReadProbe{t: t, store: runs}
	exec := NewExecutor(store, NewResolver(db), nil, &captureEmitter{}).WithRunStore(runs).WithWSBroadcaster(probe)
	in := validSaveInput("start-projection")
	in.DefinitionJSON = `{"name":"start-projection","agentless":true,"steps":[{"id":"echo","type":"transform","transform":{"input":"hello","expression":"."}}]}`
	p, err := store.Save(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := exec.Run(context.Background(), RunInput{PipelineID: p.ID, WorkspaceID: "ws_test", Mode: ModeRun}); err != nil {
		t.Fatal(err)
	}
	if !probe.seen {
		t.Fatal("missing run.started broadcast")
	}
}
