//go:build !clionly

package main

import (
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
)

// `crewship db` operates on the SQLite file on THIS host. When the CLI names a
// server, that is a contradiction the command cannot resolve on the operator's
// behalf, so it refuses.
//
// This test used to assert the opposite for two of these rows. The old rule
// printed a note for a REMOTE server and stayed silent for localhost /
// 127.0.0.1, on the inference that a server on this host must be using this
// host's default data directory. That inference is false for every dev clone
// (crewshipd runs with DATABASE_URL=file:./crewship.db), every container, and
// every multi-instance box — and a *note* was never enough anyway for a
// command that overwrites the database. #2086 was reproduced through exactly
// this hole, on http://localhost:8083.
func TestDBLocalOnlyGate(t *testing.T) {
	origServer, origProfile, origCfg := flagServer, flagProfile, cliCfg
	t.Cleanup(func() { flagServer, flagProfile, cliCfg = origServer, origProfile, origCfg })
	t.Setenv("CREWSHIP_SERVER", "")
	t.Setenv("CREWSHIP_PROFILE", "")
	t.Setenv("DATABASE_URL", "")
	t.Setenv("CREWSHIP_DATA_DIR", t.TempDir())

	tests := []struct {
		name       string
		server     string
		wantRefuse bool
	}{
		{"remote server configured", "https://crewship-dev2.example.com", true},
		// Loopback is not an exemption — this is the shape the bug was
		// reproduced in.
		{"localhost target", "http://localhost:8080", true},
		{"loopback target", "http://127.0.0.1:8082", true},
		// Nothing names a server, so "the local file" is the only reading.
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
			if tt.wantRefuse {
				if err == nil {
					t.Fatalf("ran against the local file while targeting %s; output:\n%s", tt.server, out)
				}
				for _, want := range []string{tt.server, "--local", "crewship.db"} {
					if !strings.Contains(err.Error(), want) {
						t.Errorf("refusal does not mention %q:\n%v", want, err)
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("RunE: %v", err)
			}
			if !strings.Contains(out, "crewship.db") {
				t.Errorf("did not name the database file it used:\n%s", out)
			}
		})
	}
}

// --local is the operator saying "I mean the file". It gets past the gate and
// says out loud which file, and which server it is NOT talking to.
func TestDBLocalOnlyGate_LocalFlagOverrides(t *testing.T) {
	origServer, origProfile, origCfg := flagServer, flagProfile, cliCfg
	t.Cleanup(func() { flagServer, flagProfile, cliCfg = origServer, origProfile, origCfg })
	t.Setenv("CREWSHIP_SERVER", "")
	t.Setenv("CREWSHIP_PROFILE", "")
	t.Setenv("DATABASE_URL", "")
	t.Setenv("CREWSHIP_DATA_DIR", t.TempDir())

	flagServer, flagProfile = "", ""
	cliCfg = &cli.CLIConfig{Server: "https://crewship-dev2.example.com"}
	restoreSnapshotList = true
	t.Cleanup(func() { restoreSnapshotList = false })
	// Set it where production declares it — dbCmd's persistent flags — so the
	// test exercises the real inheritance, not a copy.
	covResetFlags(t, dbCmd)
	if err := dbCmd.PersistentFlags().Set("local", "true"); err != nil {
		t.Fatalf("set --local: %v", err)
	}
	t.Cleanup(func() { _ = dbCmd.PersistentFlags().Set("local", "false") })

	var err error
	out := covCaptureAll(t, func() {
		err = restoreSnapshotCmd.RunE(restoreSnapshotCmd, nil)
	})
	if err != nil {
		t.Fatalf("--local was refused: %v\noutput:\n%s", err, out)
	}
	if !strings.Contains(out, "crewship.db") {
		t.Errorf("--local run does not name the database file:\n%s", out)
	}
	if !strings.Contains(out, "crewship-dev2.example.com") {
		t.Errorf("--local run does not say which server it is NOT acting on:\n%s", out)
	}
}
