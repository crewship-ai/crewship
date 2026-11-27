//go:build linux && real_gridcraft_engine

package api

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/artifacts"
	"github.com/crewship-ai/crewship/internal/craftsession"
	"github.com/crewship-ai/crewship/internal/mooring"
)

type undoTestEngine struct{ manager *craftsession.Manager }

func (e undoTestEngine) Open(ctx context.Context, base []byte) (CraftNativeSheetSession, func(), error) {
	session, release, err := e.manager.Acquire(ctx, sheetKey(base), func() ([]byte, error) { return base, nil })
	if err != nil {
		return nil, nil, err
	}
	return undoTestSession{Session: session}, release, nil
}

func (e undoTestEngine) Retain(session CraftNativeSheetSession, snapshot []byte) {
	e.manager.Rekey(session.(undoTestSession).Key(), sheetKey(snapshot))
}

func (e undoTestEngine) Discard(session CraftNativeSheetSession) {
	session.(undoTestSession).Close()
}

type undoTestSession struct{ *craftsession.Session }

func (s undoTestSession) Apply(ctx context.Context, writes []CraftNativeSheetWrite) (int, error) {
	ops := make([]craftsession.WriteOp, 0, len(writes))
	for _, w := range writes {
		ops = append(ops, craftsession.WriteOp{Cell: w.Cell, Input: w.Input, Range: w.Range, Values: w.Values})
	}
	return s.Session.Apply(ctx, ops)
}

func (s undoTestSession) ReadRange(ctx context.Context, rng string, formulas bool) (CraftNativeSheetValues, error) {
	values, err := s.ReadRangeWith(ctx, rng, craftsession.ReadOptions{Formulas: formulas})
	if err != nil {
		return CraftNativeSheetValues{}, err
	}
	return CraftNativeSheetValues{Range: values.Range, Sheet: values.Sheet, Values: values.Values, HeldValues: values.HeldValues}, nil
}

func (s undoTestSession) GetCell(ctx context.Context, address string) (CraftNativeSheetCell, error) {
	cell, err := s.Session.GetCell(ctx, address)
	if err != nil {
		return CraftNativeSheetCell{}, err
	}
	return CraftNativeSheetCell{Cell: cell.Cell, Input: cell.Input, Formula: cell.Formula, Value: cell.Value}, nil
}

func (s undoTestSession) Sheets(ctx context.Context) ([]CraftNativeSheetInfo, error) {
	infos, err := s.Session.Sheets(ctx)
	out := make([]CraftNativeSheetInfo, 0, len(infos))
	for _, info := range infos {
		out = append(out, CraftNativeSheetInfo{Name: info.Name, Used: info.Used})
	}
	return out, err
}

func undoTestWorkbook(t *testing.T) []byte {
	t.Helper()
	// GETPIVOTDATA refers to a missing pivot. A workbook with a cached result
	// can hold that value even though the engine cannot calculate the formula.
	parts := []sitPart{
		{"[Content_Types].xml", `<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/><Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/><Override PartName="/xl/worksheets/sheet1.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/></Types>`},
		{"_rels/.rels", `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/></Relationships>`},
		{"xl/workbook.xml", `<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="L" sheetId="1" r:id="rId1"/></sheets></workbook>`},
		{"xl/_rels/workbook.xml.rels", `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/></Relationships>`},
		{"xl/worksheets/sheet1.xml", sitSheetXML(sitCells{"C2": "'007", "D2": "'=abc", "E2": `=GETPIVOTDATA("Status",Z99)|73`})},
	}
	var buf bytes.Buffer
	archive := zip.NewWriter(&buf)
	for _, part := range parts {
		w, err := archive.Create(part.name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(w, part.body); err != nil {
			t.Fatal(err)
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// seedLegacyUndo appends a version in the old format in the same transaction
// as its operations row, as the older writer did.
func seedLegacyUndo(t *testing.T, s *sitScenario, candidate []byte, commands, inverse json.RawMessage) string {
	t.Helper()
	const operation = "legacy:undo"
	db, workspace, agent := s.f.rig.db, s.f.a.WorkspaceID, s.f.a.AgentID
	ops, replayable := craftNativeAgentResolvedOps(commands)
	if !replayable {
		t.Fatal("the legacy changes must resolve to replayable operations")
	}
	versions := artifacts.NewVersionStore(db, artifacts.VersionOptions{Enabled: true, ContentType: "gridcraft"})
	_, err := versions.Apply(t.Context(), artifacts.VersionKey{WorkspaceID: workspace, PageID: "sheet-own"}, artifacts.VersionRequest{
		BaseVersion: 1, EntryPoint: "workbook.xlsx",
		Files: map[string]artifacts.VersionFile{"workbook.xlsx": {Content: candidate, MediaType: mooring.WorkbookMediaTypeXLSX}},
		Record: func(ctx context.Context, tx *sql.Tx, receipt artifacts.VersionReceipt) error {
			return recordCraftVersionOps(ctx, tx, craftVersionOps{
				WorkspaceID: workspace, PageID: "sheet-own", Version: receipt.Version, BaseVersion: 1,
				ActorKind: "agent", ActorID: agent, OperationID: operation, EnginePin: mooring.NewNativeStore(nil, s.store).EnginePin(),
				Calculate: "calculated", Replayable: true, Ops: ops, Inverse: inverse,
			})
		},
	}, func(context.Context, *sql.Tx) (string, error) { return "agent:" + agent, nil })
	if err != nil {
		t.Fatal(err)
	}
	return operation
}

func TestSheetITUndoWithTheRealEngine(t *testing.T) {
	enginePath := os.Getenv("GRIDCRAFT_ENGINE_WASM")
	module, err := os.ReadFile(enginePath)
	if err != nil {
		t.Fatalf("real GridCraft module at GRIDCRAFT_ENGINE_WASM: %v", err)
	}
	sum := sha256.Sum256(module)
	engine := undoTestEngine{manager: craftsession.NewManager(craftsession.Config{
		Launch:      craftsession.ExecLauncher("gridcraft", enginePath, hex.EncodeToString(sum[:]), 4096),
		InputPolicy: craftsession.InputPolicy(CraftNativeAgentInputPolicy(false)),
	})}
	t.Cleanup(engine.manager.Close)
	adapter := NewCraftNativeSheetAgentAdapter(engine)

	for _, tc := range []struct {
		name         string
		held, legacy bool
	}{
		{name: "literal text"},
		{name: "held pivot formula", held: true},
		{name: "legacy literal text", legacy: true},
		{name: "legacy held pivot formula", held: true, legacy: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base := undoTestWorkbook(t)
			s := newSheetITScenarioWithWorkbook(t, base, "workbook.xlsx", mooring.WorkbookMediaTypeXLSX)
			s.m.config.AgentApply, s.m.config.AgentInspect = adapter.Apply, adapter.Inspect
			human := s.humanSession()
			session, release, err := engine.Open(t.Context(), base)
			if err != nil {
				t.Fatal(err)
			}
			original, err := session.(undoTestSession).ReadRangeWith(t.Context(), "'L'!C2:E2", craftsession.ReadOptions{})
			release()
			if err != nil || len(original.Values) != 1 || len(original.Values[0]) != 3 || len(original.HeldValues) != 1 || original.HeldValues[0] != "E2" || original.Values[0][0] != "007" || original.Values[0][1] != "=abc" {
				t.Fatalf("fixture values and held cells: %+v, %v", original, err)
			}
			if cached := original.Values[0][2]; cached != "73" && cached != float64(73) {
				t.Fatalf("the held pivot formula has lost its cached value: %#v", cached)
			}
			cells := []string{"C2", "D2"}
			if tc.held {
				cells = []string{"E2"}
			}
			var commands []map[string]any
			for _, cell := range cells {
				commands = append(commands, sitCmd("cell.set", map[string]any{"sheet": "L", "cell": cell, "input": "done"}))
			}
			var result struct {
				OperationID string `json:"operation_id"`
				Undoable    bool   `json:"undoable"`
			}
			if tc.legacy {
				old := `[{"range":"L!C2","values":[["007"]]},{"range":"L!D2","values":[["=abc"]]}]`
				if tc.held {
					old = `[{"range":"L!E2","values":[["=GETPIVOTDATA(\"Status\",Z99)"]]}]`
				}
				raw, err := json.Marshal(commands)
				if err != nil {
					t.Fatal(err)
				}
				candidate, err := adapter.Apply(t.Context(), CraftNativeDocumentProfile{Application: "gridcraft"}, base, raw)
				if err != nil {
					t.Fatalf("prepare the legacy agent version: %v", err)
				}
				result.OperationID = seedLegacyUndo(t, s, candidate, raw, json.RawMessage(old))
				result.Undoable = true
			} else {
				s.send("Set the selected cells to done", true)
				call := s.agent()
				generation := s.acquire(call)
				applied := s.tool(call, "craft_sheet_apply", map[string]any{"generation": generation, "base_version": "1", "request_id": "real-undo-apply", "commands": sitCommands(commands...)})
				if json.Unmarshal([]byte(applied), &result) != nil || result.OperationID == "" || s.head() != 2 {
					t.Fatalf("apply: %s (head %d)", applied, s.head())
				}
				s.tool(call, "craft_sheet_release", map[string]any{"generation": generation})
			}
			w := s.undo(human, s.humanLease(human), result.OperationID)
			if tc.held {
				if (!tc.legacy && result.Undoable) || w.Code != 409 || !strings.Contains(w.Body.String(), `"code":"not_undoable"`) || s.head() != 2 {
					t.Fatalf("the held Excel value cannot be restored: card undoable=%v, Undo %d %s, head %d", result.Undoable, w.Code, w.Body, s.head())
				}
				return
			}
			if !result.Undoable || w.Code != 201 {
				t.Fatalf("Undo %d %s, card undoable=%v", w.Code, w.Body, result.Undoable)
			}
			persisted := s.stored(3)
			warm, release, err := engine.Open(t.Context(), persisted)
			if err != nil {
				t.Fatal(err)
			}
			engine.Discard(warm)
			release()
			saved, release, err := engine.Open(t.Context(), persisted)
			if err != nil {
				t.Fatal(err)
			}
			defer release()
			for i, expected := range []string{"007", "=abc"} {
				got, err := saved.GetCell(t.Context(), "'L'!"+cells[i])
				if err != nil || got.Formula != "" || got.Value != expected {
					t.Errorf("%s: value=%#v formula=%q error=%v, want literal %q", cells[i], got.Value, got.Formula, err, expected)
				}
			}
		})
	}
}
