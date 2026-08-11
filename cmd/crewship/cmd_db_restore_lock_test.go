//go:build !clionly

package main

import (
	"database/sql"
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/database"
)

// stageRestoreFixture builds a data dir holding a live database plus one
// valid pre-migration snapshot of it, distinguishable by content: the
// snapshot says "snapshot", the live DB says "live". Whether the restore
// actually ran is then a question the marker row answers, not something the
// test has to infer from log lines.
func stageRestoreFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("CREWSHIP_DATA_DIR", dir)

	dd, err := database.DefaultDataDir()
	if err != nil {
		t.Fatalf("data dir: %v", err)
	}
	dbPath := dd.DatabasePath()

	// The snapshot content. Close before copying: the last WAL connection to
	// close checkpoints, so the bytes on disk are complete.
	db, err := database.Open("file:" + dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE marker (v TEXT)`); err != nil {
		t.Fatalf("create marker: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO marker (v) VALUES ('snapshot')`); err != nil {
		t.Fatalf("insert marker: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}

	// RestoreSnapshot only accepts a file matching this DB's pre-migrate
	// naming, so the fixture has to use the real shape.
	snap := dbPath + ".pre-migrate-v1-to-v2-20260101T000000Z.bak"
	raw, err := os.ReadFile(dbPath)
	if err != nil {
		t.Fatalf("read db for snapshot: %v", err)
	}
	if err := os.WriteFile(snap, raw, 0o600); err != nil {
		t.Fatalf("write snapshot: %v", err)
	}

	// Now diverge the live DB from the snapshot.
	db, err = database.Open("file:" + dbPath)
	if err != nil {
		t.Fatalf("reopen db: %v", err)
	}
	if _, err := db.Exec(`UPDATE marker SET v = 'live'`); err != nil {
		t.Fatalf("update marker: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}
	return dbPath
}

// readMarker returns the marker row of the database at dbPath, opened as a
// throwaway connection.
func readMarker(t *testing.T, dbPath string) string {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open for marker read: %v", err)
	}
	defer db.Close()
	var v string
	if err := db.QueryRow(`SELECT v FROM marker`).Scan(&v); err != nil {
		t.Fatalf("read marker: %v", err)
	}
	return v
}

// serveHealthOn starts a stand-in crewshipd health endpoint on a free local
// port and points $CREWSHIP_PORT at it. This is the *unrelated* instance: it
// answers /api/health exactly like a real server and has nothing to do with
// the database under test.
func serveHealthOn(t *testing.T) {
	t.Helper()
	ln, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/health", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"status":"ok"}`)
	})
	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	t.Setenv("CREWSHIP_PORT", strconv.Itoa(ln.Addr().(*net.TCPAddr).Port))
}

// healthPortAnswers reports whether something answers /api/health on
// $CREWSHIP_PORT — the exact probe the old guard used, reimplemented here
// rather than called from production code, which no longer contains it. It
// exists so a case labelled "an unrelated server IS answering" can assert its
// own premise instead of trusting the fixture.
func healthPortAnswers(t *testing.T) bool {
	t.Helper()
	port := os.Getenv("CREWSHIP_PORT")
	if port == "" {
		port = "8080"
	}
	client := &http.Client{Timeout: 800 * time.Millisecond}
	resp, err := client.Get("http://localhost:" + port + "/api/health")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// pinDeadPort points $CREWSHIP_PORT at a port nothing listens on, so a dev
// slot running on :8080 (the default) on the same box cannot make the test
// flap. Bind-then-release is the portable way to name a port the kernel just
// confirmed was free.
func pinDeadPort(t *testing.T) {
	t.Helper()
	ln, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	if err := ln.Close(); err != nil {
		t.Fatalf("close listener: %v", err)
	}
	t.Setenv("CREWSHIP_PORT", strconv.Itoa(port))
}

// TestRestoreSnapshotDatabaseInUseGuard pins the guard to the file being
// overwritten rather than to an HTTP port.
//
// The old guard probed http://localhost:$CREWSHIP_PORT/api/health, which is
// unrelated to the database path, and failed in both directions:
//
//   - "unrelated server on the probed port": a sandbox restore was refused
//     because some other Crewship answered on that port — a false block on a
//     database nobody had open.
//   - "server holding the database, nothing on the probed port": the restore
//     went ahead and swapped the file under a live crewshipd — the dangerous
//     direction, and the reason the check had to move onto the file.
func TestRestoreSnapshotDatabaseInUseGuard(t *testing.T) {
	origServer, origProfile, origCfg := flagServer, flagProfile, cliCfg
	t.Cleanup(func() { flagServer, flagProfile, cliCfg = origServer, origProfile, origCfg })

	tests := []struct {
		name string
		// holdDatabaseOpen simulates a live crewshipd: a connection to the
		// target DB that stays open (and idle, which is the hard case — an
		// idle WAL connection holds no write lock).
		holdDatabaseOpen bool
		// healthServer starts an unrelated server answering on the port the
		// old guard probed.
		healthServer bool
		wantRefused  bool
		// wantErrContains is checked on refusal: the message must name the
		// database file, since a URL tells the operator nothing about which
		// file is at risk.
		wantErrContains string
		wantMarker      string
	}{
		{
			name:             "database held open by another connection: refuse",
			holdDatabaseOpen: true,
			healthServer:     false,
			wantRefused:      true,
			wantErrContains:  "crewship.db",
			wantMarker:       "live",
		},
		{
			name:             "database held open and a server answering: refuse",
			holdDatabaseOpen: true,
			healthServer:     true,
			wantRefused:      true,
			wantErrContains:  "crewship.db",
			wantMarker:       "live",
		},
		{
			name:             "unrelated server on the probed port, database free: allow",
			holdDatabaseOpen: false,
			healthServer:     true,
			wantRefused:      false,
			wantMarker:       "snapshot",
		},
		{
			name:             "nothing running at all: allow",
			holdDatabaseOpen: false,
			healthServer:     false,
			wantRefused:      false,
			wantMarker:       "snapshot",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("CREWSHIP_SERVER", "")
			t.Setenv("CREWSHIP_PROFILE", "")
			flagServer, flagProfile = "", ""
			cliCfg = &cli.CLIConfig{}

			dbPath := stageRestoreFixture(t)

			if tt.healthServer {
				serveHealthOn(t)
			} else {
				pinDeadPort(t)
			}

			if tt.holdDatabaseOpen {
				held, err := database.Open("file:" + dbPath)
				if err != nil {
					t.Fatalf("hold db open: %v", err)
				}
				t.Cleanup(func() { _ = held.Close() })
				// One read, then idle — a live crewshipd's normal state, and
				// the one the guard must catch. The read also pins the
				// fixture to a holder that definitely has the WAL index
				// mapped, rather than one that happens to because this
				// database was already in WAL mode on disk.
				var marker string
				if err := held.QueryRow(`SELECT v FROM marker`).Scan(&marker); err != nil {
					t.Fatalf("holder query: %v", err)
				}
			}

			// Cross-check the fixture, so a case labelled "unrelated server
			// answering" cannot silently degrade into "nothing answering" and
			// pass for the wrong reason.
			if answers := healthPortAnswers(t); answers != tt.healthServer {
				t.Fatalf("fixture: health port answers = %v, want %v", answers, tt.healthServer)
			}

			restoreSnapshotList = false
			restoreSnapshotYes = true
			t.Cleanup(func() { restoreSnapshotYes = false })

			var err error
			out := covCaptureAll(t, func() {
				err = restoreSnapshotCmd.RunE(restoreSnapshotCmd, nil)
			})

			switch {
			case tt.wantRefused && err == nil:
				t.Fatalf("restore was allowed, want refusal; output:\n%s", out)
			case !tt.wantRefused && err != nil:
				t.Fatalf("restore refused (%v), want it to proceed; output:\n%s", err, out)
			}
			if tt.wantRefused {
				if !strings.Contains(err.Error(), tt.wantErrContains) {
					t.Errorf("error %q does not name the database file (want substring %q)", err, tt.wantErrContains)
				}
				if strings.Contains(err.Error(), "http://") {
					t.Errorf("error points at a URL instead of the database file: %v", err)
				}
			}
			if got := readMarker(t, dbPath); got != tt.wantMarker {
				t.Errorf("marker = %q, want %q (restore %s)", got, tt.wantMarker,
					map[bool]string{true: "should NOT have run", false: "should have run"}[tt.wantRefused])
			}
		})
	}
}

// TestDatabaseInUseProbeErrors: "I could not open the file" and "someone is
// using the file" are different answers. Conflating them was how the old
// guard misled the operator; the probe must not repeat it under a new name.
func TestDatabaseInUseProbeErrors(t *testing.T) {
	dir := t.TempDir()

	tests := []struct {
		name      string
		setup     func(t *testing.T) string
		wantInUse bool
		wantErr   bool
	}{
		{
			name: "missing file is not in use and is not an error",
			setup: func(t *testing.T) string {
				return dir + "/does-not-exist.db"
			},
		},
		{
			name: "directory in place of the database file is an error, not 'in use'",
			setup: func(t *testing.T) string {
				p := dir + "/a-directory.db"
				if err := os.Mkdir(p, 0o755); err != nil {
					t.Fatalf("mkdir: %v", err)
				}
				return p
			},
			wantErr: true,
		},
		{
			name: "non-database file is an error, not 'in use'",
			setup: func(t *testing.T) string {
				p := dir + "/garbage.db"
				if err := os.WriteFile(p, []byte("this is not a SQLite database at all"), 0o600); err != nil {
					t.Fatalf("write: %v", err)
				}
				return p
			},
			wantErr: true,
		},
		{
			name: "closed database is free",
			setup: func(t *testing.T) string {
				p := dir + "/closed.db"
				db, err := database.Open("file:" + p)
				if err != nil {
					t.Fatalf("open: %v", err)
				}
				if _, err := db.Exec(`CREATE TABLE t (x)`); err != nil {
					t.Fatalf("exec: %v", err)
				}
				if err := db.Close(); err != nil {
					t.Fatalf("close: %v", err)
				}
				return p
			},
		},
		{
			name: "open database is in use",
			setup: func(t *testing.T) string {
				p := dir + "/held.db"
				db, err := database.Open("file:" + p)
				if err != nil {
					t.Fatalf("open: %v", err)
				}
				if _, err := db.Exec(`CREATE TABLE t (x)`); err != nil {
					t.Fatalf("exec: %v", err)
				}
				t.Cleanup(func() { _ = db.Close() })
				return p
			},
			wantInUse: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := tt.setup(t)
			inUse, err := databaseInUse(path)
			if (err != nil) != tt.wantErr {
				t.Fatalf("databaseInUse err = %v, wantErr %v", err, tt.wantErr)
			}
			if inUse != tt.wantInUse {
				t.Errorf("databaseInUse = %v, want %v (err %v)", inUse, tt.wantInUse, err)
			}
			if tt.wantErr && inUse {
				t.Errorf("an unreadable database must not be reported as in use")
			}
		})
	}
}
