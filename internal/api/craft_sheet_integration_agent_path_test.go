package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/craftsession"
	"github.com/crewship-ai/crewship/internal/mooring"
)

// humanLease takes the edit lease in the person's session and returns its generation.
func (s *sitScenario) humanLease(token string) string {
	s.t.Helper()
	w := craftNativeLeaseCall(s.m.human, s.human, token, "sheet-own", "acquire", `{}`)
	var lease map[string]any
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &lease) != nil {
		s.t.Fatalf("human acquire: %d %s", w.Code, w.Body)
	}
	for _, key := range []string{"generation", "lease_generation"} {
		switch g := lease[key].(type) {
		case string:
			return g
		case float64:
			return fmt.Sprint(int64(g))
		}
	}
	if inner, ok := lease["lease"].(map[string]any); ok {
		switch g := inner["generation"].(type) {
		case string:
			return g
		case float64:
			return fmt.Sprint(int64(g))
		}
	}
	s.t.Fatalf("human acquire without a generation: %s", w.Body)
	return ""
}

// humanSave is the editor's save of a workbook Sheet: the file as the raw body, the binding in
// X-Craft-* headers.
func (s *sitScenario) humanSave(token, generation, base string, body []byte, attempt, rebase string) *httptest.ResponseRecorder {
	s.t.Helper()
	return s.humanSaveWith(token, generation, base, body, attempt, rebase, nil)
}

// humanSaveWith is humanSave with more request headers (X-Craft-Touched).
func (s *sitScenario) humanSaveWith(token, generation, base string, body []byte, attempt, rebase string, headers map[string]string) *httptest.ResponseRecorder {
	s.t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/pages/sheet-own/craft/document/save", bytes.NewReader(body)).WithContext(s.human)
	r.SetPathValue("slug", "sheet-own")
	r.Header.Set("Content-Type", mooring.WorkbookMediaTypeXLSM)
	r.Header.Set("X-Craft-Session", token)
	r.Header.Set("X-Craft-Generation", generation)
	r.Header.Set("X-Craft-Base-Version", base)
	r.Header.Set("X-Craft-Editor-Revision", "sit-doc:"+base)
	r.Header.Set("X-Craft-Engine-Pin", mooring.NewNativeStore(nil, s.store).EnginePin())
	r.Header.Set("X-Craft-Content-Hash", workbookHash(body))
	if attempt != "" {
		r.Header.Set("X-Craft-Attempt", attempt)
	}
	if rebase != "" {
		r.Header.Set("X-Craft-Rebase", rebase)
	}
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	r.ContentLength = int64(len(body))
	w := httptest.NewRecorder()
	s.m.documents.saveSnapshot(w, r)
	return w
}

type sitOperationsAnswer struct {
	From            string `json:"from"`
	To              string `json:"to"`
	Head            string `json:"head"`
	HeadContentHash string `json:"head_content_hash"`
	Replayable      bool   `json:"replayable"`
	Versions        []struct {
		Version     string          `json:"version"`
		BaseVersion string          `json:"base_version"`
		ActorKind   string          `json:"actor_kind"`
		ActorName   string          `json:"actor_name"`
		OperationID string          `json:"operation_id"`
		EnginePin   string          `json:"engine_pin"`
		Calculate   string          `json:"calculate"`
		Replayable  bool            `json:"replayable"`
		Ops         json.RawMessage `json:"ops"`
	} `json:"versions"`
}

func (s *sitScenario) operations(token, from string) (int, sitOperationsAnswer, string) {
	s.t.Helper()
	w := s.humanGet(token, "/operations?from="+from, s.m.documents.listOperations)
	var a sitOperationsAnswer
	_ = json.Unmarshal(w.Body.Bytes(), &a)
	return w.Code, a, w.Body.String()
}

// The whole agent path over a workbook Sheet: a message with the Sheet attached, the agent reads
// it (document.inspect, cell.get, columnByHeader), changes it in one apply (edit.clearContents,
// cell.set, range.setValues) with a fenced save, retries the lost answer, and the person sees the
// new version, its craft_version_ops row and the operations listing from their base.
func TestSheetITAgentWorkbookPathFromMessageToOperations(t *testing.T) {
	s := newSheetITScenario(t)
	s.send("Mark item 7 done, clear item 8 and add item 9", true)

	human := s.humanSession()
	before := s.humanGet(human, "", s.m.documents.readSnapshot)
	if before.Code != 200 || !bytes.Equal(before.Body.Bytes(), s.v1) || before.Header().Get("X-Craft-Version") != "1" || before.Header().Get("ETag") != `"`+workbookHash(s.v1)+`"` {
		t.Fatalf("the person's workbook before the change: %d %v", before.Code, before.Header())
	}

	call := s.agent()
	// Reads. Each is reported on its own so one gap does not hide the rest of the path.
	if text, refused := s.read(call, map[string]any{"command": "document.inspect", "params": map[string]any{}}); refused || !strings.Contains(text, `"name":"L"`) {
		t.Errorf("GAP/BUG document.inspect of a workbook Sheet refused (production open check parses the xlsx as native JSON, craft_native_sheet_session.go Inspect→checkOpened): %s", text)
	}
	if text, refused := s.read(call, map[string]any{"command": "cell.get", "params": map[string]any{"sheet": "L", "cell": "B1"}}); refused || !strings.Contains(text, `"value":"Status"`) {
		t.Errorf("GAP/BUG cell.get of a workbook Sheet refused: %s", text)
	}
	if text, refused := s.read(call, map[string]any{"command": "range.columnByHeader", "params": map[string]any{"sheet": "L", "header": "Status"}}); refused || !strings.Contains(text, `"headerCell":"B1"`) || !strings.Contains(text, `"range":"B2:B9"`) {
		t.Errorf("range.columnByHeader of a workbook Sheet: %s", text)
	}

	generation := s.acquire(call)
	commands := sitCommands(
		sitCmd("range.clear", map[string]any{"sheet": "L", "range": "B8:B9"}),
		sitCmd("cell.set", map[string]any{"sheet": "L", "cell": "B8", "input": "done"}),
		sitCmd("range.setValues", map[string]any{"sheet": "L", "range": "A10:B10", "values": []any{[]any{"Item 9", "new"}}}),
	)
	apply := map[string]any{"generation": generation, "base_version": "1", "request_id": "sit-op-1", "commands": commands}
	applied := s.tool(call, "craft_sheet_apply", apply)
	var result craftNativeChatApplyResult
	if json.Unmarshal([]byte(applied), &result) != nil || result.PageID != "sheet-own" || result.BaseVersion != 1 || result.Version != 2 {
		t.Fatalf("apply result: %s", applied)
	}
	// The card offers Undo only when the server kept what the change replaced.
	if !strings.Contains(applied, `"undoable":true`) {
		t.Errorf("apply result is not undoable: %s", applied)
	}
	v2 := s.stored(2)
	if result.ContentHash != workbookHash(v2) {
		t.Fatalf("content_hash %s is not the stored version's %s", result.ContentHash, workbookHash(v2))
	}
	// The answer was lost: the exact same request again is the same version, the engine is not rerun.
	_, appliesBefore := s.wb.count()
	if again := s.tool(call, "craft_sheet_apply", apply); again != applied || s.head() != 2 {
		t.Fatalf("an exact retry changed something: version %d\n%s\n%s", s.head(), again, applied)
	}
	if _, appliesAfter := s.wb.count(); appliesAfter != appliesBefore {
		t.Fatalf("the retry ran the engine again: %d → %d", appliesBefore, appliesAfter)
	}
	s.tool(call, "craft_sheet_release", map[string]any{"generation": generation})

	// The stored file: the cells changed as asked, every part it did not touch byte for byte.
	p1, p2 := sitParts(t, s.v1), sitParts(t, v2)
	cells := sitParseSheet(string(p2["xl/worksheets/sheet1.xml"]))
	if cells["B8"] != "done" || cells["B9"] != "" || cells["A10"] != "Item 9" || cells["B10"] != "new" || cells["B7"] != "open" {
		t.Fatalf("sheet L after the change: %v", cells)
	}
	for name, b := range p1 {
		if name == "xl/worksheets/sheet1.xml" || name == "xl/pivotCache/pivotCacheDefinition1.xml" {
			continue
		}
		if got, ok := p2[name]; !ok || sitSHA(got) != sitSHA(b) {
			t.Errorf("part %s changed or lost by the agent version (present %v)", name, ok)
		}
	}

	// craft_version_ops: the editor's execute names, the sheet in every address, the request ID.
	rows := s.opsRows()
	if len(rows) != 1 {
		t.Fatalf("craft_version_ops rows: %+v", rows)
	}
	row := rows[0]
	wantOps := `[{"command":"edit.clearContents","params":{"range":"'L'!B8:B9"}},{"command":"cell.set","params":{"cell":"'L'!B8","input":"done"}},{"command":"range.setValues","params":{"range":"'L'!A10:B10","values":[["Item 9","new"]]}}]`
	if row.Version != 2 || row.Base != 1 || row.Kind != "agent" || row.ActorID != s.f.a.AgentID || row.Name == "" || row.Name != s.agentN || !strings.HasSuffix(row.OperationID, ":sit-op-1") || row.OperationID == ":sit-op-1" || row.Pin != mooring.NewNativeStore(nil, s.store).EnginePin() || !row.Replayable || row.Ops != wantOps {
		t.Fatalf("ops row %+v\nwant ops %s", row, wantOps)
	}
	// Undo needs the inverse of what was applied: B8:B9 back to "open", A10:B10 cleared.
	if row.Inverse == "[]" || !strings.Contains(row.Inverse, "'L'!B8") || !strings.Contains(row.Inverse, "open") || !strings.Contains(row.Inverse, "'L'!A10") {
		t.Errorf("GAP the agent version's inverse is not recorded (craftNativeAgentWorkbookRecord never sets Inverse): %s", row.Inverse)
	}

	// GET operations from the person's base.
	code, a, raw := s.operations(human, "1")
	if code != 200 || a.From != "1" || a.To != "2" || a.Head != "2" || a.HeadContentHash != workbookHash(v2) || !a.Replayable || len(a.Versions) != 1 {
		t.Fatalf("operations from 1: %d %s", code, raw)
	}
	if v := a.Versions[0]; v.Version != "2" || v.BaseVersion != "1" || v.ActorKind != "agent" || v.ActorName != s.agentN || v.OperationID != row.OperationID || !v.Replayable || string(v.Ops) != wantOps || v.Calculate != "calculated" {
		t.Fatalf("operations version %+v", v)
	}
	for _, bad := range []string{"", "0", "-1", "x", "1.5", "3", "99999999999999999999"} {
		code, _, raw = s.operations(human, bad)
		if code != 400 || !strings.Contains(raw, `"code":"from_invalid"`) {
			t.Errorf("from=%q: %d %s", bad, code, raw)
		}
	}
	// Nothing newer than the head: an empty, replayable answer.
	if code, a, raw = s.operations(human, "2"); code != 200 || !a.Replayable || len(a.Versions) != 0 || a.Head != "2" {
		t.Errorf("from the head: %d %s", code, raw)
	}

	// The person saves over the agent's version: a human version has no operations, so from 1 the
	// chain is no longer replayable (AND over every version), from 2 neither.
	generationH := s.humanLease(human)
	cells["C1"] = "checked by the person"
	mine := sitWorkbook(cells, sitInitialOther())
	if w := s.humanSave(human, generationH, "2", mine, "sit-human-1", ""); w.Code != 200 || s.head() != 3 {
		t.Fatalf("person's save over the agent's version: %d %s, head %d", w.Code, w.Body, s.head())
	}
	code, a, raw = s.operations(human, "1")
	if code != 200 || a.Replayable || len(a.Versions) != 2 || !a.Versions[0].Replayable || a.Versions[1].Replayable || a.Versions[1].ActorKind != "human" || a.Versions[1].Ops != nil || a.HeadContentHash != workbookHash(mine) {
		t.Fatalf("operations over a human version: %d %s", code, raw)
	}
}

// An agent write that does not name its sheet is refused as invalid with the instruction to name
// it, through the whole path: nothing is opened, nothing is stored, no operations row.
func TestSheetITAgentWriteWithoutItsSheetIsInvalid(t *testing.T) {
	s := newSheetITScenario(t)
	s.send("Set B8 to done", true)
	call := s.agent()
	generation := s.acquire(call)
	for i, command := range []map[string]any{
		sitCmd("cell.set", map[string]any{"cell": "B8", "input": "done"}),
		sitCmd("range.clear", map[string]any{"range": "B8:B9"}),
		sitCmd("range.setValues", map[string]any{"range": "A10:B10", "values": []any{[]any{"x", "y"}}}),
	} {
		text, refused := s.toolCall(call, "craft_sheet_apply", map[string]any{"generation": generation, "base_version": "1", "request_id": fmt.Sprintf("unnamed-%d", i), "commands": []any{command}})
		if !refused || !strings.Contains(text, "invalid") || !strings.Contains(text, "name the sheet") {
			t.Errorf("unnamed %v: the agent is not told to name the sheet (chat-run replyChatSheetRefusal replaces nativeAgentChangeRefused's instruction): refused=%v %s", command["command"], refused, text)
		}
	}
	if opens, applies := s.wb.count(); applies != 0 || s.head() != 1 || len(s.opsRows()) != 0 {
		t.Fatalf("an unnamed write reached the engine (%d opens, %d applies) or stored: head %d", opens, applies, s.head())
	}
}

// The engine reports that saving the change would lose parts of the workbook: the agent is told
// fidelity_loss, nothing is stored and no operations row is written.
func TestSheetITAgentChangeWithAFidelityLossStoresNothing(t *testing.T) {
	s := newSheetITScenario(t)
	s.wb.loss = &craftsession.FidelityLossError{Parts: []craftsession.FidelityPart{{Code: "pivot_fields", Count: 2}}}
	s.send("Mark item 7 done", true)
	call := s.agent()
	generation := s.acquire(call)
	text, refused := s.toolCall(call, "craft_sheet_apply", map[string]any{"generation": generation, "base_version": "1", "request_id": "sit-loss", "commands": sitCommands(sitCmd("cell.set", map[string]any{"sheet": "L", "cell": "B8", "input": "done"}))})
	if !refused || !strings.Contains(text, "fidelity_loss") {
		t.Errorf("a fidelity loss reached the agent as: refused=%v %s", refused, text)
	}
	if s.head() != 1 || len(s.opsRows()) != 0 {
		t.Fatalf("a lossy change was stored: head %d, ops %+v", s.head(), s.opsRows())
	}
}

// The person saves while the agent works from version 1: the agent's apply over the old base is
// refused (the consent no longer covers what would change) and nothing is stored over the person's
// version.
func TestSheetITAgentChangeOverAStaleBaseStoresNothing(t *testing.T) {
	s := newSheetITScenario(t)
	s.send("Mark item 7 done", true)
	human := s.humanSession()
	generationH := s.humanLease(human)
	mine := sitWorkbook(sitCells{"A1": "Name", "B1": "Status", "C1": "person"}, sitInitialOther())
	if w := s.humanSave(human, generationH, "1", mine, "sit-person-first", ""); w.Code != 200 || s.head() != 2 {
		t.Fatalf("person's save: %d %s", w.Code, w.Body)
	}
	if w := craftNativeLeaseCall(s.m.human, s.human, human, "sheet-own", "release", `{"generation":"`+generationH+`"}`); w.Code != 200 {
		t.Fatalf("release: %d %s", w.Code, w.Body)
	}
	call := s.agent()
	generation := s.acquire(call)
	text, refused := s.toolCall(call, "craft_sheet_apply", map[string]any{"generation": generation, "base_version": "1", "request_id": "sit-stale", "commands": sitCommands(sitCmd("cell.set", map[string]any{"sheet": "L", "cell": "B8", "input": "done"}))})
	if !refused || !(strings.Contains(text, "stale") || strings.Contains(text, "consent")) {
		t.Errorf("an apply over a stale base: refused=%v %s", refused, text)
	}
	if s.head() != 2 || !bytes.Equal(s.stored(2), mine) || len(s.opsRows()) != 0 {
		t.Fatalf("the person's version was overwritten: head %d", s.head())
	}
}
