//go:build !clionly

package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
)

func TestCheckCLIConfigServerScheme(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		cfg        *cli.CLIConfig
		wantStatus string
		mustHave   string // substring required in detail or hint
	}{
		{
			name:       "nil config → INFO",
			cfg:        nil,
			wantStatus: "INFO",
			mustHave:   "no server configured",
		},
		{
			name:       "empty server → INFO",
			cfg:        &cli.CLIConfig{Server: ""},
			wantStatus: "INFO",
		},
		{
			name:       "whitespace-only server → INFO",
			cfg:        &cli.CLIConfig{Server: "   "},
			wantStatus: "INFO",
		},
		{
			name:       "https public host → PASS",
			cfg:        &cli.CLIConfig{Server: "https://crewship.example.com"},
			wantStatus: "PASS",
			mustHave:   "https",
		},
		{
			name:       "https with port → PASS",
			cfg:        &cli.CLIConfig{Server: "https://crewship.example.com:8443"},
			wantStatus: "PASS",
			mustHave:   ":8443",
		},
		{
			name:       "http localhost → PASS (loopback fine)",
			cfg:        &cli.CLIConfig{Server: "http://localhost:8080"},
			wantStatus: "PASS",
			mustHave:   "loopback",
		},
		{
			name:       "http 127.0.0.1 → PASS",
			cfg:        &cli.CLIConfig{Server: "http://127.0.0.1:8080"},
			wantStatus: "PASS",
		},
		{
			name:       "http ::1 → PASS",
			cfg:        &cli.CLIConfig{Server: "http://[::1]:8080"},
			wantStatus: "PASS",
		},
		{
			name:       "http public host → WARN",
			cfg:        &cli.CLIConfig{Server: "http://crewship.example.com"},
			wantStatus: "WARN",
			mustHave:   "plaintext HTTP",
		},
		{
			name:       "http LAN IP → WARN",
			cfg:        &cli.CLIConfig{Server: "http://192.168.1.201:8080"},
			wantStatus: "WARN",
			mustHave:   "plaintext HTTP",
		},
		{
			name:       "https case-insensitive → PASS",
			cfg:        &cli.CLIConfig{Server: "HTTPS://crewship.example.com"},
			wantStatus: "PASS",
		},
		{
			name:       "non-http scheme → FAIL",
			cfg:        &cli.CLIConfig{Server: "ftp://files.example.com"},
			wantStatus: "FAIL",
			mustHave:   "unsupported scheme",
		},
		{
			name:       "malformed url → FAIL",
			cfg:        &cli.CLIConfig{Server: "http://[invalid"},
			wantStatus: "FAIL",
			mustHave:   "malformed",
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := checkCLIConfigServerScheme(tc.cfg)
			if got.status != tc.wantStatus {
				t.Errorf("status = %q, want %q (detail=%q hint=%q)",
					got.status, tc.wantStatus, got.detail, got.hint)
			}
			if tc.mustHave != "" {
				combined := got.detail + " " + got.hint
				if !strings.Contains(combined, tc.mustHave) {
					t.Errorf("expected detail/hint to contain %q, got detail=%q hint=%q",
						tc.mustHave, got.detail, got.hint)
				}
			}
			if got.name != "cli server scheme" {
				t.Errorf("name = %q, want \"cli server scheme\"", got.name)
			}
		})
	}
}

func TestIsLoopbackHost(t *testing.T) {
	t.Parallel()
	cases := []struct {
		host string
		want bool
	}{
		{"localhost", true},
		{"LocalHost", true},
		{"  localhost  ", true},
		{"127.0.0.1", true},
		{"127.1.2.3", true}, // entire 127.0.0.0/8 is loopback
		{"::1", true},
		{"", true},
		{"crewship.example.com", false},
		{"192.168.1.1", false},
		{"10.0.0.1", false},
		{"8.8.8.8", false},
		{"::ffff:127.0.0.1", true}, // IPv4-mapped loopback
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.host, func(t *testing.T) {
			t.Parallel()
			if got := isLoopbackHost(tc.host); got != tc.want {
				t.Errorf("isLoopbackHost(%q) = %v, want %v", tc.host, got, tc.want)
			}
		})
	}
}

// TestCheckCLIConfigPerms_Modes table-drives the perm check against
// a synthesised config file in a temp dir. We point CREWSHIP_CONFIG
// at the temp file so the check picks it up without touching the
// caller's real ~/.crewship/cli-config.yaml.
//
// Skipped on Windows because the file-mode bits don't map to unix
// r/w/x there — same skip pattern as the existing data-dir-perm check.
func TestCheckCLIConfigPerms_Modes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file mode bits don't map cleanly on Windows")
	}

	cases := []struct {
		name       string
		mode       os.FileMode
		wantStatus string
	}{
		{"0600 (canonical)", 0o600, "PASS"},
		{"0400 (read-only owner — stricter is fine)", 0o400, "PASS"},
		{"0644 (group+other read) → WARN", 0o644, "WARN"},
		{"0660 (group rw) → WARN", 0o660, "WARN"},
		{"0606 (other rw) → WARN", 0o606, "WARN"},
		{"0666 (world-writable) → WARN", 0o666, "WARN"},
		{"0700 (executable owner-only) → PASS (stricter than 0600)", 0o700, "PASS"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "cli-config.yaml")
			if err := os.WriteFile(path, []byte("server: http://localhost:8080\n"), 0o600); err != nil {
				t.Fatalf("seed: %v", err)
			}
			if err := os.Chmod(path, tc.mode); err != nil {
				t.Fatalf("chmod: %v", err)
			}
			t.Setenv("CREWSHIP_CONFIG", path)

			got := checkCLIConfigPerms()
			if got.status != tc.wantStatus {
				t.Errorf("mode %#o: status = %q, want %q (detail=%q)",
					tc.mode, got.status, tc.wantStatus, got.detail)
			}
		})
	}
}

func TestCheckCLIConfigPerms_Missing(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file mode bits don't map cleanly on Windows")
	}
	dir := t.TempDir()
	missing := filepath.Join(dir, "no-such-file.yaml")
	t.Setenv("CREWSHIP_CONFIG", missing)
	got := checkCLIConfigPerms()
	if got.status != "INFO" {
		t.Errorf("missing file: status = %q, want INFO", got.status)
	}
	if !strings.Contains(got.detail, "no cli-config.yaml yet") {
		t.Errorf("detail = %q, want hint about crewship login", got.detail)
	}
}
