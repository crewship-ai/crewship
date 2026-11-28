//go:build linux && real_gridcraft_engine

package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/mooring"
)

// The ordinary realtime tests use a synthetic engine. This test drives the
// same HTTP and chat-run entry points with the packaged WASM adapter and reopens
// the committed XLSX in a cold engine session.
func TestSheetITRealtimeWithTheRealEngine(t *testing.T) {
	engine := newRealSheetEngine(t)
	adapter := NewCraftNativeSheetAgentAdapter(engine)
	for _, order := range []string{"agent_first", "human_first"} {
		t.Run(order, func(t *testing.T) {
			base := realSheetTestWorkbook(t, sitCells{"B2": "open B2", "B3": "open B3", "B4": "open B4"})
			submitted := realSheetTestWorkbook(t, sitCells{"B2": "human B2", "B3": "human B3", "B4": "open B4"})
			s := newSheetITScenarioWithWorkbook(t, base, "workbook.xlsx", mooring.WorkbookMediaTypeXLSX)
			s.m.config.AgentApply, s.m.config.AgentInspect = adapter.Apply, adapter.Inspect
			s.send("Update B3 and B4", true)
			call := s.agent()
			generation := s.acquire(call)
			editor := s.realtimeAdmission()
			human := func() {
				w := s.humanSaveWith(editor.SessionToken, editor.Generation, "1", submitted, "real-sheet-human", "", map[string]string{"X-Craft-Touched": `["'L'!B2:B3"]`})
				if w.Code != 200 {
					t.Fatalf("human save: %d %s", w.Code, w.Body)
				}
				if order == "agent_first" {
					var merged craftRealtimeMerged
					if err := json.Unmarshal(w.Body.Bytes(), &merged); err != nil || merged.Kind != "merged" ||
						len(merged.Overlap) != 1 || merged.Overlap[0] != (craftSheetRange{Sheet: "L", A1: "B3"}) ||
						!bytes.Equal(merged.Bytes, s.stored(3)) {
						t.Fatalf("merged response: %+v (%v)", merged.Receipt, err)
					}
				}
			}
			var operation string
			agent := func() {
				text, refused := s.toolCall(call, "craft_sheet_apply", map[string]any{
					"generation": generation, "base_version": "1", "request_id": "real-sheet-agent",
					"commands": sitCommands(sitCmd("range.setValues", map[string]any{"sheet": "L", "range": "B3:B4", "values": [][]string{{"agent B3"}, {"agent B4"}}})),
				})
				var receipt struct {
					ID       string `json:"operation_id"`
					Undoable bool   `json:"undoable"`
				}
				if refused || json.Unmarshal([]byte(text), &receipt) != nil || receipt.ID == "" || !receipt.Undoable {
					t.Fatalf("agent apply: %s", text)
				}
				operation = receipt.ID
			}
			if order == "agent_first" {
				agent()
				human()
			} else {
				human()
				agent()
			}
			read := func(version int64, wanted []any) {
				t.Helper()
				stored := s.stored(version)
				warm, release, err := engine.Open(t.Context(), stored)
				if err != nil {
					t.Fatal(err)
				}
				engine.Discard(warm)
				release()
				cold, release, err := engine.Open(t.Context(), stored)
				if err != nil {
					t.Fatal(err)
				}
				defer release()
				got, err := cold.ReadRange(t.Context(), "'L'!B2:B4", false)
				if err != nil || len(got.Values) != len(wanted) {
					t.Fatalf("reopen version %d: %v %v", version, got.Values, err)
				}
				for i, row := range got.Values {
					if len(row) != 1 || !craftUndoSameInput(row[0], wanted[i]) {
						t.Fatalf("reopened v%d values = %v, want %v", version, got.Values, wanted)
					}
				}
			}
			read(3, []any{"human B2", "agent B3", "agent B4"})
			w := s.undo(editor.SessionToken, editor.Generation, operation)
			if w.Code != 201 || !strings.Contains(w.Body.String(), `"restored":2`) {
				t.Fatalf("undo real merge: %d %s", w.Code, w.Body)
			}
			restored := "open B3"
			if order == "human_first" {
				restored = "human B3"
			}
			read(4, []any{"human B2", restored, "open B4"})

			// A stale capture can cross the actual workbook's Undo version.
			// The adapter may rewrite the workbook, so prove the cell inputs
			// from the cold persisted bytes rather than from warm engine state.
			stale := realSheetTestWorkbook(t, sitCells{"B2": "open B2", "B3": "open B3", "B4": "open B4", "C2": "human C2"})
			w = s.humanSaveWith(editor.SessionToken, editor.Generation, "1", stale, "real-sheet-save-after-undo", "", map[string]string{"X-Craft-Touched": `["'L'!C2"]`})
			var afterUndo craftRealtimeMerged
			wantedHeadRange := "B2"
			if order == "human_first" {
				wantedHeadRange = "B2:B3"
			}
			if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &afterUndo) != nil ||
				len(afterUndo.AgentRanges) != 0 || len(afterUndo.Overlap) != 0 ||
				len(afterUndo.HeadRanges) != 1 || afterUndo.HeadRanges[0] != (craftSheetRange{Sheet: "L", A1: wantedHeadRange}) ||
				!bytes.Equal(afterUndo.Bytes, s.stored(5)) {
				t.Fatalf("real merge over Undo: HTTP %d, effective agent %v, head %v", w.Code, afterUndo.AgentRanges, afterUndo.HeadRanges)
			}
		})
	}
}

func TestSheetITUnqualifiedChatWorkbookWithTheRealEngine(t *testing.T) {
	engine := newRealSheetEngine(t)
	adapter := NewCraftNativeSheetAgentAdapter(engine)
	workbook := func(cells sitCells) []byte { return realSheetTestWorkbook(t, cells) }
	base := workbook(sitInitialL())
	s := newSheetITScenarioWithWorkbook(t, base, "workbook.xlsx", mooring.WorkbookMediaTypeXLSX)
	s.m.config.AgentApply, s.m.config.AgentInspect = adapter.Apply, adapter.Inspect
	testUnqualifiedChatWorkbook(t, s, workbook)
}

func TestSheetITChatToolScalarContractWithTheRealEngine(t *testing.T) {
	engine := newRealSheetEngine(t)
	adapter := NewCraftNativeSheetAgentAdapter(engine)
	profile := CraftNativeDocumentProfile{Application: "gridcraft"}
	base := realSheetTestNamedWorkbook(t, "Data", sitCells{"B2": "number before", "B3": "boolean before", "B4": "null before", "B5": "string before", "B6": "literal before"})
	s := newSheetITScenarioWithWorkbook(t, base, "workbook.xlsx", mooring.WorkbookMediaTypeXLSX)
	s.m.config.AgentApply, s.m.config.AgentInspect = adapter.Apply, adapter.Inspect
	s.send("Update cells in Data", true)
	call := s.agent()
	generation := s.acquire(call)

	for _, params := range []map[string]any{
		{"range": "Data!A1:D5"},
		{"range": "Data!B2", "sheet": "Data"},
	} {
		text, refused := s.read(call, sitCmd("sheet.read", params))
		if !refused || !strings.Contains(text, `"reason":"invalid_request"`) || !strings.Contains(text, craftSheetInvalidReadQuery) {
			t.Fatalf("qualified read: refused=%v response=%s", refused, text)
		}
	}
	text, refused := s.read(call, sitCmd("sheet.read", map[string]any{"range": "A1:D5", "sheet": "Data"}))
	if refused || !strings.Contains(text, "number before") || s.head() != 1 || len(s.opsRows()) != 0 {
		t.Fatalf("canonical read: refused=%v response=%s head=%d", refused, text, s.head())
	}

	apply := func(base, id string, commands ...map[string]any) map[string]any {
		return map[string]any{"generation": generation, "base_version": base, "request_id": id, "commands": sitCommands(commands...)}
	}
	// The first failed dev1 command and request ID, using this test's live lease and base.
	numeric := apply("1", "set-data-b2-to-42-20261128", sitCmd("cell.set", map[string]any{"cell": "B2", "input": 42, "sheet": "Data"}))
	for _, invalid := range []struct {
		name   string
		params map[string]any
		reason string
	}{
		{"object", map[string]any{"cell": "B2", "input": map[string]any{"number": 42}, "sheet": "Data"}, CraftSheetReasonInvalid},
		{"missing", map[string]any{"cell": "B2", "sheet": "Data"}, CraftSheetReasonInvalid},
		{"formula", map[string]any{"cell": "B2", "input": "=1+1", "sheet": "Data"}, CraftSheetReasonFormula},
	} {
		args := apply("1", "refuse-"+invalid.name, sitCmd("cell.set", invalid.params))
		text, refused := s.toolCall(call, "craft_sheet_apply", args)
		if !refused || !strings.Contains(text, `"reason":"`+invalid.reason+`"`) || s.head() != 1 || len(s.opsRows()) != 0 {
			t.Fatalf("invalid write: refused=%v response=%s head=%d", refused, text, s.head())
		}
	}
	receipt := s.tool(call, "craft_sheet_apply", numeric)
	retry := s.tool(call, "craft_sheet_apply", numeric)
	if receipt != retry || s.head() != 2 || len(s.opsRows()) != 1 {
		t.Fatalf("retry changed the receipt or head: %s / %s, head %d", receipt, retry, s.head())
	}
	more := apply("2", "typed-boolean-null-and-text",
		sitCmd("cell.set", map[string]any{"cell": "B3", "input": true, "sheet": "Data"}),
		sitCmd("cell.set", map[string]any{"cell": "B4", "input": nil, "sheet": "Data"}),
		sitCmd("cell.set", map[string]any{"cell": "B5", "input": "42", "sheet": "Data"}),
		sitCmd("cell.set", map[string]any{"cell": "B6", "input": "'42", "sheet": "Data"}))
	second := s.tool(call, "craft_sheet_apply", more)
	operation := func(text string) string {
		t.Helper()
		var result craftNativeChatApplyResult
		if err := json.Unmarshal([]byte(text), &result); err != nil || !result.Undoable || result.OperationID == "" {
			t.Fatalf("undoable receipt: %s (%v)", text, err)
		}
		return result.OperationID
	}

	check := func(workbook []byte, wanted []any) {
		t.Helper()
		warm, release, err := engine.Open(t.Context(), workbook)
		if err != nil {
			t.Fatal(err)
		}
		engine.Discard(warm)
		release()
		cold, release, err := engine.Open(t.Context(), workbook)
		if err != nil {
			t.Fatal(err)
		}
		defer release()
		values, err := cold.ReadRange(t.Context(), "'Data'!B2:B6", false)
		if err != nil || len(values.Values) != len(wanted) {
			t.Fatalf("read cold workbook: %+v (%v)", values, err)
		}
		for i, row := range values.Values {
			if len(row) != 1 || !reflect.DeepEqual(row[0], wanted[i]) {
				t.Fatalf("row %d: got %#v, want %#v", i+2, row, wanted[i])
			}
		}
	}
	// Existing cell entry interprets "42" as a number and "'42" as text.
	wanted := []any{float64(42), true, nil, float64(42), "42"}
	check(s.stored(3), wanted)
	rows := s.opsRows()
	if len(rows) != 2 || !rows[0].Replayable || !rows[1].Replayable ||
		!strings.Contains(rows[0].Ops, `"values":[[42]]`) ||
		!strings.Contains(rows[1].Ops, `"values":[[true]]`) ||
		!strings.Contains(rows[1].Ops, `"values":[[null]]`) ||
		!strings.Contains(rows[1].Ops, `"input":"42"`) ||
		!strings.Contains(rows[1].Ops, `"input":"'42"`) {
		t.Fatalf("typed replay journal: %+v", rows)
	}
	replayed := base
	for _, row := range rows {
		var err error
		replayed, err = adapter.Apply(t.Context(), profile, replayed, json.RawMessage(row.Ops))
		if err != nil {
			t.Fatalf("replay version %d: %v", row.Version, err)
		}
	}
	check(replayed, wanted)

	s.tool(call, "craft_sheet_release", map[string]any{"generation": generation})
	human := s.humanSession()
	lease := s.humanLease(human)
	for _, undo := range []struct {
		id       string
		restored int
	}{
		{operation(second), 4},
		{operation(receipt), 1},
	} {
		response := s.undo(human, lease, undo.id)
		if response.Code != 201 || !strings.Contains(response.Body.String(), fmt.Sprintf(`"restored":%d`, undo.restored)) {
			t.Fatalf("undo %s: %d %s", undo.id, response.Code, response.Body)
		}
	}
	check(s.stored(5), []any{"number before", "boolean before", "null before", "string before", "literal before"})
}

func TestSheetITChatRangeReplaceWithTheRealEngine(t *testing.T) {
	engine := newRealSheetEngine(t)
	adapter := NewCraftNativeSheetAgentAdapter(engine)
	base := realSheetTestNamedWorkbook(t, "Data", sitCells{
		"A1": "L_007", "A2": "L_=1+1", "A3": "l_small", "A4": "inside L_label",
		"A5": "=1+1|2", "A6": "L_'quoted", "B1": "L_outside",
	})
	s := newSheetITScenarioWithWorkbook(t, base, "workbook.xlsx", mooring.WorkbookMediaTypeXLSX)
	s.m.config.AgentApply, s.m.config.AgentInspect = adapter.Apply, adapter.Inspect
	s.send("Replace the exact prefix in Data", true)
	call := s.agent()
	generation := s.acquire(call)

	apply := func(base, id, find, replace, match string) map[string]any {
		return map[string]any{"generation": generation, "base_version": base, "request_id": id, "commands": sitCommands(
			sitCmd("range.replace", map[string]any{"sheet": "Data", "range": "A1:A6", "find": find, "replace": replace, "match": match}),
		)}
	}
	for _, args := range []map[string]any{
		apply("1", "bad-match", "L_", "Loan_", "contains"),
		apply("1", "empty-find", "", "Loan_", "prefix"),
	} {
		if response, refused := s.toolCall(call, "craft_sheet_apply", args); !refused || s.head() != 1 {
			t.Fatalf("invalid replace: refused=%v head=%d: %s", refused, s.head(), response)
		}
	}
	args := apply("1", "literal-replace", "L_", "", "prefix")
	result := s.tool(call, "craft_sheet_apply", args)
	if retry := s.tool(call, "craft_sheet_apply", args); result != retry {
		t.Fatalf("replace retry changed the result: %s / %s", result, retry)
	}
	var changed craftNativeChatApplyResult
	if err := json.Unmarshal([]byte(result), &changed); err != nil || changed.Version != 2 || changed.ChangedCells != 3 ||
		changed.Summary.CellsChanged != 3 || !changed.Undoable || !changed.CommandsOmitted || len(changed.Commands) != 0 {
		t.Fatalf("bad replace receipt: %s (%v)", result, err)
	}
	if response, refused := s.toolCall(call, "craft_sheet_apply", apply("1", "literal-replace", "L_", "other", "prefix")); !refused || s.head() != 2 {
		t.Fatalf("same ID accepted different input: refused=%v head=%d: %s", refused, s.head(), response)
	}
	noMatch := apply("2", "no-match", "Missing", "Other", "prefix")
	first := s.tool(call, "craft_sheet_apply", noMatch)
	if retry := s.tool(call, "craft_sheet_apply", noMatch); first != retry || s.head() != 2 || len(s.opsRows()) != 1 {
		t.Fatalf("zero-match retry changed the receipt or head: %s / %s, head %d", first, retry, s.head())
	}
	var unchanged craftNativeChatApplyResult
	if err := json.Unmarshal([]byte(first), &unchanged); err != nil || unchanged.Version != 2 ||
		unchanged.ChangedCells != 0 || unchanged.Undoable || !strings.Contains(string(unchanged.Receipt), `"unchanged":true`) {
		t.Fatalf("bad zero-match receipt: %s (%v)", first, err)
	}

	read := func(version int64, expected [][]any) {
		t.Helper()
		workbook := s.stored(version)
		session, release, err := engine.Open(t.Context(), workbook)
		if err != nil {
			t.Fatal(err)
		}
		defer release()
		values, err := session.ReadRange(t.Context(), "'Data'!A1:B6", true)
		if err != nil || !reflect.DeepEqual(values.Values, expected) {
			t.Fatalf("version %d: got %#v want %#v, %v", version, values.Values, expected, err)
		}
		literal, err := session.GetCell(t.Context(), "'Data'!A2")
		if err != nil || literal.Formula != "" {
			t.Fatalf("literal formula-looking value became a formula: %+v, %v", literal, err)
		}
	}
	read(2, [][]any{{"007", "L_outside"}, {"=1+1", nil}, {"l_small", nil}, {"inside L_label", nil}, {"=1+1", nil}, {"'quoted", nil}})
	s.tool(call, "craft_sheet_release", map[string]any{"generation": generation})
	human := s.humanSession()
	response := s.undo(human, s.humanLease(human), changed.OperationID)
	if response.Code != 201 || !strings.Contains(response.Body.String(), `"restored":3`) {
		t.Fatalf("undo: %d %s", response.Code, response.Body)
	}
	read(3, [][]any{{"L_007", "L_outside"}, {"L_=1+1", nil}, {"l_small", nil}, {"inside L_label", nil}, {"=1+1", nil}, {"L_'quoted", nil}})
}
