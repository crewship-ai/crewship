package memory

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"unicode/utf8"
)

func TestWorkspaceMemory_Init(t *testing.T) {
	dir := t.TempDir()

	// Write workspace memory files
	os.MkdirAll(filepath.Join(dir, "crews"), 0o755)
	os.WriteFile(filepath.Join(dir, "WORKSPACE.md"), []byte("# Workspace\n## Strategy\nFocus on developer tools."), 0o644)
	os.WriteFile(filepath.Join(dir, "crews", "dev.md"), []byte("# Dev Crew\nShipped 5 features this month."), 0o644)

	wm, err := NewWorkspaceMemory(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer wm.Close()

	// Should be searchable after init (reindexes on creation)
	results, err := wm.Search("developer tools", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 {
		t.Error("workspace memory should find 'developer tools'")
	}

	// Should find crew summaries
	results, err = wm.Search("features", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 {
		t.Error("workspace memory should find 'features' from crew summary")
	}
}

func TestWorkspaceMemory_GetContext(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "WORKSPACE.md"), []byte("# Workspace\nOrg-wide policy: all deploys require approval."), 0o644)

	wm, err := NewWorkspaceMemory(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer wm.Close()

	block, used, incomplete := wm.GetContext(context.Background(), 5000)

	if block == "" {
		t.Error("GetContext should return non-empty block")
	}
	if incomplete {
		t.Error("an uncancelled context should never yield incomplete=true")
	}
	if used <= 0 {
		t.Error("used chars should be > 0")
	}
	if used > 5000 {
		t.Errorf("used chars (%d) should not exceed budget (5000)", used)
	}

	// GetContext now returns raw content; framing is the orchestrator's
	// job (assembleSections in buildWorkspaceMemoryBlock). The marker
	// strings must NOT appear here — if they did the orchestrator's
	// wrapper would nest them on every render.
	if containsStr(block, "[WORKSPACE MEMORY]") {
		t.Error("GetContext must not include the [WORKSPACE MEMORY] marker — framing is the orchestrator's job")
	}
	if containsStr(block, "[END WORKSPACE MEMORY]") {
		t.Error("GetContext must not include the [END WORKSPACE MEMORY] marker")
	}
	if !containsStr(block, "all deploys require approval") {
		t.Error("missing workspace content")
	}
}

func TestWorkspaceMemory_Empty(t *testing.T) {
	dir := t.TempDir() // empty dir

	wm, err := NewWorkspaceMemory(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer wm.Close()

	block, used, incomplete := wm.GetContext(context.Background(), 5000)

	if block != "" {
		t.Errorf("empty workspace should return empty block, got %q", block)
	}
	if incomplete {
		t.Error("an uncancelled context should never yield incomplete=true")
	}
	if used != 0 {
		t.Errorf("empty workspace should use 0 chars, got %d", used)
	}
}

func TestWorkspaceMemory_BudgetTruncation(t *testing.T) {
	dir := t.TempDir()

	// Write a large workspace file
	bigContent := "# Workspace\n" + repeatStr("Policy detail line. ", 200)
	os.WriteFile(filepath.Join(dir, "WORKSPACE.md"), []byte(bigContent), 0o644)

	wm, err := NewWorkspaceMemory(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer wm.Close()

	// Tiny budget → should truncate
	block, used, _ := wm.GetContext(context.Background(), 500)

	if used > 600 { // allow some margin for markers
		t.Errorf("used chars (%d) should be near budget (500)", used)
	}
	if !containsStr(block, "truncated") {
		t.Error("should contain truncation marker")
	}
}

// countdownDoneCtx is a context.Context whose Done() channel only starts
// reporting "cancelled" after being polled fireAfter times. GetContext's
// filepath.Walk callback checks ctx.Done() once per visited file via a
// non-blocking select, so this lets a test deterministically land the
// cancellation partway through a multi-file walk — reproducing a real
// ctx.WithTimeout firing mid-scan — without depending on wall-clock
// timing, which would make the test flaky under CI load.
type countdownDoneCtx struct {
	context.Context
	n         int32
	fireAfter int32
	done      chan struct{}
}

func (c *countdownDoneCtx) Done() <-chan struct{} {
	if atomic.AddInt32(&c.n, 1) >= c.fireAfter {
		return c.done
	}
	return nil // never-ready channel: select falls through to default
}

// TestWorkspaceMemory_GetContext_TimeoutMidWalk_ReportsIncomplete is the
// #1637 finding-2 regression: GetContext's filepath.Walk aborts on
// ctx.Done() via `case <-ctx.Done(): return filepath.SkipAll`, handing
// back whatever partial file set it already had — previously with no
// signal at all that the read was cut short. A slow or stalled workspace
// filesystem then reads to the orchestrator as complete-but-small rather
// than partial, and the [MEMORY BUDGET] meter tells the model it saw
// everything when it did not.
func TestWorkspaceMemory_GetContext_TimeoutMidWalk_ReportsIncomplete(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 5; i++ {
		content := fmt.Sprintf("# Note %d\nSome workspace content here.", i)
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("note%d.md", i)), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	wm, err := NewWorkspaceMemory(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer wm.Close()

	// Discover how many filesystem entries filepath.Walk visits before it
	// reaches the FIRST .md file — the FTS5 engine's own index files
	// (index.sqlite, -shm, -wal) sort before "noteN.md" alphabetically
	// and get visited first, so this can't be a hardcoded constant
	// without coupling the test to the engine's internal file count.
	var total, firstMD int
	if err := filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		total++
		if firstMD == 0 && strings.HasSuffix(p, ".md") {
			firstMD = total
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if firstMD == 0 || firstMD >= total {
		t.Fatalf("test setup: no room to abort after the first .md file (total=%d firstMD=%d)", total, firstMD)
	}

	closedCh := make(chan struct{})
	close(closedCh)
	// Abort right after the first .md file lands, so at least one file
	// is collected before the abort — mirroring the reviewer's "partial
	// file set" reproduction rather than the trivial zero-files case.
	cctx := &countdownDoneCtx{Context: context.Background(), fireAfter: int32(firstMD + 1), done: closedCh}

	content, used, incomplete := wm.GetContext(cctx, 100000)

	if !incomplete {
		t.Fatal("expected incomplete=true when the walk is aborted by ctx.Done() mid-scan")
	}
	if content == "" || used == 0 {
		t.Errorf("expected some partial content collected before the abort, got content=%q used=%d", content, used)
	}
	// Not all 5 notes should have made it in — that's the whole point of
	// aborting mid-walk. This keeps the test honest: if a future change
	// makes the walk finish before the countdown fires, this catches it
	// rather than silently passing on a no-op abort.
	full := "# Note 0\nSome workspace content here.\n# Note 1\nSome workspace content here.\n" +
		"# Note 2\nSome workspace content here.\n# Note 3\nSome workspace content here.\n# Note 4\nSome workspace content here."
	if len(content) >= len(full) {
		t.Errorf("expected an incomplete scan to collect fewer than all 5 files, got %d bytes (full would be ~%d)", len(content), len(full))
	}
}

// TestWorkspaceMemory_GetContext_NoCancellation_NeverIncomplete guards the
// countdownDoneCtx-based test above against a false positive: a normal,
// uncancelled context walking the same five files must finish and must
// not report incomplete.
func TestWorkspaceMemory_GetContext_NoCancellation_NeverIncomplete(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 5; i++ {
		content := fmt.Sprintf("# Note %d\nSome workspace content here.", i)
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("note%d.md", i)), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	wm, err := NewWorkspaceMemory(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer wm.Close()

	content, used, incomplete := wm.GetContext(context.Background(), 100000)
	if incomplete {
		t.Error("an uncancelled walk over a small directory must not report incomplete")
	}
	for i := 0; i < 5; i++ {
		want := fmt.Sprintf("Note %d", i)
		if !containsStr(content, want) {
			t.Errorf("expected complete scan to include %q, got %q", want, content)
		}
	}
	if used == 0 {
		t.Error("expected non-zero used for a complete scan with content")
	}
}

// TestWorkspaceMemory_GetContext_TruncationNeverSplitsUTF8Rune is the
// #1637 finding-1 regression for GetContext's own truncation branch
// (separate from assembleSectionsEmitted's, which internal/orchestrator
// covers): workspace files are markdown authored by agents and commonly
// carry multi-byte text (this product carries Czech throughout), and the
// old `section[:remaining]` was a raw byte slice with no rune-boundary
// awareness. Sweeping a range of tight budgets over dense multi-byte
// content reproduces the reviewer's "cut lands inside a lead byte's
// continuation" failure for at least one budget in range if the fix
// regresses.
func TestWorkspaceMemory_GetContext_TruncationNeverSplitsUTF8Rune(t *testing.T) {
	dir := t.TempDir()
	czech := "Příliš žluťoučký kůň úpěl ďábelské ódy. Zvlášť tři přátelé škrábali stůl."
	content := ""
	for len(content) < 4000 {
		content += czech + " "
	}
	if err := os.WriteFile(filepath.Join(dir, "WORKSPACE.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	wm, err := NewWorkspaceMemory(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer wm.Close()

	for budget := 80; budget < 600; budget++ {
		block, _, _ := wm.GetContext(context.Background(), budget)
		if !utf8.ValidString(block) {
			t.Fatalf("budget=%d produced invalid UTF-8:\n%q", budget, block)
		}
	}
}

func containsStr(s, substr string) bool {
	return len(s) > 0 && len(substr) > 0 && filepath.Base(s) != "" && // avoid import confusion
		findStr(s, substr)
}

func findStr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func repeatStr(s string, n int) string {
	result := ""
	for i := 0; i < n; i++ {
		result += s
	}
	return result
}
