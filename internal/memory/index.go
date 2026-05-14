package memory

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
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

	// Walk the memory directory for .md files. The agent (UID 1001) has
	// write access into this directory; the indexer runs as the sidecar
	// (UID 1002). Without the symlink check, an agent could plant a
	// symlink like `.memory/AGENT.md → /etc/shadow` and have the sidecar
	// read+index it under a different uid. The walker uses Lstat-style
	// FileInfo, so symlinks show up as ModeSymlink rather than as their
	// target's type — we can detect and skip them here. The follow-up
	// O_NOFOLLOW open below catches any TOCTOU race between this walk
	// and the read.
	var files []string
	err := filepath.Walk(e.basePath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // skip unreadable files
		}
		// Reject any symlink — neither files nor directories. An agent
		// has no legitimate reason to symlink into .memory/; if they
		// want a file indexed, they should write the .md content
		// directly.
		if info.Mode()&os.ModeSymlink != 0 {
			return nil
		}
		if info.IsDir() {
			// Skip hidden dirs except the base itself
			if strings.HasPrefix(info.Name(), ".") && path != e.basePath {
				return filepath.SkipDir
			}
			return nil
		}
		// Skip non-regular files (devices, FIFOs, sockets) — same uid-
		// crossing concern as symlinks.
		if !info.Mode().IsRegular() {
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
		data, err := readRegularNoFollow(fpath)
		if err != nil {
			// O_NOFOLLOW returns ELOOP (or "too many levels of
			// symbolic links") if the agent raced us between Walk and
			// open by replacing the file with a symlink. Either way,
			// silently skip — the .md is just gone from this index pass.
			continue
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

// readRegularNoFollow opens path with O_NOFOLLOW so the open syscall
// fails (with ELOOP) if path's last component is a symlink. We Stat
// after opening to confirm the file is still regular — defends against
// the agent racing us between Lstat at walk time and Open here. Returns
// the file contents or an error; the caller treats any error as "skip
// this entry from this index pass."
//
// Note: O_NOFOLLOW only checks the FINAL component. Intermediate
// symlinks in the path (e.g. .memory/sub being a symlink to /tmp/sub)
// would still be traversed. For our threat model — agent has write
// access to .memory/ but not to its parent directories — the final
// component is the only attacker-controlled hop, so this is enough.
func readRegularNoFollow(path string) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() {
		return nil, fmt.Errorf("not a regular file: %s", path)
	}
	return io.ReadAll(f)
}
