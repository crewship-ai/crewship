//go:build linux && real_gridcraft_engine

package api

import (
	"bytes"
	"encoding/json"
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
