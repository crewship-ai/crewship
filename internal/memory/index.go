package memory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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

	// Clear existing index inside the transaction so readers never observe
	// an empty table mid-reindex: with the DELETE outside the tx (or
	// outside any read-blocking lock) a concurrent reader could land in
	// the window between DELETE and the in-tx INSERTs and see no chunks.
	if _, err := tx.Exec("DELETE FROM memory_chunks"); err != nil {
		return fmt.Errorf("clear index: %w", err)
	}

	stmt, err := tx.Prepare("INSERT INTO memory_chunks (file, content) VALUES (?, ?)")
	if err != nil {
		return fmt.Errorf("prepare insert: %w", err)
	}
	defer stmt.Close()

	// Rebuild the per-file content-hash map from scratch so the incremental
	// fast path (ReindexPath) is consistent with the just-rebuilt index. We
	// stage it in a local and only swap it in on a successful commit, so a
	// rolled-back reindex leaves the previous map intact.
	hashes := make(map[string]string)

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
		hashes[relPath] = hashContent(data)
	}

	// Update metadata
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := tx.Exec(`
		INSERT OR REPLACE INTO memory_meta (key, value)
		VALUES ('last_indexed', ?)
	`, now); err != nil {
		return fmt.Errorf("update meta: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return err
	}
	e.fileHashes = hashes
	return nil
}

// ReindexPath incrementally re-indexes a SINGLE memory file, identified by its
// path relative to the engine base (e.g. "AGENT.md" or "daily/2026-06-29.md").
// Only that file's chunks are touched: its existing rows are DELETEd and the
// freshly-chunked content re-INSERTed in one transaction, leaving every other
// file's chunks intact. This is the per-write fast path — its cost is
// O(size of the changed file), NOT O(corpus) like ReindexContext. It is the
// fix for finding P2 (full-corpus reindex on every memory write).
//
// Behaviour:
//   - Unchanged content (hash matches the last index of this file) is a no-op
//     and returns 0 — a redundant write/watcher tick does zero index work.
//   - A vanished or unreadable file (deleted, or swapped for a symlink and
//     rejected by the O_NOFOLLOW open) has its chunks removed and returns 0,
//     nil: an absent file is correctly absent from the index.
//   - Otherwise the file's chunks are replaced and the number of chunks
//     (re)inserted is returned.
//
// FTS correctness is preserved: the (file, content) rows written here are
// identical in shape to those ReindexContext writes, so Search() behaves the
// same whether a file was indexed incrementally or by a full rebuild.
func (e *Engine) ReindexPath(ctx context.Context, relPath string) (int, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if err := ctx.Err(); err != nil {
		return 0, err
	}

	// Resolve the file key exactly as ReindexContext does (filepath.Rel of the
	// cleaned join) so the DELETE here matches the `file` column a prior full
	// reindex would have written. Reject anything that escapes the base.
	clean := filepath.Clean(relPath)
	if clean == "." || clean == "" || filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return 0, fmt.Errorf("reindex path: illegal relative path %q", relPath)
	}
	fpath := filepath.Join(e.basePath, clean)
	fileKey, err := filepath.Rel(e.basePath, fpath)
	if err != nil {
		fileKey = filepath.Base(fpath)
	}

	data, readErr := readRegularNoFollow(fpath)
	if readErr != nil {
		// File is gone / unreadable / a symlink we refuse to follow. Drop any
		// chunks it had and forget its hash so a later recreate re-indexes it.
		if _, err := e.db.ExecContext(ctx, "DELETE FROM memory_chunks WHERE file = ?", fileKey); err != nil {
			return 0, fmt.Errorf("delete chunks for %s: %w", fileKey, err)
		}
		delete(e.fileHashes, fileKey)
		return 0, nil
	}

	h := hashContent(data)
	if prev, ok := e.fileHashes[fileKey]; ok && prev == h {
		// Content unchanged since we last indexed this file — nothing to do.
		return 0, nil
	}

	chunks := ChunkMarkdown(fileKey, string(data))

	tx, err := e.db.Begin()
	if err != nil {
		return 0, fmt.Errorf("begin incremental reindex tx: %w", err)
	}
	defer tx.Rollback()

	// Replace just this file's chunks. The DELETE+INSERT runs inside one
	// transaction so a concurrent Search never observes the file with zero
	// chunks mid-update (WAL snapshot isolation), matching ReindexContext.
	if _, err := tx.Exec("DELETE FROM memory_chunks WHERE file = ?", fileKey); err != nil {
		return 0, fmt.Errorf("delete chunks for %s: %w", fileKey, err)
	}

	stmt, err := tx.Prepare("INSERT INTO memory_chunks (file, content) VALUES (?, ?)")
	if err != nil {
		return 0, fmt.Errorf("prepare insert: %w", err)
	}
	defer stmt.Close()

	for _, chunk := range chunks {
		if _, err := stmt.Exec(chunk.File, chunk.Content); err != nil {
			return 0, fmt.Errorf("insert chunk %s: %w", fileKey, err)
		}
	}

	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := tx.Exec(`
		INSERT OR REPLACE INTO memory_meta (key, value)
		VALUES ('last_indexed', ?)
	`, now); err != nil {
		return 0, fmt.Errorf("update meta: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return 0, err
	}
	e.fileHashes[fileKey] = h
	return len(chunks), nil
}

// hashContent returns a hex-encoded SHA-256 of file content, used to detect
// whether a file changed since its last index pass.
func hashContent(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// readRegularNoFollow opens path safely for indexing:
//   - O_NOFOLLOW makes the open syscall fail (with ELOOP) if path's
//     final component is a symlink — defends against the agent racing
//     us between Lstat at walk time and Open here.
//   - O_NONBLOCK keeps Open from hanging when an attacker swaps a
//     regular .md for a FIFO (named pipe) with no writer. Without
//     O_NONBLOCK the Open call would block until a writer connects,
//     which can be never — Reindex holds e.mu the whole time, so a
//     hung Open soft-DoSes every memory operation in the process.
//     CodeRabbit caught this on the first review pass.
//   - After open we re-Stat and reject anything that isn't a regular
//     file (sockets, devices, FIFOs that survived O_NONBLOCK because
//     they happened to have a writer, etc.).
//
// On nested symlink swaps in INTERMEDIATE path components (e.g.
// .memory/sub being itself a symlink), O_NOFOLLOW does not protect
// us — the kernel only checks the final component. Two layered
// defenses: (1) the agent has write access only to .memory/ leaves
// (not the parent dir) so it can't replace `sub` with a symlink at
// our threat model layer, (2) the walk-time Lstat in the caller
// already rejects symlinks at every level it visits, so a swap that
// happens between walk and open would have to be at a depth the walker
// already traversed — racing it requires winning a millisecond TOCTOU
// against an indexer that's reading hundreds of files in a tight loop.
// Acceptable residual risk; documented for follow-up if the threat
// model widens.
func readRegularNoFollow(path string) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
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
