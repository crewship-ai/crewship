package memory

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// WorkspaceMemory wraps a memory Engine for workspace-level (cross-crew) memory.
// It lives on the host filesystem (not inside a container) and is managed by crewshipd.
// Captain/Coordinator agents read from it; Coordinator writes to it.
type WorkspaceMemory struct {
	engine *Engine
	path   string
}

// NewWorkspaceMemory creates a workspace memory manager at the given path.
// It initializes the FTS5 engine and performs an initial reindex.
// Path is typically ~/.crewship/memory/{workspace-id}/.
func NewWorkspaceMemory(workspacePath string) (*WorkspaceMemory, error) {
	if err := os.MkdirAll(workspacePath, 0o755); err != nil {
		return nil, fmt.Errorf("create workspace memory dir: %w", err)
	}

	engine, err := New(workspacePath, DefaultConfig())
	if err != nil {
		return nil, fmt.Errorf("init workspace memory engine: %w", err)
	}

	// Index existing files on startup
	if err := engine.Reindex(); err != nil {
		engine.Close()
		return nil, fmt.Errorf("initial workspace reindex: %w", err)
	}

	return &WorkspaceMemory{engine: engine, path: workspacePath}, nil
}

// Search performs a FTS5 search across workspace memory.
func (w *WorkspaceMemory) Search(query string, limit int) ([]SearchResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return w.engine.Search(ctx, query, limit)
}

// GetContext returns a formatted workspace memory block for system prompt injection.
// It reads markdown files directly (not via FTS5 search) to build the context.
// Returns empty string and 0 if no workspace memory files exist.
func (w *WorkspaceMemory) GetContext(budget int) (string, int) {
	// Walk workspace dir for .md files and read their content directly.
	// This is a host-level operation (workspace memory lives on host, not in container).
	var files []struct {
		rel     string
		content string
	}

	filepath.Walk(w.path, func(fpath string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		if !strings.HasSuffix(info.Name(), ".md") {
			return nil
		}
		data, err := os.ReadFile(fpath)
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(w.path, fpath)
		files = append(files, struct {
			rel     string
			content string
		}{rel, strings.TrimSpace(string(data))})
		return nil
	})

	if len(files) == 0 {
		return "", 0
	}

	var b strings.Builder
	b.WriteString("[WORKSPACE MEMORY]\n")
	totalChars := len("[WORKSPACE MEMORY]\n")

	markerEnd := "[END WORKSPACE MEMORY]\n"
	reservedEnd := len(markerEnd)

	for _, f := range files {
		section := fmt.Sprintf("--- %s ---\n%s\n", f.rel, f.content)
		if totalChars+len(section)+reservedEnd > budget {
			remaining := budget - totalChars - reservedEnd - 20
			if remaining > 50 {
				section = section[:remaining] + "\n...(truncated)\n"
				b.WriteString(section)
				totalChars += len(section)
			}
			break
		}
		b.WriteString(section)
		totalChars += len(section)
	}

	b.WriteString(markerEnd)
	totalChars += reservedEnd

	return b.String(), totalChars
}

// Reindex rebuilds the FTS5 index from workspace memory files.
func (w *WorkspaceMemory) Reindex() error {
	return w.engine.Reindex()
}

// Close shuts down the workspace memory engine.
func (w *WorkspaceMemory) Close() error {
	return w.engine.Close()
}
