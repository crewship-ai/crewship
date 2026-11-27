package api

import (
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

func (s *sitScenario) undo(token, generation, operation string) *httptest.ResponseRecorder {
	s.t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/pages/sheet-own/craft/document/undo", strings.NewReader(`{"operation_id":`+strconv.Quote(operation)+`}`)).WithContext(s.human)
	r.SetPathValue("slug", "sheet-own")
	r.Header.Set("X-Craft-Session", token)
	r.Header.Set("X-Craft-Generation", generation)
	w := httptest.NewRecorder()
	s.m.documents.undoOperation(w, r)
	return w
}

func (s *sitScenario) headSheets(token string) map[string]sitCells {
	s.t.Helper()
	w := s.humanGet(token, "", s.m.documents.readSnapshot)
	if w.Code != 200 {
		s.t.Fatalf("read head: %d %s", w.Code, w.Body)
	}
	p := sitParts(s.t, workbookRead(s.t, w).Bytes)
	return map[string]sitCells{"L": sitParseSheet(string(p["xl/worksheets/sheet1.xml"])), "Other": sitParseSheet(string(p["xl/worksheets/sheet2.xml"]))}
}

// Undo C over the whole path: the agent changes three cells of sheet L, one of them over a formula;
// the person then changes one of those cells and a cell of sheet Other. Undo, asked by the person's
// editor with the operation ID, puts back the two untouched cells (the formula as a formula), keeps
// the person's cell and counts it skipped, keeps the person's change on Other, and is the person's
// new version with its own operations row. A repeat answers the same.
func TestSheetITUndoOfAnAgentChangeKeepsTheLaterHumanWork(t *testing.T) {
	s := newSheetITScenario(t)
	human := s.humanSession()
	// Version 2: L!C2 holds a formula over L!B2.
	l := sitInitialL()
	l["C2"] = "=L!B2|open"
	generationH := s.humanLease(human)
	if w := s.humanSaveWith(human, generationH, "1", sitWorkbook(l, sitInitialOther()), "sit-undo-v2", "", map[string]string{"X-Craft-Touched": `["'L'!C2"]`}); w.Code != 200 || s.head() != 2 {
		t.Fatalf("seed: %d %s", w.Code, w.Body)
	}
	if w := craftNativeLeaseCall(s.m.human, s.human, human, "sheet-own", "release", `{"generation":"`+generationH+`"}`); w.Code != 200 {
		t.Fatalf("release: %d %s", w.Code, w.Body)
	}

	// Version 3: the agent.
	s.send("Mark items 6 and 7 done and fix C2", true)
	call := s.agent()
	generation := s.acquire(call)
	applied := s.tool(call, "craft_sheet_apply", map[string]any{"generation": generation, "base_version": "2", "request_id": "undo-1", "commands": sitCommands(
		sitCmd("cell.set", map[string]any{"sheet": "L", "cell": "B7", "input": "done"}),
		sitCmd("cell.set", map[string]any{"sheet": "L", "cell": "B8", "input": "done"}),
		sitCmd("cell.set", map[string]any{"sheet": "L", "cell": "C2", "input": "fixed"}),
	)})
	var result struct {
		Version     int64  `json:"version"`
		OperationID string `json:"operation_id"`
		Undoable    bool   `json:"undoable"`
	}
	if json.Unmarshal([]byte(applied), &result) != nil || result.Version != 3 || result.OperationID == "" {
		t.Fatalf("agent apply: %s", applied)
	}
	if !result.Undoable {
		t.Errorf("agent apply is not offered as undoable: %s", applied)
	}
	s.tool(call, "craft_sheet_release", map[string]any{"generation": generation})
	if rows := s.opsRows(); len(rows) != 2 || rows[1].Inverse == "" || rows[1].Inverse == "[]" {
		t.Fatalf("the agent's row keeps no inverse: %+v", rows)
	}

	// Version 4: the person changes L!B7 (an agent cell) and Other!A2.
	sheets := s.headSheets(human)
	if sheets["L"]["C2"] != "fixed" {
		t.Fatalf("agent change not in the head: C2=%q", sheets["L"]["C2"])
	}
	sheets["L"]["B7"] = "person"
	sheets["Other"]["A2"] = "person"
	generationH = s.humanLease(human)
	if w := s.humanSave(human, generationH, "3", sitWorkbook(sheets["L"], sheets["Other"]), "sit-undo-v4", ""); w.Code != 200 || s.head() != 4 {
		t.Fatalf("the person's later save: %d %s", w.Code, w.Body)
	}

	// Undo of the agent's operation.
	w := s.undo(human, generationH, result.OperationID)
	if w.Code != 201 || !strings.Contains(w.Body.String(), `"version":"5"`) || !strings.Contains(w.Body.String(), `"skipped":1`) || s.head() != 5 {
		t.Fatalf("undo: %d %s, head %d", w.Code, w.Body, s.head())
	}
	got := s.headSheets(human)
	if got["L"]["B8"] != "open" || got["L"]["B7"] != "person" || got["Other"]["A2"] != "person" {
		t.Errorf("after undo: L!B8=%q (want open) L!B7=%q (want person) Other!A2=%q (want person)", got["L"]["B8"], got["L"]["B7"], got["Other"]["A2"])
	}
	if expr, _, ok := sitFormula(got["L"]["C2"]); !ok || expr != "L!B2" {
		t.Errorf("after undo L!C2=%q, want the formula =L!B2 back", got["L"]["C2"])
	}
	// The repeat answers the same version; nothing new is stored.
	if again := s.undo(human, generationH, result.OperationID); again.Code != 200 || !strings.Contains(again.Body.String(), `"version":"5"`) || s.head() != 5 {
		t.Fatalf("repeated undo: %d %s, head %d", again.Code, again.Body, s.head())
	}
	// Two rows: the agent's change, and the undo as the person's replayable version 5.
	rows := s.opsRows()
	if len(rows) < 2 || rows[1].Version != 3 || rows[1].Kind != "agent" || rows[1].OperationID != result.OperationID {
		t.Fatalf("craft_version_ops after undo, the agent's row: %+v", rows)
	}
	if len(rows) != 3 {
		t.Errorf("the undo version 5 has no operations row (want 3 rows): %d rows", len(rows))
	} else if u := rows[2]; u.Version != 5 || u.Kind != "human" || !u.Replayable || u.OperationID != "undo:"+result.OperationID {
		t.Errorf("the undo's row: version %d kind %q replayable %v operation_id %q; want 5 human true %q", u.Version, u.Kind, u.Replayable, u.OperationID, "undo:"+result.OperationID)
	}
	var source string
	if err := s.f.rig.db.QueryRow(`SELECT json_extract(metadata,'$.source') FROM audit_logs WHERE action='artifact.sheet_saved' ORDER BY created_at DESC, id DESC LIMIT 1`).Scan(&source); err != nil || source != "undo" {
		t.Errorf("audit source of the undo version: %q %v", source, err)
	}
	// An unknown operation is not found.
	if missing := s.undo(human, generationH, result.OperationID+"-nope"); missing.Code != 404 {
		t.Errorf("undo of an unknown operation: %d %s", missing.Code, missing.Body)
	}
}

// undo decides by the addresses the person touched, from the operations
// log, and for a person's version without operations by the cell's raw input, never by a normalized
// value. The agent typed 1 into L!B7; the person later typed the text "001" there (the same number
// once normalized): undo keeps "001" and counts it skipped. The agent's untouched L!B8 comes back;
// the answer says how many came back and how many stayed. The person retyping exactly the agent's
// input is the accepted gap (c) and is only logged.
func TestSheetITUndoByTheCellsRawInputNotItsValue(t *testing.T) {
	s := newSheetITScenario(t)
	human := s.humanSession()
	s.send("Set item 6 to 1 and mark item 7 done", true)
	call := s.agent()
	generation := s.acquire(call)
	applied := s.tool(call, "craft_sheet_apply", map[string]any{"generation": generation, "base_version": "1", "request_id": "undo-raw", "commands": sitCommands(
		sitCmd("cell.set", map[string]any{"sheet": "L", "cell": "B7", "input": "1"}),
		sitCmd("cell.set", map[string]any{"sheet": "L", "cell": "B8", "input": "done"}),
		sitCmd("cell.set", map[string]any{"sheet": "L", "cell": "B6", "input": "kept"}),
	)})
	var result struct {
		OperationID string `json:"operation_id"`
	}
	if json.Unmarshal([]byte(applied), &result) != nil || result.OperationID == "" || s.head() != 2 {
		t.Fatalf("agent apply: %s", applied)
	}
	s.tool(call, "craft_sheet_release", map[string]any{"generation": generation})
	sheets := s.headSheets(human)
	sheets["L"]["B7"] = "'001" // the person's text 001
	sheets["L"]["B6"] = "kept" // the person retyped exactly the agent's input (gap c)
	generationH := s.humanLease(human)
	if w := s.humanSave(human, generationH, "2", sitWorkbook(sheets["L"], sheets["Other"]), "sit-undo-raw", ""); w.Code != 200 || s.head() != 3 {
		t.Fatalf("the person's save: %d %s", w.Code, w.Body)
	}
	w := s.undo(human, generationH, result.OperationID)
	got := s.headSheets(human)
	if got["L"]["B7"] != "001" {
		t.Errorf("undo overwrote the person's \"001\" (the agent wrote 1): L!B7=%q, answer %d %s", got["L"]["B7"], w.Code, w.Body)
	}
	if got["L"]["B8"] != "open" {
		t.Errorf("the agent's untouched L!B8 did not come back: %q (%d %s)", got["L"]["B8"], w.Code, w.Body)
	}
	if !strings.Contains(w.Body.String(), `"skipped":`) || !strings.Contains(w.Body.String(), `"restored":`) {
		t.Errorf("the undo's answer does not say how many cells came back and how many stayed: %s", w.Body)
	}
	t.Logf("gap (c): L!B6 retyped by the person as the agent's input, after undo %q; answer %s", got["L"]["B6"], w.Body)
}

func TestSheetITAgentWriteRollsBackWhenUndoJournalCannotBeStored(t *testing.T) {
	raw, err := os.ReadFile("../../testdata/craft/sheet-realtime-scenarios.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Initial    map[string]sitCells
		Concurrent struct {
			Agent []struct {
				Sheet  string
				Range  string
				Values [][]string
			}
		}
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	if len(fixture.Concurrent.Agent) == 0 {
		t.Fatal("workbook fixture needs agent writes")
	}
	commands := make([]map[string]any, 0, len(fixture.Concurrent.Agent))
	for _, edit := range fixture.Concurrent.Agent {
		commands = append(commands, sitCmd("range.setValues", map[string]any{"sheet": edit.Sheet, "range": edit.Range, "values": edit.Values}))
	}
	s := newSheetITScenario(t, sitWorkbook(fixture.Initial["L"], fixture.Initial["Other"]))
	engineApply := s.m.config.AgentApply
	var candidates atomic.Int32
	s.m.config.AgentApply = func(ctx context.Context, p CraftNativeDocumentProfile, base []byte, commands json.RawMessage) ([]byte, error) {
		candidate, err := engineApply(ctx, p, base, commands)
		if err == nil {
			candidates.Add(1)
		}
		return candidate, err
	}
	s.send("Update the status cells", true)
	call := s.agent()
	generation := s.acquire(call)
	// The editor is closed. Fail the mandatory inverse insert after the
	// engine and audit callbacks have run, inside the publication transaction.
	mustExec(t, s.f.rig.db, `CREATE TRIGGER sit_reject_agent_undo_journal BEFORE INSERT ON craft_version_ops
			WHEN NEW.actor_kind='agent' BEGIN SELECT RAISE(ABORT,'test inverse insert failure'); END`)
	applied, refused := s.toolCall(call, "craft_sheet_apply", map[string]any{"generation": generation, "base_version": "1", "request_id": "sit-inverse-insert", "commands": sitCommands(commands...)})
	if candidates.Load() != 1 || !refused || s.head() != 1 || len(s.opsRows()) != 0 {
		t.Fatalf("failed inverse insert: candidates=%d refused=%v head=%d ops=%d response=%s", candidates.Load(), refused, s.head(), len(s.opsRows()), applied)
	}
	var audit int
	if err := s.f.rig.db.QueryRow(`SELECT count(*) FROM audit_logs WHERE action='artifact.sheet_saved' AND json_extract(metadata,'$.source')='agent'`).Scan(&audit); err != nil || audit != 0 {
		t.Fatalf("failed inverse insert left %d save audit rows: %v", audit, err)
	}
	got := s.headSheets(s.humanSession())
	for sheet, cells := range fixture.Initial {
		if !maps.Equal(got[sheet], cells) {
			t.Errorf("persisted %s = %v, want %v", sheet, got[sheet], cells)
		}
	}
}
