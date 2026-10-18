package main

import (
	"bytes"
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
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/testutil"
)

// The actual CLI stages a private human Page through the authenticated Router
// and migrated SQLite. This does not require a document engine or Doc routes.
func TestAcceptance_EmptyHumanPageBinary(t *testing.T) {
	db := testutil.MigratedDB(t).DB
	const workspace = "cemptypageworkspace000001"
	const token = "crewship_cli_empty_human_page_fixture"
	for _, query := range []string{
		`INSERT INTO users(id,email) VALUES('empty-page-owner','empty-page@example.invalid')`,
		`INSERT INTO workspaces(id,name,slug) VALUES('cemptypageworkspace000001','Empty Pages','empty-pages')`,
		`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('empty-page-member','cemptypageworkspace000001','empty-page-owner','OWNER')`,
		`INSERT INTO crews(id,workspace_id,name,slug) VALUES('empty-page-crew','cemptypageworkspace000001','Lookout','lookout')`,
	} {
		if _, err := db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO cli_tokens(id,user_id,name,token_hash,tier) VALUES('empty-page-token','empty-page-owner','test',?,'STANDARD')`, sha256HexToken(token)); err != nil {
		t.Fatal(err)
	}
	audit := journal.NewWriter(db, slog.Default(), journal.WriterOptions{ChainKey: bytes.Repeat([]byte{3}, 32)})
	t.Cleanup(func() { _ = audit.Close() })
	router, err := api.NewRouter(db, "empty-human-page-test-secret-32-chars", slog.Default(), api.WithJournal(audit))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(router.Shutdown)
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	config := filepath.Join(t.TempDir(), "cli.yaml")
	if err := os.WriteFile(config, []byte(fmt.Sprintf("server: %s\ntoken: %s\nworkspace: %s\nformat: json\n", server.URL, token, workspace)), 0600); err != nil {
		t.Fatal(err)
	}
	binary := buildCrewshipBinary(t)
	run := func(input string, args ...string) ([]byte, error) {
		t.Helper()
		command := exec.CommandContext(t.Context(), binary, args...)
		command.Stdin = strings.NewReader(input)
		for _, value := range os.Environ() {
			if !strings.HasPrefix(value, "CREWSHIP_") {
				command.Env = append(command.Env, value)
			}
		}
		command.Env = append(command.Env, "CREWSHIP_CONFIG="+config, "CREWSHIP_NO_SLUG_CACHE=1", "NO_COLOR=1")
		return command.CombinedOutput()
	}
	// Existing panel authoring remains usable with the same credential and Router.
	normalSpec := strings.ReplaceAll(pageAcceptSpecYAML, pageAcceptSlug, "normal-panel-page")
	normal, err := run(normalSpec, "page", "create", "--file", "-")
	if err != nil {
		t.Fatalf("legacy Page create: %v %s", err, normal)
	}
	var normalPanels int
	if err := db.QueryRow(`SELECT COUNT(*) FROM page_panels JOIN pages ON pages.id=page_panels.page_id WHERE pages.workspace_id=? AND pages.slug='normal-panel-page'`, workspace).Scan(&normalPanels); err != nil || normalPanels != 1 {
		t.Fatalf("legacy panels=%d %v", normalPanels, err)
	}
	emptySpec := func(slug string) string {
		return fmt.Sprintf("apiVersion: crewship/v1\nkind: Page\nmetadata:\n  name: Private staging title\n  slug: %s\nspec:\n  panels: []\n", slug)
	}
	for _, tc := range []struct {
		name, slug, input string
		flags             []string
	}{
		{name: "strict default", slug: "empty-default-denied", input: emptySpec("empty-default-denied")},
		{name: "crew owner", slug: "empty-crew-denied", input: emptySpec("empty-crew-denied"), flags: []string{"--allow-empty", "--owner", "crew/lookout"}},
		{name: "panels", slug: "empty-panels-denied", input: strings.ReplaceAll(pageAcceptSpecYAML, pageAcceptSlug, "empty-panels-denied"), flags: []string{"--allow-empty"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := run(tc.input, append([]string{"page", "create", "--file", "-"}, tc.flags...)...)
			if err == nil {
				t.Fatalf("invalid staging accepted: %s", out)
			}
			var count int
			if err := db.QueryRow(`SELECT COUNT(*) FROM pages WHERE workspace_id=? AND slug=?`, workspace, tc.slug).Scan(&count); err != nil || count != 0 {
				t.Fatalf("invalid request persisted Page: count=%d %v", count, err)
			}
		})
	}
	created, err := run(emptySpec("empty-human-private"), "page", "create", "--allow-empty", "--file", "-")
	if err != nil {
		t.Fatalf("explicit empty human Page create: %v %s", err, created)
	}
	var page struct {
		ID, Owner string
		Panels    []any
	}
	if json.Unmarshal(created, &page) != nil || page.ID == "" || page.Owner != "user/empty-page-owner" || len(page.Panels) != 0 {
		t.Fatalf("empty human Page wire: %s", created)
	}
	var owner, crew string
	var panels int
	if err := db.QueryRow(`SELECT owner_user_id,COALESCE(owner_crew_id,''),(SELECT COUNT(*) FROM page_panels WHERE page_id=pages.id) FROM pages WHERE id=? AND workspace_id=?`, page.ID, workspace).Scan(&owner, &crew, &panels); err != nil || owner != "empty-page-owner" || crew != "" || panels != 0 {
		t.Fatalf("persisted owner=%q crew=%q panels=%d %v", owner, crew, panels, err)
	}
	if err := audit.Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	var summary, payload string
	if err := db.QueryRow(`SELECT summary,payload FROM journal_entries WHERE workspace_id=? AND entry_type=? AND json_extract(payload,'$.page_id')=?`, workspace, string(journal.EntryPageSpecChanged), page.ID).Scan(&summary, &payload); err != nil {
		t.Fatal(err)
	}
	var opaque map[string]any
	if json.Unmarshal([]byte(payload), &opaque) != nil || len(opaque) != 2 || opaque["page_id"] != page.ID || opaque["created"] != true || summary != "Page created" {
		t.Fatalf("staging audit must contain only opaque identity and creation flag: %s %s", summary, payload)
	}
	if strings.Contains(summary+payload, "empty-human-private") || strings.Contains(summary+payload, "Private staging title") {
		t.Fatalf("staging audit disclosed private metadata: %s %s", summary, payload)
	}
	var total int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pages WHERE workspace_id=?`, workspace).Scan(&total); err != nil || total != 2 {
		t.Fatalf("unexpected created Pages=%d %v", total, err)
	}
}
