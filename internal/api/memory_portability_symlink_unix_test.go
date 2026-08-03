//go:build !windows

package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Needs a real symlink, which needs privileges on Windows. A build tag
// keeps that a compile-time decision rather than a runtime t.Skip that
// reads as a pass.

// Export must not read through a symlink an agent planted in its own
// memory directory: the response is scoped to one agent and has to stay
// that way.
func TestMemoryExport_DoesNotFollowSymlinks(t *testing.T) {
	h, db, userID, wsID, crewID, base := newMemPortHandlerTest(t)
	seedTestAgentInCrew(t, db, wsID, crewID, "alex")
	seedAgentMemory(t, base, crewID, "alex", "AGENT.md", "mine\n")

	otherDir := t.TempDir()
	other := filepath.Join(otherDir, "other-crew.md")
	if err := os.WriteFile(other, []byte("another crew's memory"), 0o644); err != nil {
		t.Fatal(err)
	}
	memDir := filepath.Join(base, "crews", crewID, "agents", "alex", ".memory")
	if err := os.Symlink(other, filepath.Join(memDir, "pins.md")); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("GET", "/api/v1/memory/export?crew_id="+crewID+"&agent_slug=alex", nil)
	req = withWorkspaceUser(req, userID, wsID, "ADMIN")
	rr := httptest.NewRecorder()
	h.Export(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if strings.Contains(rr.Body.String(), "another crew") {
		t.Fatal("export returned content from outside the agent's memory")
	}
}
