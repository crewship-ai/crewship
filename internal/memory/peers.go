package memory

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// PR-E F6 — per-user peer cards.
//
// A peer card is a small (≤1500 byte) markdown profile the agent reads
// when interacting with a specific operator. One file per (agent, user)
// pair, lazily created by the auxiliary PeerCardSync routine after the
// pair has accumulated enough interaction signal (PRD §6 F6:
// "≥10 messages OR ≥5 minutes session").
//
// On-disk layout:
//
//	/output/{agent_slug}/.memory/peers/{user_slug}.md
//
// user_slug derivation is intentionally a one-way hash so the filename
// never carries PII into a directory listing or a stack trace:
//
//	slug = sha256(user_id || "\x00" || workspace_id)[:16]
//
// The workspace component is load-bearing — without it, a user
// interacting in two workspaces would collide in any agent that
// belongs to both, breaking the per-workspace isolation guarantee.
// The null byte separator stops a "u1ws2" + "u1w" + "s2" boundary
// confusion attack (unlikely with CUIDs in practice but cheap to
// defend against).
//
// # Read path
//
// The orchestrator's prompt assembly looks up chat.created_by at
// session start and reads exactly ONE peer card (the opener's).
// Other users' cards are never injected. This is intentional — the
// agent should not see other operators' peer cards even if it has
// them on disk, to avoid the model "reading the room" via
// cross-operator gossip.
//
// # Write path
//
// Peer cards are written exclusively by the PeerCardSync routine
// (internal/consolidate/peer_card_writer.go). Direct agent writes
// via memory.write(tier=peers) are subject to the same per-tier
// flock + cap protection (already in tools.go); whether to allow
// agents to write peer cards directly in future is gated by
// policy.ActionMemoryWrite — agents currently must go through
// memory.write which enforces the user_peer_consent opt-out check
// via PeerWriter below.

// PeerCapBytes is the hard cap per peer card. Matches tools.go
// capPeerBytes — exported here so callers outside the dispatcher
// don't import the unexported constant.
const PeerCapBytes = capPeerBytes

// UserSlug derives the stable, PII-free filename slug for a user
// within a workspace. Same function the writer + reader + audit
// path use — keep this as the single source of truth so a future
// algorithm change (e.g. moving to a per-workspace HMAC) only
// touches one site.
func UserSlug(userID, workspaceID string) string {
	if userID == "" {
		return ""
	}
	h := sha256.New()
	h.Write([]byte(userID))
	h.Write([]byte{0})
	h.Write([]byte(workspaceID))
	sum := h.Sum(nil)
	return hex.EncodeToString(sum)[:16]
}

// PeerPaths bundles the per-agent peers/ root directory. Solo
// agents and workspace-less callers will still have a valid AgentDir;
// peers is per-agent, not per-crew, so no CrewDir is needed.
type PeerPaths struct {
	AgentDir string // .../crew/agents/{slug}/.memory
}

// PeersDir returns the absolute path of the peers/ subdirectory.
// Callers create it lazily on first write.
func (p PeerPaths) PeersDir() string {
	return filepath.Join(p.AgentDir, "peers")
}

// CardPath returns the on-disk path for a peer card given the
// pre-derived user_slug. Use UserSlug(userID, workspaceID) to
// derive the slug at the call site so the (userID, workspaceID)
// scope is explicit rather than buried in this helper.
func (p PeerPaths) CardPath(userSlug string) string {
	return filepath.Join(p.PeersDir(), userSlug+".md")
}

// LoadPeerCard reads the peer card for a (user, workspace) pair.
// Missing file returns ("", nil) — a fresh user the agent hasn't
// met yet is the common case, not an error.
//
// Inbound prompt-injection scan is the caller's responsibility:
// the orchestrator and sidecar wrap the result in ScanContent +
// Quarantine before forwarding to the model. This function is the
// raw byte read.
func LoadPeerCard(p PeerPaths, userID, workspaceID string) (string, error) {
	slug := UserSlug(userID, workspaceID)
	if slug == "" {
		return "", nil
	}
	return loadPeerFile(p.CardPath(slug))
}

// LoadPeerCardBySlug reads a peer card by its pre-derived slug.
// Used by the GDPR endpoints that already have the slug (e.g.
// during a sweep-and-delete walk where re-deriving from user_id
// is redundant work). Returns ("", nil) for missing file.
func LoadPeerCardBySlug(p PeerPaths, userSlug string) (string, error) {
	if userSlug == "" {
		return "", nil
	}
	return loadPeerFile(p.CardPath(userSlug))
}

// WritePeerCard persists content for the given (user, workspace)
// pair. Cap-enforced (PeerCapBytes) and flock-protected. The
// per-(agent, user) UNIQUE constraint on the peer_cards DB
// mirror is satisfied by the slug derivation being deterministic
// — repeated writes overwrite in place rather than creating
// new rows.
//
// This function does NOT check user_peer_consent. Callers MUST
// resolve the opt-out flag (via the API layer or the routine
// runner) BEFORE invoking. The split keeps this primitive
// orthogonal to the GDPR layer that lives in package api.
func WritePeerCard(p PeerPaths, userID, workspaceID, content string) error {
	if strings.TrimSpace(content) == "" {
		return errors.New("peers: empty content rejected (use DeletePeerCard to clear)")
	}
	if len(content) > PeerCapBytes {
		return fmt.Errorf("peers: content %d bytes exceeds cap %d", len(content), PeerCapBytes)
	}
	slug := UserSlug(userID, workspaceID)
	if slug == "" {
		return errors.New("peers: user_id required")
	}
	path := p.CardPath(slug)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("peers: mkdir: %w", err)
	}
	lk := NewFileLock(path + ".lock")
	if err := lk.Lock(); err != nil {
		return fmt.Errorf("peers: lock: %w", err)
	}
	defer func() { _ = lk.Unlock() }()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return fmt.Errorf("peers: write: %w", err)
	}
	return nil
}

// DeletePeerCard removes the on-disk file for a (user, workspace)
// pair. Idempotent. Returns nil even when the file does not exist —
// the GDPR delete path runs this across every agent in a workspace
// and a "card was never extracted for this user" hit is normal.
func DeletePeerCard(p PeerPaths, userID, workspaceID string) error {
	slug := UserSlug(userID, workspaceID)
	if slug == "" {
		return nil
	}
	return DeletePeerCardBySlug(p, slug)
}

// DeletePeerCardBySlug removes a card by its slug. Used by sweep
// operations that walk the index table (peer_cards) and call this
// per row.
func DeletePeerCardBySlug(p PeerPaths, userSlug string) error {
	if userSlug == "" {
		return nil
	}
	path := p.CardPath(userSlug)
	lk := NewFileLock(path + ".lock")
	if err := lk.Lock(); err != nil {
		return fmt.Errorf("peers: lock: %w", err)
	}
	defer func() { _ = lk.Unlock() }()
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("peers: remove: %w", err)
	}
	return nil
}

// ListPeerSlugs returns every peer card slug present on disk for
// this agent, sorted lexicographically. Used by routine sweeps
// that want to reconcile the disk against the DB index, and by the
// agent-card UI showing "this agent has cards for N peers".
// Returns nil slice when peers/ does not exist.
func ListPeerSlugs(p PeerPaths) ([]string, error) {
	entries, err := os.ReadDir(p.PeersDir())
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("peers: list: %w", err)
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		// strip ".md"
		out = append(out, strings.TrimSuffix(e.Name(), ".md"))
	}
	sort.Strings(out)
	return out, nil
}

// loadPeerFile is the shared "ENOENT → empty string" wrapper.
func loadPeerFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return string(data), nil
}
