package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

// TestEvalBaselineSave_NeverObservableTorn is the #2124 regression test for
// `eval baseline save`, in the shape #1999 used for progress.jsonl.
//
// The baseline is not a `-o` path the operator named: `save <name>` derives
// ~/.crewship/eval-baselines/<name>.json itself, and `eval baseline diff
// <name>` — run later, typically in CI with a non-zero exit on regression —
// reads it back as the reference. `save` used to overwrite it with
// os.WriteFile, which truncates before it writes, so an interrupted re-save
// of "main" destroyed the previous good baseline and left a torn file for
// every later diff to choke on.
//
// The invariant asserted: the file at baselinePath is only ever the whole
// previous record or the whole new one. Verified to FAIL against the
// pre-fix os.WriteFile implementation.
func TestEvalBaselineSave_NeverObservableTorn(t *testing.T) {
	home := covHomeDir(t)
	stub := covStub(t)
	covResetFlags(t, evalBaselineSaveCmd)
	runPath := "/api/v1/workspaces/" + covWSCli3 + "/pipelines/eval-a/run"
	stub.OnPost(runPath, clitest.JSONResponse(200, map[string]any{
		"run_id": "r1", "status": "COMPLETED", "duration_ms": 120, "cost_usd": 0.002,
	}))
	covSetFlags(t, evalBaselineSaveCmd, map[string]string{
		"scenarios": "eval-a", "tiers": "fast", "runs": "1",
	})

	// Every round re-saves an existing baseline, so the previous record has
	// to be recognisable: a parseable document with a known name is "whole
	// old", the freshly written record is "whole new", anything else — zero
	// bytes, a JSON prefix — is torn.
	isWhole := func(data []byte, name string) bool {
		var rec baselineRecord
		if json.Unmarshal(data, &rec) != nil {
			return false
		}
		return rec.Name == name
	}

	// 150 rounds: each round is a stubbed HTTP sweep plus the write, and the
	// pre-fix window (open O_TRUNC, then write) was hit in a few percent of
	// rounds on a loaded box; 1-0.97^150 > 98%.
	const iterations = 150
	tornObservations := 0
	for i := range iterations {
		name := "main-" + strconv.Itoa(i)
		path := covWriteBaseline(t, home, baselineRecord{
			Name: name, GeneratedAt: "2026-01-01T00:00:00Z", WorkspaceID: covWSCli3,
			Scenarios: []string{"eval-a"}, Tiers: []string{"fast"}, RunsPerCell: 1,
			Cells: map[string]baselineCell{matrixKey("eval-a", "fast"): {Pass: 1, Total: 1}},
		})

		var wg sync.WaitGroup
		stop := make(chan struct{})
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				// os.ReadFile + json.Unmarshal is exactly what diff does.
				data, err := os.ReadFile(path)
				if err != nil {
					continue
				}
				if !isWhole(data, name) {
					tornObservations++
					return
				}
			}
		}()

		err := evalBaselineSaveCmd.RunE(evalBaselineSaveCmd, []string{name})
		close(stop)
		wg.Wait()
		if err != nil {
			t.Fatalf("eval baseline save: %v", err)
		}
	}

	if tornObservations != 0 {
		t.Errorf("a baseline was observed torn (empty or partial) in %d/%d re-saves — "+
			"the O_TRUNC window is back; eval baseline save must publish via "+
			"memory.WriteFileDurable's atomic rename",
			tornObservations, iterations)
	}

	// `eval baseline list` globs this directory and shows every *.json as a
	// baseline; a leftover main.json.tmp.* would be a phantom entry.
	entries, err := os.ReadDir(filepath.Join(home, ".crewship", "eval-baselines"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if filepath.Ext(e.Name()) != ".json" {
			t.Errorf("leftover file %q in the baseline store", e.Name())
		}
	}
}
