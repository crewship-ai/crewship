//go:build !clionly

package main

// Regressions for the five defects the hand review of #2109 found in the
// #2086 fix itself. Grouped in one file because they share a single theme:
// the first PR taught `admin` / `db` / `memory` to name the database they
// read, and left the diagnostics, the restore exit path, the pagination
// ceiling and the table's output stream behind.
//
// Driven through the built binary wherever the defect is about how the
// process resolves its target or renders its exit — the same reasoning as
// acceptance_local_db_target_test.go, which these reuse the harness from.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/database"
)

// ─── shared fixtures ────────────────────────────────────────────────────────

// markedDBAt installs a fully-migrated database at path and stamps a marker
// row into app_settings, so a command's OUTPUT says which file answered it.
//
// The marker is carried in app_settings rather than a table of its own
// because that is where `telemetry status` already reads from: the install
// id it prints is the marker, with no test-only plumbing in production code.
func markedDBAt(t *testing.T, path, installID string) {
	t.Helper()
	m := migratedFixture()
	if m.err != nil {
		t.Fatalf("build migrated fixture: %v", m.err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir for %s: %v", path, err)
	}
	if err := os.WriteFile(path, m.closed, 0o600); err != nil {
		t.Fatalf("install database at %s: %v", path, err)
	}
	db, err := database.Open("file:" + path)
	if err != nil {
		t.Fatalf("open %s to stamp the marker: %v", path, err)
	}
	for _, kv := range [][2]string{
		{"telemetry_opt_in", "1"},
		{"telemetry_install_id", installID},
	} {
		if _, err := db.Exec(
			`INSERT INTO app_settings (key, value, updated_at) VALUES (?, ?, datetime('now'))
			 ON CONFLICT (key) DO UPDATE SET value = excluded.value`, kv[0], kv[1]); err != nil {
			db.Close()
			t.Fatalf("stamp %s: %v", kv[0], err)
		}
	}
	// Closed, not held: the command under test opens this file itself, and a
	// clean close checkpoints the WAL away so the bytes on disk are the whole
	// database.
	if err := db.Close(); err != nil {
		t.Fatalf("close %s: %v", path, err)
	}
}

// runLocalDBCLIStreams is runLocalDBCLI with stdout and stderr kept apart.
// Combined output cannot answer "is this table still parseable", which is
// the whole question in TestAcceptance_AdminListUsers_AdvisoryStaysOffStdout.
func runLocalDBCLIStreams(t *testing.T, cfgPath, dataDir string, extraEnv []string, args ...string) (string, string, error) {
	t.Helper()
	cmd := exec.Command(buildCrewshipBinary(t), args...)
	cmd.Env = append(os.Environ(),
		"CREWSHIP_CONFIG="+cfgPath,
		"CREWSHIP_DATA_DIR="+dataDir,
		"NO_COLOR=1",
		"DATABASE_URL=",
		"CREWSHIP_SERVER=", "CREWSHIP_PROFILE=", "CREWSHIP_TOKEN=", "CREWSHIP_WORKSPACE=")
	cmd.Env = append(cmd.Env, extraEnv...)
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}

// ─── finding 1: the diagnostics answered from a database nobody named ───────

// The headline regression, in the command whose job is to tell an operator
// the truth about this instance.
//
// `openLocalDBReadOnly` resolved ResolveDefaultDataDir() directly, so
// `crewship telemetry status` and five checks in `crewship doctor` read
// ~/.crewship/crewship.db no matter what DATABASE_URL said — on a dev clone
// (DATABASE_URL=file:./crewship.db) that file is months stale, and nothing in
// the output named it. Same confident-wrong-answer shape as #2086, in the
// diagnostic for #2086.
func TestAcceptance_TelemetryStatus_ReadsTheDatabaseDATABASEURLNames(t *testing.T) {
	dataDir := t.TempDir()
	markedDBAt(t, filepath.Join(dataDir, "crewship.db"), "decoy-install-id")

	realDB := filepath.Join(t.TempDir(), "crewship.db")
	markedDBAt(t, realDB, "real-install-id")

	cfgPath := filepath.Join(t.TempDir(), "cli-config.yaml")
	if err := os.WriteFile(cfgPath, []byte("format: table\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	out, err := runLocalDBCLI(t, cfgPath, dataDir,
		[]string{"DATABASE_URL=file:" + realDB}, "telemetry", "status")
	if err != nil {
		t.Fatalf("telemetry status: %v\noutput: %s", err, out)
	}
	if strings.Contains(out, "decoy-install-id") {
		t.Errorf("read the default data dir's database while DATABASE_URL named another:\n%s", out)
	}
	if !strings.Contains(out, "real-install-id") {
		t.Errorf("did not read the database DATABASE_URL names:\n%s", out)
	}
	if !strings.Contains(out, realDB) {
		t.Errorf("did not name the database file it read — the omission is how the wrong one goes unnoticed:\n%s", out)
	}
}

// The helper under all of them. Unit-level because the five doctor checks
// share it and a per-check acceptance run would pay for a full doctor sweep
// (container probe, /healthz, DSN reachability) five times over.
func TestOpenLocalDBReadOnly_HonoursDatabaseURL(t *testing.T) {
	dd := tempDataDir(t)
	markedDBAt(t, dd.DatabasePath(), "decoy-install-id")

	realDB := filepath.Join(t.TempDir(), "crewship.db")
	markedDBAt(t, realDB, "real-install-id")
	t.Setenv("DATABASE_URL", "file:"+realDB)

	db, err := openLocalDBReadOnly(t.Context())
	if err != nil {
		t.Fatalf("openLocalDBReadOnly: %v", err)
	}
	defer db.Close()

	var got string
	if err := db.QueryRowContext(t.Context(),
		`SELECT value FROM app_settings WHERE key = 'telemetry_install_id'`).Scan(&got); err != nil {
		t.Fatalf("read the marker: %v", err)
	}
	if got != "real-install-id" {
		t.Errorf("opened the database marked %q; DATABASE_URL named the one marked %q", got, "real-install-id")
	}
}

// A doctor row that reports a schema version has to say which file's schema
// it is. Without it the operator cannot tell a stale decoy from the live
// instance, which is the state a dev clone is permanently in.
func TestCheckDBMigrationVersion_NamesTheDatabaseItRead(t *testing.T) {
	dd := tempDataDir(t)
	markedDBAt(t, dd.DatabasePath(), "decoy-install-id")

	realDB := filepath.Join(t.TempDir(), "crewship.db")
	markedDBAt(t, realDB, "real-install-id")
	t.Setenv("DATABASE_URL", "file:"+realDB)

	res := checkDBMigrationVersion(t.Context())
	if !strings.Contains(res.detail+res.hint, realDB) {
		t.Errorf("the row does not name the database it read (status=%s detail=%q hint=%q); wanted %s",
			res.status, res.detail, res.hint, realDB)
	}
	if strings.Contains(res.detail+res.hint, dd.DatabasePath()) {
		t.Errorf("the row named the default data dir's database while DATABASE_URL named another: %q", res.detail)
	}
}

// ─── finding 2: `memory restore` exited past its own deferred closes ────────

// runMemoryRestore called os.Exit(1) on ErrVersionNotFound, which skips
// `defer db.Close()` and `defer cancel()` — and, visibly from outside,
// skips the CLI's whole error path. An agent that asked for --format json
// (the contract the repo says agents drive) got a bare prose line instead of
// the envelope every other failure emits.
func TestAcceptance_MemoryRestore_MissingVersionUsesTheCLIErrorPath(t *testing.T) {
	dataDir := localDBFixture(t)
	cfgPath := filepath.Join(t.TempDir(), "cli-config.yaml")
	if err := os.WriteFile(cfgPath, []byte("format: table\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	blobRoot := filepath.Join(dataDir, "memory", "versions")
	canonical := filepath.Join(dataDir, "memory", "learned-x.md")

	stdout, stderr, err := runLocalDBCLIStreams(t, cfgPath, dataDir, nil,
		"memory", "restore", "ws-local", "crew:c1/learned-x.md",
		"0000000000000000000000000000000000000000000000000000000000000000",
		canonical, "--local", "--blob-root", blobRoot, "--format", "json")
	if err == nil {
		t.Fatalf("exited 0 for a version that does not exist:\nstdout: %s\nstderr: %s", stdout, stderr)
	}
	if code := exitCodeOf(t, err); code != 1 {
		t.Errorf("exit code %d, want 1 (the documented \"blob not found\")\nstderr: %s", code, stderr)
	}
	var env struct {
		Error struct {
			Message  string `json:"message"`
			ExitCode int    `json:"exit_code"`
		} `json:"error"`
	}
	// The gate's "note: … reads the local database at …" precedes the
	// envelope on the same stream; the envelope is the trailing object.
	envelope := stderr
	if i := strings.Index(stderr, "{"); i >= 0 {
		envelope = stderr[i:]
	}
	if jsonErr := json.Unmarshal([]byte(envelope), &env); jsonErr != nil {
		t.Fatalf("stderr is not the structured error envelope (%v) — the command exited past the CLI error path:\n%s",
			jsonErr, stderr)
	}
	if !strings.Contains(env.Error.Message, "version not found") {
		t.Errorf("envelope message = %q, want the shared \"version not found\" wording", env.Error.Message)
	}
	if env.Error.ExitCode != 1 {
		t.Errorf("envelope exit_code = %d, want 1", env.Error.ExitCode)
	}
}

// ─── finding 3: the page ceiling discarded what it had found ────────────────

// endlessVersionsStub pages forever. Every page carries one row under the
// requested prefix but on a DIFFERENT exact path — the sibling-row shape the
// ceiling exists for — and the first page additionally carries `matches` rows
// on the exact path.
//
// It reproduces the real ordering: /api/v1/admin/memory/versions orders by
// (written_at, id) across the whole workspace, not per path, so a
// rarely-written file in an active workspace really does sit behind an
// unbounded run of unrelated rows.
func endlessVersionsStub(t *testing.T, path string, matches int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/admin/memory/versions" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		rows := []string{}
		if r.URL.Query().Get("cursor") == "" {
			for i := 0; i < matches; i++ {
				rows = append(rows, fmt.Sprintf(
					`{"id":"mv-hit-%02d","path":%q,"tier":"learned","sha256":"hit%02d",`+
						`"bytes":1,"written_at":"2026-01-02T03:04:05Z","written_by":"w"}`, i, path, i))
			}
		}
		rows = append(rows, fmt.Sprintf(
			`{"id":"mv-sib","path":%q,"tier":"learned","sha256":"sib","bytes":1,`+
				`"written_at":"2026-01-02T03:04:05Z","written_by":"w"}`, path+"-sibling"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"workspace_id":"ws_test","rows":[%s],"next_cursor":"more","limit":500,"filters_applied":{}}`,
			strings.Join(rows, ","))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// `memory show` documents exit 1 / "version not found" for a sha that is not
// in the chain. Because it walks with limit=0 + matchSha, a sha that is
// genuinely absent runs the walk to the ceiling — and the ceiling's error
// replaced the documented answer with "narrow the path", advice the operator
// cannot take because the path argument is already exact.
func TestAcceptance_MemoryShow_MissingShaIsNotFoundNotThePageCeiling(t *testing.T) {
	const path = "crew:c1/rarely-written.md"
	srv := endlessVersionsStub(t, path, 0)
	dataDir := localDBFixture(t)
	cfg := localDBStubConfig(t, srv.URL)

	stdout, stderr, err := runLocalDBCLIStreams(t, cfg, dataDir, nil,
		"memory", "show", "ws_test", path, "nosuchsha")
	if err == nil {
		t.Fatalf("exited 0 for a sha that is not in the chain:\nstdout: %s\nstderr: %s", stdout, stderr)
	}
	if code := exitCodeOf(t, err); code != 1 {
		t.Errorf("exit code %d, want 1\nstderr: %s", code, stderr)
	}
	if !strings.Contains(stderr, "version not found") {
		t.Errorf("did not report the documented \"version not found\":\n%s", stderr)
	}
	if strings.Contains(stderr, "narrow the path") {
		t.Errorf("reported the page ceiling for an argument that is already an exact path:\n%s", stderr)
	}
}

// …and `memory log` must hand back the rows it did find. Discarding them
// turns a slow answer into no answer, for a query that was answerable.
func TestAcceptance_MemoryLog_PageCeilingKeepsTheMatchesItFound(t *testing.T) {
	const path = "crew:c1/rarely-written.md"
	srv := endlessVersionsStub(t, path, 3)
	dataDir := localDBFixture(t)
	cfg := localDBStubConfig(t, srv.URL)

	stdout, stderr, err := runLocalDBCLIStreams(t, cfg, dataDir, nil,
		"memory", "log", "ws_test", path, "--limit", "50", "--format", "text")
	if err != nil {
		t.Fatalf("memory log gave up instead of returning what it found: %v\nstdout: %s\nstderr: %s",
			err, stdout, stderr)
	}
	if got := strings.Count(stdout, " B  w"); got != 3 {
		t.Errorf("printed %d rows, want the 3 matches collected before the ceiling:\n%s", got, stdout)
	}
	// Truncation that nobody is told about is the other failure mode: the
	// operator must know the list is partial before they act on it.
	if !strings.Contains(stderr, "partial") && !strings.Contains(stderr, "truncated") {
		t.Errorf("truncated silently — stderr says nothing about the ceiling:\n%s", stderr)
	}
}

// ─── finding 4: the advisory landed in the middle of a parseable table ──────

// Every other note this change adds goes to stderr so pipes keep working.
// This one went to stdout, so `crewship admin list-users | awk 'NR>1 {print $1}'`
// emitted a blank line and "(workspace-scoped;" as if they were user rows.
func TestAcceptance_AdminListUsers_AdvisoryStaysOffStdout(t *testing.T) {
	stub := newAdminUsersStub(t)
	dataDir := localDBFixture(t)
	cfg := localDBStubConfig(t, stub.srv.URL)

	stdout, stderr, err := runLocalDBCLIStreams(t, cfg, dataDir, nil, "admin", "list-users")
	if err != nil {
		t.Fatalf("list-users: %v\nstdout: %s\nstderr: %s", err, stdout, stderr)
	}
	// One header line plus one user row from the stub, and nothing else:
	// this is exactly what a script parses.
	lines := strings.Split(strings.TrimRight(stdout, "\n"), "\n")
	if len(lines) != 2 {
		t.Errorf("stdout is no longer a %d-line table:\n%q", 2, stdout)
	}
	// The review's own reproducer: `crewship admin list-users | awk 'NR>1 {print $1}'`
	// must yield addresses and nothing else.
	for i, l := range lines {
		if strings.TrimSpace(l) == "" {
			t.Errorf("blank line %d injected into the table:\n%q", i+1, stdout)
			continue
		}
		if i == 0 {
			continue // header
		}
		if first := strings.Fields(l)[0]; !strings.Contains(first, "@") {
			t.Errorf("column 1 of row %d is %q, not an email — prose leaked into the table", i+1, first)
		}
	}
	if !strings.Contains(stderr, "workspace-scoped") {
		t.Errorf("the advisory is gone entirely; it belongs on stderr:\n%s", stderr)
	}
}
