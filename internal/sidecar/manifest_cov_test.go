package sidecar

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// requireNoCrewDir skips tests that depend on the hard-coded
// /crew/manifest.json path being absent (the normal situation on a dev
// machine — the path only exists inside crew containers).
func requireNoCrewDir(t *testing.T) {
	t.Helper()
	if _, err := os.Stat("/crew"); err == nil {
		t.Skip("/crew exists on this host; manifest tests assume it is absent")
	}
}

func TestCovMergeCredentials(t *testing.T) {
	tests := []struct {
		name      string
		existing  []ManifestCredEntry
		additions []ManifestCredEntry
		want      []ManifestCredEntry
	}{
		{"empty both", nil, nil, nil},
		{
			"add to empty",
			nil,
			[]ManifestCredEntry{{Name: "gh", Agent: "viktor", Type: "SECRET"}},
			[]ManifestCredEntry{{Name: "gh", Agent: "viktor", Type: "SECRET"}},
		},
		{
			"dedupes on name+agent",
			[]ManifestCredEntry{{Name: "gh", Agent: "viktor", Type: "SECRET"}},
			[]ManifestCredEntry{
				{Name: "gh", Agent: "viktor", Type: "ENV"}, // dup key, different type → dropped
				{Name: "gh", Agent: "eva", Type: "SECRET"}, // same name, other agent → kept
			},
			[]ManifestCredEntry{
				{Name: "gh", Agent: "viktor", Type: "SECRET"},
				{Name: "gh", Agent: "eva", Type: "SECRET"},
			},
		},
		{
			"dedupes within additions",
			nil,
			[]ManifestCredEntry{
				{Name: "aws", Agent: "nela", Type: "SECRET"},
				{Name: "aws", Agent: "nela", Type: "SECRET"},
			},
			[]ManifestCredEntry{{Name: "aws", Agent: "nela", Type: "SECRET"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := mergeCredentials(tt.existing, tt.additions)
			if len(got) != len(tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("[%d] got %+v, want %+v", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestCovReadManifestDefaultWhenMissing(t *testing.T) {
	requireNoCrewDir(t)

	m, err := readManifest()
	if err != nil {
		t.Fatalf("readManifest: %v", err)
	}
	if m.Version != 1 {
		t.Errorf("default manifest version = %d, want 1", m.Version)
	}
	if len(m.Packages.Apt) != 0 || len(m.Credentials) != 0 || len(m.SetupCommands) != 0 {
		t.Errorf("default manifest should be empty, got %+v", m)
	}
}

func TestCovWriteManifestFailsWithoutCrewDir(t *testing.T) {
	requireNoCrewDir(t)
	if err := os.MkdirAll("/crew", 0o755); err == nil {
		os.Remove("/crew")
		t.Skip("/crew is creatable on this host; cannot exercise the write-error path")
	}

	err := writeManifest(&CrewManifest{Version: 1})
	if err == nil {
		t.Fatal("expected writeManifest to fail when /crew cannot be created")
	}
}

func TestCovHandleGetManifestDefault(t *testing.T) {
	requireNoCrewDir(t)

	srv := NewServer(ServerConfig{Addr: "127.0.0.1:0", Logger: covLogger()})
	req := httptest.NewRequest("GET", "http://localhost:9119/manifest", nil)
	w := httptest.NewRecorder()
	srv.handleGetManifest(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var m CrewManifest
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if m.Version != 1 {
		t.Errorf("version = %d, want 1", m.Version)
	}
}

func TestCovHandleUpdateManifestInvalidJSON(t *testing.T) {
	srv := NewServer(ServerConfig{Addr: "127.0.0.1:0", Logger: covLogger()})
	req := httptest.NewRequest("PATCH", "http://localhost:9119/manifest", strings.NewReader("{nope"))
	w := httptest.NewRecorder()
	srv.handleUpdateManifest(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "invalid JSON") {
		t.Errorf("body = %s", w.Body.String())
	}
}

// TestCovHandleUpdateManifestWriteFailure walks the full merge path (missing
// manifest → fresh default, packages/credentials/setup commands merged) and
// then hits the write-error branch because /crew is not creatable on the
// test host.
func TestCovHandleUpdateManifestWriteFailure(t *testing.T) {
	requireNoCrewDir(t)
	if err := os.MkdirAll("/crew", 0o755); err == nil {
		os.Remove("/crew")
		t.Skip("/crew is creatable on this host; cannot exercise the write-error path")
	}

	srv := NewServer(ServerConfig{Addr: "127.0.0.1:0", Logger: covLogger()})
	patch := `{
		"packages": {"apt": ["jq"], "npm": ["pnpm"], "pip": ["requests"]},
		"credentials": [{"name": "gh", "agent": "viktor", "type": "SECRET"}],
		"setup_commands": ["corepack enable"]
	}`
	req := httptest.NewRequest("PATCH", "http://localhost:9119/manifest", strings.NewReader(patch))
	w := httptest.NewRecorder()
	srv.handleUpdateManifest(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 (write failure), got %d: %s", w.Code, w.Body.String())
	}
	var body map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid JSON error body: %v", err)
	}
	if body["error"] == "" {
		t.Error("expected error message in body")
	}
}
