package memory

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"

	"github.com/crewship-ai/crewship/internal/scrubber"
)

// WriteConfig parameterises a single memory write. Zero value means
// no cap, no scrubbing, and no allowlist — the same loose contract
// older direct-filesystem writes ran under.
//
// MaxBytes is the hard ceiling on `len(content)`. 0 means no cap.
// Callers gating per-tier (agent / crew / pins) supply different
// limits.
//
// Scrubber + ScrubberMode + AllowlistRegex are forwarded to the
// scrubber's Validate path. Scrubber=nil disables scrubbing entirely
// even if ScrubberMode is set.
type WriteConfig struct {
	MaxBytes       int
	Scrubber       *scrubber.Scrubber
	ScrubberMode   scrubber.Mode
	AllowlistRegex string
	// Verifier runs after the scrubber + cap pass but before the
	// flock + write. Zero value (VerifierConfig{}, Mode=VerifierOff)
	// is "no verification" — matches the legacy contract.
	// Production-side callers wire this from workspace_settings
	// .memory_config.verifier_mode to support per-workspace policy.
	Verifier VerifierConfig
}

// WriteResult is what WriteFile returns to its caller. A rejected
// write is NOT an error — it's a structured outcome the caller can
// surface as an HTTP 422 or a memory.write_rejected journal entry.
// Real errors (filesystem failures, context cancellation) come back
// as `error` instead.
//
// RejectionKind ∈ {"", "cap", "scrubber"}. RejectionDetail carries
// the kind-specific structured fields the API + journal can render
// without re-interpreting the rejection.
type WriteResult struct {
	BytesWritten    int
	Rejected        bool
	RejectionKind   string
	RejectionDetail map[string]any
	Hits            []scrubber.Hit
	// VerifierFindings is the verifier's per-rule findings list,
	// populated when WriteConfig.Verifier was active regardless of
	// whether the write was rejected. Allows callers to log near-
	// miss signals (e.g. a citation that ALMOST matched) for
	// post-hoc tuning. Empty when verifier was off.
	VerifierFindings []VerifierFinding
}

// WriteFile persists `content` at `path` with optional cap + scrubber
// validation, file-locking the destination during the write window,
// and replacing the target atomically via a sibling tempfile +
// os.Rename so concurrent readers either see the prior content or
// the new content, never a partial mid-write state.
//
// The contract is intentionally minimal:
//
//   - ctx is checked once up-front and once between scrubber/cap and
//     the syscalls. After Rename succeeds, cancellation has no effect
//     — the file is durable on disk.
//
//   - The .lock sentinel is created on first call; it persists on
//     disk. flock state is per-fd, not per-inode, so a leftover
//     empty lockfile does not "stay locked".
//
//   - Parent directories are created with MkdirAll(0o755) to match
//     the loose contract callers like consolidator.appendRules
//     already rely on.
//
//   - The tempfile is created in the same directory as the target so
//     the rename is on the same filesystem (cross-fs rename would
//     fall back to a copy and lose atomicity).
//
// Callers that want a non-blocking write should run WriteFile in a
// goroutine — the flock acquire here is blocking by design.
func WriteFile(ctx context.Context, path string, content []byte, cfg WriteConfig) (WriteResult, error) {
	// Defend against nil ctx — the preflight check would otherwise
	// panic on the very first dereference. Callers that haven't yet
	// wired a context get background timing semantics.
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return WriteResult{}, fmt.Errorf("preflight context check: %w", err)
	}

	// 1. Cap check happens first — rejecting on size never touches disk.
	if cfg.MaxBytes > 0 && len(content) > cfg.MaxBytes {
		// Best-effort current_size so the agent prompt can render
		// useful context — failure to stat the existing file is
		// non-fatal; just omit the field.
		detail := map[string]any{
			"bytes_attempted": len(content),
			"bytes_limit":     cfg.MaxBytes,
		}
		if st, err := os.Stat(path); err == nil {
			detail["current_size"] = int(st.Size())
		}
		return WriteResult{
			Rejected:        true,
			RejectionKind:   "cap",
			RejectionDetail: detail,
		}, nil
	}

	// 2. Scrubber validation. Block + Warn return immediately;
	// Redact rewrites the content we are about to persist.
	effective := content
	var hits []scrubber.Hit
	if cfg.Scrubber != nil {
		vres := cfg.Scrubber.ValidateWithAllowlist(string(content), cfg.ScrubberMode, cfg.AllowlistRegex)
		hits = vres.Hits
		switch cfg.ScrubberMode {
		case scrubber.ModeBlock:
			if vres.Decision == scrubber.DecisionReject {
				return WriteResult{
					Rejected:      true,
					RejectionKind: "scrubber",
					RejectionDetail: map[string]any{
						"hits": len(hits),
					},
					Hits: hits,
				}, nil
			}
		case scrubber.ModeRedact:
			// Persist the cleaned form. Hits surface separately
			// for journalling so an operator can see *what* was
			// redacted without re-reading the on-disk content.
			effective = []byte(vres.Cleaned)
		case scrubber.ModeWarn:
			// Allow + carry hits to caller; on-disk content is
			// unchanged.
		}
	}

	// 2.5. Verifier pass — citation-staleness + (future) contradiction
	// checks. Runs against the effective content (post-redact in
	// scrubber.ModeRedact) so a redacted-then-rejected sequence is
	// impossible: the verifier reads what would actually land on
	// disk. Zero-value VerifierConfig (Mode=VerifierOff) bypasses
	// the call without I/O.
	var verifierFindings []VerifierFinding
	if cfg.Verifier.Mode != VerifierOff {
		vres, vErr := VerifyWrite(ctx, effective, cfg.Verifier)
		if vErr != nil {
			return WriteResult{}, fmt.Errorf("verifier: %w", vErr)
		}
		verifierFindings = vres.Findings
		if vres.Decision == VerifierReject {
			return WriteResult{
				Rejected:      true,
				RejectionKind: "verifier",
				RejectionDetail: map[string]any{
					"verifier_kind":     vres.Kind,
					"verifier_findings": vres.Findings,
				},
				VerifierFindings: vres.Findings,
			}, nil
		}
	}

	// 3. Filesystem path: MkdirAll, lock, tempfile, fsync, rename.
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return WriteResult{}, fmt.Errorf("mkdir parent: %w", err)
	}

	lk := newWriteLock(path + ".lock")
	if err := lk.Lock(); err != nil {
		return WriteResult{}, fmt.Errorf("acquire write lock: %w", err)
	}
	defer func() { _ = lk.Unlock() }()

	if err := ctx.Err(); err != nil {
		return WriteResult{}, fmt.Errorf("post-lock context check: %w", err)
	}

	// Random suffix avoids tempfile collisions when two writers race
	// inside the lock window (only one holds the lock, but we want
	// the suffix to be unique per attempt so a crashed prior attempt
	// doesn't shadow the current one).
	var randBuf [6]byte
	if _, err := rand.Read(randBuf[:]); err != nil {
		return WriteResult{}, fmt.Errorf("rand for tempname: %w", err)
	}
	tmpPath := path + ".tmp." + hex.EncodeToString(randBuf[:])

	// O_EXCL on the tempfile so we never overwrite a leftover one
	// from a crashed prior writer with our partial content. If it
	// somehow exists with the same random suffix (extremely
	// unlikely) we surface the EEXIST and the caller can retry.
	f, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return WriteResult{}, fmt.Errorf("open tempfile: %w", err)
	}

	cleanupTmp := func() {
		_ = os.Remove(tmpPath)
	}

	if _, err := f.Write(effective); err != nil {
		_ = f.Close()
		cleanupTmp()
		return WriteResult{}, fmt.Errorf("write tempfile: %w", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		cleanupTmp()
		return WriteResult{}, fmt.Errorf("fsync tempfile: %w", err)
	}
	if err := f.Close(); err != nil {
		cleanupTmp()
		return WriteResult{}, fmt.Errorf("close tempfile: %w", err)
	}

	if err := os.Rename(tmpPath, path); err != nil {
		cleanupTmp()
		return WriteResult{}, fmt.Errorf("atomic rename: %w", err)
	}

	// fsync the parent directory so the rename's directory-entry
	// update lands on disk. os.Rename is atomic at the inode level
	// but on ext4/xfs/btrfs the rename's metadata commit is buffered
	// in the directory's page cache until the next dirty-data flush;
	// a crash between rename and that flush can revive the prior
	// directory entry pointing at our (now-removed) tmpPath, making
	// the rename effectively roll back. fsync on the dir handle forces
	// the entry update to the journal before we report success. We do
	// not roll back the data file on dir-fsync failure — the file is
	// already at `path` and a follow-up writer will overwrite it.
	if dir, openErr := os.Open(filepath.Dir(path)); openErr == nil {
		syncErr := dir.Sync()
		closeErr := dir.Close()
		if syncErr != nil {
			return WriteResult{}, fmt.Errorf("fsync parent dir: %w", syncErr)
		}
		if closeErr != nil {
			return WriteResult{}, fmt.Errorf("close parent dir: %w", closeErr)
		}
	} else {
		return WriteResult{}, fmt.Errorf("open parent dir for fsync: %w", openErr)
	}

	return WriteResult{
		BytesWritten:     len(effective),
		Hits:             hits,
		VerifierFindings: verifierFindings,
	}, nil
}

// FileLock is an OS-level advisory lock anchored at a sentinel file
// (created on first Lock if missing). Unix uses flock(2); Windows uses
// LockFileEx via the build-tagged sibling file. The sentinel persists
// on disk; flock state is per-fd, not per-inode, so a leftover empty
// lockfile does not "stay locked" across runs.
//
// External callers (consolidator's appendRules / snapshotPins) wrap
// their O_APPEND writes with Lock / Unlock to serialise concurrent
// writers without paying for the full atomic-replace dance that
// WriteFile does internally.
type FileLock struct {
	path string
	f    *os.File
}

// writeLock is the unexported alias WriteFile uses internally so older
// call sites do not need to import the public surface to talk to
// themselves. Same machinery.
type writeLock = FileLock

// NewFileLock returns an unlocked FileLock that will operate on the
// given sentinel path. Construction does no I/O.
func NewFileLock(path string) *FileLock {
	return &FileLock{path: path}
}

func newWriteLock(path string) *writeLock {
	return &FileLock{path: path}
}
