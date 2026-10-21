package mooring

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
