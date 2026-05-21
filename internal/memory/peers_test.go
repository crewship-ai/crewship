package memory

import (
	"strings"
	"testing"
)

func TestUserSlug_Deterministic(t *testing.T) {
	a := UserSlug("u1", "ws1")
	b := UserSlug("u1", "ws1")
	if a != b || len(a) != 16 {
		t.Errorf("expected stable 16-hex slug; got a=%q b=%q", a, b)
	}
}

// The workspace component MUST influence the slug — same user in
// two workspaces gets two distinct files. This is the cross-workspace
// isolation guarantee, so a regression here would silently leak peer
// cards between tenants.
func TestUserSlug_WorkspaceIsolation(t *testing.T) {
	a := UserSlug("u1", "ws1")
	b := UserSlug("u1", "ws2")
	if a == b {
		t.Errorf("slug collided across workspaces: %q == %q", a, b)
	}
}

// Boundary-confusion defence — the null separator means
// ("u1u2", "ws") and ("u1", "u2ws") never collide. Without the
// separator, the simple concat would hash the same byte sequence
// for both. Verified with two non-empty workspace IDs so the
// empty-input guard doesn't short-circuit the comparison.
func TestUserSlug_BoundaryConfusion(t *testing.T) {
	a := UserSlug("u1u2", "ws")
	b := UserSlug("u1", "u2ws")
	if a == b {
		t.Errorf("boundary collision: %q == %q", a, b)
	}
}

func TestUserSlug_EmptyUser(t *testing.T) {
	if got := UserSlug("", "ws"); got != "" {
		t.Errorf("expected empty slug for empty user; got %q", got)
	}
}

// Cross-workspace isolation only holds when workspaceID is provided —
// an empty workspaceID would silently collapse every workspace onto
// the same slug for the same user, defeating the design. Fail closed
// (return "") instead.
func TestUserSlug_EmptyWorkspace(t *testing.T) {
	if got := UserSlug("u1", ""); got != "" {
		t.Errorf("expected empty slug for empty workspaceID; got %q", got)
	}
}

func TestPeerCard_WriteReadDeleteRoundTrip(t *testing.T) {
	dir := t.TempDir()
	p := PeerPaths{AgentDir: dir}

	// Missing → empty + nil.
	body, err := LoadPeerCard(p, "u1", "ws1")
	if err != nil || body != "" {
		t.Fatalf("expected empty miss; got body=%q err=%v", body, err)
	}

	// Write + read back.
	want := "Pavel: technical, terse, Czech."
	if err := WritePeerCard(p, "u1", "ws1", want); err != nil {
		t.Fatalf("WritePeerCard: %v", err)
	}
	body, err = LoadPeerCard(p, "u1", "ws1")
	if err != nil || body != want {
		t.Errorf("round-trip mismatch: got %q (err=%v)", body, err)
	}

	// Slugs visible in the directory listing — and they must be the
	// derived hash, not the raw user_id.
	slugs, err := ListPeerSlugs(p)
	if err != nil {
		t.Fatalf("ListPeerSlugs: %v", err)
	}
	want_slug := UserSlug("u1", "ws1")
	if len(slugs) != 1 || slugs[0] != want_slug {
		t.Errorf("expected [%q], got %v", want_slug, slugs)
	}
	for _, s := range slugs {
		if strings.Contains(s, "u1") || strings.Contains(s, "ws1") {
			t.Errorf("slug %q leaks user_id/workspace_id raw text", s)
		}
	}

	// Delete is idempotent — call twice, no error.
	if err := DeletePeerCard(p, "u1", "ws1"); err != nil {
		t.Errorf("DeletePeerCard: %v", err)
	}
	if err := DeletePeerCard(p, "u1", "ws1"); err != nil {
		t.Errorf("DeletePeerCard idempotent failed: %v", err)
	}
	body, _ = LoadPeerCard(p, "u1", "ws1")
	if body != "" {
		t.Errorf("post-delete read should be empty; got %q", body)
	}
}

func TestPeerCard_CapEnforced(t *testing.T) {
	dir := t.TempDir()
	p := PeerPaths{AgentDir: dir}
	big := strings.Repeat("x", PeerCapBytes+1)
	if err := WritePeerCard(p, "u1", "ws1", big); err == nil {
		t.Errorf("expected cap rejection on oversize write")
	}
	atCap := strings.Repeat("y", PeerCapBytes)
	if err := WritePeerCard(p, "u1", "ws1", atCap); err != nil {
		t.Errorf("at-cap write rejected: %v", err)
	}
}

func TestPeerCard_EmptyRejected(t *testing.T) {
	dir := t.TempDir()
	p := PeerPaths{AgentDir: dir}
	if err := WritePeerCard(p, "u1", "ws1", "   "); err == nil {
		t.Errorf("expected empty-content rejection")
	}
}

func TestPeerCard_EmptyUserIDRejected(t *testing.T) {
	dir := t.TempDir()
	p := PeerPaths{AgentDir: dir}
	if err := WritePeerCard(p, "", "ws1", "anything"); err == nil {
		t.Errorf("expected error when user_id is empty")
	}
}

// Multi-write, multi-user listing. Important for the GDPR
// cross-agent sweep — the operator endpoint walks peer_cards
// rows and the disk MUST agree with the DB index.
func TestPeerCard_MultiUserList(t *testing.T) {
	dir := t.TempDir()
	p := PeerPaths{AgentDir: dir}
	for _, u := range []string{"alice", "bob", "carol"} {
		if err := WritePeerCard(p, u, "ws1", u+" is here"); err != nil {
			t.Fatalf("write %s: %v", u, err)
		}
	}
	slugs, err := ListPeerSlugs(p)
	if err != nil {
		t.Fatalf("ListPeerSlugs: %v", err)
	}
	if len(slugs) != 3 {
		t.Errorf("expected 3 slugs, got %d: %v", len(slugs), slugs)
	}
}
