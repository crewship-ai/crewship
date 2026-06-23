package main

// Coverage tests for cmd_crew_cache.go — list/prune of devcontainer
// cache images plus the parsing/formatting helpers.

import (
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

func TestParseDurationExtended(t *testing.T) {
	cases := []struct {
		in      string
		want    time.Duration
		wantErr bool
	}{
		{"30d", 30 * 24 * time.Hour, false},
		{" 2d ", 2 * 24 * time.Hour, false},
		{"72h", 72 * time.Hour, false},
		{"15m", 15 * time.Minute, false},
		{"", 0, true},
		{"   ", 0, true},
		{"-5d", 0, true},
		{"xd", 0, true},
		{"banana", 0, true},
	}
	for _, tc := range cases {
		got, err := parseDurationExtended(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("parseDurationExtended(%q): expected error, got %v", tc.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseDurationExtended(%q): %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("parseDurationExtended(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestFormatSize(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{
		{0, "0 B"},
		{512, "512 B"},
		{2048, "2.0 KB"},
		{5 * 1024 * 1024, "5.0 MB"},
		{3 * 1024 * 1024 * 1024, "3.0 GB"},
	}
	for _, tc := range cases {
		if got := formatSize(tc.in); got != tc.want {
			t.Errorf("formatSize(%d) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestFormatAge(t *testing.T) {
	if got := formatAge(0); got != "—" {
		t.Errorf("formatAge(0) = %q", got)
	}
	now := time.Now()
	cases := []struct {
		ts   int64
		want string
	}{
		{now.Add(-10 * time.Second).Unix(), "10s"},
		{now.Add(-5 * time.Minute).Unix(), "5m"},
		{now.Add(-3 * time.Hour).Unix(), "3h"},
		{now.Add(-49 * time.Hour).Unix(), "2d"},
	}
	for _, tc := range cases {
		if got := formatAge(tc.ts); got != tc.want {
			t.Errorf("formatAge(%d ago) = %q, want %q", tc.ts, got, tc.want)
		}
	}
}

func TestFetchCacheImages(t *testing.T) {
	t.Run("sorted by tag", func(t *testing.T) {
		s := clitest.NewStubServer()
		defer s.Close()
		s.OnGet("/api/v1/cache/images", clitest.JSONResponse(200, map[string]any{
			"images": []map[string]any{
				{"tag": "zeta", "size": 10},
				{"tag": "alpha", "size": 20},
			},
		}))
		imgs, err := fetchCacheImages(covStubClient(s))
		if err != nil {
			t.Fatal(err)
		}
		if len(imgs) != 2 || imgs[0].Tag != "alpha" || imgs[1].Tag != "zeta" {
			t.Errorf("imgs = %+v, want sorted [alpha zeta]", imgs)
		}
	})

	t.Run("API error surfaces", func(t *testing.T) {
		s := clitest.NewStubServer()
		defer s.Close()
		s.OnGet("/api/v1/cache/images", clitest.ErrorResponse(503, "docker down"))
		_, err := fetchCacheImages(covStubClient(s))
		if err == nil || !strings.Contains(err.Error(), "docker down") {
			t.Fatalf("got %v", err)
		}
	})

	t.Run("undecodable body errors", func(t *testing.T) {
		s := clitest.NewStubServer()
		defer s.Close()
		s.OnGet("/api/v1/cache/images", clitest.TextResponse(200, "not json"))
		if _, err := fetchCacheImages(covStubClient(s)); err == nil {
			t.Fatal("expected decode error")
		}
	})
}

func TestDeleteCacheImage(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnDelete("/api/v1/cache/images/crewship-cache:abc", clitest.EmptyResponse(204))

	if err := deleteCacheImage(covStubClient(s), "crewship-cache:abc", false); err != nil {
		t.Fatalf("delete: %v", err)
	}
	calls := s.CallsFor("DELETE", "/api/v1/cache/images/crewship-cache:abc")
	if len(calls) != 1 {
		t.Fatalf("expected 1 DELETE, got %d", len(calls))
	}
	if strings.Contains(calls[0].Query, "force=true") {
		t.Errorf("force flag leaked into non-force delete: %q", calls[0].Query)
	}

	// force=true rides as a query parameter.
	if err := deleteCacheImage(covStubClient(s), "crewship-cache:abc", true); err != nil {
		t.Fatalf("force delete: %v", err)
	}
	calls = s.CallsFor("DELETE", "/api/v1/cache/images/crewship-cache:abc")
	if len(calls) != 2 || !strings.Contains(calls[1].Query, "force=true") {
		t.Errorf("force delete query = %q, want force=true", calls[1].Query)
	}

	// API failure propagates.
	s.OnDelete("/api/v1/cache/images/crewship-cache:ref", clitest.ErrorResponse(409, "image referenced"))
	err := deleteCacheImage(covStubClient(s), "crewship-cache:ref", false)
	if err == nil || !strings.Contains(err.Error(), "image referenced") {
		t.Fatalf("got %v", err)
	}
}

func TestCrewCacheListRunE(t *testing.T) {
	t.Run("no auth", func(t *testing.T) {
		saveCLIState(t)
		cliCfg = &cli.CLIConfig{}
		if err := crewCacheListCmd.RunE(crewCacheListCmd, nil); err == nil || !strings.Contains(err.Error(), "not logged in") {
			t.Errorf("got %v", err)
		}
	})

	t.Run("no workspace", func(t *testing.T) {
		saveCLIState(t)
		cliCfg = &cli.CLIConfig{Token: "tok"}
		flagWorkspace = ""
		t.Setenv("CREWSHIP_WORKSPACE", "")
		if err := crewCacheListCmd.RunE(crewCacheListCmd, nil); err == nil || !strings.Contains(err.Error(), "workspace") {
			t.Errorf("got %v", err)
		}
	})

	t.Run("renders table with size/age/usage", func(t *testing.T) {
		s := clitest.NewStubServer()
		defer s.Close()
		covSetupCLI(t, s)
		s.OnGet("/api/v1/cache/images", clitest.JSONResponse(200, map[string]any{
			"images": []map[string]any{
				{"tag": "crewship-cache:used", "size": 2048, "created_at": time.Now().Add(-3 * time.Hour).Unix(), "referenced_by": []string{"crew-a"}},
				{"tag": "crewship-cache:free", "size": 100, "created_at": 0},
			},
		}))

		out, err := covCaptureStdoutCli7(t, func() error {
			return crewCacheListCmd.RunE(crewCacheListCmd, nil)
		})
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		for _, want := range []string{"crewship-cache:used", "2.0 KB", "crew-a", "—"} {
			if !strings.Contains(out, want) {
				t.Errorf("table missing %q:\n%s", want, out)
			}
		}
	})
}

// covSetPruneFlags snapshots + sets the prune command's bound globals.
func covSetPruneFlags(t *testing.T, olderThan string, unused, force bool) {
	t.Helper()
	origOlder, origUnused, origForce := cachePruneOlderThan, cachePruneUnused, cachePruneForce
	t.Cleanup(func() {
		cachePruneOlderThan, cachePruneUnused, cachePruneForce = origOlder, origUnused, origForce
	})
	cachePruneOlderThan, cachePruneUnused, cachePruneForce = olderThan, unused, force
}

func TestCrewCachePruneRunE(t *testing.T) {
	oldTS := time.Now().Add(-60 * 24 * time.Hour).Unix()
	freshTS := time.Now().Add(-1 * time.Hour).Unix()

	t.Run("force prune removes only old unreferenced images", func(t *testing.T) {
		s := clitest.NewStubServer()
		defer s.Close()
		covSetupCLI(t, s)
		covSetPruneFlags(t, "", false, true) // defaults to --older-than 30d
		s.OnGet("/api/v1/cache/images", clitest.JSONResponse(200, map[string]any{
			"images": []map[string]any{
				{"tag": "old-unused", "size": 1, "created_at": oldTS},
				{"tag": "old-referenced", "size": 1, "created_at": oldTS, "referenced_by": []string{"crew-a"}},
				{"tag": "fresh-unused", "size": 1, "created_at": freshTS},
			},
		}))
		s.OnDelete("/api/v1/cache/images/old-unused", clitest.EmptyResponse(204))

		if err := crewCachePruneCmd.RunE(crewCachePruneCmd, nil); err != nil {
			t.Fatalf("prune: %v", err)
		}
		deletes := 0
		for _, c := range s.Calls() {
			if c.Method == "DELETE" {
				deletes++
				if c.Path != "/api/v1/cache/images/old-unused" {
					t.Errorf("unexpected delete target %s", c.Path)
				}
			}
		}
		if deletes != 1 {
			t.Errorf("expected exactly 1 delete, got %d", deletes)
		}
	})

	t.Run("nothing matches", func(t *testing.T) {
		s := clitest.NewStubServer()
		defer s.Close()
		covSetupCLI(t, s)
		covSetPruneFlags(t, "", false, true)
		s.OnGet("/api/v1/cache/images", clitest.JSONResponse(200, map[string]any{
			"images": []map[string]any{
				{"tag": "fresh", "size": 1, "created_at": freshTS},
			},
		}))
		if err := crewCachePruneCmd.RunE(crewCachePruneCmd, nil); err != nil {
			t.Fatalf("prune: %v", err)
		}
		for _, c := range s.Calls() {
			if c.Method == "DELETE" {
				t.Errorf("no deletes expected, got %s %s", c.Method, c.Path)
			}
		}
	})

	t.Run("invalid --older-than", func(t *testing.T) {
		s := clitest.NewStubServer()
		defer s.Close()
		covSetupCLI(t, s)
		covSetPruneFlags(t, "bogus", false, true)
		err := crewCachePruneCmd.RunE(crewCachePruneCmd, nil)
		if err == nil || !strings.Contains(err.Error(), "invalid --older-than") {
			t.Fatalf("got %v", err)
		}
	})

	t.Run("--unused only skips age filter", func(t *testing.T) {
		s := clitest.NewStubServer()
		defer s.Close()
		covSetupCLI(t, s)
		covSetPruneFlags(t, "", true, true) // unused: no implied 30d default
		s.OnGet("/api/v1/cache/images", clitest.JSONResponse(200, map[string]any{
			"images": []map[string]any{
				{"tag": "fresh-unused", "size": 1, "created_at": freshTS},
			},
		}))
		s.OnDelete("/api/v1/cache/images/fresh-unused", clitest.EmptyResponse(204))
		if err := crewCachePruneCmd.RunE(crewCachePruneCmd, nil); err != nil {
			t.Fatalf("prune --unused: %v", err)
		}
		if n := len(s.CallsFor("DELETE", "/api/v1/cache/images/fresh-unused")); n != 1 {
			t.Errorf("fresh unreferenced image should be pruned with --unused, deletes=%d", n)
		}
	})

	t.Run("non-TTY confirmation defaults to abort", func(t *testing.T) {
		// Without --force the prompt reads stdin; under `go test` stdin is
		// empty so the answer is not "y" → abort, no deletes.
		s := clitest.NewStubServer()
		defer s.Close()
		covSetupCLI(t, s)
		covSetPruneFlags(t, "", false, false)
		s.OnGet("/api/v1/cache/images", clitest.JSONResponse(200, map[string]any{
			"images": []map[string]any{
				{"tag": "old-unused", "size": 1, "created_at": oldTS},
			},
		}))
		out, err := covCaptureStdoutCli7(t, func() error {
			return crewCachePruneCmd.RunE(crewCachePruneCmd, nil)
		})
		if err != nil {
			t.Fatalf("prune aborted run: %v", err)
		}
		if !strings.Contains(out, "Will remove 1 cache image(s)") {
			t.Errorf("expected confirmation listing, got %q", out)
		}
		for _, c := range s.Calls() {
			if c.Method == "DELETE" {
				t.Error("aborted prune must not delete anything")
			}
		}
	})

	t.Run("delete failure is reported but not fatal", func(t *testing.T) {
		s := clitest.NewStubServer()
		defer s.Close()
		covSetupCLI(t, s)
		covSetPruneFlags(t, "", false, true)
		s.OnGet("/api/v1/cache/images", clitest.JSONResponse(200, map[string]any{
			"images": []map[string]any{
				{"tag": "old-a", "size": 1, "created_at": oldTS},
				{"tag": "old-b", "size": 1, "created_at": oldTS},
			},
		}))
		s.OnDelete("/api/v1/cache/images/old-a", clitest.ErrorResponse(500, "boom"))
		s.OnDelete("/api/v1/cache/images/old-b", clitest.EmptyResponse(204))

		if err := crewCachePruneCmd.RunE(crewCachePruneCmd, nil); err != nil {
			t.Fatalf("partial prune should still succeed: %v", err)
		}
		if n := len(s.CallsFor("DELETE", "/api/v1/cache/images/old-b")); n != 1 {
			t.Errorf("surviving target should still be deleted")
		}
	})
}

func TestCrewCacheCmdStructure(t *testing.T) {
	t.Parallel()
	have := map[string]bool{}
	for _, sub := range crewCacheCmd.Commands() {
		have[sub.Name()] = true
	}
	for _, want := range []string{"list", "prune"} {
		if !have[want] {
			t.Errorf("crew cache missing subcommand %q", want)
		}
	}
	for _, name := range []string{"older-than", "unused", "force"} {
		if crewCachePruneCmd.Flags().Lookup(name) == nil {
			t.Errorf("prune missing --%s", name)
		}
	}
}
