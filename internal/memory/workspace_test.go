package memory

import (
	"context"
	"os"
	"path/filepath"
	"testing"
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

	block, used := wm.GetContext(context.Background(), 5000)

	if block == "" {
		t.Error("GetContext should return non-empty block")
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

	block, used := wm.GetContext(context.Background(), 5000)

	if block != "" {
		t.Errorf("empty workspace should return empty block, got %q", block)
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
	block, used := wm.GetContext(context.Background(), 500)

	if used > 600 { // allow some margin for markers
		t.Errorf("used chars (%d) should be near budget (500)", used)
	}
	if !containsStr(block, "truncated") {
		t.Error("should contain truncation marker")
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
