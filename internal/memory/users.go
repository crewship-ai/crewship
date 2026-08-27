package memory

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// PR #10 F6 — evolving per-user operator model.
//
// A user model is a small (≤1500 byte) markdown profile distilled from
// an operator's accumulated interactions ACROSS the workspace. Unlike a
// peer card (one file per agent+user, written by PeerCardSync), the
// user model is keyed on (user, workspace) ALONE — it captures how an
// operator likes to work in general, not how they relate to one
// specific agent. Every agent in a crew reads the same model file.
//
// On-disk layout (crew-shared, so all agents in a crew see it):
//
//	/crew/shared/.memory/users/{user_slug}.md
//
// user_slug derivation reuses memory.UserSlug (sha256(user_id ||
// "\x00" || workspace_id)[:16]) — the same one-way hash the peer card
// subsystem uses, so the filename never carries PII into a directory
// listing or a stack trace, and the (user, workspace) scope is
// byte-identical at every read/write/audit site.
//
// # Read path
//
// The orchestrator's prompt assembly looks up chat.created_by at
// session start and reads exactly ONE user model (the opener's),
// emitting it as an [OPERATOR MODEL] block placed BEFORE [PEER
// CONTEXT]. Other operators' models are never injected, mirroring the
// "no cross-operator gossip" rule from the peer card subsystem.
//
// # Write path
//
// User models are written exclusively by the UserModelSync routine
// (internal/consolidate/user_model_writer.go). The extraction prompt
// MERGES the prior model with the new transcript, preserving prior
// high-confidence fields when the latest session is silent about them
// — the model accretes a stable picture of the operator rather than
// being overwritten by whatever the last session happened to mention.
//
// The same per-(user, workspace) opt-out flag (user_peer_consent) that
// gates peer cards also gates user models — opting out of one is opting
// out of both. Callers MUST resolve the opt-out flag before invoking
// WriteUserModel; the primitive stays orthogonal to the GDPR layer.

// UserModelCapBytes is the hard cap per user model. Matches the peer
// card cap (capPeerBytes) — both are 1.5 KB "hint, not fact" surfaces
// read on every run, so the budget envelope is identical.
const UserModelCapBytes = capPeerBytes

// UserModelPaths bundles the crew-shared memory root. The user model
// is per (user, workspace) and agent-independent, so it lives under
// the crew-shared memory directory rather than any single agent's.
type UserModelPaths struct {
	SharedDir string // .../crews/{crew}/shared/.memory
}

// UsersDir returns the absolute path of the users/ subdirectory.
// Callers create it lazily on first write.
func (p UserModelPaths) UsersDir() string {
	return filepath.Join(p.SharedDir, "users")
}

// ModelPath returns the on-disk path for a user model given the
// pre-derived user_slug. Use UserSlug(userID, workspaceID) to derive
// the slug at the call site so the (userID, workspaceID) scope is
// explicit rather than buried in this helper.
func (p UserModelPaths) ModelPath(userSlug string) string {
	return filepath.Join(p.UsersDir(), userSlug+".md")
}

// LoadUserModel reads the user model for a (user, workspace) pair.
// Missing file returns ("", nil) — a fresh operator the workspace has
// no model for yet is the common case, not an error.
//
// Inbound prompt-injection scan is the caller's responsibility: the
// orchestrator wraps the result in ScanContent + Quarantine before
// forwarding to the model. This function is the raw byte read.
func LoadUserModel(p UserModelPaths, userID, workspaceID string) (string, error) {
	slug := UserSlug(userID, workspaceID)
	if slug == "" {
		return "", nil
	}
	return loadUserModelFile(p.ModelPath(slug))
}

// LoadUserModelBySlug reads a user model by its pre-derived slug. Used
// by the GDPR + sweep paths that already hold the slug. Returns
// ("", nil) for a missing file or an empty slug.
func LoadUserModelBySlug(p UserModelPaths, userSlug string) (string, error) {
	if userSlug == "" {
		return "", nil
	}
	return loadUserModelFile(p.ModelPath(userSlug))
}

// WriteUserModel persists content for the given (user, workspace)
// pair. Cap-enforced (UserModelCapBytes) and flock-protected. The
// deterministic slug means repeated writes overwrite in place rather
// than creating new files.
//
// This function does NOT check user_peer_consent. Callers MUST resolve
// the opt-out flag before invoking. The split keeps this primitive
// orthogonal to the GDPR layer that lives in package api / the routine.
func WriteUserModel(p UserModelPaths, userID, workspaceID, content string) error {
	if strings.TrimSpace(content) == "" {
		return errors.New("users: empty content rejected (use DeleteUserModel to clear)")
	}
	if len(content) > UserModelCapBytes {
		return fmt.Errorf("users: content %d bytes exceeds cap %d", len(content), UserModelCapBytes)
	}
	slug := UserSlug(userID, workspaceID)
	if slug == "" {
		return errors.New("users: user_id and workspace_id required")
	}
	path := p.ModelPath(slug)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("users: mkdir: %w", err)
	}
	lk := NewFileLock(path + ".lock")
	if err := lk.Lock(); err != nil {
		return fmt.Errorf("users: lock: %w", err)
	}
	defer func() { _ = lk.Unlock() }()
	if err := writeMemoryFileNoFollow(path, []byte(content), 0o644); err != nil {
		return fmt.Errorf("users: write: %w", err)
	}
	return nil
}

// DeleteUserModel removes the on-disk file for a (user, workspace)
// pair. Idempotent. Returns nil even when the file does not exist —
// the GDPR / opt-out delete path runs across every workspace and a
// "model was never extracted for this user" hit is normal.
func DeleteUserModel(p UserModelPaths, userID, workspaceID string) error {
	slug := UserSlug(userID, workspaceID)
	if slug == "" {
		return nil
	}
	return DeleteUserModelBySlug(p, slug)
}

// DeleteUserModelBySlug removes a model by its slug. Used by sweep
// operations that walk the index table (user_models) and call this
// per row.
//
// It removes the lock sentinel too. The sentinel persists on purpose on
// the WRITE path (writer.go) — flock state is per-fd, so a leftover
// lockfile costs nothing and saves a create on the next write — but a
// delete is the one moment where nothing more is coming. After an
// erasure the index row is gone and the content is gone, so a zero-byte
// `<slug>.md.lock` is the ONLY remaining artefact, and the slug is
// derivable: sha256(user_id ‖ 0x00 ‖ workspace_id)[:16] can be recomputed
// by anyone holding the workspace id and a list of users. A directory
// listing would then still answer "did this workspace ever hold a model
// for this person?" — the question the erasure was asked to stop
// answering (#1701).
func DeleteUserModelBySlug(p UserModelPaths, userSlug string) error {
	if userSlug == "" {
		return nil
	}
	path := p.ModelPath(userSlug)
	// Fast path: nothing to delete. Checking first also avoids trying to
	// create a .lock file inside a users/ directory that was never
	// created (the lazy-mkdir-on-write contract means a workspace that
	// never wrote a model has no users/ dir at all). os.Stat ENOENT
	// here is the common opt-out-with-no-card case.
	//
	// The sentinel is checked as well as the model: a sentinel stranded
	// by a pre-#1701 build outlives the model file, so a purge that only
	// looked at the model would leave every historical marker in place
	// forever.
	if !anyExists(path, path+lockSuffix) {
		return nil
	}
	return removeUnderLock(path, "users")
}

// DeleteUserModelEverywhere removes every on-disk copy of a user model
// for the given slug, regardless of which crew directory currently
// holds it.
//
// A user model's identity is (user, workspace); crew only decides which
// shared directory the file physically sits in, and that directory
// changes over the model's lifetime as the operator's most-active crew
// shifts (consolidate.RunUserModelSync recomputes it on every sweep).
// The user_models index row tracks only the CURRENT crew — its ON
// CONFLICT DO UPDATE moves crew_id forward on a refresh but never
// touches whatever file an earlier crew already held — so a purge keyed
// on that one row only ever reaches the newest copy and leaves every
// prior copy as an orphan nothing points at any more.
//
// This is the fix: enumerate every crew directory actually present on
// disk under basePath, plus the crew-less workspace fallback, and
// delete the slug's file from each. GDPR erasure (self-service delete,
// admin SAR cascade) must use this instead of reconstructing a single
// "expected" path from a stored crew_id, because a purge that only
// agrees with the writer about where the CURRENT file lives is exactly
// the kind of purge that misses an old one.
//
// basePath is the same OutputBasePath the writer and the sweep use.
// Returns the number of files actually removed and the first error
// encountered — best-effort, so one unreadable crew directory does not
// stop the sweep from clearing the rest.
func DeleteUserModelEverywhere(basePath, userSlug string) (removed int, err error) {
	if basePath == "" || userSlug == "" {
		return 0, nil
	}

	var firstErr error
	tryDelete := func(sharedDir string) {
		p := UserModelPaths{SharedDir: sharedDir}
		path := p.ModelPath(userSlug)
		existed := anyExists(path, path+lockSuffix)
		if delErr := DeleteUserModelBySlug(p, userSlug); delErr != nil {
			if firstErr == nil {
				firstErr = delErr
			}
			return
		}
		if existed {
			removed++
		}
	}

	// Crew-less workspace fallback — matches consolidate.UserModelPathsFor
	// with an empty crewID.
	tryDelete(filepath.Join(basePath, "workspace", "shared", ".memory"))

	// Every crew directory actually present on disk, not just the one the
	// index row currently names.
	crewsRoot := filepath.Join(basePath, "crews")
	entries, readErr := os.ReadDir(crewsRoot)
	if readErr != nil && !errors.Is(readErr, fs.ErrNotExist) {
		if firstErr == nil {
			firstErr = readErr
		}
		return removed, firstErr
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		tryDelete(filepath.Join(crewsRoot, e.Name(), "shared", ".memory"))
	}
	return removed, firstErr
}

// ListUserModelSlugs returns every user model slug present on disk for
// this crew-shared memory, sorted lexicographically. Used by routine
// sweeps reconciling disk against the user_models DB index. Returns a
// nil slice when users/ does not exist.
func ListUserModelSlugs(p UserModelPaths) ([]string, error) {
	entries, err := os.ReadDir(p.UsersDir())
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("users: list: %w", err)
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		out = append(out, strings.TrimSuffix(e.Name(), ".md"))
	}
	sort.Strings(out)
	return out, nil
}

// loadUserModelFile is the shared "ENOENT → empty string" wrapper.
func loadUserModelFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return string(data), nil
}
