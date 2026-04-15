package memory

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Reindex scans all markdown files in the memory directory and rebuilds
// the FTS5 index. This is called periodically or on-demand by the sidecar.
// Note: Direct filesystem access is intentional — see engine.go for rationale.
func (e *Engine) Reindex() error {
	return e.ReindexContext(context.Background())
}

// ReindexContext is like Reindex but respects context cancellation. Use this
// for request-scoped or shutdown-aware reindexing.
func (e *Engine) ReindexContext(ctx context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if err := ctx.Err(); err != nil {
		return err
	}

	// Clear existing index
	if _, err := e.db.Exec("DELETE FROM memory_chunks"); err != nil {
		return fmt.Errorf("clear index: %w", err)
	}

	// Walk the memory directory for .md files
	var files []string
	err := filepath.Walk(e.basePath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // skip unreadable files
		}
		if info.IsDir() {
			// Skip hidden dirs except the base itself
			if strings.HasPrefix(info.Name(), ".") && path != e.basePath {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(info.Name(), ".md") {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("walk memory dir: %w", err)
	}

	tx, err := e.db.Begin()
	if err != nil {
		return fmt.Errorf("begin reindex tx: %w", err)
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare("INSERT INTO memory_chunks (file, content) VALUES (?, ?)")
	if err != nil {
		return fmt.Errorf("prepare insert: %w", err)
	}
	defer stmt.Close()

	for _, fpath := range files {
		if err := ctx.Err(); err != nil {
			return err
		}
		data, err := os.ReadFile(fpath)
		if err != nil {
			continue // skip unreadable files
		}

		// Make file paths relative to basePath for cleaner display
		relPath, err := filepath.Rel(e.basePath, fpath)
		if err != nil {
			relPath = filepath.Base(fpath)
		}

		chunks := ChunkMarkdown(relPath, string(data))
		for _, chunk := range chunks {
			if _, err := stmt.Exec(chunk.File, chunk.Content); err != nil {
				return fmt.Errorf("insert chunk %s: %w", relPath, err)
			}
		}
	}

	// Update metadata
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := tx.Exec(`
		INSERT OR REPLACE INTO memory_meta (key, value)
		VALUES ('last_indexed', ?)
	`, now); err != nil {
		return fmt.Errorf("update meta: %w", err)
	}

	return tx.Commit()
}
