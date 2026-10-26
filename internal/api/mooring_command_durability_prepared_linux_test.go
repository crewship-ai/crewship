//go:build linux && wordcraft_c1_integration && wordcraft_c1_durability

package api

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/crewship-ai/crewship/internal/mooring"
)

// Prepared opt-in stress scenarios. These use an owned fixture SQLite database
// and actual private C1 host/helper processes, never a development URL. Selecting
// the tags requires real binaries; missing artifacts fail rather than skip.
// The current 256-operation and 32-generation caps intentionally remain in place.
func TestCommandNative5000ChangesTenColdRestarts(t *testing.T) {
	runCommandNativeDurability(t, 5000, 10)
}

// A write after EACH reopen persists its new generation. Reopening repeatedly
// without a write would keep restoring the same old SQL generation set and
// would not exercise the 32-generation boundary.
func TestCommandNative34ChangesThirtyThreeColdRestarts(t *testing.T) {
	runCommandNativeDurability(t, 34, 33)
}

func runCommandNativeDurability(t *testing.T, changes, restarts int) {
	t.Helper()
	if changes < restarts+1 {
		t.Fatal("each session must commit a change to exercise persisted restart generations")
	}
	config := actualCommandNativeConfig(t, "durability-fixture")
	f, manager, request := commandProductTestManager(t)
	mustExec(t, f.r.db, `DELETE FROM mooring_collab_documents WHERE page_id='admission-page'`)
	var port *actualCommandNativeObservedPort
	starts := 0
	start := func(ctx context.Context, runtime string) (commandProductPort, error) {
		c := config
		c.RuntimeRef = runtime
		native, err := startCommandNativePort(ctx, c)
		if err != nil {
			return nil, err
		}
		starts++
		port = &actualCommandNativeObservedPort{commandNativePort: native}
		return port, nil
	}
	open := func(create bool) (*commandProductFactory, *commandProductDocument) {
		factory, err := newCommandProductFactory(true, manager, start)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(factory.Close)
		document, err := factory.Open(request, create)
		if err != nil {
			t.Fatalf("cold open %d: %v (same document; do not reset its data or caps)", starts, err)
		}
		return factory, document
	}
	type sqlHead struct {
		revision            uint64
		checkpoint          string
		operations, changes int64
	}
	readSQL := func() sqlHead {
		var head sqlHead
		if err := f.r.db.QueryRow(`SELECT revision,checkpoint_base64,(SELECT count(*) FROM mooring_collab_operations WHERE page_id='admission-page'),(SELECT count(*) FROM mooring_collab_changes WHERE page_id='admission-page') FROM mooring_collab_documents WHERE page_id='admission-page'`).Scan(&head.revision, &head.checkpoint, &head.operations, &head.changes); err != nil {
			t.Fatal(err)
		}
		return head
	}
	receipts := map[string]string{}
	checkReceipts := func() {
		rows, err := f.r.db.Query(`SELECT op_id,result_json FROM mooring_collab_operations WHERE page_id='admission-page'`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		count := 0
		for rows.Next() {
			var op, raw string
			if err := rows.Scan(&op, &raw); err != nil {
				t.Fatal(err)
			}
			canonical, err := mooring.CanonicalJSON([]byte(raw))
			want, exists := receipts[op]
			if err != nil || !exists || string(canonical) != want {
				t.Fatal("historical durable receipt changed or was replaced", op, err)
			}
			count++
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		if count != len(receipts) {
			t.Fatalf("dedup receipts pruned: have%d want%d", count, len(receipts))
		}
	}
	factory, document := open(true)
	projection, err := port.Projection(request.Context())
	if err != nil || len(projection.IDs) != 1 {
		t.Fatal("initial projection must contain one body paragraph", err)
	}
	block := projection.IDs[0]
	expected := ""
	completed, coldRestarts := 0, 0
	pattern := []string{"A", "ž", "🙂"}
	for session := 0; session <= restarts; session++ {
		admission, err := document.store.Admit(request.Context(), true)
		if err != nil {
			t.Fatal(err)
		}
		actor := commandEngineActorBinding{transportParticipant: admission.participant, sourceUser: admission.source.UserID, actor: "human:" + admission.source.UserID}
		work := changes / (restarts + 1)
		if session < changes%(restarts+1) {
			work++
		}
		for index := 0; index < work; index++ {
			text := pattern[completed%len(pattern)]
			op := fmt.Sprintf("%s:durability-%05d", projection.Generation, completed+1)
			raw, err := json.Marshal(map[string]any{
				"op_id": op, "generation": projection.Generation, "participant": actor.actor,
				"base_revision": uint64(completed), "command": "text.insert", "params": map[string]any{"text": text},
				"target": map[string]any{"block_id": block, "unit": "utf8_bytes", "start": len(expected), "end": len(expected)},
			})
			if err != nil {
				t.Fatal(err)
			}
			outcome, err := document.store.Submit(request, admission, actor, raw)
			if err != nil {
				t.Fatalf("change %d/%d session %d: %v; target is not reduced to fit current capacity", completed+1, changes, session+1, err)
			}
			receiptJSON, err := json.Marshal(outcome)
			if err != nil {
				t.Fatal(err)
			}
			canonicalReceipt, err := mooring.CanonicalJSON(receiptJSON)
			if err != nil {
				t.Fatal(err)
			}
			receipts[op] = string(canonicalReceipt)
			completed++
			expected += text
			if outcome.Revision != uint64(completed) || outcome.OpID != op {
				t.Fatalf("wrong durable receipt after change %d", completed)
			}
			projection, err = port.Projection(request.Context())
			if err != nil || projection.Revision != uint64(completed) || commandDurabilityText(t, projection) != expected {
				t.Fatalf("native text/revision diverged at change %d: %v", completed, err)
			}
		}
		commandReleaseAdmission(document.store, admission)
		before := readSQL()
		if before.revision != uint64(completed) || before.operations != int64(completed) || before.changes != int64(completed) {
			t.Fatal("SQL head differs from completed changes")
		}
		checkReceipts()
		t.Logf("session=%d changes=%d text_bytes=%d sha256=%x", session+1, completed, len(expected), sha256.Sum256([]byte(expected)))
		if session == restarts {
			break
		}
		previousGeneration := projection.Generation
		previousPort := port
		factory.Close()
		select {
		case <-previousPort.Done():
		default:
			t.Fatal("cold restart did not terminate the previous native host")
		}
		factory, document = open(false)
		coldRestarts++
		if port == previousPort || port.stages != 0 {
			t.Fatal("reused runtime or replayed a mutation during cold open")
		}
		projection, err = port.Projection(request.Context())
		if err != nil || projection.Generation == previousGeneration || len(projection.IDs) != 1 || projection.IDs[0] != block || projection.Revision != uint64(completed) || commandDurabilityText(t, projection) != expected {
			t.Fatalf("same document lost text/identity after cold restart %d: %v", coldRestarts, err)
		}
		if after := readSQL(); after != before {
			t.Fatal("cold open rewrote SQL or replayed operations")
		}
		checkReceipts()
	}
	if completed != changes || coldRestarts != restarts || starts != restarts+1 {
		t.Fatalf("incomplete scenario: changes=%d restarts=%d starts=%d", completed, coldRestarts, starts)
	}
}

func commandDurabilityText(t *testing.T, projection commandNativeProjection) string {
	t.Helper()
	var native struct {
		Body []struct {
			Kind string `json:"kind"`
			Text string `json:"text"`
		} `json:"body"`
	}
	if err := json.Unmarshal([]byte(projection.NativeJSON), &native); err != nil || len(native.Body) != 1 || native.Body[0].Kind != "para" {
		t.Fatal("durability scenario gained or lost a body block", err)
	}
	return native.Body[0].Text
}
