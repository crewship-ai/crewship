//go:build unix

package sidecar

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMemoryTrustedAnchorRejectsTierSwapIntoAnotherTier(t *testing.T) {
	s, base := newWriteTestServer(t)
	sibling := t.TempDir()
	victim := filepath.Join(sibling, "AGENT.md")
	if err := os.WriteFile(victim, []byte("other-agent-secret"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(base, base+"-original"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(sibling, base); err != nil {
		t.Fatalf("create required symlink fixture: %v", err)
	}
	// The old pathname guard accepts a replaced tier base: both base and
	// target parent resolve to the other tier. The trusted ancestor must
	// reject the symlink component, even though it stays inside the ancestor.
	if _, err := safeJoinUnder(base, "AGENT.md"); err != nil {
		t.Fatalf("fixture must reach rooted guard: %v", err)
	}
	for _, write := range []bool{false, true} {
		r := httptest.NewRequest("GET", "/memory/read?file=AGENT.md", nil)
		if write {
			r = httptest.NewRequest("POST", "/memory/write", strings.NewReader(`{"file":"AGENT.md","content":"attacker replacement"}`))
		}
		w := httptest.NewRecorder()
		if write {
			s.handleMemoryWrite(w, r)
		} else {
			s.handleMemoryRead(w, r)
		}
		if w.Code < 400 || strings.Contains(w.Body.String(), "other-agent-secret") {
			t.Fatalf("outside tier response %d %s", w.Code, w.Body.String())
		}
	}
	got, err := os.ReadFile(victim)
	if err != nil || string(got) != "other-agent-secret" {
		t.Fatalf("other tier changed: %q %v", got, err)
	}
	if _, err := os.Lstat(victim + ".lock"); !os.IsNotExist(err) {
		t.Fatalf("other tier lock created: %v", err)
	}
}
