package server

// #2142 — the crewshipd side of the fix.
//
// GET /crews/{id}/files/download is the ONE funnel every download reaches on
// this side of the Unix socket, whichever internal/api door dialed it —
// AgentFileDownload and CrewFileDownload both forward here. internal/api
// already refuses the six generated per-agent MCP-config files on both
// doors (proxy_files.go's isProtectedAgentConfigPath /
// IsProtectedCrewConfigPath), but a caller that reaches this endpoint
// directly — a future internal/api route, or crewshipd itself if it ever
// grows another HTTP-facing caller — got no such protection. This pins the
// funnel itself, independent of what called it.

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

var protectedConfigRelPaths = []string{
	".mcp.json",
	".cursor/mcp.json",
	".factory/mcp.json",
	".gemini/settings.json",
	"opencode.json",
	".codex/config.toml",
}

// seedCrewOutputFile writes content directly under the storage base's
// /output tree (<basePath>/<crewID>/<slug>/<rel>), the same shape
// resolveCrewFileKey resolves a "<crewID>/<slug>/..." download path to.
func seedCrewOutputFile(t *testing.T, basePath, crewID, slug, rel, content string) {
	t.Helper()
	full := filepath.Join(basePath, crewID, slug, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir for %s: %v", full, err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", full, err)
	}
}

func downloadCrewFile(s *Server, crewID, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("GET", "/crews/"+crewID+"/files/download?path="+path, nil)
	req.SetPathValue("id", crewID)
	rec := httptest.NewRecorder()
	s.ipcMux.ServeHTTP(rec, req)
	return rec
}

// TestHandleFileDownload_RefusesProtectedAgentConfig_AnySlug pins the funnel
// guard directly: whatever agent slug the path names, the six generated
// files never leave this endpoint as bytes, even though the file genuinely
// exists in storage and would otherwise download cleanly.
func TestHandleFileDownload_RefusesProtectedAgentConfig_AnySlug(t *testing.T) {
	s, dir := newFileServer(t)
	const crewID = "crew-2142"

	for _, slug := range []string{"riley", "some-other-agent-nobody-named-in-this-test"} {
		for _, rel := range protectedConfigRelPaths {
			t.Run(slug+"/"+rel, func(t *testing.T) {
				seedCrewOutputFile(t, dir, crewID, slug, rel, "SECRET-MCP-CREDENTIAL")

				rec := downloadCrewFile(s, crewID, crewID+"/"+slug+"/"+rel)
				if rec.Code != http.StatusForbidden {
					t.Fatalf("status = %d, want 403; body=%s", rec.Code, rec.Body.String())
				}
				if rec.Body.String() == "SECRET-MCP-CREDENTIAL" {
					t.Fatalf("LEAK: protected config bytes were served (status %d)", rec.Code)
				}
			})
		}
	}
}

// TestHandleFileDownload_StillServesOrdinaryAgentFiles guards against the
// lazy fix: the funnel must still serve everything else. A file that merely
// LIVES alongside the protected ones, or shares a directory name with one of
// them one level up, must download normally.
func TestHandleFileDownload_StillServesOrdinaryAgentFiles(t *testing.T) {
	s, dir := newFileServer(t)
	const crewID, slug = "crew-2142b", "riley"

	cases := []struct {
		name, rel, content string
	}{
		{"ordinary output file", "report.md", "the actual deliverable"},
		{"nested file that merely CONTAINS a protected name", "docs/.mcp.json/notes.md", "not the real thing"},
		{"skills directory, not the config file", ".codex/skills/reviewer/SKILL.md", "a skill, not a secret"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			seedCrewOutputFile(t, dir, crewID, slug, tc.rel, tc.content)
			rec := downloadCrewFile(s, crewID, crewID+"/"+slug+"/"+tc.rel)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
			}
			if rec.Body.String() != tc.content {
				t.Fatalf("body = %q, want %q", rec.Body.String(), tc.content)
			}
		})
	}
}

// TestHandleFileDownload_SharedTreeUnaffected pins that the guard is scoped
// to the per-agent /output tree, not the crew's shared bind tree — a file
// literally named ".mcp.json" under shared/ is user-authored content placed
// there on purpose (a Crew manifest `files:` entry), not the generated
// credential file, and resolveCrewFileKey routes it to a different storage
// key entirely (crews/<id>/shared/... vs <id>/<slug>/...).
func TestHandleFileDownload_SharedTreeUnaffected(t *testing.T) {
	s, dir := newFileServer(t)
	const crewID = "crew-2142c"

	sharedFile := filepath.Join(dir, "crews", crewID, "shared", ".mcp.json")
	if err := os.MkdirAll(filepath.Dir(sharedFile), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(sharedFile, []byte("bundled, not generated"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	rec := downloadCrewFile(s, crewID, "shared/.mcp.json")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != "bundled, not generated" {
		t.Fatalf("body = %q, want the seeded content", rec.Body.String())
	}
}
