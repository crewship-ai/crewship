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
	"github.com/crewship-ai/crewship/internal/testutil"
)

// A real CLI process against a real router on a migrated SQLite: the page's
// icon and colour as an operator sets them (#2563). Set on create, read back
// from the document and the index, changed without a document, cleared with
// "", and refused by name for a value outside the crew registry.
func TestAcceptance_PageAvatar(t *testing.T) {
	db := testutil.MigratedDB(t).DB
	const ws = "cavataracceptancews0"
	token := "crewship_cli_pagesavatar0000000000000000"
	for _, q := range []string{
		`INSERT INTO workspaces(id,name,slug) VALUES('cavataracceptancews0','Avatar','avatar-cli')`,
		`INSERT INTO crews(id,workspace_id,name,slug,network_mode) VALUES('avatar-cli-crew','cavataracceptancews0','Operations','ops','free')`,
		`INSERT INTO users(id,email,full_name) VALUES('avatar-cli-owner','avatar@example.invalid','Avatar Owner')`,
		`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('avatar-cli-member','cavataracceptancews0','avatar-cli-owner','OWNER')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO cli_tokens(id,user_id,name,token_hash,created_at) VALUES('avatar-cli-token','avatar-cli-owner','test',?,datetime('now'))`, sha256HexToken(token)); err != nil {
		t.Fatal(err)
	}
	router, err := api.NewRouter(db, "this-is-a-32-char-test-secret-pad", slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(router)
	defer srv.Close()
	cfg := filepath.Join(t.TempDir(), "cli.yaml")
	if err := os.WriteFile(cfg, []byte("server: "+srv.URL+"\nworkspace: "+ws+"\ntoken: "+token+"\nformat: json\n"), 0600); err != nil {
		t.Fatal(err)
	}
	binary := buildCrewshipBinary(t)
	run := func(args ...string) (string, error) {
		cmd := exec.Command(binary, args...)
		cmd.Env = append(os.Environ(), "CREWSHIP_CONFIG="+cfg, "CREWSHIP_SERVER=", "CREWSHIP_PROFILE=", "CREWSHIP_TOKEN=", "CREWSHIP_WORKSPACE=", "NO_COLOR=1")
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	must := func(args ...string) string {
		out, err := run(args...)
		if err != nil {
			t.Fatalf("CLI %v: %v\n%s", args, err, out)
		}
		return out
	}
	pageFile := filepath.Join(t.TempDir(), "page.yaml")
	pageYAML := "apiVersion: crewship/v1\nkind: Page\nmetadata:\n  slug: health\n  name: Health\nspec:\n  panels:\n    - id: mysql\n      schema: status.v1\n      owner: crew/ops\n      producer: script/check-mysql.sh\n      sla: 30s\n"
	if err := os.WriteFile(pageFile, []byte(pageYAML), 0600); err != nil {
		t.Fatal(err)
	}

	type avatar struct {
		Name, Icon, Color string
		Panels            []json.RawMessage
	}
	get := func() avatar {
		var page avatar
		if err := json.Unmarshal([]byte(must("page", "get", "health")), &page); err != nil {
			t.Fatal(err)
		}
		return page
	}

	// Set on create.
	var created avatar
	if err := json.Unmarshal([]byte(must("page", "create", "--file", pageFile, "--icon", "rocket", "--color", "amber")), &created); err != nil {
		t.Fatal(err)
	}
	if created.Icon != "rocket" || created.Color != "amber" {
		t.Fatalf("create echoed %+v, want rocket/amber", created)
	}
	// The index row carries it — that is what the rail draws from.
	var rows []avatar
	if err := json.Unmarshal([]byte(must("page", "list")), &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Icon != "rocket" || rows[0].Color != "amber" {
		t.Fatalf("page list rows: %+v", rows)
	}

	// Changed without a document: the name and the panel survive untouched.
	must("page", "update", "health", "--color", "cyan")
	if got := get(); got.Icon != "rocket" || got.Color != "cyan" || got.Name != "Health" || len(got.Panels) != 1 {
		t.Fatalf("after --color cyan alone: %+v", got)
	}
	// Cleared with "".
	must("page", "update", "health", "--icon", "")
	if got := get(); got.Icon != "" || got.Color != "cyan" {
		t.Fatalf("after --icon \"\": %+v", got)
	}
	// A document plus the flags sends both.
	must("page", "update", "health", "--file", pageFile, "--icon", "chart")
	if got := get(); got.Icon != "chart" || got.Color != "cyan" {
		t.Fatalf("after --file with --icon chart: %+v", got)
	}

	// Refused by name, and the refusal says what the set is.
	if out, err := run("page", "update", "health", "--icon", "memory"); err == nil || !strings.Contains(out, "crew icon") {
		t.Fatalf("a panel icon was accepted as a page icon: %v %s", err, out)
	}
	if out, err := run("page", "update", "health", "--color", "#ff0000"); err == nil || !strings.Contains(out, "crew palette") {
		t.Fatalf("a hex was accepted as a page colour: %v %s", err, out)
	}
	if got := get(); got.Icon != "chart" || got.Color != "cyan" {
		t.Fatalf("a refused update changed the avatar: %+v", got)
	}
	// Neither a document nor a flag is not an update.
	if out, err := run("page", "update", "health"); err == nil || !strings.Contains(out, "--icon or --color") {
		t.Fatalf("update with nothing to send: %v %s", err, out)
	}
}
