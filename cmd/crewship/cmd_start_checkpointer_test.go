package main

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// database.WithManagedWAL turns SQLite's inline autocheckpoint off, which
// makes the StartCheckpointerAsync goroutine the ONLY thing folding the WAL
// back into the database. The two are a pair: shipping the option without
// the goroutine grows the -wal file until the disk fills, and there is no
// runtime symptom until it does.
//
// A source-level assertion is the honest test here. The alternative —
// booting the daemon in-process — would drag in Docker providers, the
// listener and the migration path just to observe two lines of wiring, and
// would still not fail if someone deleted one of them and the other
// silently kept working. This test fails the build the moment the pairing
// is broken, which is the only outcome that matters.
//
// It mirrors the source-scanning style of
// internal/api/admin_authz_floor_test.go's TestEveryAdminReadDeclaresFloor.
func TestDaemonPairsManagedWALWithCheckpointer(t *testing.T) {
	src, err := os.ReadFile("cmd_start.go")
	if err != nil {
		t.Fatalf("read cmd_start.go: %v", err)
	}
	text := string(src)

	tests := []struct {
		name    string
		pattern *regexp.Regexp
		why     string
	}{
		{
			name:    "daemon opens the database with WithManagedWAL",
			pattern: regexp.MustCompile(`database\.Open\([^)]*database\.WithManagedWAL\(\)`),
			why:     "without it the daemon pays SQLite's inline checkpoint on random agent writes (p99 26.1ms vs 8.9ms measured)",
		},
		{
			name:    "daemon starts the checkpointer",
			pattern: regexp.MustCompile(`database\.StartCheckpointerAsync\(`),
			why:     "with autocheckpoint disabled by WithManagedWAL, nothing else reclaims the WAL and the disk fills",
		},
		{
			name:    "the checkpointer stop is deferred so it runs before db.Close",
			pattern: regexp.MustCompile(`defer database\.StartCheckpointerAsync\([^\n]*\)\(\)`),
			why:     "the shutdown TRUNCATE is a write; if it races db.Close() the WAL survives into the next boot",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if !tc.pattern.MatchString(text) {
				t.Errorf("cmd_start.go no longer matches %v\nwhy this matters: %s", tc.pattern, tc.why)
			}
		})
	}

	// Ordering: the checkpointer must be armed BEFORE migrations run.
	// Migrations are the most write-heavy phase of a boot, and with
	// autocheckpoint off that is exactly when an unattended WAL grows
	// fastest.
	ckpt := strings.Index(text, "database.StartCheckpointerAsync(")
	migrate := strings.Index(text, "database.Migrate(")
	switch {
	case ckpt < 0 || migrate < 0:
		t.Fatal("could not locate both the checkpointer start and the migration call")
	case ckpt > migrate:
		t.Error("the checkpointer is started after database.Migrate; migrations are the heaviest write phase of a boot and must not run with an unattended WAL")
	}

	// Only the daemon may disable autocheckpoint. Every other Open caller
	// is a short-lived process that will not run the loop, so for them the
	// built-in autocheckpoint is the only thing keeping the WAL in check.
	for _, f := range []string{"cmd_telemetry.go", "cmd_admin.go"} {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		if strings.Contains(string(b), "WithManagedWAL") {
			t.Errorf("%s uses database.WithManagedWAL but is not a long-lived process that runs the checkpointer", f)
		}
	}
}
