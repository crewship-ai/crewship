package pipeline

import (
	"context"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// #1420 — end-to-end: run N writes routine.state.cursor via a step's
// state_write binding; run N+1 reads it back through {{ routine.state.cursor }}.
func TestExecutor_RoutineState_WriteThenReadNextRun(t *testing.T) {
	db := openStoreTestDB(t)
	defer db.Close()
	store := NewStore(db)
	exec := NewExecutor(store, NewResolver(db), &echoRunner{}, nil).
		WithStateStore(NewRoutineStateStore(db))
	ctx := context.Background()

	in := validSaveInput("wm")
	in.DefinitionJSON = `{
	  "dsl_version":"1.0","name":"wm",
	  "inputs":[{"name":"next","type":"string"}],
	  "steps":[
	    {"id":"work","type":"agent_run","agent_slug":"w",
	     "prompt":"prev={{ routine.state.cursor }}",
	     "state_write":{"cursor":"{{ inputs.next }}"}}
	  ]
	}`
	p, err := store.Save(ctx, in)
	if err != nil {
		t.Fatalf("save: %v", err)
	}

	// Run 1: cursor unset ⇒ reads empty; persists cursor="100".
	res1, err := exec.Run(ctx, RunInput{
		PipelineID: p.ID, WorkspaceID: "ws_test", Mode: ModeRun,
		Inputs: map[string]any{"next": "100"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := res1.StepOutputs["work"]; got != "prev=" {
		t.Errorf("run1 should read empty cursor, got %q", got)
	}

	// Run 2: reads run 1's write.
	res2, err := exec.Run(ctx, RunInput{
		PipelineID: p.ID, WorkspaceID: "ws_test", Mode: ModeRun,
		Inputs: map[string]any{"next": "200"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := res2.StepOutputs["work"]; got != "prev=100" {
		t.Errorf("run2 should read cursor written by run1, got %q", got)
	}
}

// #1420 — state is isolated per schedule bucket.
func TestRoutineStateStore_IsolatedPerSchedule(t *testing.T) {
	db := openStoreTestDB(t)
	defer db.Close()
	s := NewRoutineStateStore(db)
	ctx := context.Background()

	if err := s.Write(ctx, "pln_1", "sched_a", "cursor", "A1"); err != nil {
		t.Fatal(err)
	}
	if err := s.Write(ctx, "pln_1", "sched_b", "cursor", "B1"); err != nil {
		t.Fatal(err)
	}
	if err := s.Write(ctx, "pln_1", "", "cursor", "default"); err != nil {
		t.Fatal(err)
	}

	a, _ := s.Load(ctx, "pln_1", "sched_a")
	b, _ := s.Load(ctx, "pln_1", "sched_b")
	d, _ := s.Load(ctx, "pln_1", "")
	if a["cursor"] != "A1" || b["cursor"] != "B1" || d["cursor"] != "default" {
		t.Errorf("buckets leaked: a=%q b=%q default=%q", a["cursor"], b["cursor"], d["cursor"])
	}

	// A different pipeline never sees pln_1's state.
	other, _ := s.Load(ctx, "pln_2", "sched_a")
	if len(other) != 0 {
		t.Errorf("cross-pipeline leak: %v", other)
	}
}

// #1420 — state is durable: a fresh store instance on the same DB (simulating a
// process restart) reads what a prior instance wrote, and an upsert overwrites.
func TestRoutineStateStore_SurvivesRestartAndUpserts(t *testing.T) {
	db := openStoreTestDB(t)
	defer db.Close()
	ctx := context.Background()

	writer := NewRoutineStateStore(db)
	if err := writer.Write(ctx, "pln_1", "sched_a", "cursor", "v1"); err != nil {
		t.Fatal(err)
	}
	// Overwrite (watermark advances).
	if err := writer.Write(ctx, "pln_1", "sched_a", "cursor", "v2"); err != nil {
		t.Fatal(err)
	}

	// "Restart": a brand-new store over the same DB handle.
	reloaded := NewRoutineStateStore(db)
	got, err := reloaded.Load(ctx, "pln_1", "sched_a")
	if err != nil {
		t.Fatal(err)
	}
	if got["cursor"] != "v2" {
		t.Errorf("expected upserted v2 after restart, got %q", got["cursor"])
	}
}

// #1420 — the schedule bucket resolves from the trigger so a wake probe reads
// the SAME bucket its main scheduled routine writes (bonus watermark probe).
func TestStateScheduleID_BucketResolution(t *testing.T) {
	cases := []struct {
		via  TriggeredVia
		by   string
		want string
	}{
		{TriggeredViaSchedule, "psched_1", "psched_1"},
		{TriggeredViaWakeCheck, "psched_1", "psched_1"}, // probe shares the schedule's bucket
		{TriggeredViaManual, "user_1", ""},
		{TriggeredViaWebhook, "wh_1", ""},
		{TriggeredViaCallPipeline, "run_parent", ""},
	}
	for _, c := range cases {
		got := stateScheduleID(RunInput{TriggeredVia: c.via, TriggeredByID: c.by})
		if got != c.want {
			t.Errorf("stateScheduleID(via=%s) = %q, want %q", c.via, got, c.want)
		}
	}
}

// #1420 — save-time template validation accepts the routine.state namespace and
// rejects a malformed shape.
func TestValidate_RoutineStateNamespace(t *testing.T) {
	ok := &DSL{
		DSLVersion: "1.0", Name: "s",
		Steps: []Step{{ID: "a", Type: StepAgentRun, AgentSlug: "w", Prompt: "{{ routine.state.cursor }}"}},
	}
	if err := Validate(ok, nil, nil); err != nil {
		t.Errorf("routine.state.cursor should validate, got %v", err)
	}
	bad := &DSL{
		DSLVersion: "1.0", Name: "s",
		Steps: []Step{{ID: "a", Type: StepAgentRun, AgentSlug: "w", Prompt: "{{ routine.nope }}"}},
	}
	if err := Validate(bad, nil, nil); err == nil || !strings.Contains(err.Error(), "routine.state") {
		t.Errorf("routine.nope should be rejected, got %v", err)
	}
}
