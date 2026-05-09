package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// Tests for the file-IO + diff-math helpers in cmd_eval_baseline.go.
// HTTP round-trips are tested at the integration layer; unit tests
// here cover the pure logic + on-disk round-trip.

func TestIsValidBaselineName(t *testing.T) {
	good := []string{"main", "v1", "feat-routines-x", "PR_285", "abc123"}
	for _, s := range good {
		if !isValidBaselineName(s) {
			t.Errorf("expected %q to be valid", s)
		}
	}
	bad := []string{
		"",                         // empty
		"with spaces",              // space
		"slash/in/name",            // slash
		"dot.name",                 // dot
		"a" + repeat("b", 64),      // 65 chars > 64 max
	}
	for _, s := range bad {
		if isValidBaselineName(s) {
			t.Errorf("expected %q to be invalid", s)
		}
	}
}

func repeat(s string, n int) string {
	out := ""
	for i := 0; i < n; i++ {
		out += s
	}
	return out
}

func TestPassRate(t *testing.T) {
	cases := []struct {
		pass, total int
		want        float64
	}{
		{0, 0, 0},
		{0, 5, 0},
		{5, 5, 1},
		{3, 10, 0.3},
		{1, 3, 1.0 / 3.0},
	}
	for _, c := range cases {
		got := passRate(c.pass, c.total)
		if got != c.want {
			t.Errorf("passRate(%d,%d) = %v, want %v", c.pass, c.total, got, c.want)
		}
	}
}

func TestMergeUnique_PreservesOrderAndDedups(t *testing.T) {
	a := []string{"alpha", "beta", "gamma"}
	b := []string{"beta", "delta", "alpha"}
	got := mergeUnique(a, b)
	want := []string{"alpha", "beta", "delta", "gamma"}
	if !slicesEqual(got, want) {
		t.Errorf("got %v, want %v (sorted union)", got, want)
	}
}

func TestMergeUnique_EmptyInputs(t *testing.T) {
	if got := mergeUnique(nil, nil); len(got) != 0 {
		t.Errorf("nil inputs should return empty, got %v", got)
	}
	if got := mergeUnique(nil, []string{"x"}); !slicesEqual(got, []string{"x"}) {
		t.Errorf("nil + ['x'] = %v, want ['x']", got)
	}
	if got := mergeUnique([]string{"x"}, nil); !slicesEqual(got, []string{"x"}) {
		t.Errorf("['x'] + nil = %v, want ['x']", got)
	}
}

func TestComputeRegressionRows_VerdictMatrix(t *testing.T) {
	baseline := baselineRecord{
		Name:      "main",
		Scenarios: []string{"a", "b", "removed"},
		Tiers:     []string{"fast"},
		Cells: map[string]baselineCell{
			matrixKey("a", "fast"):       {Pass: 5, Total: 5, AvgCost: 0.001},
			matrixKey("b", "fast"):       {Pass: 5, Total: 5, AvgCost: 0.001},
			matrixKey("removed", "fast"): {Pass: 5, Total: 5, AvgCost: 0.001},
		},
	}
	current := map[string]scenarioCell{
		matrixKey("a", "fast"):   {Pass: 5, Total: 5, AvgCost: 0.001}, // STABLE
		matrixKey("b", "fast"):   {Pass: 2, Total: 5, AvgCost: 0.001}, // REGRESSION (1.0 → 0.4 = -60pp)
		matrixKey("new", "fast"): {Pass: 5, Total: 5, AvgCost: 0.001}, // NEW
		// "removed/fast" missing → REMOVED
	}

	rows := computeRegressionRows(baseline,
		current,
		[]string{"a", "b", "new"},
		[]string{"fast"},
		0.10, // ±10pp tolerance
	)

	verdicts := map[string]string{}
	for _, r := range rows {
		verdicts[r.Scenario] = r.Verdict
	}
	if verdicts["a"] != "STABLE" {
		t.Errorf("a/fast: got %q, want STABLE", verdicts["a"])
	}
	if verdicts["b"] != "REGRESSION" {
		t.Errorf("b/fast: got %q, want REGRESSION", verdicts["b"])
	}
	if verdicts["new"] != "NEW" {
		t.Errorf("new/fast: got %q, want NEW", verdicts["new"])
	}
	if verdicts["removed"] != "REMOVED" {
		t.Errorf("removed/fast: got %q, want REMOVED", verdicts["removed"])
	}
}

func TestComputeRegressionRows_ImprovedVerdict(t *testing.T) {
	baseline := baselineRecord{
		Scenarios: []string{"flaky"},
		Tiers:     []string{"fast"},
		Cells: map[string]baselineCell{
			matrixKey("flaky", "fast"): {Pass: 5, Total: 10},
		},
	}
	current := map[string]scenarioCell{
		matrixKey("flaky", "fast"): {Pass: 9, Total: 10}, // 0.5 → 0.9 = +40pp
	}
	rows := computeRegressionRows(baseline, current, []string{"flaky"}, []string{"fast"}, 0.10)
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	if rows[0].Verdict != "IMPROVED" {
		t.Errorf("got %q, want IMPROVED", rows[0].Verdict)
	}
}

func TestComputeRegressionRows_SkipsCellsMissingInBoth(t *testing.T) {
	// Cross-product allScenarios × allTiers can produce phantom
	// (slug, tier) pairs that exist in NEITHER baseline nor
	// current. Those must NOT emit STABLE rows — that would
	// falsely imply the matrices agree on cells nobody measured.
	// Regression test for the CodeRabbit round-2 finding on
	// cmd_eval_baseline.go:531.
	baseline := baselineRecord{
		Scenarios: []string{"a"},
		Tiers:     []string{"fast"},
		Cells: map[string]baselineCell{
			matrixKey("a", "fast"): {Pass: 5, Total: 5},
		},
	}
	current := map[string]scenarioCell{
		matrixKey("b", "smart"): {Pass: 5, Total: 5},
	}
	// Union of axes: scenarios={a,b}, tiers={fast,smart}.
	// Cross-product → 4 cells. Only 2 were measured (a/fast in
	// baseline, b/smart in current). The other 2 (a/smart, b/fast)
	// must be skipped, not labeled STABLE.
	rows := computeRegressionRows(baseline, current,
		[]string{"a", "b"}, []string{"fast", "smart"}, 0.10)

	if len(rows) != 2 {
		t.Fatalf("expected 2 rows (only the measured cells), got %d: %+v", len(rows), rows)
	}
	wantPresent := map[string]string{
		"a/fast":  "REMOVED", // present in baseline only
		"b/smart": "NEW",     // present in current only
	}
	for _, r := range rows {
		key := r.Scenario + "/" + r.Tier
		want, ok := wantPresent[key]
		if !ok {
			t.Errorf("unexpected row %s with verdict %q (phantom cell?)", key, r.Verdict)
			continue
		}
		if r.Verdict != want {
			t.Errorf("row %s: got verdict %q, want %q", key, r.Verdict, want)
		}
	}
}

func TestComputeRegressionRows_ToleranceBoundary(t *testing.T) {
	// Delta exactly at tolerance threshold should be STABLE,
	// not REGRESSION — strict-less-than comparison matters for
	// CI determinism (a flaky 1pp measurement noise shouldn't
	// trip).
	baseline := baselineRecord{
		Scenarios: []string{"x"},
		Tiers:     []string{"fast"},
		Cells: map[string]baselineCell{
			matrixKey("x", "fast"): {Pass: 9, Total: 10},
		},
	}
	current := map[string]scenarioCell{
		matrixKey("x", "fast"): {Pass: 8, Total: 10}, // 0.9 → 0.8 = -10pp
	}
	// At exactly --tolerance 0.10 → STABLE (delta is -0.1, not < -0.1)
	rows := computeRegressionRows(baseline, current, []string{"x"}, []string{"fast"}, 0.10)
	if rows[0].Verdict != "STABLE" {
		t.Errorf("delta == tolerance should be STABLE, got %q", rows[0].Verdict)
	}
}

func TestCellsToBaseline_RoundTrip(t *testing.T) {
	matrix := map[string]scenarioCell{
		matrixKey("a", "fast"):  {Pass: 3, Total: 5, AvgCost: 0.012, AvgMs: 1500},
		matrixKey("b", "smart"): {Pass: 5, Total: 5, AvgCost: 0.045, AvgMs: 5500},
	}
	out := cellsToBaseline(matrix)
	if len(out) != len(matrix) {
		t.Fatalf("size mismatch: got %d, want %d", len(out), len(matrix))
	}
	a := out[matrixKey("a", "fast")]
	if a.Pass != 3 || a.Total != 5 || a.AvgCost != 0.012 || a.AvgMs != 1500 {
		t.Errorf("round-trip a/fast: got %+v", a)
	}
}

func TestBaselineFile_RoundTrip(t *testing.T) {
	// On-disk save+load must produce a byte-identical record.
	// Ensures the JSON schema is stable across versions and
	// nothing critical got dropped during marshal/unmarshal.
	tmpdir := t.TempDir()
	t.Setenv("HOME", tmpdir)

	rec := baselineRecord{
		Name:        "test-baseline",
		GeneratedAt: "2026-05-08T12:00:00Z",
		WorkspaceID: "ws_test",
		Scenarios:   []string{"a", "b"},
		Tiers:       []string{"fast", "smart"},
		RunsPerCell: 5,
		Cells: map[string]baselineCell{
			matrixKey("a", "fast"):  {Pass: 5, Total: 5, AvgCost: 0.012, AvgMs: 1500},
			matrixKey("a", "smart"): {Pass: 5, Total: 5, AvgCost: 0.045, AvgMs: 4500},
			matrixKey("b", "fast"):  {Pass: 3, Total: 5, AvgCost: 0.011, AvgMs: 1700},
		},
	}

	path, err := baselinePath(rec.Name)
	if err != nil {
		t.Fatalf("baselinePath: %v", err)
	}
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Verify file landed in the expected place
	wantDir := filepath.Join(tmpdir, ".crewship", "eval-baselines")
	if dir := filepath.Dir(path); dir != wantDir {
		t.Errorf("baseline path dir = %q, want %q", dir, wantDir)
	}

	// Round-trip read
	read, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var got baselineRecord
	if err := json.Unmarshal(read, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Name != rec.Name || got.RunsPerCell != rec.RunsPerCell {
		t.Errorf("round-trip mismatch: got name=%q runs=%d, want %q runs=%d",
			got.Name, got.RunsPerCell, rec.Name, rec.RunsPerCell)
	}
	if len(got.Cells) != len(rec.Cells) {
		t.Errorf("cell count mismatch: got %d, want %d", len(got.Cells), len(rec.Cells))
	}
	for k, want := range rec.Cells {
		gotCell, ok := got.Cells[k]
		if !ok {
			t.Errorf("missing key %q", k)
			continue
		}
		if gotCell != want {
			t.Errorf("key %q: got %+v, want %+v", k, gotCell, want)
		}
	}
}

func TestBaselinePath_RejectsInvalidName(t *testing.T) {
	if _, err := baselinePath("../etc/passwd"); err == nil {
		t.Error("expected path-traversal name to be rejected")
	}
	if _, err := baselinePath(""); err == nil {
		t.Error("expected empty name to be rejected")
	}
	if _, err := baselinePath("name with spaces"); err == nil {
		t.Error("expected space-containing name to be rejected")
	}
}
