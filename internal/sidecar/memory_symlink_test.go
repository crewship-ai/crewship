package sidecar

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// These tests lock finding MEM (LOW/MED) from the 2026-06 security audit
// (.claude/context/SECURITY-AUDIT-2026-06.md): safeJoinUnder
// (internal/sidecar/memory_write.go) used to EvalSymlinks only the PARENT
// directory of the resolved path, never the final component. So an agent
// (uid 1001) that planted a symlink AS the final component (e.g.
// .memory/AGENT.md -> /some/path-owned-by-uid-1002) survived the prefix check
// (the symlink file itself lives inside base, so its parent resolves inside
// base), and handleMemoryRead's os.ReadFile then transparently followed that
// symlink — crossing the UID boundary the sidecar is supposed to enforce.
//
// The fix lstats the final component and rejects any symlink outright on both
// the read and write paths. These tests now assert the SECURE behavior: a
// final-component symlink escaping base is REJECTED. They would FAIL again if
// the guard regressed.

// outsideSecret creates a file OUTSIDE the memory base (a sibling temp dir,
// modeling a path owned by a different UID) and returns its absolute path and
// the secret body it holds.
func outsideSecret(t *testing.T) (string, string) {
	t.Helper()
	outsideDir := t.TempDir() // distinct from the base's TempDir
	secretPath := filepath.Join(outsideDir, "secret.txt")
	const body = "uid-1002-only: sidecar-private-credential\n"
	if err := os.WriteFile(secretPath, []byte(body), 0o600); err != nil {
		t.Fatalf("write outside secret: %v", err)
	}
	return secretPath, body
}

// plantFinalSymlink creates base/<rel> as a symlink pointing at target. The
// symlink's parent dir is created first so the symlink itself lives inside
// base (only the final component escapes).
func plantFinalSymlink(t *testing.T, base, rel, target string) {
	t.Helper()
	link := filepath.Join(base, rel)
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatalf("mkdir parent: %v", err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}
}

// TestSafeJoinUnder_FinalComponentSymlink_Rejected proves at the unit level
// that safeJoinUnder now rejects a path whose final component is a symlink
// escaping base, rather than handing back the in-base path for os.ReadFile /
// os.WriteFile to follow out.
func TestSafeJoinUnder_FinalComponentSymlink_Rejected(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink semantics differ on windows; finding targets the linux sidecar")
	}
	base := t.TempDir()
	secretPath, _ := outsideSecret(t)
	plantFinalSymlink(t, base, "AGENT.md", secretPath)

	if _, err := safeJoinUnder(base, "AGENT.md"); err == nil {
		t.Fatal("MEM regression: safeJoinUnder must reject a final-component symlink that escapes base")
	}
}

// TestHandleMemoryRead_FinalSymlink_CrossUIDBlocked proves the end-to-end fix
// through the read handler: an agent that plants .memory/AGENT.md as a symlink
// to a sidecar-private file must NOT get that file's bytes back over the
// memory_read API — the handler refuses with 403.
func TestHandleMemoryRead_FinalSymlink_CrossUIDBlocked(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink semantics differ on windows; finding targets the linux sidecar")
	}
	s, base := newReadTestServer(t)
	secretPath, _ := outsideSecret(t)
	plantFinalSymlink(t, base, "AGENT.md", secretPath)

	req := httptest.NewRequest("GET", "http://localhost/memory/read?file=AGENT.md", nil)
	rr := httptest.NewRecorder()
	s.handleMemoryRead(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("MEM regression: read of a final-component symlink escape must return 403, got %d (body=%s)", rr.Code, rr.Body.String())
	}
}

// TestSafeJoinUnder_ParentSymlink_AlreadyRejected is a positive regression
// guard: the EXISTING parent-dir EvalSymlinks check already blocks a
// DIRECTORY-component symlink escape (e.g. .memory/daily -> /etc then
// daily/passwd). This is the secure half of the surface; keep it green so a
// future refactor of safeJoinUnder can't silently regress it.
func TestSafeJoinUnder_ParentSymlink_AlreadyRejected(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink semantics differ on windows; finding targets the linux sidecar")
	}
	base := t.TempDir()
	outsideDir := t.TempDir()
	// .memory/daily is a symlink to an out-of-base directory; the requested
	// file is the final component UNDER that symlinked dir, so the parent
	// resolves outside base and must be rejected.
	if err := os.Symlink(outsideDir, filepath.Join(base, "daily")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	if err := os.WriteFile(filepath.Join(outsideDir, "passwd"), []byte("x"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	if _, err := safeJoinUnder(base, "daily/passwd"); err == nil {
		t.Fatal("parent-symlink escape was accepted — the existing parent-dir guard regressed")
	}
}

// --- Secure target (the post-fix invariant) ---------------------------------
//
// safeJoinUnder (and the read/write handlers) must resolve / reject the FINAL
// component too, so a planted .memory/AGENT.md -> /outside file is refused
// rather than followed. These lock that invariant.

func TestSafeJoinUnder_FinalSymlink_SecureTarget(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink semantics differ on windows; finding targets the linux sidecar")
	}
	base := t.TempDir()
	secretPath, _ := outsideSecret(t)
	plantFinalSymlink(t, base, "AGENT.md", secretPath)
	if _, err := safeJoinUnder(base, "AGENT.md"); err == nil {
		t.Fatal("final-component symlink escape must be rejected")
	}
}

func TestHandleMemoryRead_FinalSymlink_SecureTarget(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink semantics differ on windows; finding targets the linux sidecar")
	}
	s, base := newReadTestServer(t)
	secretPath, _ := outsideSecret(t)
	plantFinalSymlink(t, base, "AGENT.md", secretPath)
	req := httptest.NewRequest("GET", "http://localhost/memory/read?file=AGENT.md", nil)
	rr := httptest.NewRecorder()
	s.handleMemoryRead(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 for final-component symlink escape", rr.Code)
	}
}
