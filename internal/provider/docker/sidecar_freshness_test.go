package docker

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestSidecarStaleReason exercises the pure freshness classifier that backs the
// startup assertion (#1390). A sidecar meaningfully older than the server binary
// it is deployed alongside was not rebuilt for this deploy and may be missing
// sidecar-side features shipped since (the live dev1 symptom was #1387 token_fp
// absent on /health). Everything ambiguous fails open (returns "").
func TestSidecarStaleReason(t *testing.T) {
	base := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name         string
		sidecarMtime time.Time
		serverMtime  time.Time
		wantStale    bool
	}{
		{
			name:         "sidecar far older than server -> stale",
			sidecarMtime: base.Add(-2 * time.Hour),
			serverMtime:  base,
			wantStale:    true,
		},
		{
			name:         "sidecar newer than server -> fresh",
			sidecarMtime: base.Add(1 * time.Minute),
			serverMtime:  base,
			wantStale:    false,
		},
		{
			name:         "sidecar equal to server -> fresh",
			sidecarMtime: base,
			serverMtime:  base,
			wantStale:    false,
		},
		{
			name:         "sidecar within skew window -> fresh (same build)",
			sidecarMtime: base.Add(-30 * time.Second),
			serverMtime:  base,
			wantStale:    false,
		},
		{
			name:         "sidecar just past skew window -> stale",
			sidecarMtime: base.Add(-(startupMtimeSkew + time.Second)),
			serverMtime:  base,
			wantStale:    true,
		},
		{
			name:         "zero sidecar time -> fail open",
			sidecarMtime: time.Time{},
			serverMtime:  base,
			wantStale:    false,
		},
		{
			name:         "zero server time -> fail open",
			sidecarMtime: base,
			serverMtime:  time.Time{},
			wantStale:    false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sidecarStaleReason(tt.sidecarMtime, tt.serverMtime)
			if (got != "") != tt.wantStale {
				t.Fatalf("sidecarStaleReason(%v, %v) = %q; wantStale=%v",
					tt.sidecarMtime, tt.serverMtime, got, tt.wantStale)
			}
		})
	}
}

// TestAssertSidecarFreshAtStartup_WarnsOnStale drives the Provider-level startup
// assertion against real files with controlled mtimes and asserts it emits a
// loud WARN referencing #1390 for a stale sidecar, and stays silent for a fresh
// one. The method only stats files + logs, so a Provider with just cfg+logger
// (no docker client) is sufficient.
func TestAssertSidecarFreshAtStartup_WarnsOnStale(t *testing.T) {
	dir := t.TempDir()
	server := filepath.Join(dir, "server-bin")
	sidecar := filepath.Join(dir, "crewship-sidecar")
	for _, f := range []string{server, sidecar} {
		if err := os.WriteFile(f, []byte("x"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	serverT := time.Now()
	// Sidecar two hours older than the server binary -> stale.
	if err := os.Chtimes(server, serverT, serverT); err != nil {
		t.Fatal(err)
	}
	staleT := serverT.Add(-2 * time.Hour)
	if err := os.Chtimes(sidecar, staleT, staleT); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))
	p := &Provider{cfg: Config{SidecarBinaryPath: sidecar}, logger: logger}

	p.assertSidecarFreshAtStartup(server)

	out := buf.String()
	if !strings.Contains(out, "#1390") {
		t.Fatalf("expected a stale-sidecar WARN mentioning #1390, got:\n%s", out)
	}
	if !strings.Contains(out, "level=WARN") {
		t.Fatalf("expected WARN level, got:\n%s", out)
	}

	// Fresh sidecar (rebuilt alongside the server) -> no warning.
	buf.Reset()
	freshT := serverT.Add(time.Minute)
	if err := os.Chtimes(sidecar, freshT, freshT); err != nil {
		t.Fatal(err)
	}
	p.assertSidecarFreshAtStartup(server)
	if buf.Len() != 0 {
		t.Fatalf("expected no warning for a fresh sidecar, got:\n%s", buf.String())
	}
}

// TestAssertSidecarFreshAtStartup_NoBindMount verifies the assertion is a no-op
// (fail-open, no warning) when no sidecar path is configured — the container
// then uses the baked-in sidecar and there is nothing to compare.
func TestAssertSidecarFreshAtStartup_NoBindMount(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))
	p := &Provider{cfg: Config{SidecarBinaryPath: ""}, logger: logger}
	p.assertSidecarFreshAtStartup("/nonexistent/server")
	if buf.Len() != 0 {
		t.Fatalf("expected no warning when no sidecar is bind-mounted, got:\n%s", buf.String())
	}
}
