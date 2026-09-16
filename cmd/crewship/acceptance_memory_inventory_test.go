package main

// acceptance_memory_inventory_test.go — `crewship memory inventory` against
// a real server (#2579).
//
// A real CLI process → the real router → a migrated SQLite and a real
// storage root with knowledge files in it. The route's own contract (bounds,
// symlink refusal, personal-file exclusion) is proved in
// internal/api/memory_inventory_test.go; this file proves what only the
// binary can — that --agent and --crew resolve a slug to the right route,
// that the table and the machine format carry the server's documents and
// per-scope states, and that a miss and a mis-typed flag pair exit non-zero.

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

func TestAcceptance_MemoryInventory(t *testing.T) {
	db := testutil.MigratedDB(t).DB
	const ws = "cmeminvworkspace00001"
	const token = "crewship_cli_meminventory000000000000000"
	for _, q := range []string{
		`INSERT INTO workspaces(id,name,slug) VALUES('cmeminvworkspace00001','Inventory','inventory-cli')`,
		`INSERT INTO crews(id,workspace_id,name,slug,network_mode) VALUES('meminv-crew','cmeminvworkspace00001','Backend','backend','free')`,
		`INSERT INTO users(id,email,full_name) VALUES('meminv-owner','owner@example.invalid','Owner')`,
		`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('meminv-m1','cmeminvworkspace00001','meminv-owner','OWNER')`,
		`INSERT INTO agents(id,crew_id,workspace_id,name,slug) VALUES('cmeminvagent0000000000001','meminv-crew','cmeminvworkspace00001','Martin','martin')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO cli_tokens(id,user_id,name,token_hash,created_at) VALUES('meminv-token','meminv-owner','test',?,datetime('now'))`,
		sha256HexToken(token)); err != nil {
		t.Fatal(err)
	}

	// The storage layout the server reads (internal/api/agent_persona.go):
	// crews/<crewID>/agents/<slug>/.memory and crews/<crewID>/shared/.memory,
	// plus <workspaceRoot>/<workspaceID> for workspace-wide notes.
	storageRoot := t.TempDir()
	workspaceRoot := t.TempDir()
	agentDir := filepath.Join(storageRoot, "crews", "meminv-crew", "agents", "martin", ".memory")
	crewDir := filepath.Join(storageRoot, "crews", "meminv-crew", "shared", ".memory")
	wsDir := filepath.Join(workspaceRoot, ws)
	for path, body := range map[string]string{
		filepath.Join(agentDir, "AGENT.md"):               "# Martin\nRemembers the deploy runbook.\n",
		filepath.Join(agentDir, "daily", "2026-09-15.md"): "stood up at 09:00\n",
		filepath.Join(agentDir, "users", "owner.md"):      "PRIVATE — must never be listed\n",
		filepath.Join(crewDir, "CREW.md"):                 "# Backend\nShared conventions.\n",
		filepath.Join(wsDir, "handbook.md"):               "workspace-wide handbook\n",
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	router, err := api.NewRouter(db, "this-is-a-32-char-test-secret-pad", slog.Default(),
		api.WithOutputBasePath(storageRoot), api.WithMemoryInventoryRoot(workspaceRoot))
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(router)
	defer srv.Close()

	binary := buildCrewshipBinary(t)
	configFor := func(format string) string {
		cfg := filepath.Join(t.TempDir(), "cli.yaml")
		if err := os.WriteFile(cfg, []byte("server: "+srv.URL+"\nworkspace: "+ws+"\ntoken: "+token+"\nformat: "+format+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		return cfg
	}
	run := func(cfg string, args ...string) (string, error) {
		cmd := exec.Command(binary, args...)
		cmd.Env = append(os.Environ(), "CREWSHIP_CONFIG="+cfg, "CREWSHIP_SERVER=", "CREWSHIP_PROFILE=", "CREWSHIP_TOKEN=", "CREWSHIP_WORKSPACE=", "NO_COLOR=1")
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	jsonCfg, tableCfg, quietCfg := configFor("json"), configFor("table"), configFor("quiet")
	must := func(cfg string, args ...string) string {
		out, err := run(cfg, args...)
		if err != nil {
			t.Fatalf("CLI %v: %v\n%s", args, err, out)
		}
		return out
	}
	decode := func(out string) memoryInventory {
		var inv memoryInventory
		if err := json.Unmarshal([]byte(out), &inv); err != nil {
			t.Fatalf("memory inventory did not print the server's JSON: %v\n%s", err, out)
		}
		return inv
	}
	docIDs := func(inv memoryInventory) []string {
		ids := make([]string, 0, len(inv.Documents))
		for _, d := range inv.Documents {
			ids = append(ids, d.ID)
		}
		return ids
	}

	// ── --agent by slug: agent + crew + workspace scopes ───────────────────
	agentInv := decode(must(jsonCfg, "memory", "inventory", "--agent", "martin"))
	if agentInv.Source != "current_files" {
		t.Errorf("source = %q, want current_files", agentInv.Source)
	}
	wantScopes := map[string]string{"agent": "available", "crew": "available", "workspace": "available"}
	for scope, state := range wantScopes {
		if agentInv.Scopes[scope] != state {
			t.Errorf("scopes[%s] = %q, want %q (%v)", scope, agentInv.Scopes[scope], state, agentInv.Scopes)
		}
	}
	wantIDs := "agent:AGENT.md,agent:daily/2026-09-15.md,crew:CREW.md,workspace:handbook.md"
	if got := strings.Join(docIDs(agentInv), ","); got != wantIDs {
		t.Fatalf("documents = %s, want %s", got, wantIDs)
	}
	for _, d := range agentInv.Documents {
		if d.State != "available" || d.Bytes == nil || *d.Bytes == 0 || d.Content == "" || d.Revision == "" || d.UpdatedAt == "" {
			t.Errorf("document %s is not a readable document: %+v", d.ID, d)
		}
		if d.Scope != "workspace" && d.HistoryPath == "" {
			t.Errorf("document %s has no history_path for `memory versions list`", d.ID)
		}
		if strings.Contains(d.Content, "PRIVATE") {
			t.Errorf("personal file leaked into %s", d.ID)
		}
	}
	if agentInv.Documents[0].Content != "# Martin\nRemembers the deploy runbook.\n" {
		t.Errorf("AGENT.md content was not carried verbatim: %q", agentInv.Documents[0].Content)
	}
	if agentInv.Documents[2].HistoryPath != "crew:meminv-crew/CREW.md" {
		t.Errorf("crew history_path = %q, want the crew ID form", agentInv.Documents[2].HistoryPath)
	}

	// ── --agent by id resolves the same route ──────────────────────────────
	if got := docIDs(decode(must(jsonCfg, "memory", "inventory", "--agent", "cmeminvagent0000000000001"))); strings.Join(got, ",") != wantIDs {
		t.Errorf("by id: documents = %v", got)
	}

	// ── --crew: shared + workspace scopes, no agent scope ──────────────────
	crewInv := decode(must(jsonCfg, "memory", "inventory", "--crew", "backend"))
	if _, has := crewInv.Scopes["agent"]; has {
		t.Errorf("crew inventory reports an agent scope: %v", crewInv.Scopes)
	}
	if crewInv.Scopes["crew"] != "available" || crewInv.Scopes["workspace"] != "available" {
		t.Errorf("crew scopes = %v", crewInv.Scopes)
	}
	if got := strings.Join(docIDs(crewInv), ","); got != "crew:CREW.md,workspace:handbook.md" {
		t.Errorf("crew documents = %s", got)
	}

	// ── the table: one row per document, scope line above it ───────────────
	table := must(tableCfg, "memory", "inventory", "--agent", "martin")
	for _, cell := range []string{"Scopes: agent=available crew=available workspace=available", "ID", "STATE", "BYTES", "UPDATED", "REVISION",
		"agent:AGENT.md", "agent:daily/2026-09-15.md", "crew:CREW.md", "workspace:handbook.md", "available"} {
		if !strings.Contains(table, cell) {
			t.Errorf("the table does not show %q:\n%s", cell, table)
		}
	}
	if strings.Contains(table, "Remembers the deploy runbook") {
		t.Errorf("the table prints document content:\n%s", table)
	}

	// ── quiet: bare document ids, nothing else ─────────────────────────────
	if got := strings.TrimSpace(must(quietCfg, "memory", "inventory", "--crew", "backend")); got != "crew:CREW.md\nworkspace:handbook.md" {
		t.Errorf("quiet output = %q", got)
	}

	// ── an empty crew is "empty", not an error ─────────────────────────────
	if _, err := db.Exec(`INSERT INTO crews(id,workspace_id,name,slug,network_mode) VALUES('meminv-crew2','cmeminvworkspace00001','Lookout','lookout','free')`); err != nil {
		t.Fatal(err)
	}
	emptyInv := decode(must(jsonCfg, "memory", "inventory", "--crew", "lookout"))
	if emptyInv.Scopes["crew"] != "empty" || len(emptyInv.Documents) != 1 || emptyInv.Documents[0].ID != "workspace:handbook.md" {
		t.Errorf("empty crew: scopes=%v documents=%v", emptyInv.Scopes, docIDs(emptyInv))
	}
	emptyTable := must(tableCfg, "memory", "inventory", "--crew", "lookout")
	if !strings.Contains(emptyTable, "Scopes: crew=empty workspace=available") {
		t.Errorf("the empty crew's scope line is missing:\n%s", emptyTable)
	}

	// ── refusals exit non-zero with a sentence, not a stack ────────────────
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"neither flag", []string{"memory", "inventory"}, "exactly one of --agent and --crew"},
		{"both flags", []string{"memory", "inventory", "--agent", "martin", "--crew", "backend"}, "exactly one of --agent and --crew"},
		{"unknown agent", []string{"memory", "inventory", "--agent", "nobody"}, "agent not found"},
		{"unknown crew", []string{"memory", "inventory", "--crew", "nowhere"}, "crew not found"},
	} {
		out, err := run(tableCfg, tc.args...)
		if err == nil {
			t.Errorf("%s: exit 0\n%s", tc.name, out)
			continue
		}
		if !strings.Contains(out, tc.want) {
			t.Errorf("%s: output lacks %q:\n%s", tc.name, tc.want, out)
		}
	}
}
