package api

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// sitReplayOps is the editor's replay of the operations log over its own state: the commands exactly
// as GET …/operations lists them (edit.clearContents, cell.set, range.setValues, each address
// naming its sheet). Anything else fails the test, so a listing the editor could not replay shows.
func sitReplayOps(t *testing.T, sheets map[string]sitCells, ops json.RawMessage) {
	t.Helper()
	var list []struct {
		Command string `json:"command"`
		Params  struct {
			Cell   string  `json:"cell"`
			Range  string  `json:"range"`
			Input  string  `json:"input"`
			Values [][]any `json:"values"`
		} `json:"params"`
	}
	if err := json.Unmarshal(ops, &list); err != nil || len(list) == 0 {
		t.Fatalf("operations the editor cannot read: %s", ops)
	}
	s := &sitSession{cells: sheets}
	for _, op := range list {
		var err error
		switch op.Command {
		case "cell.set":
			err = s.SetCell(t.Context(), op.Params.Cell, op.Params.Input)
		case "range.setValues":
			_, err = s.WriteRange(t.Context(), op.Params.Range, op.Params.Values)
		case "edit.clearContents":
			err = s.ClearRange(t.Context(), op.Params.Range)
		default:
			t.Fatalf("operation the editor does not execute: %s", op.Command)
		}
		if err != nil {
			t.Fatalf("replay %s: %v", op.Command, err)
		}
	}
}

// Acceptance (two sheets, a lost confirmation). The person types on sheet Other without saving;
// the agent saves a change on sheet L; the agent's answer is lost and it sends the same request
// again, once right away and once after the person's save. The person's editor is refused as moved,
// replays the agent's operations over its unsaved work and saves. Reopened, the Sheet holds both
// changes, craft_version_ops and GET operations hold exactly one agent operation, and the exported
// xlsx has both cells and the formula over them recalculated.
//
// The formula Other!C1 = L!B8&"+"&Other!A2 is calculated by the stand-ins of the engine (the agent's
// save) and the editor (the person's save); that the real engine and editor calculate it is the
// browser test's. This test proves what the server keeps, lists and exports.
func TestSheetITAcceptanceTwoSheetsAndALostConfirmation(t *testing.T) {
	const formula = `L!B8&"+"&Other!A2`
	initialOther := sitInitialOther()
	initialOther["C1"] = "=" + formula + "|open+X1"
	s := newSheetITScenario(t)
	v1 := sitWorkbook(sitInitialL(), initialOther)
	human := s.humanSession()
	generationH := s.humanLease(human)
	if w := s.humanSave(human, generationH, "1", v1, "sit-acc-v1", ""); w.Code != 200 || s.head() != 2 {
		t.Fatalf("seed: %d %s", w.Code, w.Body)
	}
	if w := craftNativeLeaseCall(s.m.human, s.human, human, "sheet-own", "release", `{"generation":"`+generationH+`"}`); w.Code != 200 {
		t.Fatalf("release: %d %s", w.Code, w.Body)
	}
	base := "2"

	// The person's editor opens version 2 and types on sheet Other, unsaved.
	opened := s.humanGet(human, "", s.m.documents.readSnapshot)
	if opened.Code != 200 || !bytes.Equal(workbookRead(t, opened).Bytes, v1) || workbookRead(t, opened).Version != base {
		t.Fatalf("open: %d %v", opened.Code, opened.Header())
	}
	parts := sitParts(t, workbookRead(t, opened).Bytes)
	editor := map[string]sitCells{"L": sitParseSheet(string(parts["xl/worksheets/sheet1.xml"])), "Other": sitParseSheet(string(parts["xl/worksheets/sheet2.xml"]))}
	editor["Other"]["A2"] = "person"

	// The agent saves a change on sheet L.
	s.send("Mark item 7 done", true)
	call := s.agent()
	generation := s.acquire(call)
	apply := map[string]any{"generation": generation, "base_version": base, "request_id": "acc-1", "commands": sitCommands(sitCmd("cell.set", map[string]any{"sheet": "L", "cell": "B8", "input": "done"}))}
	first := s.tool(call, "craft_sheet_apply", apply)
	var result craftNativeChatApplyResult
	if json.Unmarshal([]byte(first), &result) != nil || result.Version != 3 {
		t.Fatalf("agent apply: %s", first)
	}
	// The confirmation is lost: the same request again, right away.
	if again := s.tool(call, "craft_sheet_apply", apply); again != first || s.head() != 3 {
		t.Fatalf("retry right away: head %d\n%s\n%s", s.head(), again, first)
	}
	s.tool(call, "craft_sheet_release", map[string]any{"generation": generation})

	// The person saves the unsaved work over base 2: moved, with where the operations are.
	stale := sitWorkbook(editor["L"], editor["Other"])
	generationH = s.humanLease(human)
	w := s.humanSave(human, generationH, base, stale, "sit-acc-person-1", "")
	if w.Code != 409 || !strings.Contains(w.Body.String(), `"code":"moved"`) || !strings.Contains(w.Body.String(), `"head":"3"`) || !strings.Contains(w.Body.String(), "operations?from="+base) {
		t.Fatalf("the person's save over the old base: %d %s", w.Code, w.Body)
	}
	code, ops, raw := s.operations(human, base)
	if code != 200 || !ops.Replayable || len(ops.Versions) != 1 || ops.Versions[0].ActorKind != "agent" || ops.Head != "3" || ops.HeadContentHash != workbookHash(s.stored(3)) {
		t.Fatalf("operations from %s: %d %s", base, code, raw)
	}
	// The editor replays them over its unsaved work, recalculates and saves on top of the head.
	sitReplayOps(t, editor, ops.Versions[0].Ops)
	sitRecalc(editor)
	merged := sitWorkbook(editor["L"], editor["Other"])
	if w = s.humanSave(human, generationH, "3", merged, "sit-acc-person-2", ""); w.Code != 200 || s.head() != 4 {
		t.Fatalf("the person's replayed save: %d %s, head %d", w.Code, w.Body, s.head())
	}
	if w := craftNativeLeaseCall(s.m.human, s.human, human, "sheet-own", "release", `{"generation":"`+generationH+`"}`); w.Code != 200 {
		t.Fatalf("release: %d %s", w.Code, w.Body)
	}

	// The same agent request once more, after the person's save: still the agent's version 3, never a
	// fourth version over the person's work.
	generation = s.acquire(call)
	apply["generation"] = generation
	late, refused := s.toolCall(call, "craft_sheet_apply", apply)
	if s.head() != 4 {
		t.Fatalf("a late retry stored another version: head %d (%v %s)", s.head(), refused, late)
	}
	if !refused && !strings.Contains(late, `"version":3`) {
		t.Errorf("a late retry answered something other than the agent's version 3: %s", late)
	}
	s.tool(call, "craft_sheet_release", map[string]any{"generation": generation})

	// Reopened: both changes, the formula over both.
	reopened := s.humanGet(human, "", s.m.documents.readSnapshot)
	got := sitParts(t, workbookRead(t, reopened).Bytes)
	l, other := sitParseSheet(string(got["xl/worksheets/sheet1.xml"])), sitParseSheet(string(got["xl/worksheets/sheet2.xml"]))
	if reopened.Code != 200 || l["B8"] != "done" || other["A2"] != "person" || other["C1"] != "="+formula+"|done+person" {
		t.Fatalf("reopened: %d L!B8=%q Other!A2=%q Other!C1=%q", reopened.Code, l["B8"], other["A2"], other["C1"])
	}
	// Exactly one agent operation, in the table and in the listing.
	rows := s.opsRows()
	if len(rows) != 1 || rows[0].Version != 3 || !strings.HasSuffix(rows[0].OperationID, ":acc-1") {
		t.Fatalf("craft_version_ops: %+v", rows)
	}
	code, ops, raw = s.operations(human, "1")
	agents := 0
	for _, v := range ops.Versions {
		if v.ActorKind == "agent" {
			agents++
		}
	}
	if code != 200 || agents != 1 || ops.Head != "4" || len(ops.Versions) != 3 {
		t.Fatalf("operations from 1: %d %s", code, raw)
	}
	// The exported xlsx is the head: both cells and the recalculated formula.
	export := s.humanGet(human, "/export.xlsx", s.m.documents.exportXLSX)
	if export.Code != 200 || !bytes.Equal(export.Body.Bytes(), workbookRead(t, reopened).Bytes) {
		t.Fatalf("export: %d, equals the head %v", export.Code, bytes.Equal(export.Body.Bytes(), workbookRead(t, reopened).Bytes))
	}
	ex := sitParts(t, export.Body.Bytes())
	if el, eo := sitParseSheet(string(ex["xl/worksheets/sheet1.xml"])), sitParseSheet(string(ex["xl/worksheets/sheet2.xml"])); el["B8"] != "done" || eo["A2"] != "person" || eo["C1"] != "="+formula+"|done+person" {
		t.Fatalf("exported cells: L!B8=%q Other!A2=%q Other!C1=%q", el["B8"], eo["A2"], eo["C1"])
	}
}
