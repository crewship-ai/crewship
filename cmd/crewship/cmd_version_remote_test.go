package main

import (
	"encoding/json"
	"log/slog"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/api"
	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/database"
	"github.com/crewship-ai/crewship/internal/testutil"
)

// #1645 — `crewship version --remote`.
//
// These drive the real cobra RunE against a REAL api.NewRouter (migrated DB,
// real auth middleware, real handler) rather than a stubbed HTTP response.
// That matters here more than usual: the whole feature is "the answer comes
// from the server, not from this binary", and a stub would have let a command
// that printed its OWN commit twice pass.

// versionRemoteWorkspaceID is CUID-shaped so the CLI client treats it as an
// already-resolved id and does not fire a slug→id resolution request.
const versionRemoteWorkspaceID = "cverws0000000000000001"

// serverCommit is deliberately NOT this test binary's commit, so an
// assertion on it cannot be satisfied by the CLI reporting itself.
const serverCommit = "0f1e2d3c4b5a69788796a5b4c3d2e1f00f1e2d3c"

const serverBuildTime = "2026-07-30T04:05:06Z"

func setupVersionRemoteServer(t *testing.T) (url, token string) {
	t.Helper()

	dbh := testutil.MigratedDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	db := dbh.DB
	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("seed exec %q: %v", q, err)
		}
	}
	mustExec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'Version IT', 'version-it')`, versionRemoteWorkspaceID)
	mustExec(`INSERT INTO users (id, email, full_name) VALUES ('vr-user', 'vr@ex.com', 'VR')`)
	mustExec(`INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES ('vrm-user', ?, 'vr-user', 'OWNER')`, versionRemoteWorkspaceID)

	token = "crewship_cli_vruser000000000000000000000"
	mustExec(`INSERT INTO cli_tokens (id, user_id, name, token_hash, created_at) VALUES ('clt-vr', 'vr-user', 't', ?, datetime('now'))`, sha256HexToken(token))

	r, err := api.NewRouter(db, "this-is-a-32-char-test-secret-pad", logger)
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	// Exactly what cmd_start.go does at boot.
	r.SetBuild("v9.9.9-serverside", serverCommit, serverBuildTime)

	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return srv.URL, token
}

func runVersionRemote(t *testing.T) string {
	t.Helper()
	origRemote := flagVersionRemote
	flagVersionRemote = true
	t.Cleanup(func() { flagVersionRemote = origRemote })

	return covCaptureStdoutCli5(t, func() {
		if err := versionCmd.RunE(versionCmd, nil); err != nil {
			t.Fatalf("version --remote: %v", err)
		}
	})
}

// TestVersionRemote_PrintsTheServersCommitNotItsOwn is the acceptance for the
// issue: dev1 sat a day behind main and nothing could say so. The command has
// to name the *server's* build.
func TestVersionRemote_PrintsTheServersCommitNotItsOwn(t *testing.T) {
	saveCLIState(t)
	url, token := setupVersionRemoteServer(t)
	cliCfg = &cli.CLIConfig{Token: token, Workspace: versionRemoteWorkspaceID, Server: url}

	out := runVersionRemote(t)

	if !strings.Contains(out, serverCommit) {
		t.Errorf("output does not name the server's commit %s:\n%s", serverCommit, out)
	}
	if !strings.Contains(out, serverBuildTime) {
		t.Errorf("output does not name the server's build time %s:\n%s", serverBuildTime, out)
	}
	if !strings.Contains(out, "v9.9.9-serverside") {
		t.Errorf("output does not name the server's version:\n%s", out)
	}
	// And it must still report the local binary — the two-sided comparison is
	// the point; a report of one side cannot tell you which one is stale.
	if !strings.Contains(out, "crewship "+version) {
		t.Errorf("output dropped the local binary's own version %q:\n%s", version, out)
	}
	// The server's URL has to be named too: "some server is at commit X" is
	// not actionable when a workstation has three profiles.
	if !strings.Contains(out, url) {
		t.Errorf("output does not name which server it asked (%s):\n%s", url, out)
	}
}

// The schema number is the second half of #1645's ask — so the migration
// comparison can name both sides. Past the legacy sequential block it is a
// YYYYMMDDHHMMSS timestamp, so it doubles as "how recent is the newest
// schema change this server was built with".
func TestVersionRemote_PrintsTheSchemaTheServerExpects(t *testing.T) {
	saveCLIState(t)
	url, token := setupVersionRemoteServer(t)
	cliCfg = &cli.CLIConfig{Token: token, Workspace: versionRemoteWorkspaceID, Server: url}

	out := runVersionRemote(t)

	want := database.MaxKnownMigrationVersion()
	if want <= 0 {
		t.Fatalf("MaxKnownMigrationVersion()=%d — assertion would be vacuous", want)
	}
	if !strings.Contains(out, "schema:  "+strconv.Itoa(want)) {
		t.Errorf("output does not name the server's schema version %d:\n%s", want, out)
	}
}

// --format json has to carry the same facts, because the reason to reach for
// it is a script comparing a deploy's commit against what origin/main is at.
func TestVersionRemote_JSONCarriesBothSides(t *testing.T) {
	saveCLIState(t)
	url, token := setupVersionRemoteServer(t)
	cliCfg = &cli.CLIConfig{Token: token, Workspace: versionRemoteWorkspaceID, Server: url}

	origFormat := flagFormat
	flagFormat = "json"
	t.Cleanup(func() { flagFormat = origFormat })

	out := runVersionRemote(t)

	var payload struct {
		Client struct {
			Version string `json:"version"`
			Commit  string `json:"commit"`
		} `json:"client"`
		Server struct {
			URL           string `json:"url"`
			Version       string `json:"version"`
			Commit        string `json:"commit"`
			BuildTime     string `json:"build_time"`
			SchemaVersion int    `json:"schema_version"`
		} `json:"server"`
	}
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatalf("decode %q: %v", out, err)
	}
	if payload.Server.Commit != serverCommit {
		t.Errorf("server.commit=%q want %q", payload.Server.Commit, serverCommit)
	}
	if payload.Server.BuildTime != serverBuildTime {
		t.Errorf("server.build_time=%q want %q", payload.Server.BuildTime, serverBuildTime)
	}
	if payload.Server.Version != "v9.9.9-serverside" {
		t.Errorf("server.version=%q want %q", payload.Server.Version, "v9.9.9-serverside")
	}
	if payload.Server.URL != url {
		t.Errorf("server.url=%q want %q", payload.Server.URL, url)
	}
	if payload.Server.SchemaVersion != database.MaxKnownMigrationVersion() {
		t.Errorf("server.schema_version=%d want %d", payload.Server.SchemaVersion, database.MaxKnownMigrationVersion())
	}
	if payload.Client.Version != version {
		t.Errorf("client.version=%q want the local binary's %q", payload.Client.Version, version)
	}
	// The client block must not be a copy of the server block — that is the
	// bug this whole command exists to make visible.
	if payload.Client.Commit == serverCommit {
		t.Errorf("client.commit echoes the server's %q; the two sides were not read separately", serverCommit)
	}
}

// Without --remote the command stays offline. `crewship version` is the first
// thing run on a broken install; making it dial the network would turn a
// local question into a timeout.
func TestVersion_WithoutRemoteMakesNoRequest(t *testing.T) {
	saveCLIState(t)
	// A server URL that would fail loudly if dialled.
	cliCfg = &cli.CLIConfig{Token: "crewship_cli_x", Server: "http://127.0.0.1:1"}

	out := covCaptureStdoutCli5(t, func() {
		if err := versionCmd.RunE(versionCmd, nil); err != nil {
			t.Fatalf("version: %v", err)
		}
	})
	if strings.Contains(out, "server") {
		t.Errorf("plain `crewship version` reported a server section:\n%s", out)
	}
	if !strings.Contains(out, "crewship "+version) {
		t.Errorf("plain `crewship version` lost its local report:\n%s", out)
	}
}
