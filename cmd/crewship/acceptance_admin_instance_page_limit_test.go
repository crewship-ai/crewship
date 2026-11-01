//go:build !clionly

package main

import (
	"encoding/json"
	"fmt"
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

// A real CLI process against a real router: the page limit defaults to 100, an
// instance administrator sets it (bounds enforced), a non-admin is refused, and
// a workspace at the limit gets the pages_limit refusal from `page create`
// while lowering the limit deletes nothing.
func TestAcceptance_AdminInstancePageLimit(t *testing.T) {
	t.Setenv(backup.InstanceOwnerEmailEnv, "")
	db := testutil.MigratedDB(t).DB
	admin, member := "crewship_cli_pagelimitadmin00000000", "crewship_cli_pagelimitmember000000"
	for _, q := range []string{
		`INSERT INTO workspaces(id,name,slug,created_at) VALUES('pl-a','Alpha','alpha','2026-01-01 00:00:00'),('pl-b','Beta','beta','2026-06-01 00:00:00')`,
		`INSERT INTO users(id,email,full_name) VALUES('pl-boss','boss@alpha.invalid','Boss'),('pl-eve','eve@beta.invalid','Eve')`,
		`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('pl-m1','pl-a','pl-boss','OWNER'),('pl-m2','pl-b','pl-eve','OWNER')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%v\n%s", err, q)
		}
	}
	for id, tok := range map[string]string{"pl-boss": admin, "pl-eve": member} {
		if _, err := db.Exec(`INSERT INTO cli_tokens(id,user_id,name,token_hash,created_at) VALUES(?,?,'test',?,datetime('now'))`, "tok-"+id, id, sha256HexToken(tok)); err != nil {
			t.Fatal(err)
		}
	}
	router, err := api.NewRouter(db, "this-is-a-32-char-test-secret-pad", slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(router)
	defer srv.Close()
	binary := buildCrewshipBinary(t)
	run := func(token string, args ...string) (string, error) {
		cfg := filepath.Join(t.TempDir(), "cli.yaml")
		if err := os.WriteFile(cfg, []byte("server: "+srv.URL+"\ntoken: "+token+"\nformat: table\n"), 0600); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(binary, args...)
		cmd.Env = append(os.Environ(), "CREWSHIP_CONFIG="+cfg, "CREWSHIP_SERVER=", "CREWSHIP_PROFILE=", "CREWSHIP_TOKEN=", "CREWSHIP_WORKSPACE=", "NO_COLOR=1", "DATABASE_URL=")
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	show := func() adminPageLimit {
		t.Helper()
		raw, err := run(admin, "admin", "instance", "page-limit", "get", "--format", "json")
		if err != nil {
			t.Fatalf("get: %v\n%s", err, raw)
		}
		var got adminPageLimit
		if err := json.Unmarshal([]byte(raw), &got); err != nil {
			t.Fatalf("json: %v\n%s", err, raw)
		}
		return got
	}

	if got := show(); got.MaxPagesPerWorkspace != 100 || got.Default != 100 || got.Min != 10 || got.Max != 10000 {
		t.Fatalf("defaults = %+v", got)
	}
	// Bare command shows too.
	if out, err := run(admin, "admin", "instance", "page-limit"); err != nil || !strings.Contains(out, "Pages per workspace: 100") {
		t.Fatalf("show: %v\n%s", err, out)
	}
	// Not an instance administrator: refused, nothing written.
	for _, args := range [][]string{{"page-limit"}, {"page-limit", "set", "500"}} {
		if out, err := run(member, append([]string{"admin", "instance"}, args...)...); err == nil {
			t.Fatalf("non-admin %v succeeded:\n%s", args, out)
		}
	}
	// Invalid values are refused (locally or by the server).
	for _, v := range []string{"abc", "9", "0", "-4", "10001", "12.5"} {
		if out, err := run(admin, "admin", "instance", "page-limit", "set", v); err == nil {
			t.Fatalf("set %s accepted:\n%s", v, out)
		}
	}
	if run1, err := run(admin, "admin", "instance", "page-limit", "set"); err == nil {
		t.Fatalf("set without a value accepted:\n%s", run1)
	}
	if got := show(); got.MaxPagesPerWorkspace != 100 {
		t.Fatalf("a refused set wrote: %+v", got)
	}

	if out, err := run(admin, "admin", "instance", "page-limit", "set", "12"); err != nil || !strings.Contains(out, "Pages per workspace: 12") {
		t.Fatalf("set: %v\n%s", err, out)
	}
	if got := show(); got.MaxPagesPerWorkspace != 12 {
		t.Fatalf("after set = %+v", got)
	}
	var audited int
	if err := db.QueryRow(`SELECT count(*) FROM instance_audit_logs WHERE action='instance.page_limit_set'`).Scan(&audited); err != nil || audited != 1 {
		t.Fatalf("audit rows %d %v", audited, err)
	}

	// The workspace holds 12 pages: the next create is refused with the page
	// limit, not a storage message.
	for i := 0; i < 12; i++ {
		if _, err := db.Exec(`INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json,created_at,updated_at) VALUES(?,?,?,?,?,'{}','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`, fmt.Sprintf("pl-p%d", i), "pl-a", fmt.Sprintf("p%d", i), "P", "pl-boss"); err != nil {
			t.Fatal(err)
		}
	}
	pageFile := filepath.Join(t.TempDir(), "page.yaml")
	if err := os.WriteFile(pageFile, []byte("apiVersion: crewship/v1\nkind: Page\nmetadata:\n  slug: extra\n  name: Extra\nspec:\n  panels: []\n"), 0600); err != nil {
		t.Fatal(err)
	}
	out, err := run(admin, "page", "create", "--workspace", "alpha", "--allow-empty", "--file", pageFile)
	if err == nil || !strings.Contains(out, "reached its page limit") || !strings.Contains(out, "limit is 12") {
		t.Fatalf("create at the limit: %v\n%s", err, out)
	}
	// Lowering the limit below the count deletes nothing.
	if _, err := run(admin, "admin", "instance", "page-limit", "set", "10"); err != nil {
		t.Fatal(err)
	}
	var pagesLeft int
	_ = db.QueryRow(`SELECT count(*) FROM pages WHERE workspace_id='pl-a'`).Scan(&pagesLeft)
	if pagesLeft != 12 {
		t.Fatalf("lowering the limit changed pages: %d", pagesLeft)
	}
	// Raising it lets creation through.
	if _, err := run(admin, "admin", "instance", "page-limit", "set", "13"); err != nil {
		t.Fatal(err)
	}
	if out, err := run(admin, "page", "create", "--workspace", "alpha", "--allow-empty", "--file", pageFile); err != nil {
		t.Fatalf("create under the raised limit: %v\n%s", err, out)
	}
}
