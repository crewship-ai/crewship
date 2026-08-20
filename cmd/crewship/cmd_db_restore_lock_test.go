//go:build !clionly

package main

import (
	"bytes"
	"database/sql"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
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

// corruptLiveDatabase replaces the live database with bytes that are not a
// SQLite file at all, leaving the snapshot beside it intact.
//
// This is not a contrived state: it is precisely the state restore-snapshot
// exists for. It is also the one state the lock probe cannot answer — SQLite
// cannot take a lock on a file it refuses to recognise — so it is where "the
// probe failed" and "someone is using the file" have to stay different
// answers.
func corruptLiveDatabase(t *testing.T, dbPath string) []byte {
	t.Helper()
	garbage := []byte("this is not a SQLite database at all, it is a torn file")
	if err := os.WriteFile(dbPath, garbage, 0o600); err != nil {
		t.Fatalf("corrupt db: %v", err)
	}
	// A stale WAL beside a corrupt main file is what a killed server leaves;
	// remove ours so the probe fails on the header rather than on WAL replay,
	// keeping the case pinned to the error the finding named.
	for _, suffix := range []string{"-wal", "-shm"} {
		if err := os.Remove(dbPath + suffix); err != nil && !os.IsNotExist(err) {
			t.Fatalf("remove %s%s: %v", dbPath, suffix, err)
		}
	}
	return garbage
}

// holdDatabase opens dbPath and keeps it open for the rest of the test,
// running one statement so the WAL index is mapped — without it an
// as-yet-unused connection holds no lock for the probe to see (see
// databaseInUse's own notes). This is the stand-in for a live crewshipd.
func holdDatabase(t *testing.T, dbPath string) {
	t.Helper()
	held, err := database.Open("file:" + dbPath)
	if err != nil {
		t.Fatalf("hold db open: %v", err)
	}
	t.Cleanup(func() { _ = held.Close() })
	var n int
	if err := held.QueryRow(`SELECT COUNT(*) FROM marker`).Scan(&n); err != nil {
		t.Fatalf("holder query: %v", err)
	}
}

// TestRestoreSnapshotRechecksBeforeTheSwap covers the window the guard's own
// documentation claims to cover but did not: the confirmation prompt.
//
// The probe ran, then snapshot selection, then an UNBOUNDED prompt, then the
// swap. An operator who stops crewshipd, starts the restore and pauses to read
// the prompt gives systemd's Restart=always (or a colleague) all the time it
// needs to bring the server back; the swap then renames a file into place
// under a process holding it open, which is the exact torn-database outcome
// the guard exists to prevent. Answering "is it in use" once, before a wait of
// unknown length, answers about a moment that has passed.
//
// The prompt is where the window is, so the test takes the database inside the
// prompt.
func TestRestoreSnapshotRechecksBeforeTheSwap(t *testing.T) {
	guardCLIState(t)

	tests := []struct {
		name string
		// takeDatabaseAtThePrompt: crewshipd comes back while the operator is
		// still reading the confirmation.
		takeDatabaseAtThePrompt bool
		wantRefused             bool
		wantMarker              string
	}{
		{
			name:                    "crewshipd returns while the operator reads the prompt: refuse",
			takeDatabaseAtThePrompt: true,
			wantRefused:             true,
			wantMarker:              "live",
		},
		{
			name:                    "nothing takes the database during the prompt: restore",
			takeDatabaseAtThePrompt: false,
			wantRefused:             false,
			wantMarker:              "snapshot",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("CREWSHIP_SERVER", "")
			t.Setenv("CREWSHIP_PROFILE", "")
			flagServer, flagProfile = "", ""
			cliCfg = &cli.CLIConfig{}

			dbPath := stageRestoreFixture(t)
			pinDeadPort(t)

			// The prompt is the window, so it is also the seam: confirming is
			// what the operator does slowly, and dbConfirm is where the test
			// gets to act "during" it.
			origConfirm := dbConfirm
			t.Cleanup(func() { dbConfirm = origConfirm })
			dbConfirm = func(string) bool {
				if tt.takeDatabaseAtThePrompt {
					holdDatabase(t, dbPath)
				}
				return true
			}

			restoreSnapshotList, restoreSnapshotYes, restoreSnapshotForce = false, false, false
			t.Cleanup(func() {
				restoreSnapshotList, restoreSnapshotYes, restoreSnapshotForce = false, false, false
			})

			var err error
			out := covCaptureAll(t, func() {
				err = restoreSnapshotCmd.RunE(restoreSnapshotCmd, nil)
			})

			switch {
			case tt.wantRefused && err == nil:
				t.Fatalf("restore was allowed after the database was taken during the prompt; output:\n%s", out)
			case !tt.wantRefused && err != nil:
				t.Fatalf("restore refused (%v), want it to proceed; output:\n%s", err, out)
			}
			if tt.wantRefused && !strings.Contains(err.Error(), "crewship.db") {
				t.Errorf("error %q does not name the database file", err)
			}
			if got := readMarker(t, dbPath); got != tt.wantMarker {
				t.Errorf("marker = %q, want %q", got, tt.wantMarker)
			}
		})
	}
}

// TestRestoreSnapshotForce pins the two halves of --force apart.
//
// Refusing on ANY probe failure locked the operator out of the one command
// that exists for a broken database: a corrupt file cannot answer the probe,
// so restore-snapshot refused to restore over it, and the project rule is that
// everything goes through the CLI — never a DB shell — so there was nothing
// legitimate left to do. --force lifts that. It does NOT lift a definite "in
// use": that answer is knowable, and writing anyway tears the file under a
// running server, which is the defect this whole guard exists for.
func TestRestoreSnapshotForce(t *testing.T) {
	guardCLIState(t)

	tests := []struct {
		name string
		// corrupt: the live database is not a SQLite file, so the probe
		// errors and can say nothing about who holds it.
		corrupt bool
		// heldOpen: a live crewshipd stand-in — the probe answers "in use",
		// definitely.
		heldOpen     bool
		force        bool
		wantRefused  bool
		wantErrParts []string
		// wantWarning: the loud stderr paragraph naming what is being
		// overridden. An override the operator cannot see in the transcript
		// afterwards is not an override, it is a silent fail-open.
		wantWarning []string
	}{
		{
			name:        "corrupt database, no --force: refuse and say the probe failed",
			corrupt:     true,
			wantRefused: true,
			wantErrParts: []string{
				"cannot determine",
				"crewship.db",
				"not a database", // the underlying probe error, not a guess
				"--force",        // and the way out
			},
		},
		{
			name:        "corrupt database with --force: restore proceeds",
			corrupt:     true,
			force:       true,
			wantRefused: false,
			wantWarning: []string{"--force", "not a database", "crewship.db"},
		},
		{
			name:        "database genuinely in use, with --force: still refuse",
			heldOpen:    true,
			force:       true,
			wantRefused: true,
			wantErrParts: []string{
				"open by another process",
				"--force does not override",
			},
		},
		{
			name:         "database genuinely in use, no --force: refuse",
			heldOpen:     true,
			wantRefused:  true,
			wantErrParts: []string{"open by another process"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("CREWSHIP_SERVER", "")
			t.Setenv("CREWSHIP_PROFILE", "")
			flagServer, flagProfile = "", ""
			cliCfg = &cli.CLIConfig{}

			dbPath := stageRestoreFixture(t)
			pinDeadPort(t)

			if tt.heldOpen {
				holdDatabase(t, dbPath)
			}
			var garbage []byte
			if tt.corrupt {
				garbage = corruptLiveDatabase(t, dbPath)
			}

			restoreSnapshotList, restoreSnapshotYes = false, true
			restoreSnapshotForce = tt.force
			t.Cleanup(func() {
				restoreSnapshotList, restoreSnapshotYes, restoreSnapshotForce = false, false, false
			})

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
			for _, want := range tt.wantErrParts {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q should mention %q", err, want)
				}
			}
			for _, want := range tt.wantWarning {
				if !strings.Contains(out, want) {
					t.Errorf("forced run should warn about %q; output:\n%s", want, out)
				}
			}

			// What actually happened on disk. A refusal must leave the file
			// exactly as it was — including when it was already garbage.
			switch {
			case tt.wantRefused && tt.corrupt:
				raw, rerr := os.ReadFile(dbPath)
				if rerr != nil {
					t.Fatalf("read db: %v", rerr)
				}
				if !bytes.Equal(raw, garbage) {
					t.Errorf("refused restore rewrote the database file")
				}
			case tt.wantRefused:
				if got := readMarker(t, dbPath); got != "live" {
					t.Errorf("marker = %q, want %q (restore should NOT have run)", got, "live")
				}
			default:
				if got := readMarker(t, dbPath); got != "snapshot" {
					t.Errorf("marker = %q, want %q (restore should have run)", got, "snapshot")
				}
			}
		})
	}
}

// TestDBWriteGuardOutcomes pins the guard's three-way decision directly, since
// it is shared by restore-snapshot and repair-ledger and the third outcome —
// "the probe could not answer" — is not reachable end-to-end from every
// command (repair-ledger reads the ledger first, so a file corrupt enough to
// defeat the probe fails earlier, on the read).
func TestDBWriteGuardOutcomes(t *testing.T) {
	tests := []struct {
		name        string
		setup       func(t *testing.T, dir string) string
		force       bool
		wantErr     []string
		wantWarning bool
	}{
		{
			name: "free database: proceed",
			setup: func(t *testing.T, dir string) string {
				p := filepath.Join(dir, "free.db")
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
			name: "held open: refuse",
			setup: func(t *testing.T, dir string) string {
				return guardHeldDB(t, dir, "held.db")
			},
			wantErr: []string{"open by another process", "--force does not override"},
		},
		{
			name: "held open, with --force: still refuse",
			setup: func(t *testing.T, dir string) string {
				return guardHeldDB(t, dir, "held-force.db")
			},
			force:   true,
			wantErr: []string{"open by another process", "--force does not override"},
		},
		{
			name: "probe cannot answer: refuse, naming the probe error",
			setup: func(t *testing.T, dir string) string {
				p := filepath.Join(dir, "garbage.db")
				if err := os.WriteFile(p, []byte("not a database"), 0o600); err != nil {
					t.Fatalf("write: %v", err)
				}
				return p
			},
			wantErr: []string{"cannot determine", "not a database", "--force"},
		},
		{
			name: "probe cannot answer, with --force: proceed, loudly",
			setup: func(t *testing.T, dir string) string {
				p := filepath.Join(dir, "garbage-force.db")
				if err := os.WriteFile(p, []byte("not a database"), 0o600); err != nil {
					t.Fatalf("write: %v", err)
				}
				return p
			},
			force:       true,
			wantWarning: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := tt.setup(t, t.TempDir())
			g := dbWriteGuard{
				path:  path,
				verb:  "restoring",
				risk:  "a live server holds the database open and would see a torn file",
				force: tt.force,
			}
			var err error
			out := covCaptureAll(t, func() { err = g.check(true) })

			if len(tt.wantErr) == 0 && err != nil {
				t.Fatalf("guard refused (%v), want it to allow the write", err)
			}
			if len(tt.wantErr) > 0 && err == nil {
				t.Fatalf("guard allowed the write, want a refusal")
			}
			for _, want := range tt.wantErr {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q should mention %q", err, want)
				}
			}
			if warned := strings.Contains(out, "WARNING"); warned != tt.wantWarning {
				t.Errorf("warning printed = %v, want %v; output:\n%s", warned, tt.wantWarning, out)
			}
			// The re-probe must not repeat the paragraph: two copies read as
			// two separate problems.
			if tt.wantWarning {
				quiet := covCaptureAll(t, func() { _ = g.check(false) })
				if strings.Contains(quiet, "WARNING") {
					t.Errorf("re-probe repeated the --force warning:\n%s", quiet)
				}
			}
		})
	}
}

// guardHeldDB creates a database under dir and keeps a connection on it for
// the rest of the test.
func guardHeldDB(t *testing.T, dir, name string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	db, err := database.Open("file:" + p)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE t (x)`); err != nil {
		t.Fatalf("exec: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return p
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
