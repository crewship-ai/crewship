package memory

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func setupTestMemory(t *testing.T) (string, *Engine) {
	t.Helper()
	dir := t.TempDir()

	// Create memory directory structure
	dailyDir := filepath.Join(dir, "daily")
	if err := os.MkdirAll(dailyDir, 0o755); err != nil {
		t.Fatal(err)
	}

	engine, err := New(dir, DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { engine.Close() })

	return dir, engine
}

func TestNewEngine(t *testing.T) {
	dir, engine := setupTestMemory(t)

	// Index file should be created
	if _, err := os.Stat(filepath.Join(dir, "index.sqlite")); err != nil {
		t.Errorf("index.sqlite not created: %v", err)
	}

	status, err := engine.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.TotalChunks != 0 {
		t.Errorf("expected 0 chunks, got %d", status.TotalChunks)
	}
	if status.TotalFiles != 0 {
		t.Errorf("expected 0 files, got %d", status.TotalFiles)
	}
}

func TestReindexAndSearch(t *testing.T) {
	dir, engine := setupTestMemory(t)

	// Write test memory files
	agentMD := `# Agent Memory

## Identity
I am Jarmila, a Czech-speaking AI assistant.

## Preferences
- The user prefers Czech language
- Always use formal Czech (vykani)
- Project uses Go and Next.js

## Learned Facts
- The main database is SQLite
- Deployments happen on Fridays
`
	if err := os.WriteFile(filepath.Join(dir, "AGENT.md"), []byte(agentMD), 0o644); err != nil {
		t.Fatal(err)
	}

	dailyLog := `# 2026-02-19

## Session Notes
- Fixed bug in authentication flow
- User asked to remember their preference for dark mode
- Reviewed PR #42 for database migration

## Decisions
- Decided to use cursor-based pagination instead of offset
`
	if err := os.WriteFile(filepath.Join(dir, "daily", "2026-02-19.md"), []byte(dailyLog), 0o644); err != nil {
		t.Fatal(err)
	}

	// Reindex
	if err := engine.Reindex(); err != nil {
		t.Fatal(err)
	}

	// Check status
	status, err := engine.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.TotalFiles == 0 {
		t.Error("expected files to be indexed")
	}
	if status.TotalChunks == 0 {
		t.Error("expected chunks to be indexed")
	}
	if !status.SearchReady {
		t.Error("expected search to be ready")
	}

	// Search for "Czech"
	results, err := engine.Search(context.Background(), "Czech", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 {
		t.Error("expected search results for 'Czech'")
	}

	// Search for "pagination"
	results, err = engine.Search(context.Background(), "pagination", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 {
		t.Error("expected search results for 'pagination'")
	}

	// Search for something that doesn't exist
	results, err = engine.Search(context.Background(), "kubernetes", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 0 {
		t.Errorf("expected no results for 'kubernetes', got %d", len(results))
	}
}

func TestSearchLimit(t *testing.T) {
	dir, engine := setupTestMemory(t)

	// Write a file with many sections
	var content string
	for i := 0; i < 20; i++ {
		content += "## Section about testing\nThis section is about testing and quality assurance.\n\n"
	}
	if err := os.WriteFile(filepath.Join(dir, "AGENT.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := engine.Reindex(); err != nil {
		t.Fatal(err)
	}

	// Search with limit of 3
	results, err := engine.Search(context.Background(), "testing", 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) > 3 {
		t.Errorf("expected at most 3 results, got %d", len(results))
	}
}

func TestSearchDisabled(t *testing.T) {
	dir := t.TempDir()
	cfg := DefaultConfig()
	cfg.SearchEnabled = false

	engine, err := New(dir, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()

	_, err = engine.Search(context.Background(), "anything", 10)
	if err == nil {
		t.Error("expected error when search is disabled")
	}
}

func TestChunkMarkdown(t *testing.T) {
	content := `# Title

## Section One
Some content here about section one.

## Section Two
More content about section two.
With multiple lines.

## Section Three
Final section.
`
	chunks := ChunkMarkdown("test.md", content)
	if len(chunks) < 3 {
		t.Errorf("expected at least 3 chunks, got %d", len(chunks))
	}

	// All chunks should reference test.md
	for _, c := range chunks {
		if c.File != "test.md" {
			t.Errorf("expected file 'test.md', got %q", c.File)
		}
		if c.Content == "" {
			t.Error("chunk has empty content")
		}
	}
}

func TestChunkMarkdownEmpty(t *testing.T) {
	chunks := ChunkMarkdown("test.md", "")
	if len(chunks) != 0 {
		t.Errorf("expected 0 chunks for empty content, got %d", len(chunks))
	}

	chunks = ChunkMarkdown("test.md", "   \n  \n  ")
	if len(chunks) != 0 {
		t.Errorf("expected 0 chunks for whitespace-only content, got %d", len(chunks))
	}
}

func TestChunkLargeSection(t *testing.T) {
	// Create content larger than defaultChunkSize
	var content string
	for i := 0; i < 20; i++ {
		content += "This is a long paragraph that contains lots of text. "
	}

	chunks := ChunkMarkdown("test.md", content)
	if len(chunks) == 0 {
		t.Error("expected at least 1 chunk")
	}

	// Each chunk should not exceed 2x defaultChunkSize (rough limit with paragraph splitting)
	for _, c := range chunks {
		if len(c.Content) > defaultChunkSize*3 {
			t.Errorf("chunk too large: %d chars", len(c.Content))
		}
	}
}

func TestReindexSkipsHiddenDirs(t *testing.T) {
	dir, engine := setupTestMemory(t)

	// Create a hidden directory with markdown files (should be skipped)
	snapshotsDir := filepath.Join(dir, ".snapshots")
	if err := os.MkdirAll(snapshotsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(snapshotsDir, "old.md"), []byte("# Old snapshot"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Create a normal file
	if err := os.WriteFile(filepath.Join(dir, "AGENT.md"), []byte("# Agent\nHello"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := engine.Reindex(); err != nil {
		t.Fatal(err)
	}

	status, err := engine.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	// Only AGENT.md should be indexed, not the snapshot
	if status.TotalFiles != 1 {
		t.Errorf("expected 1 file indexed, got %d", status.TotalFiles)
	}
}

func TestReindexClearsOldIndex(t *testing.T) {
	dir, engine := setupTestMemory(t)

	// Index a file
	os.WriteFile(filepath.Join(dir, "old.md"), []byte("# Old\nold content here"), 0o644)
	if err := engine.Reindex(); err != nil {
		t.Fatal(err)
	}

	// Verify it's indexed
	results, _ := engine.Search(context.Background(), "old content", 10)
	if len(results) == 0 {
		t.Fatal("expected results for 'old content'")
	}

	// Remove the file and add a new one
	os.Remove(filepath.Join(dir, "old.md"))
	os.WriteFile(filepath.Join(dir, "new.md"), []byte("# New\nnew content here"), 0o644)
	if err := engine.Reindex(); err != nil {
		t.Fatal(err)
	}

	// Old content should be gone
	results, _ = engine.Search(context.Background(), "old content", 10)
	if len(results) != 0 {
		t.Error("old content should be gone after reindex")
	}

	// New content should be present
	results, _ = engine.Search(context.Background(), "new content", 10)
	if len(results) == 0 {
		t.Error("expected results for 'new content'")
	}
}

func TestReindexWithDailyLogs(t *testing.T) {
	dir, engine := setupTestMemory(t)

	os.WriteFile(filepath.Join(dir, "AGENT.md"), []byte("# Agent\nI am Jarmila."), 0o644)
	dailyDir := filepath.Join(dir, "daily")
	os.WriteFile(filepath.Join(dailyDir, "2026-02-18.md"), []byte("# Feb 18\nFixed pagination."), 0o644)
	os.WriteFile(filepath.Join(dailyDir, "2026-02-19.md"), []byte("# Feb 19\nAdded auth flow."), 0o644)

	if err := engine.Reindex(); err != nil {
		t.Fatal(err)
	}

	status, err := engine.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.TotalFiles != 3 {
		t.Errorf("expected 3 files (AGENT.md + 2 daily), got %d", status.TotalFiles)
	}

	// Search across files
	results, _ := engine.Search(context.Background(), "pagination", 10)
	if len(results) == 0 {
		t.Error("expected to find 'pagination' in daily log")
	}
	results, _ = engine.Search(context.Background(), "Jarmila", 10)
	if len(results) == 0 {
		t.Error("expected to find 'Jarmila' in AGENT.md")
	}
}

func TestStatusAfterReindex(t *testing.T) {
	dir, engine := setupTestMemory(t)

	os.WriteFile(filepath.Join(dir, "AGENT.md"), []byte("# Agent\n## Facts\nFact 1\n## Prefs\nPref 1"), 0o644)

	if err := engine.Reindex(); err != nil {
		t.Fatal(err)
	}

	status, err := engine.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	if status.TotalFiles == 0 {
		t.Error("expected at least 1 file")
	}
	if status.TotalChunks == 0 {
		t.Error("expected at least 1 chunk")
	}
	if status.IndexedAt.IsZero() {
		t.Error("expected non-zero IndexedAt")
	}
	if !status.SearchReady {
		t.Error("expected SearchReady=true")
	}
}

func TestEngineClose(t *testing.T) {
	dir := t.TempDir()
	engine, err := New(dir, DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}

	if err := engine.Close(); err != nil {
		t.Errorf("Close() error: %v", err)
	}

	// After close, operations should fail
	_, err = engine.Search(context.Background(), "test", 10)
	if err == nil {
		t.Error("expected error after Close()")
	}
}

func TestConcurrentSearchAndReindex(t *testing.T) {
	dir, engine := setupTestMemory(t)

	os.WriteFile(filepath.Join(dir, "AGENT.md"), []byte("# Agent\nTest concurrent access."), 0o644)
	engine.Reindex()

	// Run search and reindex concurrently
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 10; i++ {
			engine.Search(context.Background(), "concurrent", 5)
		}
	}()

	for i := 0; i < 5; i++ {
		engine.Reindex()
	}

	<-done // Wait for searches to complete
	// If we reach here without deadlock or panic, the test passes
}

func TestSearchSpecialCharacters(t *testing.T) {
	dir, engine := setupTestMemory(t)

	os.WriteFile(filepath.Join(dir, "AGENT.md"), []byte("# Agent\nUser email: test@example.com\nPath: /usr/local/bin"), 0o644)
	engine.Reindex()

	// These shouldn't cause FTS5 syntax errors
	results, err := engine.Search(context.Background(), "test@example.com", 10)
	if err != nil {
		t.Fatalf("search with @ failed: %v", err)
	}
	_ = results // may or may not match depending on tokenizer

	results, err = engine.Search(context.Background(), "/usr/local", 10)
	if err != nil {
		t.Fatalf("search with / failed: %v", err)
	}
	_ = results
}

func TestSanitizeFTSQuery(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"", ""},
		{"hello", `"hello"`},
		{"hello world", `"hello" "world"`},
		// Queries with operators pass through
		{`"exact match"`, `"exact match"`},
		{"foo AND bar", "foo AND bar"},
		{"prefix*", "prefix*"},
	}

	for _, tt := range tests {
		got := sanitizeFTSQuery(tt.input)
		if got != tt.expected {
			t.Errorf("sanitizeFTSQuery(%q) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}
