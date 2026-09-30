package main

import (
	"database/sql"
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

// A real CLI process against a real router: Admin › Data retention. The
// instance admin sits in "people" and sets the retention of "lab", which they
// do not belong to, then of every workspace. Nothing is stubbed.
func TestAcceptance_AdminInstanceRetention(t *testing.T) {
	t.Setenv(backup.InstanceOwnerEmailEnv, "")
	db := testutil.MigratedDB(t).DB
	token := "crewship_cli_instretention000000000000"
	old := time.Now().Add(-40 * 24 * time.Hour).UTC().Format(time.RFC3339)
	for _, q := range []string{
		`INSERT INTO workspaces(id,name,slug,created_at) VALUES('ir-people','People','people','2026-01-01 00:00:00'),('ir-lab','Lab','lab','2026-06-01 00:00:00')`,
		`INSERT INTO users(id,email,full_name) VALUES('ir-boss','boss@people.invalid','Boss'),('ir-carol','carol@lab.invalid','Carol')`,
		`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('ir-m1','ir-people','ir-boss','OWNER'),('ir-m2','ir-lab','ir-carol','OWNER')`,
		`INSERT INTO inbox_items(id,workspace_id,kind,source_id,title,state,blocking,resolved_at) VALUES
			('ir-i1','ir-lab','escalation','s1','old','resolved',1,'` + old + `'),
			('ir-i2','ir-lab','escalation','s2','open','unread',1,NULL)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%v\n%s", err, q)
		}
	}
	if _, err := db.Exec(`INSERT INTO cli_tokens(id,user_id,name,token_hash,created_at) VALUES('ir-token','ir-boss','test',?,datetime('now'))`, sha256HexToken(token)); err != nil {
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
	run := func(stdin string, args ...string) (string, error) {
		cmd := exec.Command(binary, args...)
		cmd.Env = append(os.Environ(), "CREWSHIP_CONFIG="+cfg, "CREWSHIP_SERVER=", "CREWSHIP_PROFILE=", "CREWSHIP_TOKEN=", "CREWSHIP_WORKSPACE=", "NO_COLOR=1", "DATABASE_URL=")
		cmd.Stdin = strings.NewReader(stdin)
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	must := func(stdin string, args ...string) string {
		t.Helper()
		out, err := run(stdin, args...)
		if err != nil {
			t.Fatalf("CLI %v: %v\n%s", args, err, out)
		}
		return out
	}
	inboxDays := func(ws string) sql.NullInt64 {
		t.Helper()
		var d sql.NullInt64
		_ = db.QueryRow(`SELECT days FROM retention_settings WHERE workspace_id = ? AND key = 'inbox_days'`, ws).Scan(&d)
		return d
	}

	// Everything starts forever for the new windows.
	out := must("", "admin", "instance", "retention", "get")
	if !strings.Contains(out, "lab") || !strings.Contains(out, "people") || !strings.Contains(out, "forever") || !strings.Contains(out, "Fixed instance limits") {
		t.Fatalf("get:\n%s", out)
	}
	var list struct {
		Workspaces []struct {
			WorkspaceSlug string          `json:"workspace_slug"`
			Windows       map[string]*int `json:"windows"`
		} `json:"workspaces"`
	}
	raw := must("", "admin", "instance", "retention", "get", "--ws", "lab", "--format", "json")
	if err := json.Unmarshal([]byte(raw), &list); err != nil {
		t.Fatalf("get json: %v\n%s", err, raw)
	}
	if len(list.Workspaces) != 1 || list.Workspaces[0].WorkspaceSlug != "lab" || list.Workspaces[0].Windows["inbox_days"] != nil {
		t.Fatalf("get --ws lab = %s", raw)
	}

	// --dry-run shows the change and the rows the sweep would take, writes nothing.
	out = must("", "admin", "instance", "retention", "set", "--ws", "lab", "--inbox-days", "30", "--dry-run")
	if !strings.Contains(out, "would change") || !strings.Contains(out, "inbox_days forever → 30d") || !strings.Contains(out, "1 row(s) at the next sweep") {
		t.Fatalf("dry run:\n%s", out)
	}
	if inboxDays("ir-lab").Valid {
		t.Fatal("a dry run wrote")
	}

	// A declined save writes nothing; a yes writes lab only.
	if out, err := run("n\n", "admin", "instance", "retention", "set", "--ws", "lab", "--inbox-days", "30"); err == nil || !strings.Contains(out, "will change") {
		t.Fatalf("declined save: %v\n%s", err, out)
	}
	if inboxDays("ir-lab").Valid {
		t.Fatal("a declined save still wrote")
	}
	must("y\n", "admin", "instance", "retention", "set", "--ws", "lab", "--inbox-days", "30")
	if d := inboxDays("ir-lab"); !d.Valid || d.Int64 != 30 || inboxDays("ir-people").Valid {
		t.Fatalf("lab inbox_days = %v, people touched = %v", d, inboxDays("ir-people").Valid)
	}

	// --all with --yes, forever included — every existing workspace, never the defaults.
	if out := must("", "admin", "instance", "retention", "set", "--all", "--inbox-days", "forever", "--keeper-decisions-days", "365", "--yes"); !strings.Contains(out, "New workspaces are unaffected") {
		t.Fatalf("bulk save:\n%s", out)
	}
	var defaultsRows int
	_ = db.QueryRow(`SELECT COUNT(*) FROM app_settings WHERE key = 'retention.defaults'`).Scan(&defaultsRows)
	if defaultsRows != 0 {
		t.Fatal("set --all wrote the defaults for new workspaces")
	}
	var keeperRows int
	_ = db.QueryRow(`SELECT COUNT(*) FROM retention_settings WHERE key = 'keeper_decisions_days' AND days = 365`).Scan(&keeperRows)
	if keeperRows != 2 || inboxDays("ir-lab").Valid {
		t.Fatalf("after --all: keeper rows %d, lab inbox %v", keeperRows, inboxDays("ir-lab"))
	}

	// Defaults for new workspaces are their own command and touch nothing existing.
	if out := must("", "admin", "instance", "retention", "defaults", "get"); !strings.Contains(out, "inbox_days") || !strings.Contains(out, "product default") {
		t.Fatalf("defaults get:\n%s", out)
	}
	if out := must("", "admin", "instance", "retention", "defaults", "set", "--chats-days", "120", "--dry-run"); !strings.Contains(out, "chats_days") || !strings.Contains(out, "forever → 120d") || !strings.Contains(out, "nothing is deleted") {
		t.Fatalf("defaults dry run:\n%s", out)
	}
	if out, err := run("n\n", "admin", "instance", "retention", "defaults", "set", "--chats-days", "120"); err == nil || !strings.Contains(out, "will change") {
		t.Fatalf("declined defaults: %v\n%s", err, out)
	}
	_ = db.QueryRow(`SELECT COUNT(*) FROM app_settings WHERE key = 'retention.defaults'`).Scan(&defaultsRows)
	if defaultsRows != 0 {
		t.Fatal("a dry or declined defaults save wrote")
	}
	if out := must("", "admin", "instance", "retention", "defaults", "set", "--chats-days", "120", "--yes"); !strings.Contains(out, "Existing workspaces are unchanged") {
		t.Fatalf("defaults set:\n%s", out)
	}
	var chatRows int
	_ = db.QueryRow(`SELECT COUNT(*) FROM retention_settings WHERE key = 'chats_days'`).Scan(&chatRows)
	if chatRows != 0 {
		t.Fatalf("defaults set changed %d existing workspace(s)", chatRows)
	}
	var defaults struct {
		Defaults   map[string]*int `json:"defaults"`
		Configured []string        `json:"configured"`
	}
	raw = must("", "admin", "instance", "retention", "defaults", "get", "-f", "json")
	if err := json.Unmarshal([]byte(raw), &defaults); err != nil || defaults.Defaults["chats_days"] == nil || *defaults.Defaults["chats_days"] != 120 || len(defaults.Configured) != 1 {
		t.Fatalf("defaults get json: %v\n%s", err, raw)
	}

	// The CLI passes the server's refusals on.
	if out, err := run("", "admin", "instance", "retention", "set", "--ws", "lab", "--routine-runs-days", "forever", "--yes"); err == nil || !strings.Contains(out, "cannot be forever") {
		t.Fatalf("runs forever: %v\n%s", err, out)
	}
	if out, err := run("", "admin", "instance", "retention", "set", "--ws", "lab", "--chats-days", "soon"); err == nil || !strings.Contains(out, "number of days or forever") {
		t.Fatalf("bad value: %v\n%s", err, out)
	}
	if out, err := run("", "admin", "instance", "retention", "set", "--inbox-days", "5"); err == nil || !strings.Contains(out, "--ws") {
		t.Fatalf("no target: %v\n%s", err, out)
	}

	var audited int
	_ = db.QueryRow(`SELECT COUNT(*) FROM instance_audit_logs WHERE action = 'instance.retention_updated' AND target_workspace_id = 'ir-lab'`).Scan(&audited)
	var defaultsAudited int
	_ = db.QueryRow(`SELECT COUNT(*) FROM instance_audit_logs WHERE action = 'instance.retention_defaults_updated'`).Scan(&defaultsAudited)
	if defaultsAudited != 1 {
		t.Fatalf("defaults audit entries = %d, want 1", defaultsAudited)
	}
	if audited != 2 {
		t.Fatalf("lab audit entries = %d, want one per save that changed it", audited)
	}
	// Nothing was swept by setting a window: the sweep runs on its own clock.
	var inbox int
	_ = db.QueryRow(`SELECT COUNT(*) FROM inbox_items WHERE workspace_id = 'ir-lab'`).Scan(&inbox)
	if inbox != 2 {
		t.Fatalf("inbox rows = %d, a save must not delete", inbox)
	}
}
