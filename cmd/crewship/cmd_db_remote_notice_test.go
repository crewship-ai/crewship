//go:build !clionly

package main

import (
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
)

// TestDBRemoteTargetNotice: `crewship db` operates on the LOCAL SQLite file;
// when the CLI is pointed at a remote server (profile / CREWSHIP_SERVER /
// --server), the operator must be told the command will NOT touch that
// remote — silently doing local-disk maintenance while "targeting" dev2 is
// exactly the confusion the audit flagged.
func TestDBRemoteTargetNotice(t *testing.T) {
	origServer, origProfile, origCfg := flagServer, flagProfile, cliCfg
	t.Cleanup(func() { flagServer, flagProfile, cliCfg = origServer, origProfile, origCfg })
	t.Setenv("CREWSHIP_SERVER", "")
	t.Setenv("CREWSHIP_PROFILE", "")
	t.Setenv("CREWSHIP_DATA_DIR", t.TempDir())

	tests := []struct {
		name       string
		server     string
		wantNotice bool
	}{
		{"remote server configured", "https://crewship-dev2.example.com", true},
		{"localhost target", "http://localhost:8080", false},
		{"loopback target", "http://127.0.0.1:8082", false},
		{"no server configured", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			flagServer, flagProfile = "", ""
			cliCfg = &cli.CLIConfig{Server: tt.server}
			restoreSnapshotList = true
			t.Cleanup(func() { restoreSnapshotList = false })

			var err error
			out := covCaptureAll(t, func() {
				err = restoreSnapshotCmd.RunE(restoreSnapshotCmd, nil)
			})
			if err != nil {
				t.Fatalf("RunE: %v", err)
			}
			gotNotice := strings.Contains(out, "only touches the LOCAL database")
			if gotNotice != tt.wantNotice {
				t.Errorf("notice printed = %v, want %v; output:\n%s", gotNotice, tt.wantNotice, out)
			}
		})
	}
}
