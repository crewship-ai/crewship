package main

// #2072 acceptance — `crewship integration access` against the REAL api
// router, not a stubbed response. CONTRIBUTING: every API endpoint gets a CLI
// command and its acceptance test drives the CLI, because the CLI is the
// contract agents actually invoke.
//
// The scenario is the issue's: a workspace integration everyone can use, then
// somebody binds ONE agent to it. That used to revoke the integration from
// every other agent in the workspace — silently, with no audit entry and
// nothing on the integration saying it had happened. Here the second agent
// keeps it, and taking it away is a separate command that says what it did.

import (
	"database/sql"
	"log/slog"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/api"
	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/testutil"
	"github.com/spf13/cobra"
)

const (
	accessWorkspaceID = "caccessws0000000001a"
	accessCrewID      = "caccesscrew000000001"
)

// setupAccessServer builds a real router over a migrated SQLite DB with one
// workspace, an OWNER holding a CLI token, one crew and two agents.
func setupAccessServer(t *testing.T) *sql.DB {
	t.Helper()

	dbh := testutil.MigratedDB(t)
	db := dbh.DB
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("seed exec %q: %v", q, err)
		}
	}

	mustExec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'Access', 'access-ws')`, accessWorkspaceID)
	mustExec(`INSERT INTO users (id, email, full_name) VALUES ('ac-owner', 'owner@ex.com', 'Owner')`)
	mustExec(`INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES ('acm-owner', ?, 'ac-owner', 'OWNER')`,
		accessWorkspaceID)

	ownerToken := "crewship_cli_acowner00000000000000000000"
	mustExec(`INSERT INTO cli_tokens (id, user_id, name, token_hash, created_at) VALUES ('clt-ac-owner', 'ac-owner', 't', ?, datetime('now'))`,
		sha256HexToken(ownerToken))

	mustExec(`INSERT INTO crews (id, workspace_id, name, slug, network_mode) VALUES (?, ?, 'Engineering', 'engineering', 'free')`,
		accessCrewID, accessWorkspaceID)
	for _, a := range []struct{ id, name, slug, role string }{
		{"cacagentlead00000001", "Lead", "lead", "LEAD"},
		{"cacagentpepa00000001", "Pepa", "pepa", "AGENT"},
	} {
		mustExec(`INSERT INTO agents (id, crew_id, workspace_id, name, slug, agent_role, status)
		          VALUES (?, ?, ?, ?, ?, ?, 'IDLE')`,
			a.id, accessCrewID, accessWorkspaceID, a.name, a.slug, a.role)
	}

	r, err := api.NewRouter(db, "this-is-a-32-char-test-secret-pad", logger)
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)

	cliCfg = &cli.CLIConfig{
		Token:     ownerToken,
		Workspace: accessWorkspaceID,
		Server:    srv.URL,
	}
	return db
}

func accessDeclareAddFlags(c *cobra.Command) {
	c.Flags().String("name", "", "")
	c.Flags().String("display", "", "")
	c.Flags().String("transport", "streamable-http", "")
	c.Flags().String("endpoint", "", "")
	c.Flags().String("command", "", "")
	c.Flags().String("icon", "", "")
	c.Flags().String("access", "", "")
}

func accessDeclareBindFlags(c *cobra.Command) {
	c.Flags().String("agent", "", "")
	c.Flags().String("server", "", "")
	c.Flags().String("credential", "", "")
	c.Flags().String("cred-type", "bearer", "")
	c.Flags().String("cred-header", "", "")
}

// accessAddIntegration runs `crewship integration add --name <name> ...`.
func accessAddIntegration(t *testing.T, name, access string) (string, error) {
	t.Helper()
	c := covFreshCmd(intgAddCmd, accessDeclareAddFlags)
	flags := map[string]string{
		"name":     name,
		"endpoint": "https://mcp.example.com/" + name,
	}
	if access != "" {
		flags["access"] = access
	}
	covSetFlagsCli4(t, c, flags)
	return covCaptureStdoutCli4(t, func() error { return c.RunE(c, nil) })
}

// accessResolve runs `crewship integration resolve <agent>` and reports
// whether the named integration appears in the agent's effective list.
func accessResolve(t *testing.T, agentSlug, integration string) bool {
	t.Helper()
	out, err := covCaptureStdoutCli4(t, func() error {
		return intgResolveCmd.RunE(intgResolveCmd, []string{agentSlug})
	})
	if err != nil {
		t.Fatalf("`integration resolve %s`: %v (output: %s)", agentSlug, err, out)
	}
	return strings.Contains(out, integration)
}

// accessSet runs `crewship integration access <integration> <value>`.
func accessSet(t *testing.T, integration, value string) (string, error) {
	t.Helper()
	return covCaptureStdoutCli4(t, func() error {
		return intgAccessCmd.RunE(intgAccessCmd, []string{integration, value})
	})
}

// TestIntegrationAccessCLI_BindDoesNotRevokeFromOthers is #2072 end to end
// through the three commands an operator would actually run.
func TestIntegrationAccessCLI_BindDoesNotRevokeFromOthers(t *testing.T) {
	saveCLIState(t)
	db := setupAccessServer(t)

	if out, err := accessAddIntegration(t, "github", ""); err != nil {
		t.Fatalf("`integration add`: %v (output: %s)", err, out)
	}

	// A new integration is available to everyone, and says so.
	var stored string
	if err := db.QueryRow(`SELECT default_access FROM workspace_mcp_servers WHERE name = 'github'`).Scan(&stored); err != nil {
		t.Fatalf("read default_access: %v", err)
	}
	if stored != "all" {
		t.Fatalf("default_access = %q on a fresh integration, want \"all\"", stored)
	}
	for _, agent := range []string{"lead", "pepa"} {
		if !accessResolve(t, agent, "github") {
			t.Fatalf("agent %s does not resolve github before any binding", agent)
		}
	}

	// Bind ONE agent. This is the act that used to revoke the integration
	// from everyone else.
	bind := covFreshCmd(intgBindCmd, accessDeclareBindFlags)
	covSetFlagsCli4(t, bind, map[string]string{"agent": "pepa", "server": "github"})
	if out, err := covCaptureStdoutCli4(t, func() error { return bind.RunE(bind, nil) }); err != nil {
		t.Fatalf("`integration bind`: %v (output: %s)", err, out)
	}

	if !accessResolve(t, "lead", "github") {
		t.Error("binding pepa took github away from lead — #2072, the whole point of default_access")
	}
	if err := db.QueryRow(`SELECT default_access FROM workspace_mcp_servers WHERE name = 'github'`).Scan(&stored); err != nil {
		t.Fatalf("read default_access after bind: %v", err)
	}
	if stored != "all" {
		t.Errorf("default_access = %q after binding an agent, want \"all\" — a binding is not an audience change", stored)
	}
}

// TestIntegrationAccessCLI_SetsAndReadsBack covers the command's own contract:
// both values reach the column, the CLI says which state it landed in, and
// bound-only actually narrows resolution to the bound agent.
func TestIntegrationAccessCLI_SetsAndReadsBack(t *testing.T) {
	saveCLIState(t)
	db := setupAccessServer(t)

	if out, err := accessAddIntegration(t, "github", ""); err != nil {
		t.Fatalf("`integration add`: %v (output: %s)", err, out)
	}
	bind := covFreshCmd(intgBindCmd, accessDeclareBindFlags)
	covSetFlagsCli4(t, bind, map[string]string{"agent": "pepa", "server": "github"})
	if out, err := covCaptureStdoutCli4(t, func() error { return bind.RunE(bind, nil) }); err != nil {
		t.Fatalf("`integration bind`: %v (output: %s)", err, out)
	}

	out, err := accessSet(t, "github", "bound-only")
	if err != nil {
		t.Fatalf("`integration access github bound-only`: %v (output: %s)", err, out)
	}
	if !strings.Contains(out, "bound-only") {
		t.Errorf("output = %q, want it to name the state it moved to", strings.TrimSpace(out))
	}
	var stored string
	if err := db.QueryRow(`SELECT default_access FROM workspace_mcp_servers WHERE name = 'github'`).Scan(&stored); err != nil {
		t.Fatalf("read default_access: %v", err)
	}
	if stored != "bound-only" {
		t.Fatalf("default_access = %q, want bound-only — the flag never reached the column", stored)
	}
	if accessResolve(t, "lead", "github") {
		t.Error("lead still resolves github after it was made bound-only")
	}
	if !accessResolve(t, "pepa", "github") {
		t.Error("pepa is bound and must still resolve github when it is bound-only")
	}

	// And back again — narrowing is reversible without touching bindings.
	if out, err := accessSet(t, "github", "all"); err != nil {
		t.Fatalf("`integration access github all`: %v (output: %s)", err, out)
	}
	if !accessResolve(t, "lead", "github") {
		t.Error("lead does not resolve github after it was reopened to all")
	}
}

// TestIntegrationAccessCLI_RejectsUnknownValue: the vocabulary is closed, and
// a typo must not be stored — resolution fails closed on anything it does not
// recognise, so a silently accepted "All" would revoke the server from every
// unbound agent.
func TestIntegrationAccessCLI_RejectsUnknownValue(t *testing.T) {
	saveCLIState(t)
	db := setupAccessServer(t)
	if out, err := accessAddIntegration(t, "github", ""); err != nil {
		t.Fatalf("`integration add`: %v (output: %s)", err, out)
	}

	for _, bad := range []string{"everyone", "bound only", "ALL-ISH"} {
		if _, err := accessSet(t, "github", bad); err == nil {
			t.Errorf("`integration access github %s` was accepted, want an error naming the two values", bad)
		}
	}
	var stored string
	if err := db.QueryRow(`SELECT default_access FROM workspace_mcp_servers WHERE name = 'github'`).Scan(&stored); err != nil {
		t.Fatalf("read default_access: %v", err)
	}
	if stored != "all" {
		t.Errorf("default_access = %q after rejected input, want it untouched at \"all\"", stored)
	}
}

// TestIntegrationAccessCLI_CreateBoundOnly: an integration can be created
// closed, so a workspace that wants opt-in never has to pass through a window
// where every agent can reach the server.
func TestIntegrationAccessCLI_CreateBoundOnly(t *testing.T) {
	saveCLIState(t)
	db := setupAccessServer(t)

	if out, err := accessAddIntegration(t, "payroll", "bound-only"); err != nil {
		t.Fatalf("`integration add --access bound-only`: %v (output: %s)", err, out)
	}
	var stored string
	if err := db.QueryRow(`SELECT default_access FROM workspace_mcp_servers WHERE name = 'payroll'`).Scan(&stored); err != nil {
		t.Fatalf("read default_access: %v", err)
	}
	if stored != "bound-only" {
		t.Fatalf("default_access = %q, want bound-only", stored)
	}
	if accessResolve(t, "lead", "payroll") {
		t.Error("an unbound agent resolves a bound-only integration")
	}
}
