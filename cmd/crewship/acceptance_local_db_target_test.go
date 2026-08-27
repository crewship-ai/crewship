package main

// Acceptance for #2086 Critical 2: the `admin` family answered from a stale
// local SQLite file instead of the server the CLI was pointed at.
//
// Driven through the BUILT BINARY, not by calling RunE in-process, for two
// reasons. The rule in CLAUDE.md is that an endpoint's acceptance test drives
// the binary; and the defect was specifically about how the process resolves
// its target from environment + config, which an in-process RunE call with
// hand-set globals cannot reproduce — the globals ARE the thing under test.
//
// Every case seeds a local database whose contents DISAGREE with the stub
// server's. That disagreement is the assertion: a command that reads the wrong
// database does not merely lack data, it reports someone else's.

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/crewship-ai/crewship/internal/testutil"
)

// localDBFixture builds a data dir holding a migrated crewship.db seeded with
// one user that exists ONLY there. Returns the data dir.
//
// The user's address is deliberately at .invalid: if it ever shows up in the
// output of a command that was pointed at a server, the domain says out loud
// where it came from.
func localDBFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	db := testutil.MigratedDBAt(t, filepath.Join(dir, "crewship.db"))
	if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug, created_at, updated_at)
		VALUES ('ws-local', 'Local Only', 'local-only', datetime('now'), datetime('now'))`); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO users (id, email, full_name, hashed_password, created_at, updated_at)
		VALUES ('u-local', 'stale@localfile.invalid', 'Stale Local Row', 'x', datetime('now'), datetime('now'))`); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO workspace_members (id, user_id, workspace_id, role, created_at)
		VALUES ('wm-local', 'u-local', 'ws-local', 'OWNER', datetime('now'))`); err != nil {
		t.Fatalf("seed membership: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO user_sessions (id, user_id, expires_at, created_at, last_used_at)
		VALUES ('sess-local', 'u-local', datetime('now', '+1 day'), datetime('now'), datetime('now'))`); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	// The command under test opens this file itself; hand it back unheld.
	if err := db.Close(); err != nil {
		t.Fatalf("close fixture db: %v", err)
	}
	return dir
}

// localDBStubConfig writes a CLI config naming the stub server, which is how
// an operator's machine normally looks after `crewship login`.
func localDBStubConfig(t *testing.T, serverURL string) string {
	t.Helper()
	cfgPath := filepath.Join(t.TempDir(), "cli-config.yaml")
	cfg := "server: " + serverURL + "\nworkspace: ws_test\ntoken: fake-token\nformat: table\n"
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return cfgPath
}

// runLocalDBCLI runs the built binary with a controlled environment: the data
// dir holds the decoy database, the config names the stub, and every ambient
// CREWSHIP_* / DATABASE_URL is cleared so a developer box that exports one
// cannot make a passing run mean something else. extraEnv is appended last.
func runLocalDBCLI(t *testing.T, cfgPath, dataDir string, extraEnv []string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command(buildCrewshipBinary(t), args...)
	cmd.Env = append(os.Environ(),
		"CREWSHIP_CONFIG="+cfgPath,
		"CREWSHIP_DATA_DIR="+dataDir,
		"NO_COLOR=1",
		"DATABASE_URL=",
		"CREWSHIP_SERVER=", "CREWSHIP_PROFILE=", "CREWSHIP_TOKEN=", "CREWSHIP_WORKSPACE=")
	cmd.Env = append(cmd.Env, extraEnv...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// adminUsersStub answers GET /api/v1/admin/users with a user that exists only
// on the server, and records whether it was ever asked.
// called is atomic: it is written on the httptest handler's goroutine and read
// on the test's, and `cmd/crewship` has a dedicated -race job in CI. A plain
// bool here is a WARNING: DATA RACE inside a test asserting about somebody
// else's bug.
type adminUsersStub struct {
	called atomic.Bool
	srv    *httptest.Server
}

func newAdminUsersStub(t *testing.T) *adminUsersStub {
	t.Helper()
	s := &adminUsersStub{}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/admin/users":
			s.called.Store(true)
			_, _ = w.Write([]byte(`[{"id":"u-server","email":"live@server.invalid",` +
				`"full_name":"Live Server Row","avatar_url":null,"created_at":"2026-01-02T03:04:05Z",` +
				`"workspace":{"id":"ws_test","name":"Test","slug":"test"},"role":"OWNER"}]`))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"detail":"no stub for ` + r.Method + " " + r.URL.Path + `"}`))
		}
	}))
	t.Cleanup(s.srv.Close)
	return s
}

// The headline regression. `admin list-users` against a server must report
// that server's users. On the code this replaces it printed the local file's
// row — or, against the live :8083 clone, "(no users …)" and exit 0.
func TestAcceptance_AdminListUsers_ReadsTheServerNotTheLocalFile(t *testing.T) {
	stub := newAdminUsersStub(t)
	dataDir := localDBFixture(t)
	cfg := localDBStubConfig(t, stub.srv.URL)

	out, err := runLocalDBCLI(t, cfg, dataDir, nil, "admin", "list-users")
	if err != nil {
		t.Fatalf("list-users: %v\noutput: %s", err, out)
	}
	if !stub.called.Load() {
		t.Error("GET /api/v1/admin/users was never called — the command answered without contacting the server it was pointed at")
	}
	if !strings.Contains(out, "live@server.invalid") {
		t.Errorf("the server's user is missing from the output:\n%s", out)
	}
	if strings.Contains(out, "stale@localfile.invalid") {
		t.Errorf("the LOCAL database's user was reported for a server the CLI was pointed at:\n%s", out)
	}
}

// --local is the escape hatch that keeps the locked-out-operator path alive:
// same command, same host, explicitly the file.
func TestAcceptance_AdminListUsers_LocalFlagReadsTheFile(t *testing.T) {
	stub := newAdminUsersStub(t)
	dataDir := localDBFixture(t)
	cfg := localDBStubConfig(t, stub.srv.URL)

	out, err := runLocalDBCLI(t, cfg, dataDir, nil, "admin", "list-users", "--local")
	if err != nil {
		t.Fatalf("list-users --local: %v\noutput: %s", err, out)
	}
	if !strings.Contains(out, "stale@localfile.invalid") {
		t.Errorf("--local did not read the local database:\n%s", out)
	}
	if stub.called.Load() {
		t.Error("--local still contacted the server")
	}
	if !strings.Contains(out, filepath.Join(dataDir, "crewship.db")) {
		t.Errorf("--local did not name the database file it used:\n%s", out)
	}
}

// The other half of "no confident wrong answer": when the server cannot be
// reached, say so and exit non-zero. Falling back to the local file — or
// printing an empty table — would reproduce the defect with extra steps.
func TestAcceptance_AdminListUsers_ServerDownIsAnError(t *testing.T) {
	dataDir := localDBFixture(t)
	// A config naming a port nothing listens on: the stub is started and
	// closed so the address is real and definitely free.
	dead := newAdminUsersStub(t)
	url := dead.srv.URL
	dead.srv.Close()
	cfg := localDBStubConfig(t, url)

	out, err := runLocalDBCLI(t, cfg, dataDir, nil, "admin", "list-users")
	if err == nil {
		t.Fatalf("exited 0 with the server unreachable:\n%s", out)
	}
	if strings.Contains(out, "stale@localfile.invalid") {
		t.Errorf("fell back to the local database when the server was unreachable:\n%s", out)
	}
	if !strings.Contains(out, "--local") {
		t.Errorf("the failure does not point at the recovery path:\n%s", out)
	}
}

// --locked-only cannot be answered from the API, and answering it anyway with
// a client-side filter over a field the response does not carry would print
// "(no currently locked-out users)" for a workspace full of them.
func TestAcceptance_AdminListUsers_LockedOnlyRefusesOverHTTP(t *testing.T) {
	stub := newAdminUsersStub(t)
	dataDir := localDBFixture(t)
	cfg := localDBStubConfig(t, stub.srv.URL)

	out, err := runLocalDBCLI(t, cfg, dataDir, nil, "admin", "list-users", "--locked-only")
	if err == nil {
		t.Fatalf("answered --locked-only from a response with no lockout state:\n%s", out)
	}
	if !strings.Contains(out, "--local") {
		t.Errorf("refusal does not name the path that can answer it:\n%s", out)
	}

	// …and it IS answerable on the local path.
	out, err = runLocalDBCLI(t, cfg, dataDir, nil, "admin", "list-users", "--locked-only", "--local")
	if err != nil {
		t.Fatalf("--locked-only --local: %v\noutput: %s", err, out)
	}
	if !strings.Contains(out, "no currently locked-out users") {
		t.Errorf("local --locked-only did not render:\n%s", out)
	}
}

// A local-only command with no server-side route must refuse rather than
// answer from a file that may belong to a different instance. The refusal has
// to be an error — a warning followed by the wrong answer and exit 0 is the
// defect, not the fix.
func TestAcceptance_AdminSessionsList_RefusesWhenAServerIsTargeted(t *testing.T) {
	stub := newAdminUsersStub(t)
	dataDir := localDBFixture(t)
	cfg := localDBStubConfig(t, stub.srv.URL)

	out, err := runLocalDBCLI(t, cfg, dataDir, nil,
		"admin", "sessions", "list", "--email", "stale@localfile.invalid")
	if err == nil {
		t.Fatalf("exited 0 while answering about a server it cannot reach:\n%s", out)
	}
	if code := exitCodeOf(t, err); code == 0 {
		t.Errorf("exit code %d, want non-zero", code)
	}
	if !strings.Contains(out, stub.srv.URL) {
		t.Errorf("refusal does not name the server the CLI targets:\n%s", out)
	}
	if !strings.Contains(out, filepath.Join(dataDir, "crewship.db")) {
		t.Errorf("refusal does not name the database file it would have read:\n%s", out)
	}
	if !strings.Contains(out, "--local") {
		t.Errorf("refusal does not name the flag that resolves it:\n%s", out)
	}
	if strings.Contains(out, "sess-local") {
		t.Errorf("the refusal printed rows from the local file anyway:\n%s", out)
	}
}

// Same gate, the other family: `db migration-status` reported the local
// file's schema version as though it were the targeted server's. With --local
// it still works — and names the file.
func TestAcceptance_DBMigrationStatus_GateAndLocalFlag(t *testing.T) {
	stub := newAdminUsersStub(t)
	dataDir := localDBFixture(t)
	cfg := localDBStubConfig(t, stub.srv.URL)

	out, err := runLocalDBCLI(t, cfg, dataDir, nil, "db", "migration-status")
	if err == nil {
		t.Fatalf("reported a schema version for a server it never contacted:\n%s", out)
	}
	if !strings.Contains(out, "--local") {
		t.Errorf("refusal does not name --local:\n%s", out)
	}

	out, err = runLocalDBCLI(t, cfg, dataDir, nil, "db", "migration-status", "--local")
	if err != nil {
		t.Fatalf("--local: %v\noutput: %s", err, out)
	}
	if !strings.Contains(out, filepath.Join(dataDir, "crewship.db")) {
		t.Errorf("--local run does not name the database it read:\n%s", out)
	}
}

// CREWSHIP_SERVER alone — no config file server — must arm the gate too. That
// is the exact shape the bug was reproduced in on crewship-dev.
func TestAcceptance_LocalDBGate_HonoursCrewshipServerEnv(t *testing.T) {
	dataDir := localDBFixture(t)
	cfgPath := filepath.Join(t.TempDir(), "cli-config.yaml")
	if err := os.WriteFile(cfgPath, []byte("format: table\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	out, err := runLocalDBCLI(t, cfgPath, dataDir,
		[]string{"CREWSHIP_SERVER=http://localhost:8083"}, "db", "migration-status")
	if err == nil {
		t.Fatalf("CREWSHIP_SERVER did not arm the gate:\n%s", out)
	}
	if !strings.Contains(out, "http://localhost:8083") {
		t.Errorf("refusal does not name CREWSHIP_SERVER's target:\n%s", out)
	}
	// Loopback is not an exemption. Treating "the server is on localhost" as
	// "the server uses my data dir" is exactly what hid this defect: every dev
	// clone runs on loopback with DATABASE_URL=file:./crewship.db.
	if !strings.Contains(out, "CREWSHIP_SERVER") {
		t.Errorf("refusal does not say where the target came from:\n%s", out)
	}
}

// A typo'd profile must not disarm the gate.
//
// `cli.EffectiveServer` returns "" for a profile that is selected but has no
// server entry, and calls that failing closed — which it is, for dialling: an
// empty base URL reaches nothing. Reused verbatim in this gate, the same ""
// means "no server was named" and the command proceeds against the local file.
// Failing OPEN. So one mistyped CREWSHIP_PROFILE restored the entire
// pre-#2086 behaviour, for `admin reset-password` and `db restore-snapshot`
// as much as for the read-only commands.
func TestAcceptance_LocalDBGate_TypodProfileStillArmsTheGate(t *testing.T) {
	dataDir := localDBFixture(t)
	cfgPath := filepath.Join(t.TempDir(), "cli-config.yaml")
	cfg := "server: https://legacy.example\ntoken: fake-token\nformat: table\n" +
		"servers:\n  prod:\n    server: https://prod.example\n    token: t\n"
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	for _, tc := range []struct {
		name    string
		profile string
	}{
		{"profile that does not exist", "typo"},
		{"profile that exists but names no server", "serverless"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := runLocalDBCLI(t, cfgPath, dataDir,
				[]string{
					"CREWSHIP_PROFILE=" + tc.profile,
					"CREWSHIP_SERVER=https://env.example",
				},
				"db", "migration-status")
			if err == nil {
				t.Fatalf("a %s disarmed the gate and the command answered from the local file:\n%s",
					tc.name, out)
			}
			if !strings.Contains(out, tc.profile) {
				t.Errorf("refusal does not name the profile that armed it:\n%s", out)
			}
			if !strings.Contains(out, "--local") {
				t.Errorf("refusal does not name the escape hatch:\n%s", out)
			}
		})
	}
}

// With no server named anywhere, a local-only command is unambiguous and must
// still run — the recovery path on a host whose server is down.
func TestAcceptance_LocalDBGate_RunsWhenNoServerIsNamed(t *testing.T) {
	dataDir := localDBFixture(t)
	cfgPath := filepath.Join(t.TempDir(), "cli-config.yaml")
	if err := os.WriteFile(cfgPath, []byte("format: table\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	out, err := runLocalDBCLI(t, cfgPath, dataDir, nil, "db", "migration-status")
	if err != nil {
		t.Fatalf("refused an unambiguous local invocation: %v\noutput: %s", err, out)
	}
	if !strings.Contains(out, filepath.Join(dataDir, "crewship.db")) {
		t.Errorf("did not name the database it read:\n%s", out)
	}
}

// memoryVersionsStub answers the two admin memory-versions routes.
// Atomic for the same reason as adminUsersStub above.
type memoryVersionsStub struct {
	listCalled    atomic.Bool
	contentCalled atomic.Bool
	srv           *httptest.Server
}

func newMemoryVersionsStub(t *testing.T) *memoryVersionsStub {
	t.Helper()
	s := &memoryVersionsStub{}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/admin/memory/versions":
			s.listCalled.Store(true)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"workspace_id":"ws_test","rows":[` +
				`{"id":"mv-server","path":"crew:c1/learned-x.md","tier":"learned",` +
				`"sha256":"aaaabbbbcccc","bytes":42,"written_at":"2026-01-02T03:04:05Z",` +
				`"written_by":"audit-watcher"}],"next_cursor":null,"limit":50,"filters_applied":{}}`))
		case r.URL.Path == "/api/v1/admin/memory/versions/mv-server/content":
			s.contentCalled.Store(true)
			w.Header().Set("Content-Type", "text/markdown")
			_, _ = w.Write([]byte("content from the server\n"))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"detail":"no stub for ` + r.Method + " " + r.URL.Path + `"}`))
		}
	}))
	t.Cleanup(s.srv.Close)
	return s
}

// --limit means the same thing on both halves of `memory log`. The local half
// clamps inside memory.LogVersions ("<=0 → 20, >1000 → 1000"), and the help
// promises "clamped to 1000"; without the same clamp on the API half, the
// default-ish `--limit 0` would print 20 rows from the file and the entire
// chain from the server.
func TestAcceptance_MemoryLog_LimitClampMatchesTheLocalPath(t *testing.T) {
	// 25 rows on a single page, all on the same path, no next cursor.
	rows := make([]string, 0, 25)
	for i := 0; i < 25; i++ {
		rows = append(rows, fmt.Sprintf(
			`{"id":"mv-%02d","path":"crew:c1/x.md","tier":"learned","sha256":"sha%02d",`+
				`"bytes":1,"written_at":"2026-01-02T03:04:05Z","written_by":"w"}`, i, i))
	}
	page := `{"workspace_id":"ws_test","rows":[` + strings.Join(rows, ",") +
		`],"next_cursor":null,"limit":500,"filters_applied":{}}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/admin/memory/versions" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(page))
	}))
	t.Cleanup(srv.Close)

	dataDir := localDBFixture(t)
	cfg := localDBStubConfig(t, srv.URL)

	for _, tc := range []struct {
		name  string
		limit string
		want  int
	}{
		{"explicit limit is honoured", "3", 3},
		{"zero falls back to the documented default, not everything", "0", 20},
		{"negative is the same fallback", "-5", 20},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := runLocalDBCLI(t, cfg, dataDir, nil,
				"memory", "log", "ws_test", "crew:c1/x.md", "--limit", tc.limit, "--format", "text")
			if err != nil {
				t.Fatalf("memory log --limit %s: %v\noutput: %s", tc.limit, err, out)
			}
			// --format text prints one line per row, ending in the writer column.
			if got := strings.Count(out, " B  w"); got != tc.want {
				t.Errorf("printed %d rows, want %d:\n%s", got, tc.want, out)
			}
		})
	}
}

// `memory log` and `memory show` read the audit chain of the server the CLI
// targets. Both routes existed and neither had a CLI command that used them.
func TestAcceptance_MemoryVersions_ReadTheServer(t *testing.T) {
	stub := newMemoryVersionsStub(t)
	dataDir := localDBFixture(t)
	cfg := localDBStubConfig(t, stub.srv.URL)

	out, err := runLocalDBCLI(t, cfg, dataDir, nil,
		"memory", "log", "ws_test", "crew:c1/learned-x.md")
	if err != nil {
		t.Fatalf("memory log: %v\noutput: %s", err, out)
	}
	if !stub.listCalled.Load() {
		t.Error("GET /api/v1/admin/memory/versions was never called")
	}
	if !strings.Contains(out, "aaaabbbbcccc") {
		t.Errorf("the server's version row is missing:\n%s", out)
	}

	out, err = runLocalDBCLI(t, cfg, dataDir, nil,
		"memory", "show", "ws_test", "crew:c1/learned-x.md", "aaaabbbbcccc")
	if err != nil {
		t.Fatalf("memory show: %v\noutput: %s", err, out)
	}
	if !stub.contentCalled.Load() {
		t.Error("GET /api/v1/admin/memory/versions/{id}/content was never called")
	}
	if !strings.Contains(out, "content from the server") {
		t.Errorf("blob body missing:\n%s", out)
	}
}
