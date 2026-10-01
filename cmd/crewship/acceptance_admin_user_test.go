package main

import (
	"encoding/json"
	"log/slog"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/api"
	"github.com/crewship-ai/crewship/internal/backup"
	"github.com/crewship-ai/crewship/internal/testutil"
)

// A real CLI process against a real router on a migrated SQLite: Admin ›
// Users from the terminal. The person's lock shows in list-users and
// --locked-only answers over HTTP; their devices list, one is signed out,
// then the rest; the lock is lifted. Nothing is stubbed, so the CLI's idea
// of the response shape is checked against the server's, not against itself.
func TestAcceptance_AdminUserVerbs(t *testing.T) {
	t.Setenv(backup.InstanceOwnerEmailEnv, "")
	db := testutil.MigratedDB(t).DB
	const ws = "people" // the CLI resolves a slug; a cuid-shaped id would also do
	token := "crewship_cli_adminuser000000000000000000"
	future := time.Now().UTC().Add(time.Hour).Format(time.RFC3339)
	now := time.Now().UTC().Format(time.RFC3339)
	for _, q := range []string{
		`INSERT INTO workspaces(id,name,slug) VALUES('adminuseracceptws000','People','people')`,
		`INSERT INTO users(id,email,full_name) VALUES('au-owner','owner@people.invalid','Owner')`,
		`INSERT INTO users(id,email,full_name,failed_login_count,locked_until) VALUES('au-jana','jana@people.invalid','Jana',5,'` + future + `')`,
		`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('au-m1','adminuseracceptws000','au-owner','OWNER'),('au-m2','adminuseracceptws000','au-jana','MEMBER')`,
		`INSERT INTO user_sessions(id,user_id,created_at,expires_at,last_used_at,user_agent,ip) VALUES
			('au-s1','au-jana','` + now + `','` + future + `','` + now + `','Chrome on macOS','10.0.0.7'),
			('au-s2','au-jana','` + now + `','` + future + `','` + now + `','Safari on iPhone','10.0.0.8')`,
		`INSERT INTO cli_tokens(id,user_id,name,token_hash) VALUES('au-jt','au-jana','laptop','not-a-real-hash')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%v\n%s", err, q)
		}
	}
	if _, err := db.Exec(`INSERT INTO cli_tokens(id,user_id,name,token_hash,created_at) VALUES('au-token','au-owner','test',?,datetime('now'))`, sha256HexToken(token)); err != nil {
		t.Fatal(err)
	}
	router, err := api.NewRouter(db, "this-is-a-32-char-test-secret-pad", slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(router)
	defer srv.Close()
	cfg := filepath.Join(t.TempDir(), "cli.yaml")
	if err := os.WriteFile(cfg, []byte("server: "+srv.URL+"\nworkspace: "+ws+"\ntoken: "+token+"\nformat: table\n"), 0600); err != nil {
		t.Fatal(err)
	}
	binary := buildCrewshipBinary(t)
	run := func(args ...string) (string, error) {
		cmd := exec.Command(binary, args...)
		cmd.Env = append(os.Environ(), "CREWSHIP_CONFIG="+cfg, "CREWSHIP_SERVER=", "CREWSHIP_PROFILE=", "CREWSHIP_TOKEN=", "CREWSHIP_WORKSPACE=", "NO_COLOR=1", "DATABASE_URL=")
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	must := func(args ...string) string {
		t.Helper()
		out, err := run(args...)
		if err != nil {
			t.Fatalf("CLI %v: %v\n%s", args, err, out)
		}
		return out
	}

	// The lock and the devices show in the list, and --locked-only answers.
	out := must("admin", "list-users")
	if !strings.Contains(out, "LOCKED") || !strings.Contains(out, "until ") || !strings.Contains(out, "MEMBER@people") {
		t.Fatalf("list-users does not show lockout and roles:\n%s", out)
	}
	out = must("admin", "list-users", "--locked-only")
	if !strings.Contains(out, "jana@people.invalid") || strings.Contains(out, "owner@people.invalid") {
		t.Fatalf("--locked-only over HTTP:\n%s", out)
	}

	type sessionsOut struct {
		Sessions []struct {
			ID string `json:"id"`
			IP string `json:"ip"`
		} `json:"sessions"`
		CLITokens []struct {
			Name string `json:"name"`
		} `json:"cli_tokens"`
	}
	sessions := func() sessionsOut {
		t.Helper()
		var s sessionsOut
		raw := must("admin", "user", "sessions", "jana@people.invalid", "--format", "json")
		if err := json.Unmarshal([]byte(raw), &s); err != nil {
			t.Fatalf("sessions json: %v\n%s", err, raw)
		}
		return s
	}
	s := sessions()
	if len(s.Sessions) != 2 || len(s.CLITokens) != 1 || s.CLITokens[0].Name != "laptop" {
		t.Fatalf("sessions: %+v", s)
	}
	if table := must("admin", "user", "sessions", "jana@people.invalid"); !strings.Contains(table, "Chrome on macOS") || !strings.Contains(table, "10.0.0.7") {
		t.Fatalf("sessions table:\n%s", table)
	}

	must("admin", "user", "revoke-session", "jana@people.invalid", "au-s1")
	if s := sessions(); len(s.Sessions) != 1 || s.Sessions[0].ID != "au-s2" {
		t.Fatalf("after revoke-session: %+v", s)
	}
	if out := must("admin", "user", "sign-out", "jana@people.invalid"); !strings.Contains(out, "1 session") {
		t.Fatalf("sign-out:\n%s", out)
	}
	if s := sessions(); len(s.Sessions) != 0 || len(s.CLITokens) != 1 {
		t.Fatalf("sign-out must end sessions and leave CLI tokens: %+v", s)
	}

	must("admin", "user", "unlock", "jana@people.invalid")
	if out := must("admin", "list-users", "--locked-only"); strings.Contains(out, "jana@people.invalid") {
		t.Fatalf("still locked after unlock:\n%s", out)
	}

	// Someone the caller does not administer is not found — by the CLI and
	// the server alike.
	if out, err := run("admin", "user", "unlock", "stranger@elsewhere.invalid"); err == nil || !strings.Contains(out, "no account") {
		t.Fatalf("unknown person: %v\n%s", err, out)
	}

	// The workspace table carries the week's runs and the spend.
	if out := must("admin", "workspaces"); !strings.Contains(out, "RUNS 7D") || !strings.Contains(out, "People *") {
		t.Fatalf("admin workspaces:\n%s", out)
	}
}
