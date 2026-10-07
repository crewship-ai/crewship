package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/cli/clitest"
	"github.com/spf13/cobra"
)

func TestSeedPreflightRejectsBeforeMutation(t *testing.T) {
	for _, mode := range []string{"implicit-target", "missing-provider"} {
		t.Run(mode, func(t *testing.T) {
			guardCLIState(t)
			saveCLIState(t)
			covSeedEnv(t)
			t.Setenv(seedCodexAuthFileEnv, "")
			seedCodexAuthFileOverride = ""
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; w.WriteHeader(500) }))
			defer srv.Close()
			flagServer = srv.URL
			flagProfile = ""
			cliCfg = &cli.CLIConfig{Server: srv.URL, Token: "existing", Workspace: "workspace"}
			cmd := &cobra.Command{}
			cmd.SetContext(context.Background())
			cmd.Flags().Bool("with-memory", false, "")
			cmd.Flags().String("codex-auth-file", "", "")
			if mode == "missing-provider" {
				if err := os.WriteFile(".env.local", []byte("SEED_ANTHROPIC_API_KEY=from-cwd\nCREWSHIP_SERVER="+srv.URL+"\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if mode != "missing-provider" {
				t.Setenv("SEED_ANTHROPIC_API_KEY", "sk-ant-test")
			}
			if mode == "implicit-target" {
				flagServer = ""
				t.Setenv("CREWSHIP_SERVER", srv.URL)
			}
			err := runSeed(cmd, nil)
			if err == nil {
				t.Fatal("expected preflight failure")
			}
			if calls != 0 {
				t.Fatalf("preflight made %d server requests", calls)
			}
			if _, err := os.Stat(os.Getenv("CREWSHIP_CONFIG")); !os.IsNotExist(err) {
				t.Fatalf("preflight wrote client state: %v", err)
			}
		})
	}
}

func TestSeedPreflightIgnoresDirectoryProfile(t *testing.T) {
	guardCLIState(t)
	saveCLIState(t)
	covSeedEnv(t)
	t.Setenv("SEED_ANTHROPIC_API_KEY", "sk-ant-test")
	t.Setenv(seedCodexAuthFileEnv, "")
	target := "https://stage.example"
	cwd := t.TempDir()
	cli.SetWorkingDir(cwd)
	t.Cleanup(func() { cli.SetWorkingDir("") })
	raw := &cli.CLIConfig{Current: "dev", Servers: map[string]*cli.ServerProfile{
		"dev":   {Server: "https://dev.example", Token: "dev-token", Workspace: "dev-workspace"},
		"stage": {Server: target, Token: "stage-token", Workspace: "stage-workspace"},
	}, DirectoryProfiles: map[string]string{cwd: "dev"}}
	if err := cli.SaveConfig(raw); err != nil {
		t.Fatal(err)
	}
	cliCfg = raw.WithActiveProfile("")
	flagServer = target
	cmd := &cobra.Command{}
	cmd.Flags().Bool("with-memory", false, "")
	cmd.Flags().String("codex-auth-file", "", "")
	if err := seedPreflight(cmd); err != nil {
		t.Fatal(err)
	}
	if cliCfg.Token != "stage-token" || cliCfg.Workspace != "stage-workspace" {
		t.Fatalf("wrong target scope: %#v", cliCfg)
	}
	if seedTargetServer() != target || newAPIClient().BaseURL != target {
		t.Fatal("target split")
	}
}

func TestSaveSeedCredentialPreservesUnrelatedDefaults(t *testing.T) {
	cfg := &cli.CLIConfig{Server: "https://prod.example", Token: "prod-token", Workspace: "prod-ws", Current: "prod", Servers: map[string]*cli.ServerProfile{"prod": {Server: "https://prod.example", Token: "prod-token"}}, DirectoryProfiles: map[string]string{filepath.Clean("/checkout"): "prod"}}
	saveSeedCredential(cfg, "https://stage.example", "stage-token", "stage-ws")
	if cfg.Server != "https://prod.example" || cfg.Token != "prod-token" || cfg.Current != "prod" || cfg.Servers["prod"].Token != "prod-token" {
		t.Fatal("seed changed unrelated default")
	}
	found := false
	for name, p := range cfg.Servers {
		if strings.HasPrefix(name, "seed-") && p.Token == "stage-token" {
			found = true
		}
	}
	if !found {
		t.Fatal("seed credential not stored in target profile")
	}
	saveSeedCredential(cfg, "https://stage.example", "refreshed", "stage-ws")
	if len(cfg.Servers) != 2 {
		t.Fatal("repeated save created duplicate profile")
	}
}

func TestSeedPreflightExplicitProfileWinsAtSameServer(t *testing.T) {
	guardCLIState(t)
	saveCLIState(t)
	covSeedEnv(t)
	t.Setenv("SEED_ANTHROPIC_API_KEY", "sk-ant-test")
	t.Setenv(seedCodexAuthFileEnv, "")
	server := "https://stage.example"
	raw := &cli.CLIConfig{Server: server, Token: "legacy-token", Workspace: "legacy-ws", Servers: map[string]*cli.ServerProfile{
		"alpha": {Server: server, Token: "alpha-token", Workspace: "alpha-ws"},
		"zulu":  {Server: server, Token: "zulu-token", Workspace: "zulu-ws"},
	}}
	if err := cli.SaveConfig(raw); err != nil {
		t.Fatal(err)
	}
	flagServer, flagProfile = server, "zulu"
	cmd := &cobra.Command{}
	cmd.Flags().Bool("with-memory", false, "")
	cmd.Flags().String("codex-auth-file", "", "")
	if err := seedPreflight(cmd); err != nil {
		t.Fatal(err)
	}
	if cliCfg.Token != "zulu-token" || cliCfg.Workspace != "zulu-ws" {
		t.Fatalf("explicit profile ignored: %#v", cliCfg)
	}
	name := saveSeedCredential(raw, server, "fresh", "fresh-ws")
	if name != "zulu" || raw.Servers["alpha"].Token != "alpha-token" || raw.Token != "legacy-token" {
		t.Fatal("wrong credential destination")
	}
	flagServer = "https://other.example"
	if err := seedPreflight(cmd); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("mismatched profile accepted: %v", err)
	}
}

func TestSeedOfflineDemoCreatesFixturesWithoutExecution(t *testing.T) {
	s := covSeedStub(t)
	covSetupRunSeed(t, s)
	t.Setenv("SEED_ANTHROPIC_API_KEY", "")
	t.Setenv(seedCodexAuthFileEnv, "")
	covSetFlag(t, seedCmd, "offline-demo", "true")
	covSetFlag(t, seedCmd, "with-memory", "true")
	covSetFlag(t, seedCmd, "skip-issues", "true")
	out, err := covCaptureStdout(t, func() error { return runSeed(seedCmd, nil) })
	if err != nil {
		t.Fatalf("offline seed: %v\n%s", err, out)
	}
	if !strings.Contains(out, "agents are fixtures; model execution is unavailable") {
		t.Fatal("missing offline execution notice")
	}
	if len(s.CallsFor("POST", "/api/v1/memory/initialize")) == 0 {
		t.Fatal("offline memory was not provisioned on the server")
	}
	if len(s.CallsFor("POST", "/api/v1/agents")) == 0 {
		t.Fatal("no offline UI agents created")
	}
	for _, c := range s.Calls() {
		if c.Method == "POST" && (strings.HasSuffix(c.Path, "/provision") || strings.HasSuffix(c.Path, "/run") || (strings.Contains(c.Path, "/agents/") && strings.HasSuffix(c.Path, "/credentials"))) {
			t.Fatalf("offline seed started execution or assigned real credential: %s", c.Path)
		}
		if c.Path == "/api/v1/credentials" && strings.Contains(string(c.Body), "ANTHROPIC_API_KEY") {
			t.Fatal("offline seed created provider credential")
		}
	}
	cfg, err := cli.LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Current == "" {
		t.Fatal("first bootstrap did not select its initial profile")
	}
	cliCfg = cfg.WithActiveProfile("")
	if err := requireAuth(); err != nil {
		t.Fatalf("subsequent command lost bootstrap auth: %v", err)
	}
	if newAPIClient().Token != "tok-seeded-123" {
		t.Fatal("subsequent command lost bootstrap token")
	}
}

func TestSeedOfflineDemoRejectsExecutionFlags(t *testing.T) {
	for _, name := range []string{"smoke-test", "test-backup"} {
		t.Run(name, func(t *testing.T) {
			guardCLIState(t)
			saveCLIState(t)
			covSeedEnv(t)
			flagServer = "https://example.test"
			cmd := &cobra.Command{}
			cmd.Flags().Bool("offline-demo", true, "")
			cmd.Flags().Bool(name, true, "")
			if err := seedPreflight(cmd); err == nil || !strings.Contains(err.Error(), "cannot be combined") {
				t.Fatalf("accepted offline --%s: %v", name, err)
			}
		})
	}
}

func TestSeedUsersWithDisabledSignup(t *testing.T) {
	s := covSeedStub(t)
	covSetupRunSeed(t, s)
	s.OnGet(setupStatusPath, clitest.JSONResponse(200, map[string]bool{"allow_signup": false}))
	stubSeedUserProvision(s, "/api/v1/workspaces/"+covSeedWSID+"/members/provision")
	s.OnGet(adminUsers, clitest.JSONResponse(200, fixtureRoster()))
	covSetFlag(t, seedCmd, "with-users", "true")
	covSetFlag(t, seedCmd, "offline-demo", "true")
	if err := runSeed(seedCmd, nil); err != nil {
		t.Fatal(err)
	}
	if len(s.CallsFor("POST", signupPath)) != 0 {
		t.Fatal("seed required signup")
	}
}
