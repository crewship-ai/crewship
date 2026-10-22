package appengine

import (
	"context"
	"encoding/json"
	"github.com/crewship-ai/crewship/internal/database"
	"os"
	"path/filepath"
	"testing"
)

type localFixtureRunner struct{ binary, scratch string }

func (r localFixtureRunner) Run(ctx context.Context, input []byte, c Command) (EngineResult, error) {
	return RunGridCraftWorker(ctx, r.binary, r.scratch, input, c)
}
func TestRealGridCraftImportEditRestartAndRetry(t *testing.T) {
	binary := os.Getenv("CREWSHIP_TEST_GRIDCRAFT_BINARY")
	if binary == "" {
		t.Skip("set CREWSHIP_TEST_GRIDCRAFT_BINARY to the pinned CLI to execute real engine contract")
	}
	raw, err := os.ReadFile("testdata/blank-workbook.json")
	if err != nil {
		t.Fatal(err)
	}
	s, db, a, k := fixture(t)
	scratch := t.TempDir()
	engine := GridCraft{Runner: localFixtureRunner{binary, scratch}}
	request := Request{MutationID: "real-import", MandateID: "human-import", Command: Command{Kind: "import", Snapshot: json.RawMessage(raw)}}
	if _, err = s.Execute(t.Context(), k, request, a, engine); err != nil {
		t.Fatal(err)
	}
	original, err := s.Execute(t.Context(), k, edit(), a, engine)
	if err != nil {
		t.Fatal(err)
	}
	// Close the actual database and reopen it; every candidate gets a new real
	// engine process, so neither Go nor GridCraft memory can supply persistence.
	path := db.Path()
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := database.Open("file:" + path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	fresh := New(reopened.DB, Options{Enabled: true})
	retry, err := fresh.Execute(t.Context(), k, edit(), a, engine)
	if err != nil || retry.Rev != original.Rev {
		t.Fatalf("retry=%+v %v", retry, err)
	}
	state, err := fresh.Read(t.Context(), k, a)
	if err != nil || state.Rev != 2 {
		t.Fatalf("state=%+v %v", state, err)
	}
	workbook, err := decodeWorkbook(state.Snapshot)
	if err != nil {
		t.Fatal(err)
	}
	cells, err := extractCells(workbook)
	if err != nil {
		t.Fatal(err)
	}
	cell, ok := cells["Sheet1!B2"].(map[string]any)
	value := 0.0
	if ok {
		value, _ = cell["value"].(map[string]any)["v"].(json.Number).Float64()
	}
	if !ok || value != 42 {
		t.Fatalf("wrong persisted cell: %v", cell)
	}
	files, err := filepath.Glob(filepath.Join(scratch, "workbook-*"))
	if err != nil || len(files) != 0 {
		t.Fatalf("scratch survived: %v %v", files, err)
	}
}

// The pinned engine recalculates a dependent chain after a mandated input edit.
// Only the authored target is a mandated effect; C9 and D9 are accepted because
// their formulas are unchanged and a fresh full recalculation confirms them.
func TestRealGridCraftDependentFormulaRecalculation(t *testing.T) {
	binary := os.Getenv("CREWSHIP_TEST_GRIDCRAFT_BINARY")
	if binary == "" {
		t.Skip("set CREWSHIP_TEST_GRIDCRAFT_BINARY for the pinned formula engine contract")
	}
	raw, err := os.ReadFile("testdata/blank-workbook.json")
	if err != nil {
		t.Fatal(err)
	}
	runner := localFixtureRunner{binary, t.TempDir()}
	snapshot := json.RawMessage(raw)
	for _, step := range []Command{
		{Kind: "cell.set", Sheet: "Sheet1", Cell: "B2", Input: "1"},
		{Kind: "cell.set", Sheet: "Sheet1", Cell: "C9", Input: "=B2*2"},
		{Kind: "cell.set", Sheet: "Sheet1", Cell: "D9", Input: "=C9+1"},
	} {
		out, err := runner.Run(t.Context(), snapshot, step)
		if err != nil {
			t.Fatal(err)
		}
		snapshot = out.Result
	}
	engine := GridCraft{Runner: runner}
	b := Binding{Engine: engine.Identity()}
	candidate, err := engine.Prepare(t.Context(), b, State{Snapshot: snapshot}, Command{Kind: "cell.set", Sheet: "Sheet1", Cell: "B2", Input: "42"})
	if err != nil {
		t.Fatal(err)
	}
	if len(candidate.Effects) != 1 || candidate.Effects[0] != "Sheet1!B2" {
		t.Fatalf("effects=%v", candidate.Effects)
	}
	workbook, err := decodeWorkbook(candidate.Snapshot)
	if err != nil {
		t.Fatal(err)
	}
	cells, err := extractCells(workbook)
	if err != nil {
		t.Fatal(err)
	}
	for address, want := range map[string]float64{"Sheet1!B2": 42, "Sheet1!C9": 84, "Sheet1!D9": 85} {
		value, _ := cells[address].(map[string]any)["value"].(map[string]any)["v"].(json.Number).Float64()
		if value != want {
			t.Fatalf("%s=%v want %v", address, value, want)
		}
	}
	// A mandate for C9 may replace its formula and recalculate D9.
	_, err = engine.Prepare(t.Context(), b, State{Snapshot: candidate.Snapshot}, Command{Kind: "cell.set", Sheet: "Sheet1", Cell: "C9", Input: "=B2*3"})
	if err != nil {
		t.Fatalf("C9 mandate may change C9 and recalculate D9: %v", err)
	}
}

// The worker's verification recomputes from scratch, so a candidate whose cached
// dependent values were altered after the edit is reported, including a chain
// altered consistently.
func TestRealGridCraftRecalcVerificationDetectsTamperedValues(t *testing.T) {
	binary := os.Getenv("CREWSHIP_TEST_GRIDCRAFT_BINARY")
	if binary == "" {
		t.Skip("set CREWSHIP_TEST_GRIDCRAFT_BINARY for the pinned formula engine contract")
	}
	raw, err := os.ReadFile("testdata/blank-workbook.json")
	if err != nil {
		t.Fatal(err)
	}
	runner := localFixtureRunner{binary, t.TempDir()}
	snapshot := json.RawMessage(raw)
	for _, step := range []Command{
		{Kind: "cell.set", Sheet: "Sheet1", Cell: "B2", Input: "42"},
		{Kind: "cell.set", Sheet: "Sheet1", Cell: "C9", Input: "=B2*2"},
		{Kind: "cell.set", Sheet: "Sheet1", Cell: "D9", Input: "=C9+1"},
	} {
		out, err := runner.Run(t.Context(), snapshot, step)
		if err != nil {
			t.Fatal(err)
		}
		if out.Recalc == nil || out.Recalc.Mismatched != 0 || (step.Cell != "B2" && !out.Recalc.verified()) {
			t.Fatalf("honest candidate not verified: %+v", out.Recalc)
		}
		snapshot = out.Result
	}
	for _, test := range []struct {
		name    string
		values  map[string]string
		wantBad int
	}{
		{"honest", nil, 0},
		{"dependent", map[string]string{"2": `{"t":"Number","v":85.0}`}, 1},
		{"consistent chain", map[string]string{"2": `{"t":"Number","v":85.0}`, "3": `{"t":"Number","v":86.0}`}, 2},
		{"type", map[string]string{"3": `{"t":"Text","v":"85"}`}, 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			var workbook map[string]any
			if err := json.Unmarshal(snapshot, &workbook); err != nil {
				t.Fatal(err)
			}
			row := workbook["sheets"].([]any)[0].(map[string]any)["cells"].(map[string]any)["bands"].(map[string]any)["0"].(map[string]any)["rows"].(map[string]any)["8"].(map[string]any)
			for col, value := range test.values {
				row[col].(map[string]any)["value"] = json.RawMessage(value)
			}
			data, err := json.Marshal(workbook)
			if err != nil {
				t.Fatal(err)
			}
			dir := t.TempDir()
			if err = os.WriteFile(filepath.Join(dir, "candidate.json"), data, 0600); err != nil {
				t.Fatal(err)
			}
			got, err := verifyRecalc(t.Context(), binary, dir, "candidate.json")
			if err != nil {
				t.Fatal(err)
			}
			if got.Mismatched != test.wantBad || got.verified() != (test.wantBad == 0) || got.FormulaCells != 2 {
				t.Fatalf("verification=%+v", got)
			}
		})
	}
}
