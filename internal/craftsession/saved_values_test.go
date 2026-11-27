package craftsession

import (
	"context"
	"slices"
	"sync"
	"testing"

	"github.com/crewship-ai/crewship/internal/craftengine"
)

// recordingMgr is newMgr whose launcher remembers the engine arguments of every start.
func recordingMgr(t *testing.T, savedValues bool) (*Manager, func() [][]string) {
	var mu sync.Mutex
	var starts [][]string
	m := newMgr(t, func(c *Config) {
		inner := c.Launch
		c.OpenSavedValues = savedValues
		c.Launch = func(ctx context.Context, dir string, args []string) (*craftengine.Worker, error) {
			mu.Lock()
			starts = append(starts, append([]string(nil), args...))
			mu.Unlock()
			return inner(ctx, dir, args)
		}
	})
	return m, func() [][]string { mu.Lock(); defer mu.Unlock(); return append([][]string(nil), starts...) }
}

// A document this server's engine wrote after recalculating (an import, or a snapshot of an
// automatic workbook) opens with its saved values; anything else is recalculated on open.
func TestSessionOpensOnlyItsOwnEnginesDocumentsWithoutRecalculating(t *testing.T) {
	ctx := context.Background()
	m, starts := recordingMgr(t, true)
	s, release, err := m.Acquire(ctx, "doc1", doc(""))
	if err != nil {
		t.Fatal(err)
	}
	if err = s.SetCell(ctx, "A1", "5"); err != nil {
		t.Fatal(err)
	}
	snap, err := s.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	release()
	s.Close()
	if _, rel, err := m.Acquire(ctx, "doc2", func() ([]byte, error) { return snap, nil }); err != nil {
		t.Fatal(err)
	} else {
		rel()
	}
	foreign := append([]byte(nil), snap...)
	foreign = append(foreign[:len(foreign)-1], []byte(`,"x":1}`)...)
	if _, rel, err := m.Acquire(ctx, "doc3", func() ([]byte, error) { return foreign, nil }); err != nil {
		t.Fatal(err)
	} else {
		rel()
	}
	got := starts()
	if len(got) != 3 || slices.Contains(got[0], "--saved-values") || !slices.Contains(got[1], "--saved-values") || slices.Contains(got[2], "--saved-values") {
		t.Fatalf("engine starts: %q", got)
	}
}

// Off (the default, and for an engine without the option), every document is recalculated.
func TestSessionRecalculatesEveryDocumentWhenSavedValuesAreOff(t *testing.T) {
	ctx := context.Background()
	m, starts := recordingMgr(t, false)
	s, release, _ := m.Acquire(ctx, "doc1", doc(""))
	snap, err := s.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	release()
	s.Close()
	if _, rel, err := m.Acquire(ctx, "doc2", func() ([]byte, error) { return snap, nil }); err == nil {
		rel()
	}
	for _, a := range starts() {
		if slices.Contains(a, "--saved-values") {
			t.Fatalf("%q", a)
		}
	}
}

// A snapshot of a workbook in manual calculation may hold values no recalculation made.
func TestASnapshotInManualCalculationIsNotTrusted(t *testing.T) {
	ctx := context.Background()
	m, starts := recordingMgr(t, true)
	s, release, _ := m.Acquire(ctx, "doc1", doc(""))
	if _, err := s.call(ctx, "execute_command", map[string]any{"command": "formulas.calculationOptions", "params": map[string]any{"mode": "manual"}}); err != nil {
		t.Fatal(err)
	}
	snap, err := s.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	release()
	s.Close()
	if _, rel, err := m.Acquire(ctx, "doc2", func() ([]byte, error) { return snap, nil }); err == nil {
		rel()
	}
	if got := starts(); len(got) != 2 || slices.Contains(got[1], "--saved-values") {
		t.Fatalf("%q", got)
	}
}

// Saved values are on by default only for an engine known to take mcp --saved-values; for any
// other engine (an older pin) they are off and sessions open as before. CREWSHIP_CRAFT_SAVED_VALUES=0
// turns them off for every engine.
func TestSavedValuesAreOnOnlyForAnEngineThatHasThem(t *testing.T) {
	const newEngine = "c2959f753baa75f69a70b2d074ba285e58466bb849fcb98c057819ae284c1000"
	const oldEngine = "e64af2d4b201520b367da693c6c63824976032e854eb3a27e0cbe239286353c6"
	t.Setenv("CREWSHIP_CRAFT_SAVED_VALUES", "")
	if !SavedValuesFor(newEngine) || SavedValuesFor(oldEngine) || SavedValuesFor("") {
		t.Fatal("default by engine")
	}
	t.Setenv("CREWSHIP_CRAFT_SAVED_VALUES", "0")
	if SavedValuesFor(newEngine) {
		t.Fatal("the switch did not turn it off")
	}
	// An older pin: the session opens, without the option.
	t.Setenv("CREWSHIP_CRAFT_SAVED_VALUES", "")
	m, starts := recordingMgr(t, SavedValuesFor(oldEngine))
	ctx := context.Background()
	s, release, err := m.Acquire(ctx, "doc1", doc(""))
	if err != nil {
		t.Fatal(err)
	}
	snap, err := s.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	release()
	s.Close()
	_, rel, err := m.Acquire(ctx, "doc2", func() ([]byte, error) { return snap, nil })
	if err != nil {
		t.Fatalf("an older engine's session did not open: %v", err)
	}
	rel()
	for _, a := range starts() {
		if slices.Contains(a, "--saved-values") {
			t.Fatalf("%q", a)
		}
	}
}

// Validation without recalculation needs an engine whose convert takes --saved-values.
func TestSavedValidationIsOnOnlyForAnEngineWhoseConvertHasIt(t *testing.T) {
	t.Setenv("CREWSHIP_CRAFT_SAVED_VALUES", "")
	const both = "52acf14ac2a4c7386a7333f9d18e548ac24b089c02afd47c71ace7e1540d0bc3"
	const mcpOnly = "c2959f753baa75f69a70b2d074ba285e58466bb849fcb98c057819ae284c1000"
	if !SavedValuesFor(both) || !SavedValidationFor(both) || !SavedValuesFor(mcpOnly) || SavedValidationFor(mcpOnly) {
		t.Fatal("engine capabilities")
	}
	// the engine that keeps the value Excel calculated for a function it lacks (catalog 57f7620f…)
	if held := "90e7108286d7ef409604ddbfd062add72389dd70a1364417470684d8fa9af270"; !SavedValuesFor(held) || !SavedValidationFor(held) {
		t.Fatal("held-values engine capabilities")
	}
	t.Setenv("CREWSHIP_CRAFT_SAVED_VALUES", "0")
	if SavedValidationFor(both) {
		t.Fatal("the switch did not turn it off")
	}
}
