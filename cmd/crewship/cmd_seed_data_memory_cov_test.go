package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

func TestSafePathSegment(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in      string
		wantErr bool
	}{
		{"crew_abc123", false},
		{"viktor", false},
		{"with-dash.and.dot", false},
		{"", true},
		{".", true},
		{"..", true},
		{"a/b", true},
		{`a\b`, true},
		{"nul\x00byte", true},
	}
	for _, tt := range tests {
		got, err := safePathSegment(tt.in)
		if tt.wantErr {
			if err == nil {
				t.Errorf("safePathSegment(%q) should fail", tt.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("safePathSegment(%q): %v", tt.in, err)
		}
		if got != tt.in {
			t.Errorf("safePathSegment(%q) = %q, want identity", tt.in, got)
		}
	}
}

func TestResolveStorageBasePath(t *testing.T) {
	t.Run("env wins", func(t *testing.T) {
		t.Setenv("CREWSHIP_STORAGE_BASE_PATH", "/tmp/cov-storage")
		if got := resolveStorageBasePath(); got != "/tmp/cov-storage" {
			t.Errorf("got %q, want env value", got)
		}
	})
	t.Run("falls back to home", func(t *testing.T) {
		t.Setenv("CREWSHIP_STORAGE_BASE_PATH", "")
		home := t.TempDir()
		t.Setenv("HOME", home)
		if got := resolveStorageBasePath(); got != filepath.Join(home, ".crewship") {
			t.Errorf("got %q, want %q", got, filepath.Join(home, ".crewship"))
		}
	})
}

func TestWriteFileIfAbsent(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "f.md")
	if err := writeFileIfAbsent(path, "original"); err != nil {
		t.Fatalf("first write: %v", err)
	}
	if err := writeFileIfAbsent(path, "clobber attempt"); err != nil {
		t.Fatalf("second write: %v", err)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "original" {
		t.Errorf("existing file was clobbered: %q", data)
	}
}

func TestDemoMarkdownContents(t *testing.T) {
	t.Parallel()
	if got := demoAgentMD("viktor", "backend"); !strings.Contains(got, "AGENT.md — viktor") || !strings.Contains(got, "My crew: backend") {
		t.Errorf("demoAgentMD missing slug/crew:\n%s", got)
	}
	if got := demoCrewMD("backend"); !strings.Contains(got, "CREW.md — backend") || !strings.Contains(got, "Crew slug: backend") {
		t.Errorf("demoCrewMD missing crew slug:\n%s", got)
	}
	if got := demoPersonaMD("eva"); !strings.Contains(got, "PERSONA.md — eva voice") || !strings.Contains(got, "⛵ eva") {
		t.Errorf("demoPersonaMD missing slug:\n%s", got)
	}
	if got := demoPinsMD(); !strings.Contains(got, "PINNED-1") || !strings.Contains(got, "PINNED-4") {
		t.Errorf("demoPinsMD missing pins:\n%s", got)
	}
	if got := demoDailyMD("2026-05-12", "viktor"); !strings.Contains(got, "daily/2026-05-12.md — viktor") {
		t.Errorf("demoDailyMD missing header:\n%s", got)
	}
	if got := demoLearnedMD(); !strings.Contains(got, "LESSON-001") || !strings.Contains(got, "LESSON-003") {
		t.Errorf("demoLearnedMD missing lessons:\n%s", got)
	}
}

func covSeedClient(t *testing.T, agents any) (*cli.Client, *clitest.StubServer) {
	t.Helper()
	s := clitest.NewStubServer()
	t.Cleanup(s.Close)
	s.OnGet("/api/v1/agents", clitest.JSONResponse(200, agents))
	return cli.NewClient(s.URL(), "tok", covWSCli9), s
}

func TestSeedAgentMemory_WritesAllTiers(t *testing.T) {
	base := t.TempDir()
	t.Setenv("CREWSHIP_STORAGE_BASE_PATH", base)
	client, _ := covSeedClient(t, []map[string]string{
		{"slug": "viktor", "crew_id": "crew1"},
		{"slug": "orphan", "crew_id": ""},        // skipped: no crew
		{"slug": "stranger", "crew_id": "crewX"}, // skipped: crew not in seed map
	})

	if err := seedAgentMemory(context.Background(), client, map[string]string{"backend": "crew1"}); err != nil {
		t.Fatalf("seedAgentMemory: %v", err)
	}

	month := time.Now().AddDate(0, -1, 0).Format("2006-01-02")
	wantFiles := []string{
		filepath.Join(base, "crews", "crew1", "shared", ".memory", "CREW.md"),
		filepath.Join(base, "crews", "crew1", "shared", ".memory", "learned.md"),
		filepath.Join(base, "crews", "crew1", "agents", "viktor", ".memory", "AGENT.md"),
		filepath.Join(base, "crews", "crew1", "agents", "viktor", ".memory", "PERSONA.md"),
		filepath.Join(base, "crews", "crew1", "agents", "viktor", ".memory", "pins.md"),
		filepath.Join(base, "crews", "crew1", "agents", "viktor", ".memory", "daily", month+".md"),
	}
	for _, f := range wantFiles {
		if _, err := os.Stat(f); err != nil {
			t.Errorf("expected seeded file %s: %v", f, err)
		}
	}
	// Agents with no/unknown crew must not get a directory.
	for _, slug := range []string{"orphan", "stranger"} {
		matches, _ := filepath.Glob(filepath.Join(base, "crews", "*", "agents", slug))
		if len(matches) != 0 {
			t.Errorf("agent %s should be skipped, found %v", slug, matches)
		}
	}
	// Content is personalised.
	data, _ := os.ReadFile(wantFiles[2])
	if !strings.Contains(string(data), "viktor") || !strings.Contains(string(data), "backend") {
		t.Errorf("AGENT.md not personalised:\n%s", data)
	}
}

func TestSeedAgentMemory_IdempotentRerunKeepsEdits(t *testing.T) {
	base := t.TempDir()
	t.Setenv("CREWSHIP_STORAGE_BASE_PATH", base)
	client, _ := covSeedClient(t, []map[string]string{{"slug": "viktor", "crew_id": "crew1"}})
	crews := map[string]string{"backend": "crew1"}

	if err := seedAgentMemory(context.Background(), client, crews); err != nil {
		t.Fatalf("first seed: %v", err)
	}
	agentMD := filepath.Join(base, "crews", "crew1", "agents", "viktor", ".memory", "AGENT.md")
	if err := os.WriteFile(agentMD, []byte("operator edit"), 0o644); err != nil {
		t.Fatalf("simulate operator edit: %v", err)
	}
	if err := seedAgentMemory(context.Background(), client, crews); err != nil {
		t.Fatalf("re-seed: %v", err)
	}
	data, _ := os.ReadFile(agentMD)
	if string(data) != "operator edit" {
		t.Errorf("re-seed must not clobber operator edits: %q", data)
	}
}

func TestSeedAgentMemory_Errors(t *testing.T) {
	t.Run("cancelled context", func(t *testing.T) {
		t.Setenv("CREWSHIP_STORAGE_BASE_PATH", t.TempDir())
		client, _ := covSeedClient(t, []map[string]string{})
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := seedAgentMemory(ctx, client, nil); err == nil {
			t.Error("cancelled ctx should error")
		}
	})
	t.Run("no base path", func(t *testing.T) {
		t.Setenv("CREWSHIP_STORAGE_BASE_PATH", "")
		t.Setenv("HOME", "")
		client, _ := covSeedClient(t, []map[string]string{})
		err := seedAgentMemory(context.Background(), client, nil)
		if err == nil || !strings.Contains(err.Error(), "CREWSHIP_STORAGE_BASE_PATH") {
			t.Errorf("expected base-path error; got %v", err)
		}
	})
	t.Run("agents API error", func(t *testing.T) {
		t.Setenv("CREWSHIP_STORAGE_BASE_PATH", t.TempDir())
		s := clitest.NewStubServer()
		t.Cleanup(s.Close)
		s.OnGet("/api/v1/agents", clitest.ErrorResponse(500, "agents down"))
		client := cli.NewClient(s.URL(), "tok", covWSCli9)
		err := seedAgentMemory(context.Background(), client, map[string]string{})
		if err == nil || !strings.Contains(err.Error(), "list agents") {
			t.Errorf("expected list-agents error; got %v", err)
		}
	})
	t.Run("traversal crew id in seed map", func(t *testing.T) {
		base := t.TempDir()
		t.Setenv("CREWSHIP_STORAGE_BASE_PATH", base)
		client, _ := covSeedClient(t, []map[string]string{})
		err := seedAgentMemory(context.Background(), client, map[string]string{"bad": "../../etc"})
		if err == nil || !strings.Contains(err.Error(), "invalid crew_id") {
			t.Errorf("expected invalid crew_id error; got %v", err)
		}
		if _, statErr := os.Stat(filepath.Join(base, "..", "..", "etc", "shared")); statErr == nil {
			t.Error("traversal crew id must not create directories outside base")
		}
	})
	t.Run("transport error listing agents", func(t *testing.T) {
		t.Setenv("CREWSHIP_STORAGE_BASE_PATH", t.TempDir())
		s := clitest.NewStubServer()
		deadURL := s.URL()
		s.Close()
		client := cli.NewClient(deadURL, "tok", covWSCli9)
		err := seedAgentMemory(context.Background(), client, map[string]string{})
		if err == nil || !strings.Contains(err.Error(), "list agents") {
			t.Errorf("expected transport error; got %v", err)
		}
	})
	t.Run("agents decode error", func(t *testing.T) {
		t.Setenv("CREWSHIP_STORAGE_BASE_PATH", t.TempDir())
		s := clitest.NewStubServer()
		t.Cleanup(s.Close)
		s.OnGet("/api/v1/agents", clitest.TextResponse(200, "{nope"))
		client := cli.NewClient(s.URL(), "tok", covWSCli9)
		err := seedAgentMemory(context.Background(), client, map[string]string{})
		if err == nil || !strings.Contains(err.Error(), "parse agents") {
			t.Errorf("expected parse error; got %v", err)
		}
	})
	t.Run("shared mkdir blocked", func(t *testing.T) {
		base := filepath.Join(t.TempDir(), "blocker")
		// basePath itself is a regular file → MkdirAll under it fails.
		if err := os.WriteFile(base, []byte("x"), 0o644); err != nil {
			t.Fatalf("write blocker: %v", err)
		}
		t.Setenv("CREWSHIP_STORAGE_BASE_PATH", base)
		client, _ := covSeedClient(t, []map[string]string{})
		err := seedAgentMemory(context.Background(), client, map[string]string{"backend": "crew1"})
		if err == nil || !strings.Contains(err.Error(), "mkdir shared mem") {
			t.Errorf("expected shared mkdir error; got %v", err)
		}
	})
	t.Run("agent mkdir blocked", func(t *testing.T) {
		base := t.TempDir()
		t.Setenv("CREWSHIP_STORAGE_BASE_PATH", base)
		// Pre-create {base}/crews/crew1/agents as a FILE so the per-agent
		// MkdirAll fails after the shared tier succeeded.
		if err := os.MkdirAll(filepath.Join(base, "crews", "crew1"), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(base, "crews", "crew1", "agents"), []byte("x"), 0o644); err != nil {
			t.Fatalf("write blocker: %v", err)
		}
		client, _ := covSeedClient(t, []map[string]string{{"slug": "viktor", "crew_id": "crew1"}})
		err := seedAgentMemory(context.Background(), client, map[string]string{"backend": "crew1"})
		if err == nil || !strings.Contains(err.Error(), "mkdir agent mem") {
			t.Errorf("expected agent mkdir error; got %v", err)
		}
	})
	t.Run("crew shared file write blocked", func(t *testing.T) {
		base := t.TempDir()
		t.Setenv("CREWSHIP_STORAGE_BASE_PATH", base)
		sharedMem := filepath.Join(base, "crews", "crew1", "shared", ".memory")
		if err := os.MkdirAll(sharedMem, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.Chmod(sharedMem, 0o555); err != nil {
			t.Fatalf("chmod: %v", err)
		}
		t.Cleanup(func() { _ = os.Chmod(sharedMem, 0o755) })
		client, _ := covSeedClient(t, []map[string]string{})
		if err := seedAgentMemory(context.Background(), client, map[string]string{"backend": "crew1"}); err == nil {
			t.Error("expected CREW.md write failure on read-only dir")
		}
	})
	t.Run("agent file write blocked", func(t *testing.T) {
		base := t.TempDir()
		t.Setenv("CREWSHIP_STORAGE_BASE_PATH", base)
		agentMem := filepath.Join(base, "crews", "crew1", "agents", "viktor", ".memory")
		if err := os.MkdirAll(filepath.Join(agentMem, "daily"), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.Chmod(agentMem, 0o555); err != nil {
			t.Fatalf("chmod: %v", err)
		}
		t.Cleanup(func() { _ = os.Chmod(agentMem, 0o755) })
		client, _ := covSeedClient(t, []map[string]string{{"slug": "viktor", "crew_id": "crew1"}})
		if err := seedAgentMemory(context.Background(), client, map[string]string{"backend": "crew1"}); err == nil {
			t.Error("expected AGENT.md write failure on read-only dir")
		}
	})
	t.Run("traversal agent slug from API", func(t *testing.T) {
		t.Setenv("CREWSHIP_STORAGE_BASE_PATH", t.TempDir())
		client, _ := covSeedClient(t, []map[string]string{{"slug": "../evil", "crew_id": "crew1"}})
		err := seedAgentMemory(context.Background(), client, map[string]string{"backend": "crew1"})
		if err == nil || !strings.Contains(err.Error(), "invalid agent slug") {
			t.Errorf("expected invalid agent slug error; got %v", err)
		}
	})
}
