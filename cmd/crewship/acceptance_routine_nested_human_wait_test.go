package main

// #5354: a held crewship action inside a CALLED routine, and inside a batch
// item, must not hold the synchronous CLI call open. The receipt names the run
// the CLI started (the parent), and an approval completes it once.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const heldParentDef = `{"dsl_version":"1.0","name":"held-parent","steps":[
 {"id":"call","type":"call_pipeline","pipeline_slug":"held-each"}
]}`

func saveHeldRoutines(t *testing.T, owner string, withParent bool) {
	t.Helper()
	if out, err := runCLIWithin(t, owner, 60*time.Second, "routine", "save", "--name", "held-each",
		"--definition", writeRoutineDetailDefinition(t, "held-each", heldEachDef), "--author-crew", "held-crew"); err != nil {
		t.Fatalf("routine save: %v\n%s", err, out)
	}
	if !withParent {
		return
	}
	if out, err := runCLIWithin(t, owner, 60*time.Second, "routine", "save", "--name", "held-parent",
		"--definition", writeRoutineDetailDefinition(t, "held-parent", heldParentDef), "--author-crew", "held-crew"); err != nil {
		t.Fatalf("routine save parent: %v\n%s", err, out)
	}
}

func TestAcceptance_SyncRunOfRoutineCallingHeldRoutineReturnsParentReceipt(t *testing.T) {
	owner, _, actions := startHeldEachServer(t)
	saveHeldRoutines(t, owner, true)

	out, err := runCLIWithin(t, owner, 30*time.Second, "routine", "run", "held-parent")
	if err != nil {
		t.Fatalf("routine run: %v\n%s", err, out)
	}
	id := routineDetailRunIDRE.FindStringSubmatch(out)
	tok := approveTokenRE.FindStringSubmatch(out)
	if !strings.Contains(out, "WAITING") || id == nil || tok == nil {
		t.Fatalf("want a WAITING receipt with a run id and an approve command:\n%s", out)
	}
	// The receipt's run is the parent: it is listed under the parent routine.
	records, err := runRoutineDetailCLI(t, owner, "routine", "records", "held-parent")
	if err != nil || !strings.Contains(records, id[1][:16]) {
		t.Fatalf("receipt run %s is not a run of the parent routine (err=%v):\n%s", id[1], err, records)
	}
	if out, err := runCLIWithin(t, owner, 30*time.Second, "routine", "waitpoints", "approve", tok[1]); err != nil {
		t.Fatalf("approve: %v\n%s", err, out)
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		res, _ := runRoutineDetailCLI(t, owner, "routine", "result", id[1])
		if strings.Contains(strings.ToLower(res), "completed") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("parent run did not complete after the approval:\n%s", res)
		}
		time.Sleep(200 * time.Millisecond)
	}
	if out, err := runCLIWithin(t, owner, 30*time.Second, "routine", "waitpoints", "approve", tok[1]); err == nil || !strings.Contains(out, "409") {
		t.Fatalf("a second approval must answer 409:\n%s", out)
	}
	actions.mu.Lock()
	done := actions.done
	actions.mu.Unlock()
	if done != 1 {
		t.Fatalf("the approved action executed %d times, want once", done)
	}
}

func TestAcceptance_BatchRunShowsWaitingItems(t *testing.T) {
	owner, _, _ := startHeldEachServer(t)
	saveHeldRoutines(t, owner, false)
	file := filepath.Join(t.TempDir(), "items.jsonl")
	if err := os.WriteFile(file, []byte("{}\n{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := runCLIWithin(t, owner, 60*time.Second, "routine", "run", "held-each", "--batch", file)
	if err != nil {
		t.Fatalf("batch run: %v\n%s", err, out)
	}
	if !strings.Contains(out, "0 ok") || !strings.Contains(out, "2 waiting for a decision") {
		t.Fatalf("the batch summary must show waiting items apart from successes:\n%s", out)
	}
}
