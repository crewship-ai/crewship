package appengine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/crewship-ai/crewship/internal/database"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

type localFixtureRunner struct{ binary, scratch string }

func (r localFixtureRunner) Run(ctx context.Context, input []byte, c Command) (EngineResult, error) {
	return RunGridCraftWorker(ctx, r.binary, r.scratch, input, c)
}
func TestRealGridCraftImportEditRestartAndRetry(t *testing.T) {
	binary := os.Getenv("CREWSHIP_TEST_GRIDCRAFT_BINARY")
	if binary == "" {
		// SKIP-WAIVER(#3967): requires the pinned Linux engine; worker CI verifies all ten real contracts without skips.
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
		// SKIP-WAIVER(#3967): requires the pinned Linux engine; worker CI verifies all ten real contracts without skips.
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
		// SKIP-WAIVER(#3967): requires the pinned Linux engine; worker CI verifies all ten real contracts without skips.
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
			got, err := verifyRecalc(t.Context(), binary, t.TempDir(), data)
			if err != nil {
				t.Fatal(err)
			}
			sum := sha256.Sum256(data)
			if got.Mismatched != test.wantBad || got.verified() != (test.wantBad == 0) || got.FormulaCells != 2 || got.SHA256 != hex.EncodeToString(sum[:]) {
				t.Fatalf("verification=%+v", got)
			}
		})
	}
}

// largeWorkbook fills the blank fixture with n numeric cells in ten columns.
func largeWorkbook(t *testing.T, n int) []byte {
	t.Helper()
	raw, err := os.ReadFile("testdata/blank-workbook.json")
	if err != nil {
		t.Fatal(err)
	}
	var workbook map[string]any
	if err = json.Unmarshal(raw, &workbook); err != nil {
		t.Fatal(err)
	}
	bands := map[string]any{}
	for i := 0; i < n; i++ {
		row, col := i/10, i%10
		band := strconv.Itoa(row / 64)
		if bands[band] == nil {
			bands[band] = map[string]any{"rows": map[string]any{}}
		}
		rows := bands[band].(map[string]any)["rows"].(map[string]any)
		if rows[strconv.Itoa(row)] == nil {
			rows[strconv.Itoa(row)] = map[string]any{}
		}
		rows[strconv.Itoa(row)].(map[string]any)[strconv.Itoa(col)] = map[string]any{"value": map[string]any{"t": "Number", "v": json.Number(strconv.Itoa(i) + ".5")}, "formula": nil, "style": 0}
	}
	workbook["sheets"].([]any)[0].(map[string]any)["cells"] = map[string]any{"bands": bands, "len": n}
	out, err := json.Marshal(workbook)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// A 6k-cell sheet stays editable through the pinned engine even though its
// pretty-printed engine output exceeds the stored snapshot limit.
func TestRealGridCraftLargeSheetWithinCompactLimit(t *testing.T) {
	binary := os.Getenv("CREWSHIP_TEST_GRIDCRAFT_BINARY")
	if binary == "" {
		// SKIP-WAIVER(#3967): requires the pinned Linux engine; worker CI verifies all ten real contracts without skips.
		t.Skip("set CREWSHIP_TEST_GRIDCRAFT_BINARY for the pinned engine size contract")
	}
	raw := largeWorkbook(t, 6000)
	engine := GridCraft{Runner: localFixtureRunner{binary, t.TempDir()}}
	b := Binding{Engine: engine.Identity()}
	imported, err := engine.Prepare(t.Context(), b, State{}, Command{Kind: "import", Snapshot: raw})
	if err != nil {
		t.Fatal(err)
	}
	edited, err := engine.Prepare(t.Context(), b, State{Snapshot: imported.Snapshot}, Command{Kind: "cell.set", Sheet: "Sheet1", Cell: "B2", Input: "42"})
	if err != nil {
		t.Fatal(err)
	}
	if len(edited.Snapshot) > MaxSnapshotBytes || len(edited.Effects) != 1 || edited.Effects[0] != "Sheet1!B2" {
		t.Fatalf("size=%d effects=%v", len(edited.Snapshot), edited.Effects)
	}
}

// The worker's report names the exact bytes it returns, checked from a fresh
// private directory rather than the engine's own output file.
func TestRealGridCraftReportBindsReturnedBytes(t *testing.T) {
	binary := os.Getenv("CREWSHIP_TEST_GRIDCRAFT_BINARY")
	if binary == "" {
		// SKIP-WAIVER(#3967): requires the pinned Linux engine; worker CI verifies all ten real contracts without skips.
		t.Skip("set CREWSHIP_TEST_GRIDCRAFT_BINARY for the pinned formula engine contract")
	}
	raw, err := os.ReadFile("testdata/blank-workbook.json")
	if err != nil {
		t.Fatal(err)
	}
	scratch := t.TempDir()
	out, err := RunGridCraftWorker(t.Context(), binary, scratch, raw, Command{Kind: "cell.set", Sheet: "Sheet1", Cell: "C9", Input: "=B2*2"})
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(out.Result)
	if out.Recalc == nil || out.Recalc.SHA256 != hex.EncodeToString(sum[:]) || !out.Recalc.verified() {
		t.Fatalf("report=%+v", out.Recalc)
	}
	if left, _ := filepath.Glob(filepath.Join(scratch, "*")); len(left) != 0 {
		t.Fatalf("scratch survived: %v", left)
	}
}

// With iterative calculation the oracle accepts any fixed point of a cycle, so a
// forged consistent cycle passes it. The host must refuse derived changes there.
func TestRealGridCraftIterativeCycleRejectsDerivedChanges(t *testing.T) {
	binary := os.Getenv("CREWSHIP_TEST_GRIDCRAFT_BINARY")
	if binary == "" {
		// SKIP-WAIVER(#3967): requires the pinned Linux engine; worker CI verifies all ten real contracts without skips.
		t.Skip("set CREWSHIP_TEST_GRIDCRAFT_BINARY for the pinned formula engine contract")
	}
	raw, err := os.ReadFile("testdata/blank-workbook.json")
	if err != nil {
		t.Fatal(err)
	}
	var blank map[string]any
	if err = json.Unmarshal(raw, &blank); err != nil {
		t.Fatal(err)
	}
	blank["calc"].(map[string]any)["iterative"] = true
	snapshot, err := json.Marshal(blank)
	if err != nil {
		t.Fatal(err)
	}
	runner := localFixtureRunner{binary, t.TempDir()}
	for _, step := range []Command{
		{Kind: "cell.set", Sheet: "Sheet1", Cell: "B2", Input: "1"},
		{Kind: "cell.set", Sheet: "Sheet1", Cell: "C9", Input: "=B2*2"},
		{Kind: "cell.set", Sheet: "Sheet1", Cell: "D1", Input: "=E1"},
		{Kind: "cell.set", Sheet: "Sheet1", Cell: "E1", Input: "=D1"},
		{Kind: "cell.set", Sheet: "Sheet1", Cell: "F1", Input: "=D1*1000"},
	} {
		out, err := runner.Run(t.Context(), snapshot, step)
		if err != nil {
			t.Fatal(err)
		}
		snapshot = out.Result
	}
	engine := GridCraft{Runner: runner}
	b := Binding{Engine: engine.Identity()}
	// An honest dependent recalculation is refused under iterative calculation.
	if _, err = engine.Prepare(t.Context(), b, State{Snapshot: snapshot}, Command{Kind: "cell.set", Sheet: "Sheet1", Cell: "B2", Input: "42"}); !errors.Is(err, ErrEffects) {
		t.Fatalf("iterative recalculation: %v", err)
	}
	// A forged fixed point passes the oracle itself...
	// Decode numbers exactly so only the forged values differ.
	workbook, err := decodeWorkbook(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	row := workbook["sheets"].([]any)[0].(map[string]any)["cells"].(map[string]any)["bands"].(map[string]any)["0"].(map[string]any)["rows"].(map[string]any)["0"].(map[string]any)
	for col, value := range map[string]string{"3": "123456", "4": "123456", "5": "123456000"} {
		row[col].(map[string]any)["value"] = map[string]any{"t": "Number", "v": json.Number(value)}
	}
	forged, err := json.Marshal(workbook)
	if err != nil {
		t.Fatal(err)
	}
	report, err := verifyRecalc(t.Context(), binary, t.TempDir(), forged)
	if err != nil || !report.verified() {
		t.Fatalf("expected the oracle to accept the forged fixed point: %+v %v", report, err)
	}
	// ...so the adapter must refuse it even with a matching report.
	forging := runnerFunc(func(ctx context.Context, in []byte, c Command) (EngineResult, error) {
		out, err := runner.Run(ctx, in, c)
		if err != nil {
			return out, err
		}
		return EngineResult{Base: out.Base, Result: forged, Recalc: report}, nil
	})
	engine = GridCraft{Runner: forging}
	if _, err = engine.Prepare(t.Context(), b, State{Snapshot: snapshot}, Command{Kind: "cell.set", Sheet: "Sheet1", Cell: "B2", Input: "1"}); !errors.Is(err, ErrEffects) {
		t.Fatalf("forged fixed point: %v", err)
	}
	// Without any other cycle, so only the target itself is in question.
	simple, err := json.Marshal(blank)
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range []Command{
		{Kind: "cell.set", Sheet: "Sheet1", Cell: "B2", Input: "1"},
		{Kind: "cell.set", Sheet: "Sheet1", Cell: "C9", Input: "=B2*2"},
	} {
		out, err := runner.Run(t.Context(), simple, step)
		if err != nil {
			t.Fatal(err)
		}
		simple = out.Result
	}
	// A self-referencing target formula is a cycle too: any forged cached value
	// is a fixed point, so a target formula needs non-iterative calculation.
	forgeTarget := runnerFunc(func(ctx context.Context, in []byte, c Command) (EngineResult, error) {
		out, err := runner.Run(ctx, in, c)
		if err != nil {
			return out, err
		}
		candidate, err := decodeWorkbook(out.Result)
		if err != nil {
			return out, err
		}
		rows := candidate["sheets"].([]any)[0].(map[string]any)["cells"].(map[string]any)["bands"].(map[string]any)["0"].(map[string]any)["rows"].(map[string]any)
		rows["8"].(map[string]any)["2"].(map[string]any)["value"] = map[string]any{"t": "Number", "v": json.Number("4242")}
		forged, err := json.Marshal(candidate)
		if err != nil {
			return out, err
		}
		report, err := verifyRecalc(ctx, binary, t.TempDir(), forged)
		if err != nil {
			return out, err
		}
		if !report.verified() {
			t.Fatalf("expected the oracle to accept the forged target fixed point: %+v", report)
		}
		return EngineResult{Base: out.Base, Result: forged, Recalc: report}, nil
	})
	engine = GridCraft{Runner: forgeTarget}
	if _, err = engine.Prepare(t.Context(), b, State{Snapshot: simple}, Command{Kind: "cell.set", Sheet: "Sheet1", Cell: "C9", Input: "=C9"}); !errors.Is(err, ErrEffects) {
		t.Fatalf("forged target fixed point: %v", err)
	}
}
