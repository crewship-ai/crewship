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

	"github.com/crewship-ai/crewship/internal/api"
	"github.com/crewship-ai/crewship/internal/backup"
	"github.com/crewship-ai/crewship/internal/testutil"
)

// A real CLI process against a real router: Admin › People & workspaces from
// the terminal. The owner of the oldest workspace is the instance admin (no
// one is named yet), and drives every instance verb across a workspace they
// are not a member of. Nothing is stubbed.
func TestAcceptance_AdminInstanceVerbs(t *testing.T) {
	t.Setenv(backup.InstanceOwnerEmailEnv, "")
	db := testutil.MigratedDB(t).DB
	token := "crewship_cli_admininstance0000000000000"
	for _, q := range []string{
		`INSERT INTO workspaces(id,name,slug,created_at) VALUES('instaccws-people','People','people','2026-01-01 00:00:00'),('instaccws-lab','Lab','lab','2026-06-01 00:00:00')`,
		`INSERT INTO users(id,email,full_name) VALUES('ia-boss','boss@people.invalid','Boss'),('ia-carol','carol@lab.invalid','Carol')`,
		`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('ia-m1','instaccws-people','ia-boss','OWNER'),('ia-m2','instaccws-lab','ia-carol','OWNER')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%v\n%s", err, q)
		}
	}
	if _, err := db.Exec(`INSERT INTO cli_tokens(id,user_id,name,token_hash,created_at) VALUES('ia-token','ia-boss','test',?,datetime('now'))`, sha256HexToken(token)); err != nil {
		t.Fatal(err)
	}
	router, err := api.NewRouter(db, "this-is-a-32-char-test-secret-pad", slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(router)
	defer srv.Close()
	cfg := filepath.Join(t.TempDir(), "cli.yaml")
	if err := os.WriteFile(cfg, []byte("server: "+srv.URL+"\nworkspace: people\ntoken: "+token+"\nformat: table\n"), 0600); err != nil {
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
	role := func(ws, email string) string {
		t.Helper()
		var r string
		_ = db.QueryRow(`SELECT wm.role FROM workspace_members wm JOIN users u ON u.id = wm.user_id WHERE wm.workspace_id = ? AND u.email = ?`, ws, email).Scan(&r)
		return r
	}

	// A new person straight into a workspace the admin is not in.
	var created struct {
		Email    string `json:"email"`
		SetupURL string `json:"setup_url"`
	}
	raw := must("admin", "instance", "add-person", "lucie@lab.invalid", "--name", "Lucie", "--access", "lab=member", "--format", "json")
	if err := json.Unmarshal([]byte(raw), &created); err != nil || !strings.Contains(created.SetupURL, "/reset-password?token=") {
		t.Fatalf("add-person: %v\n%s", err, raw)
	}
	if role("instaccws-lab", "lucie@lab.invalid") != "MEMBER" {
		t.Fatal("add-person did not grant lab access")
	}

	must("admin", "instance", "grant", "lucie@lab.invalid", "--workspace", "people", "--role", "ADMIN")
	if role("instaccws-people", "lucie@lab.invalid") != "ADMIN" {
		t.Fatal("grant did not land")
	}

	if out := must("admin", "instance", "transfer", "lab", "--to", "lucie@lab.invalid"); !strings.Contains(out, "now owns Lab") {
		t.Fatalf("transfer:\n%s", out)
	}
	if role("instaccws-lab", "lucie@lab.invalid") != "OWNER" || role("instaccws-lab", "carol@lab.invalid") != "ADMIN" {
		t.Fatal("transfer did not swap owner and ADMIN")
	}
	must("admin", "instance", "revoke", "carol@lab.invalid", "--workspace", "lab")
	if role("instaccws-lab", "carol@lab.invalid") != "" {
		t.Fatal("revoke did not land")
	}
	// The last owner stays, and the CLI says so instead of pretending.
	if out, err := run("admin", "instance", "revoke", "lucie@lab.invalid", "--workspace", "lab"); err == nil || !strings.Contains(out, "only owner") {
		t.Fatalf("revoking the last owner: %v\n%s", err, out)
	}

	must("admin", "instance", "create-workspace", "Ops", "--slug", "ops", "--owner", "carol@lab.invalid")
	var opsOwner string
	_ = db.QueryRow(`SELECT u.email FROM workspace_members wm JOIN workspaces w ON w.id = wm.workspace_id JOIN users u ON u.id = wm.user_id WHERE w.slug = 'ops' AND wm.role = 'OWNER'`).Scan(&opsOwner)
	if opsOwner != "carol@lab.invalid" {
		t.Fatalf("ops owner = %q", opsOwner)
	}
	if _, err := run("admin", "instance", "delete-workspace", "ops", "--confirm", "nope"); err == nil {
		t.Fatal("delete-workspace accepted a wrong confirmation")
	}
	must("admin", "instance", "delete-workspace", "ops", "--confirm", "ops")

	if out := must("admin", "instance", "suspend", "lucie@lab.invalid", "--reason", "test"); !strings.Contains(out, "Suspended lucie@lab.invalid") {
		t.Fatalf("suspend:\n%s", out)
	}
	must("admin", "instance", "reactivate", "lucie@lab.invalid")

	if out := must("admin", "instance", "setup-link", "lucie@lab.invalid"); !strings.Contains(out, "/reset-password?token=") {
		t.Fatalf("setup-link:\n%s", out)
	}
	must("admin", "instance", "setup-link", "lucie@lab.invalid", "--revoke")

	if out, err := run("admin", "instance", "remove-admin", "carol@lab.invalid"); err == nil || !strings.Contains(out, "not a named instance admin") {
		t.Fatalf("remove-admin of someone never named: %v\n%s", err, out)
	}

	out := must("admin", "instance", "audit", "--for", "lab")
	for _, want := range []string{"instance.workspace_ownership_transferred", "instance.member_removed", "instance.member_added"} {
		if !strings.Contains(out, want) {
			t.Errorf("audit --for lab lacks %s:\n%s", want, out)
		}
	}

	// boss was named by the one-time bootstrap the first time anyone asked
	// (he owns the oldest workspace). Naming carol adds an admin and takes
	// nothing away: there is no fallback left to switch off.
	must("admin", "instance", "add-admin", "carol@lab.invalid")
	if out := must("admin", "instance", "audit"); !strings.Contains(out, "instance.admin_bootstrapped") || !strings.Contains(out, "instance.admin_granted") {
		t.Fatalf("boss after naming carol:\n%s", out)
	}
}
