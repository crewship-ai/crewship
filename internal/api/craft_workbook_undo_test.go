package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/artifacts"
	"github.com/crewship-ai/crewship/internal/craftapps"
	"github.com/crewship-ai/crewship/internal/mooring"
)

// An agent change is undone cell by cell: a cell still holding what the agent wrote gets its
// value from before the change back, a cell the person changed since is kept and counted skipped.
// A formula the agent replaced comes back as that formula.
func TestUndoPutsBackOnlyTheCellsNobodyChangedSince(t *testing.T) {
	ops := json.RawMessage(`[{"command":"edit.clearContents","params":{"range":"'L'!B8:B10"}},{"command":"cell.set","params":{"cell":"'L'!C1","input":"done"}}]`)
	reads, ok := craftUndoReads(ops)
	if !ok || len(reads) != 2 || reads[0].sheet != "L" || reads[0].rng != "B8:B10" || reads[1].rng != "C1" {
		t.Fatalf("reads %v %+v", ok, reads)
	}
	inverse, ok := craftInverseFrom(ops, [][][]any{{{"L-1"}, {"L-2"}, {"L-3"}}, {{nil}}})
	if !ok {
		t.Fatal("not undoable")
	}
	// A read that does not cover every cell of the range leaves no undo.
	for name, old := range map[string][][][]any{
		"fewer rows":    {{{"L-1"}, {"L-2"}}, {{nil}}},
		"a short row":   {{{"L-1"}, {}, {"L-3"}}, {{nil}}},
		"no rows":       {{}, {{nil}}},
		"a wider row":   {{{"L-1", "x"}, {"L-2"}, {"L-3"}}, {{nil}}},
		"cell not read": {{{"L-1"}, {"L-2"}, {"L-3"}}, {}},
	} {
		if _, ok := craftInverseFrom(ops, old); ok {
			t.Errorf("%s: an inverse from a read that does not cover the range", name)
		}
	}
	formula, ok := craftInverseFrom(ops, [][][]any{{{"=A1*2"}, {"L-2"}, {"L-3"}}, {{nil}}})
	if !ok {
		t.Fatal("a formula is not undoable")
	}
	// What the agent's version holds in those ranges, as the engine reads it.
	atAgent := [][][]any{{{nil}, {nil}, {nil}}, {{"done"}}}
	if commands, _, _, ok := craftUndoCommands(ops, formula, atAgent, [][][]any{{{nil}, {nil}, {nil}}, {{"done"}}}, craftUndoTouched{}); !ok || !strings.Contains(string(commands[1]), `"=A1*2"`) {
		t.Fatalf("formula undo %v %s", ok, commands)
	}
	// The person typed into B9 after the agent cleared it; C1 still says done.
	commands, restored, skipped, ok := craftUndoCommands(ops, inverse, atAgent, [][][]any{{{nil}, {"mine"}, {""}}, {{"done"}}}, craftUndoTouched{})
	if !ok || skipped != 1 || restored != 3 {
		t.Fatalf("undo %v skipped %d", ok, skipped)
	}
	got := make([]string, 0, len(commands))
	for _, c := range commands {
		got = append(got, string(c))
	}
	want := []string{
		`{"command":"range.clear","params":{"range":"C1","sheet":"L"}}`,
		`{"command":"range.setValues","params":{"range":"B8","sheet":"L","values":[["L-1"]]}}`,
		`{"command":"range.setValues","params":{"range":"B10","sheet":"L","values":[["L-3"]]}}`,
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("commands\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	// Untouched as a whole: one write of the whole range.
	commands, _, skipped, ok = craftUndoCommands(ops, inverse, atAgent, [][][]any{{{nil}, {nil}, {nil}}, {{"done"}}}, craftUndoTouched{})
	if !ok || skipped != 0 || len(commands) != 2 || string(commands[1]) != `{"command":"range.setValues","params":{"range":"B8:B10","sheet":"L","values":[["L-1"],["L-2"],["L-3"]]}}` {
		t.Fatalf("whole %v %d %s", ok, skipped, commands)
	}
	// A number the agent typed reads back as a number.
	numeric := json.RawMessage(`[{"command":"cell.set","params":{"cell":"'L'!D1","input":"5"}}]`)
	inv, _ := craftInverseFrom(numeric, [][][]any{{{float64(4)}}})
	if commands, _, skipped, ok = craftUndoCommands(numeric, inv, [][][]any{{{float64(5)}}}, [][][]any{{{float64(5)}}}, craftUndoTouched{}); !ok || skipped != 0 || len(commands) != 1 || string(commands[0]) != `{"command":"range.setValues","params":{"range":"D1","sheet":"L","values":[[4]]}}` {
		t.Fatalf("numeric %v %d %s", ok, skipped, commands)
	}
}

func (rig *workbookRig) undo(operation string) *httptest.ResponseRecorder {
	return rig.undoAt(operation, "1")
}

func (rig *workbookRig) undoAt(operation, generation string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "/api/v1/pages/native-page/craft/document/undo", strings.NewReader(`{"operation_id":`+strconv.Quote(operation)+`}`)).WithContext(rig.ctx)
	r.SetPathValue("slug", "native-page")
	r.Header.Set("X-Craft-Session", rig.token)
	r.Header.Set("X-Craft-Generation", generation)
	w := httptest.NewRecorder()
	rig.d.undoOperation(w, r)
	return w
}

// Undo of an agent change: the person's editor (holding the lease) asks with the operation ID;
// the server reads the agent's ranges in the head, puts back the untouched cells through the
// engine and saves that as the person's new version. A repeat answers the same version.
func TestUndoOfAnAgentChangeIsANewVersionOfThePerson(t *testing.T) {
	rig := workbookDocumentsFixture(t)
	mustExec(t, rig.db, `INSERT INTO agents(id,workspace_id,name,slug) VALUES('agent-u',?,'Agent U','agent-u')`, rig.ws)
	agentFile := sitWorkbook(sitCells{"C1": "done"}, sitInitialOther())
	undone := sitWorkbook(sitCells{"C1": "open"}, sitInitialOther())
	ops := json.RawMessage(`[{"command":"cell.set","params":{"cell":"'L'!C1","input":"done"}}]`)
	inverse, ok := craftInverseFrom(ops, [][][]any{{{"open"}}})
	if !ok {
		t.Fatal("inverse")
	}
	s := artifacts.NewVersionStore(rig.db, artifacts.VersionOptions{Enabled: true, ContentType: "gridcraft"})
	if _, err := s.Apply(t.Context(), artifacts.VersionKey{WorkspaceID: rig.ws, PageID: "native-page"}, artifacts.VersionRequest{BaseVersion: 1, EntryPoint: "workbook.xlsm",
		Files: map[string]artifacts.VersionFile{"workbook.xlsm": {Content: agentFile, MediaType: mooring.WorkbookMediaTypeXLSM}},
		Record: func(ctx context.Context, tx *sql.Tx, r artifacts.VersionReceipt) error {
			return recordCraftVersionOps(ctx, tx, craftVersionOps{WorkspaceID: rig.ws, PageID: "native-page", Version: r.Version, BaseVersion: 1, ActorKind: "agent", ActorID: "agent-u",
				OperationID: "run-1:r1", EnginePin: rig.pin, Calculate: "calculated", Replayable: true, Ops: ops, Inverse: inverse})
		}}, func(context.Context, *sql.Tx) (string, error) { return "agent:agent-u", nil }); err != nil {
		t.Fatal(err)
	}
	var applied []string
	rig.d.profiles = map[string]CraftNativeDocumentProfile{"gridcraft": {Application: "gridcraft"}}
	rig.d.human.agents = &craftNativeAgentManager{config: CraftNativeHTTPConfig{
		AgentInspect: func(_ context.Context, _ CraftNativeDocumentProfile, raw []byte, query json.RawMessage) (json.RawMessage, error) {
			if !bytes.Equal(raw, agentFile) || !strings.Contains(string(query), `"formulas":true`) {
				return nil, errors.New("read of another document")
			}
			return json.RawMessage(`{"command":"sheet.read","result":{"range":"C1","sheet":"L","values":[["done"]]}}`), nil
		},
		AgentApply: func(ctx context.Context, _ CraftNativeDocumentProfile, raw []byte, commands json.RawMessage) ([]byte, error) {
			if !craftNativeUndo(ctx) {
				return nil, errors.New("an undo applied as an agent change (no formulas)")
			}
			applied = append(applied, string(commands))
			return undone, nil
		},
	}}
	w := rig.undo("run-1:r1")
	if w.Code != 201 || !strings.Contains(w.Body.String(), `"version":"3"`) || !strings.Contains(w.Body.String(), `"skipped":0`) {
		t.Fatalf("undo %d %s", w.Code, w.Body)
	}
	if want := `[{"command":"range.setValues","params":{"range":"C1","sheet":"L","values":[["open"]]}}]`; len(applied) != 1 || applied[0] != want {
		t.Fatalf("applied %v", applied)
	}
	if doc := workbookRead(t, rig.read(nil)); !bytes.Equal(doc.Bytes, undone) || doc.Version != "3" {
		t.Fatalf("head after undo %s", doc.Version)
	}
	var source string
	if err := rig.db.QueryRow(`SELECT json_extract(metadata,'$.source') FROM audit_logs WHERE action='artifact.sheet_saved' ORDER BY created_at DESC, id DESC LIMIT 1`).Scan(&source); err != nil || source != "undo" {
		t.Fatalf("audit source %q %v", source, err)
	}
	if again := rig.undo("run-1:r1"); again.Code != 200 || !strings.Contains(again.Body.String(), `"version":"3"`) || len(applied) != 1 {
		t.Fatalf("repeat %d %s", again.Code, again.Body)
	}
	if missing := rig.undo("run-1:nope"); missing.Code != 404 {
		t.Fatalf("unknown operation %d %s", missing.Code, missing.Body)
	}
}

// The engine adapter takes formulas only for an undo: an agent change still may not write one.
func TestUndoMayPutBackAFormulaAnAgentMayNotWrite(t *testing.T) {
	p := CraftNativeDocumentProfile{Application: "gridcraft"}
	raw := craftTestWorkbook(t, "xlsx", "before")
	original := string(sitParts(t, raw)["xl/worksheets/sheet1.xml"])
	snapshot := workbookEdited(t, raw, "", map[string]string{
		"xl/worksheets/sheet1.xml": strings.Replace(original, "</sheetData>", `<row r="8"><c r="B8"><f>A1*2</f><v>0</v></c></row></sheetData>`, 1),
	})
	commands := json.RawMessage(`[{"command":"range.setValues","params":{"range":"B8","sheet":"L","values":[["=A1*2"]]}}]`)
	engine := &fakeSheetEngine{session: &fakeSheetSession{snapshot: snapshot}}
	if _, err := NewCraftNativeSheetAgentAdapter(engine).Apply(context.Background(), p, raw, commands); err == nil {
		t.Fatal("an agent change wrote a formula")
	}
	if _, err := NewCraftNativeSheetAgentAdapter(engine).Apply(craftNativeUndoContext(context.Background()), p, raw, commands); err != nil {
		t.Fatalf("undo of a formula: %v", err)
	}
}

// The undo is the person's version with the operations it sent to the engine: the editor holding the
// lease reads it from its base as replayable, replays it and saves the next version without moved.
func TestUndoVersionIsReplayableForTheEditor(t *testing.T) {
	rig := workbookDocumentsFixture(t)
	mustExec(t, rig.db, `INSERT INTO agents(id,workspace_id,name,slug) VALUES('agent-u',?,'Agent U','agent-u')`, rig.ws)
	agentFile := sitWorkbook(sitCells{"C1": "done"}, sitInitialOther())
	undone := sitWorkbook(sitCells{"C1": "open"}, sitInitialOther())
	ops := json.RawMessage(`[{"command":"cell.set","params":{"cell":"'L'!C1","input":"done"}}]`)
	inverse, ok := craftInverseFrom(ops, [][][]any{{{"open"}}})
	if !ok {
		t.Fatal("inverse")
	}
	s := artifacts.NewVersionStore(rig.db, artifacts.VersionOptions{Enabled: true, ContentType: "gridcraft"})
	if _, err := s.Apply(t.Context(), artifacts.VersionKey{WorkspaceID: rig.ws, PageID: "native-page"}, artifacts.VersionRequest{BaseVersion: 1, EntryPoint: "workbook.xlsm",
		Files: map[string]artifacts.VersionFile{"workbook.xlsm": {Content: agentFile, MediaType: mooring.WorkbookMediaTypeXLSM}},
		Record: func(ctx context.Context, tx *sql.Tx, r artifacts.VersionReceipt) error {
			return recordCraftVersionOps(ctx, tx, craftVersionOps{WorkspaceID: rig.ws, PageID: "native-page", Version: r.Version, BaseVersion: 1, ActorKind: "agent", ActorID: "agent-u",
				OperationID: "run-1:r1", EnginePin: rig.pin, Calculate: "calculated", Replayable: true, Ops: ops, Inverse: inverse})
		}}, func(context.Context, *sql.Tx) (string, error) { return "agent:agent-u", nil }); err != nil {
		t.Fatal(err)
	}
	var applied []string
	rig.d.profiles = map[string]CraftNativeDocumentProfile{"gridcraft": {Application: "gridcraft"}}
	rig.d.human.agents = &craftNativeAgentManager{config: CraftNativeHTTPConfig{
		AgentInspect: func(_ context.Context, _ CraftNativeDocumentProfile, raw []byte, query json.RawMessage) (json.RawMessage, error) {
			if !bytes.Equal(raw, agentFile) || !strings.Contains(string(query), `"formulas":true`) {
				return nil, errors.New("read of another document")
			}
			return json.RawMessage(`{"command":"sheet.read","result":{"range":"C1","sheet":"L","values":[["done"]]}}`), nil
		},
		AgentApply: func(ctx context.Context, _ CraftNativeDocumentProfile, raw []byte, commands json.RawMessage) ([]byte, error) {
			if !craftNativeUndo(ctx) {
				return nil, errors.New("an undo applied as an agent change (no formulas)")
			}
			applied = append(applied, string(commands))
			return undone, nil
		},
	}}
	if w := rig.undo("run-1:r1"); w.Code != 201 {
		t.Fatalf("undo %d %s", w.Code, w.Body)
	}
	listed := rig.operations("2")
	var list struct {
		Replayable bool `json:"replayable"`
		Versions   []struct {
			Version     string          `json:"version"`
			ActorKind   string          `json:"actor_kind"`
			OperationID string          `json:"operation_id"`
			Calculate   string          `json:"calculate"`
			Replayable  bool            `json:"replayable"`
			Ops         json.RawMessage `json:"ops"`
		} `json:"versions"`
	}
	if listed.Code != 200 || json.Unmarshal(listed.Body.Bytes(), &list) != nil || !list.Replayable || len(list.Versions) != 1 {
		t.Fatalf("operations from 2: %d %s", listed.Code, listed.Body)
	}
	v := list.Versions[0]
	if v.Version != "3" || v.ActorKind != "human" || v.OperationID != "undo:run-1:r1" || v.Calculate != "calculated" || !v.Replayable || string(v.Ops) != `[{"command":"range.setValues","params":{"range":"'L'!C1","values":[["open"]]}}]` {
		t.Fatalf("undo version %+v %s", v, v.Ops)
	}
	_ = applied
	other := sitInitialOther()
	other["A2"] = "edited"
	next := workbookEdited(t, undone, "", map[string]string{"xl/worksheets/sheet2.xml": sitSheetXML(other)})
	if w := rig.save(next, "3", workbookHash(next)); w.Code != 200 || !strings.Contains(w.Body.String(), `"version":"4"`) {
		t.Fatalf("the editor's save after replaying the undo: %d %s", w.Code, w.Body)
	}
}

// Undo is a fenced write: a generation that is not the caller's live lease is refused with 409
// before the engine runs, and nothing is written.
func TestUndoRefusesAStaleOrMissingLease(t *testing.T) {
	rig := workbookDocumentsFixture(t)
	mustExec(t, rig.db, `INSERT INTO agents(id,workspace_id,name,slug) VALUES('agent-u',?,'Agent U','agent-u')`, rig.ws)
	ops := json.RawMessage(`[{"command":"cell.set","params":{"cell":"'L'!C1","input":"done"}}]`)
	inverse, _ := craftInverseFrom(ops, [][][]any{{{"open"}}})
	s := artifacts.NewVersionStore(rig.db, artifacts.VersionOptions{Enabled: true, ContentType: "gridcraft"})
	if _, err := s.Apply(t.Context(), artifacts.VersionKey{WorkspaceID: rig.ws, PageID: "native-page"}, artifacts.VersionRequest{BaseVersion: 1, EntryPoint: "workbook.xlsm",
		Files: map[string]artifacts.VersionFile{"workbook.xlsm": {Content: sitWorkbook(sitCells{"C1": "done"}, sitInitialOther()), MediaType: mooring.WorkbookMediaTypeXLSM}},
		Record: func(ctx context.Context, tx *sql.Tx, r artifacts.VersionReceipt) error {
			return recordCraftVersionOps(ctx, tx, craftVersionOps{WorkspaceID: rig.ws, PageID: "native-page", Version: r.Version, BaseVersion: 1, ActorKind: "agent", ActorID: "agent-u",
				OperationID: "run-1:r1", EnginePin: rig.pin, Calculate: "calculated", Replayable: true, Ops: ops, Inverse: inverse})
		}}, func(context.Context, *sql.Tx) (string, error) { return "agent:agent-u", nil }); err != nil {
		t.Fatal(err)
	}
	engine := 0
	rig.d.profiles = map[string]CraftNativeDocumentProfile{"gridcraft": {Application: "gridcraft"}}
	rig.d.human.agents = &craftNativeAgentManager{config: CraftNativeHTTPConfig{
		AgentInspect: func(context.Context, CraftNativeDocumentProfile, []byte, json.RawMessage) (json.RawMessage, error) {
			engine++
			return json.RawMessage(`{"command":"sheet.read","result":{"range":"C1","sheet":"L","values":[["done"]]}}`), nil
		},
		AgentApply: func(context.Context, CraftNativeDocumentProfile, []byte, json.RawMessage) ([]byte, error) {
			engine++
			return sitWorkbook(sitCells{"C1": "open"}, sitInitialOther()), nil
		},
	}}
	versions := func() (n int) {
		_ = rig.db.QueryRow(`SELECT count(*) FROM artifact_versions WHERE page_id='native-page'`).Scan(&n)
		return
	}
	if w := rig.undoAt("run-1:r1", "7"); w.Code != 409 || engine != 0 || versions() != 2 {
		t.Fatalf("stale generation: %d %s, engine %d, versions %d", w.Code, w.Body, engine, versions())
	}
	mustExec(t, rig.db, `UPDATE mooring_edit_leases SET actor_id='',session_id='',expires_ns=0 WHERE page_id='native-page'`)
	if w := rig.undoAt("run-1:r1", "1"); w.Code != 409 || engine != 0 || versions() != 2 {
		t.Fatalf("no lease: %d %s, engine %d, versions %d", w.Code, w.Body, engine, versions())
	}
}

// Undo decides by the cells touched since the agent's version, not by today's
// value. A cell a later logged operation names is kept even when it holds the agent's value again;
// for a version without operations the cell's raw input is compared with the agent's version
// without normalizing ("001" is not 1). The answer counts what came back and what stayed.
func TestUndoKeepsTheCellsTouchedSinceTheAgentsVersion(t *testing.T) {
	for name, tc := range map[string]struct {
		later      []string // the human version's cells
		laterOps   string   // its logged operations, "" for a save without them
		agentRead  any      // C1 as the engine reads it in the agent's version
		headRead   any      // and in the head
		restored   int
		wantChange bool
	}{
		"text 001 the person made the number 1":    {[]string{"C1", "1"}, "", "001", float64(1), 0, false},
		"the person typed the agent's value again": {[]string{"C1", "done"}, `[{"command":"range.setValues","params":{"range":"'L'!C1","values":[["done"]]}}]`, "done", "done", 0, false},
		"nobody touched the cell (control)":        {nil, "", "done", "done", 1, true},
	} {
		t.Run(name, func(t *testing.T) {
			rig := workbookDocumentsFixture(t)
			mustExec(t, rig.db, `INSERT INTO agents(id,workspace_id,name,slug) VALUES('agent-u',?,'Agent U','agent-u')`, rig.ws)
			agentFile := sitWorkbook(sitCells{"C1": "agent"}, sitInitialOther())
			ops := json.RawMessage(`[{"command":"cell.set","params":{"cell":"'L'!C1","input":"done"}}]`)
			inverse, _ := craftInverseFrom(ops, [][][]any{{{"open"}}})
			s := artifacts.NewVersionStore(rig.db, artifacts.VersionOptions{Enabled: true, ContentType: "gridcraft"})
			key := artifacts.VersionKey{WorkspaceID: rig.ws, PageID: "native-page"}
			if _, err := s.Apply(t.Context(), key, artifacts.VersionRequest{BaseVersion: 1, EntryPoint: "workbook.xlsm",
				Files: map[string]artifacts.VersionFile{"workbook.xlsm": {Content: agentFile, MediaType: mooring.WorkbookMediaTypeXLSM}},
				Record: func(ctx context.Context, tx *sql.Tx, r artifacts.VersionReceipt) error {
					return recordCraftVersionOps(ctx, tx, craftVersionOps{WorkspaceID: rig.ws, PageID: "native-page", Version: r.Version, BaseVersion: 1, ActorKind: "agent", ActorID: "agent-u",
						OperationID: "run-1:r1", EnginePin: rig.pin, Calculate: "calculated", Replayable: true, Ops: ops, Inverse: inverse})
				}}, func(context.Context, *sql.Tx) (string, error) { return "agent:agent-u", nil }); err != nil {
				t.Fatal(err)
			}
			head := agentFile
			if tc.later != nil {
				head = sitWorkbook(sitCells{tc.later[0]: tc.later[1]}, sitInitialOther())
				if _, err := s.Apply(t.Context(), key, artifacts.VersionRequest{BaseVersion: 2, EntryPoint: "workbook.xlsm",
					Files: map[string]artifacts.VersionFile{"workbook.xlsm": {Content: head, MediaType: mooring.WorkbookMediaTypeXLSM}},
					Record: func(ctx context.Context, tx *sql.Tx, r artifacts.VersionReceipt) error {
						if tc.laterOps == "" {
							return nil
						}
						return recordCraftVersionOps(ctx, tx, craftVersionOps{WorkspaceID: rig.ws, PageID: "native-page", Version: r.Version, BaseVersion: 2, ActorKind: "human", ActorID: "u-human",
							OperationID: "keep:1", EnginePin: rig.pin, Calculate: "calculated", Replayable: true, Ops: json.RawMessage(tc.laterOps)})
					}}, func(context.Context, *sql.Tx) (string, error) { return "user:u-human", nil }); err != nil {
					t.Fatal(err)
				}
			}
			applied := 0
			rig.d.profiles = map[string]CraftNativeDocumentProfile{"gridcraft": {Application: "gridcraft"}}
			rig.d.human.agents = &craftNativeAgentManager{config: CraftNativeHTTPConfig{
				AgentInspect: func(_ context.Context, _ CraftNativeDocumentProfile, raw []byte, _ json.RawMessage) (json.RawMessage, error) {
					value := tc.headRead
					if bytes.Equal(raw, agentFile) && !bytes.Equal(raw, head) {
						value = tc.agentRead
					}
					out, _ := json.Marshal(map[string]any{"command": "sheet.read", "result": map[string]any{"range": "C1", "sheet": "L", "values": [][]any{{value}}}})
					return out, nil
				},
				AgentApply: func(context.Context, CraftNativeDocumentProfile, []byte, json.RawMessage) ([]byte, error) {
					applied++
					return sitWorkbook(sitCells{"C1": "open"}, sitInitialOther()), nil
				},
			}}
			w := rig.undo("run-1:r1")
			var out struct {
				Restored *int64 `json:"restored"`
				Skipped  int64  `json:"skipped"`
			}
			_ = json.Unmarshal(w.Body.Bytes(), &out)
			if (applied > 0) != tc.wantChange || out.Restored == nil || *out.Restored != int64(tc.restored) || out.Skipped != int64(1-tc.restored) {
				t.Fatalf("undo %d %s, engine applies %d", w.Code, w.Body, applied)
			}
		})
	}
}

// A person's save names the cells it changed since the editor's saved version (X-Craft-Touched):
// stored as a human row of addresses only, never content; "unknown" as everything; no header, no
// row (the raw comparison decides). A malformed list saves nothing.
func TestWorkbookSaveRecordsTheCellsThePersonTouched(t *testing.T) {
	rig := workbookDocumentsFixture(t)
	edited := craftTestWorkbook(t, "xlsm", "touched-1")
	ops := func(version int) (string, int) {
		var raw string
		var replayable int
		if err := rig.db.QueryRow(`SELECT ops, replayable FROM craft_version_ops WHERE page_id='native-page' AND version=?`, version).Scan(&raw, &replayable); err != nil {
			return "", -1
		}
		return raw, replayable
	}
	set := func(v string) { rig.touched = &v }
	for _, bad := range []string{`nope`, `["L!C1"]`, `["'L'!ZZZZZ1"]`, `["'L'!C1",`, `[` + strings.Repeat(`"'L'!C1",`, 2000) + `"'L'!C1"]`} {
		set(bad)
		if w := rig.save(edited, "1", workbookHash(edited)); w.Code != 400 {
			t.Fatalf("touched %.40q: %d %s, want 400", bad, w.Code, w.Body)
		}
	}
	set(`["'L'!C1","'L'!A1:B2"]`)
	if w := rig.save(edited, "1", workbookHash(edited)); w.Code != 200 {
		t.Fatalf("save with touched: %d %s", w.Code, w.Body)
	}
	if raw, replayable := ops(2); replayable != 0 || raw != `[{"command":"touched","params":{"range":"'L'!C1"}},{"command":"touched","params":{"range":"'L'!A1:B2"}}]` {
		t.Fatalf("version 2 row %q replayable %d", raw, replayable)
	}
	again := craftTestWorkbook(t, "xlsm", "touched-2")
	set("unknown")
	if w := rig.save(again, "2", workbookHash(again)); w.Code != 200 {
		t.Fatalf("save unknown: %d %s", w.Code, w.Body)
	}
	if raw, _ := ops(3); raw != `[{"command":"touched","params":{"range":"*"}}]` {
		t.Fatalf("version 3 row %q", raw)
	}
	third := craftTestWorkbook(t, "xlsm", "touched-3")
	rig.touched = nil
	if w := rig.save(third, "3", workbookHash(third)); w.Code != 200 {
		t.Fatalf("save without touched: %d %s", w.Code, w.Body)
	}
	if _, replayable := ops(4); replayable != -1 {
		t.Fatal("a save without X-Craft-Touched wrote a row")
	}
	// A large range is an address like any other: the save never fails on its size.
	big := craftTestWorkbook(t, "xlsm", "touched-4")
	set(`["'L'!A1:Z5000"]`)
	if w := rig.save(big, "4", workbookHash(big)); w.Code != 200 {
		t.Fatalf("save with a large touched range: %d %s", w.Code, w.Body)
	}
	// A sheet in neither the saved nor the new version saves, and stores unknown: undo then keeps every cell.
	gone := craftTestWorkbook(t, "xlsm", "touched-5")
	set(`["'Nope'!B8"]`)
	if w := rig.save(gone, "5", workbookHash(gone)); w.Code != 200 {
		t.Fatalf("save touching an absent sheet: %d %s", w.Code, w.Body)
	}
	if raw, _ := ops(6); raw != `[{"command":"touched","params":{"range":"*"}}]` {
		t.Fatalf("version 6 row %q, want unknown", raw)
	}
}

// The common clear: the last rows and columns of the range were empty. The engine reads the whole
// rectangle with null for every empty cell (sheet.read with an explicit range), so the inverse
// exists and undo writes the old values back.
func TestUndoOfAClearWithEmptyEdgesPutsTheValuesBack(t *testing.T) {
	ops := json.RawMessage(`[{"command":"edit.clearContents","params":{"range":"'L'!B8:D10"}}]`)
	old := [][][]any{{{"L-1", "x", nil}, {"L-2", nil, nil}, {nil, nil, nil}}}
	inverse, ok := craftInverseFrom(ops, old)
	if !ok {
		t.Fatal("a clear whose last rows and columns were empty has no inverse")
	}
	cleared := [][][]any{{{nil, nil, nil}, {nil, nil, nil}, {nil, nil, nil}}}
	commands, restored, skipped, ok := craftUndoCommands(ops, inverse, cleared, cleared, craftUndoTouched{})
	if !ok || skipped != 0 || len(commands) == 0 {
		t.Fatalf("undo of the clear: ok %v restored %d skipped %d %s", ok, restored, skipped, commands)
	}
	var joined string
	for _, c := range commands {
		joined += string(c) + "\n"
	}
	for _, want := range []string{`"L-1"`, `"x"`, `"L-2"`} {
		if !strings.Contains(joined, want) {
			t.Errorf("undo does not write %s back: %s", want, joined)
		}
	}
	if restored != 9 {
		t.Errorf("restored %d, want every cell of the range, as nobody touched them since", restored)
	}
}

func TestUndoFollowsSheetApplicationPolicy(t *testing.T) {
	for _, tc := range []struct {
		name    string
		desired craftapps.Desired
		drain   time.Duration
		status  int
	}{
		{"open", craftapps.Enabled, 0, http.StatusNotFound},
		{"draining", craftapps.Disabled, time.Hour, http.StatusNotFound},
		{"closed", craftapps.Disabled, 0, http.StatusServiceUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rig := workbookDocumentsFixture(t)
			policy := &craftapps.Policy{}
			rig.d.human.apps = newCraftAppsGate(policy, rig.db)
			for _, setting := range []struct {
				desired craftapps.Desired
				drain   time.Duration
			}{{craftapps.Enabled, 0}, {tc.desired, tc.drain}} {
				if _, _, err := policy.Set(t.Context(), rig.db, craftapps.Sheet, setting.desired, "admin", setting.drain); err != nil {
					t.Fatal(err)
				}
			}
			got := rig.undo("missing-operation")
			if got.Code != tc.status {
				t.Fatalf("Undo returned %d %s, want %d", got.Code, got.Body, tc.status)
			}
			if tc.status == http.StatusServiceUnavailable && !strings.Contains(got.Body.String(), `"reason":"app_disabled"`) {
				t.Errorf("Undo refusal: %s", got.Body)
			}
		})
	}
}
