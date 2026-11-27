package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/artifacts"
	"github.com/crewship-ai/crewship/internal/mooring"
)

// sitSaveAttempt is rig.save with an X-Craft-Attempt.
func (rig *workbookRig) sitSaveAttempt(body []byte, base, attempt string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "/api/v1/pages/native-page/craft/document/save", bytes.NewReader(body)).WithContext(rig.ctx)
	r.SetPathValue("slug", "native-page")
	r.Header.Set("Content-Type", mooring.WorkbookMediaTypeXLSM)
	r.Header.Set("X-Craft-Session", rig.token)
	r.Header.Set("X-Craft-Generation", "1")
	r.Header.Set("X-Craft-Base-Version", base)
	r.Header.Set("X-Craft-Editor-Revision", "sit-doc:"+attempt)
	r.Header.Set("X-Craft-Engine-Pin", rig.pin)
	r.Header.Set("X-Craft-Content-Hash", workbookHash(body))
	if attempt != "" {
		r.Header.Set("X-Craft-Attempt", attempt)
	}
	if rig.rebase != "" {
		r.Header.Set("X-Craft-Rebase", rig.rebase)
	}
	r.ContentLength = int64(len(body))
	w := httptest.NewRecorder()
	rig.d.saveSnapshot(w, r)
	return w
}

func (rig *workbookRig) sitHead(t *testing.T) int64 {
	t.Helper()
	var v int64
	if err := rig.db.QueryRow(`SELECT latest_version FROM artifact_version_heads WHERE page_id='native-page'`).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

// sitAgentVersion stores an agent version over base with the operations the production code resolves
// from the agent's commands, in the version's own transaction (craftNativeAgentWorkbookRecord's
// record, without the engine).
func (rig *workbookRig) sitAgentVersion(t *testing.T, base int64, agent, operationID string, commands string, body []byte) error {
	t.Helper()
	ops, replayable := craftNativeAgentResolvedOps(json.RawMessage(commands))
	s := artifacts.NewVersionStore(rig.db, artifacts.VersionOptions{Enabled: true, ContentType: "gridcraft"})
	_, err := s.Apply(t.Context(), artifacts.VersionKey{WorkspaceID: rig.ws, PageID: "native-page"}, artifacts.VersionRequest{BaseVersion: base, EntryPoint: "workbook.xlsm",
		Files: map[string]artifacts.VersionFile{"workbook.xlsm": {Content: body, MediaType: mooring.WorkbookMediaTypeXLSM}},
		Record: func(ctx context.Context, tx *sql.Tx, r artifacts.VersionReceipt) error {
			return recordCraftVersionOps(ctx, tx, craftVersionOps{WorkspaceID: rig.ws, PageID: "native-page", Version: r.Version, BaseVersion: base, ActorKind: "agent", ActorID: agent,
				OperationID: operationID, EnginePin: rig.pin, Calculate: "calculated", Replayable: replayable, Ops: ops})
		}}, func(context.Context, *sql.Tx) (string, error) { return "agent:" + agent, nil })
	return err
}

// Several editors of one person save over the same base at once: exactly one becomes the next
// version, every other one is told the Sheet moved, with the head and where the operations since
// its base are. One person holds at most two uploads at once (artifacts.maxActorStorageUploads):
// a save told artifact_storage_busy (503) stored nothing and is sent again, as the editor does,
// with the same attempt after a growing pause. A busy answer is never the final one.
func TestSheetITConcurrentWorkbookSavesOverOneBase(t *testing.T) {
	rig := workbookDocumentsFixture(t)
	const n, tries = 6, 40
	codes := make([]int, n)
	bodies := make([]string, n)
	busy := make([]int, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			body := sitWorkbook(sitCells{"A1": fmt.Sprintf("editor %d", i)}, sitInitialOther())
			pause := 5 * time.Millisecond
			for try := 0; try < tries; try++ {
				w := rig.sitSaveAttempt(body, "1", fmt.Sprintf("sit-race-%d", i))
				codes[i], bodies[i] = w.Code, w.Body.String()
				if w.Code != 503 || !strings.Contains(bodies[i], `"code":"artifact_storage_busy"`) {
					return
				}
				busy[i]++
				time.Sleep(pause)
				pause = min(2*pause, 200*time.Millisecond)
			}
		}(i)
	}
	wg.Wait()
	won := 0
	for i := range n {
		switch {
		case codes[i] == 200:
			won++
		case codes[i] == 409 && strings.Contains(bodies[i], `"code":"moved"`) && strings.Contains(bodies[i], `"head":"2"`) && strings.Contains(bodies[i], `/craft/document/operations?from=1`):
		default:
			t.Errorf("editor %d after %d busy answers: %d %s", i, busy[i], codes[i], bodies[i])
		}
	}
	if won != 1 || rig.sitHead(t) != 2 {
		t.Fatalf("%d saves won over one base, head %d: %v (busy %v)", won, rig.sitHead(t), codes, busy)
	}
	t.Logf("busy answers per editor before the final one: %v", busy)
}

// A save whose answer was lost is sent again with the same X-Craft-Attempt: one version. The same
// attempt with another body is never a second version. keep-local is a marker, never a way around
// the compare-and-swap: over a stale base it is still moved.
func TestSheetITWorkbookSaveRetryAndKeepLocalStayOneCAS(t *testing.T) {
	rig := workbookDocumentsFixture(t)
	body := sitWorkbook(sitInitialL(), sitInitialOther())
	first := rig.sitSaveAttempt(body, "1", "sit-attempt-1")
	again := rig.sitSaveAttempt(body, "1", "sit-attempt-1")
	if first.Code != 200 || again.Code != 200 || rig.sitHead(t) != 2 {
		t.Fatalf("retry: %d %s / %d %s, head %d", first.Code, first.Body, again.Code, again.Body, rig.sitHead(t))
	}
	var r1, r2 mooring.NativeSaveReceipt
	_ = json.Unmarshal(first.Body.Bytes(), &r1)
	_ = json.Unmarshal(again.Body.Bytes(), &r2)
	if r1.Version != "2" || r2.Version != "2" || r1.ContentHash != r2.ContentHash {
		t.Fatalf("retry receipts %+v %+v", r1, r2)
	}
	other := sitWorkbook(sitCells{"A1": "other"}, sitInitialOther())
	if w := rig.sitSaveAttempt(other, "1", "sit-attempt-1"); w.Code == 200 || rig.sitHead(t) != 2 {
		t.Fatalf("the same attempt with another body: %d %s, head %d", w.Code, w.Body, rig.sitHead(t))
	}
	rig.rebase = "keep-local"
	if w := rig.sitSaveAttempt(other, "1", "sit-keep-stale"); w.Code != 409 || !strings.Contains(w.Body.String(), `"code":"moved"`) || rig.sitHead(t) != 2 {
		t.Fatalf("keep-local over a stale base: %d %s", w.Code, w.Body)
	}
	if w := rig.sitSaveAttempt(other, "2", "sit-keep-head"); w.Code != 200 || rig.sitHead(t) != 3 {
		t.Fatalf("keep-local over the head: %d %s", w.Code, w.Body)
	}
	var audited string
	if err := rig.db.QueryRow(`SELECT metadata FROM audit_logs WHERE action='artifact.sheet_saved' ORDER BY rowid DESC LIMIT 1`).Scan(&audited); err != nil || !strings.Contains(audited, `"source":"keep_local"`) || !strings.Contains(audited, `"version":3`) && !strings.Contains(audited, `"version":"3"`) {
		t.Fatalf("keep-local audit %q %v", audited, err)
	}
}

// Operations listing over a chain of versions: agent versions carry the resolved commands and the
// agent's name, the head's hash is the stored file's, replayable is the AND of every version, more
// than 50 behind is 413 too_many, exactly 50 is still answered.
func TestSheetITOperationsChainLimitsAndHash(t *testing.T) {
	rig := workbookDocumentsFixture(t)
	mustExec(t, rig.db, `INSERT INTO agents(id,workspace_id,name,slug) VALUES('agent-sit',?,'Agent Sit','agent-sit')`, rig.ws)
	v2 := sitWorkbook(sitCells{"B8": "done"}, sitInitialOther())
	if err := rig.sitAgentVersion(t, 1, "agent-sit", "sit-a", `[{"command":"range.clear","params":{"range":"B8:B9","sheet":"L"}},{"command":"cell.set","params":{"cell":"B8","input":"done","sheet":"L"}}]`, v2); err != nil {
		t.Fatal(err)
	}
	// An agent version whose commands name no sheet is stored, but never replayable.
	v3 := sitWorkbook(sitCells{"C1": "x"}, sitInitialOther())
	if err := rig.sitAgentVersion(t, 2, "agent-sit", "sit-b", `[{"command":"cell.set","params":{"cell":"C1","input":"x"}}]`, v3); err != nil {
		t.Fatal(err)
	}
	read := func(from string) (int, sitOperationsAnswer, string) {
		w := rig.operations(from)
		var a sitOperationsAnswer
		_ = json.Unmarshal(w.Body.Bytes(), &a)
		return w.Code, a, w.Body.String()
	}
	code, a, raw := read("1")
	if code != 200 || a.Head != "3" || a.HeadContentHash != workbookHash(v3) || a.Replayable || len(a.Versions) != 2 {
		t.Fatalf("from 1: %d %s", code, raw)
	}
	if v := a.Versions[0]; v.ActorName != "Agent Sit" || v.OperationID != "sit-a" || !v.Replayable || string(v.Ops) != `[{"command":"edit.clearContents","params":{"range":"'L'!B8:B9"}},{"command":"cell.set","params":{"cell":"'L'!B8","input":"done"}}]` {
		t.Fatalf("agent version %+v", v)
	}
	if v := a.Versions[1]; v.Replayable || v.Ops != nil || v.OperationID != "sit-b" {
		t.Fatalf("unnamed-sheet version %+v", v)
	}
	if code, a, raw = read("2"); code != 200 || a.Replayable || len(a.Versions) != 1 {
		t.Fatalf("from 2: %d %s", code, raw)
	}
	// 50 more person versions: head 53.
	for v := int64(3); v < 53; v++ {
		body := sitWorkbook(sitCells{"A1": fmt.Sprint(v)}, sitInitialOther())
		if w := rig.sitSaveAttempt(body, fmt.Sprint(v), fmt.Sprintf("sit-chain-%d", v)); w.Code != 200 {
			t.Fatalf("save %d: %d %s", v, w.Code, w.Body)
		}
	}
	if code, _, raw = read("2"); code != 413 || !strings.Contains(raw, `"code":"too_many"`) {
		t.Fatalf("51 behind: %d %s", code, raw)
	}
	if code, a, raw = read("3"); code != 200 || len(a.Versions) != 50 || a.Head != "53" || a.Versions[0].Version != "4" || a.Versions[49].Version != "53" {
		t.Fatalf("50 behind: %d %d versions %s", code, len(a.Versions), raw[:min(len(raw), 300)])
	}
}

// A model picks request IDs; two different changes in two runs on one Sheet may well both be "r1".
// The operations row is keyed UNIQUE(workspace_id, page_id, operation_id), so the operation ID is
// the server's: the run and the request ID. A later run's change must be stored.
func TestSheetITAgentRequestIDReusedInALaterRunIsStillStored(t *testing.T) {
	rig := workbookDocumentsFixture(t)
	mustExec(t, rig.db, `INSERT INTO agents(id,workspace_id,name,slug) VALUES('agent-sit',?,'Agent Sit','agent-sit')`, rig.ws)
	first := nativeAgentOperationID(&craftNativeAgentEntry{attempt: collaborationAgentAttemptBinding{runID: "run-1"}}, "r1")
	second := nativeAgentOperationID(&craftNativeAgentEntry{attempt: collaborationAgentAttemptBinding{runID: "run-2"}}, "r1")
	if first == second {
		t.Fatalf("one operation ID %q for two runs", first)
	}
	if err := rig.sitAgentVersion(t, 1, "agent-sit", first, `[{"command":"cell.set","params":{"cell":"B8","input":"done","sheet":"L"}}]`, sitWorkbook(sitCells{"B8": "done"}, sitInitialOther())); err != nil {
		t.Fatal(err)
	}
	// The next chat, a new run, the same model-chosen request ID, another change.
	if err := rig.sitAgentVersion(t, 2, "agent-sit", second, `[{"command":"cell.set","params":{"cell":"B9","input":"done","sheet":"L"}}]`, sitWorkbook(sitCells{"B8": "done", "B9": "done"}, sitInitialOther())); err != nil || rig.sitHead(t) != 3 {
		t.Fatalf("BUG a later run's change with a reused request_id is not stored (craft_version_ops UNIQUE(page, operation_id), operation_id = request_id in craftNativeAgentWorkbookRecord): head %d, %v", rig.sitHead(t), err)
	}
}
