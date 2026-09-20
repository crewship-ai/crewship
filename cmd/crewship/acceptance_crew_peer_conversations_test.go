package main

// acceptance_crew_peer_conversations_test.go — `crewship crew
// peer-conversations --agent` against a real server (#2579).
//
// GET /api/v1/crews/{crewId}/peer-conversations filters on `agent_id` before
// the page is cut (#2458, internal/api/peer_conversation_filter_test.go);
// the CLI had only --limit, so the filter was unreachable from a terminal.
// This drives the built binary: a slug resolves to the id the handler
// matches on either side of the conversation, and the page is the filtered
// page, not a filtered copy of an unfiltered one.

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

func TestAcceptance_CrewPeerConversationsAgentFilter(t *testing.T) {
	db := testutil.MigratedDB(t).DB
	const ws = "cpeerconvworkspace0001"
	const token = "crewship_cli_peerconvs00000000000000000"
	for _, q := range []string{
		`INSERT INTO workspaces(id,name,slug) VALUES('cpeerconvworkspace0001','Peers','peers-cli')`,
		`INSERT INTO crews(id,workspace_id,name,slug,network_mode) VALUES('cpeerconvcrew00000000001','cpeerconvworkspace0001','Backend','backend','free')`,
		`INSERT INTO users(id,email,full_name) VALUES('peerconv-owner','owner@example.invalid','Owner')`,
		`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('peerconv-m1','cpeerconvworkspace0001','peerconv-owner','OWNER')`,
		`INSERT INTO agents(id,crew_id,workspace_id,name,slug) VALUES('cpeerconvagenta000000001','cpeerconvcrew00000000001','cpeerconvworkspace0001','Ava','ava')`,
		`INSERT INTO agents(id,crew_id,workspace_id,name,slug) VALUES('cpeerconvagentb000000001','cpeerconvcrew00000000001','cpeerconvworkspace0001','Ben','ben')`,
		`INSERT INTO agents(id,crew_id,workspace_id,name,slug) VALUES('cpeerconvagentc000000001','cpeerconvcrew00000000001','cpeerconvworkspace0001','Cy','cy')`,
		`INSERT INTO chats(id,agent_id,workspace_id) VALUES('peerconv-chat','cpeerconvagenta000000001','cpeerconvworkspace0001')`,
		// Newest first is the route's order; ids name the pair.
		`INSERT INTO peer_conversations(id,workspace_id,crew_id,chat_id,from_agent_id,to_agent_id,question,status,created_at)
		 VALUES('pc-ava-ben','cpeerconvworkspace0001','cpeerconvcrew00000000001','peerconv-chat','cpeerconvagenta000000001','cpeerconvagentb000000001','ava asks ben','COMPLETED','2026-09-15T10:00:01Z')`,
		`INSERT INTO peer_conversations(id,workspace_id,crew_id,chat_id,from_agent_id,to_agent_id,question,status,created_at)
		 VALUES('pc-ben-cy','cpeerconvworkspace0001','cpeerconvcrew00000000001','peerconv-chat','cpeerconvagentb000000001','cpeerconvagentc000000001','ben asks cy','COMPLETED','2026-09-15T10:00:02Z')`,
		`INSERT INTO peer_conversations(id,workspace_id,crew_id,chat_id,from_agent_id,to_agent_id,question,status,created_at)
		 VALUES('pc-cy-ava','cpeerconvworkspace0001','cpeerconvcrew00000000001','peerconv-chat','cpeerconvagentc000000001','cpeerconvagenta000000001','cy asks ava','COMPLETED','2026-09-15T10:00:03Z')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if _, err := db.Exec(`INSERT INTO cli_tokens(id,user_id,name,token_hash,created_at) VALUES('peerconv-token','peerconv-owner','test',?,datetime('now'))`,
		sha256HexToken(token)); err != nil {
		t.Fatal(err)
	}
	router, err := api.NewRouter(db, "this-is-a-32-char-test-secret-pad", slog.Default())
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
	jsonCfg, tableCfg := configFor("json"), configFor("table")
	ids := func(args ...string) string {
		t.Helper()
		out, err := run(jsonCfg, args...)
		if err != nil {
			t.Fatalf("CLI %v: %v\n%s", args, err, out)
		}
		var rows []struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal([]byte(out), &rows); err != nil {
			t.Fatalf("not the server's JSON: %v\n%s", err, out)
		}
		got := make([]string, 0, len(rows))
		for _, r := range rows {
			got = append(got, r.ID)
		}
		return strings.Join(got, ",")
	}

	if got := ids("crew", "peer-conversations", "backend"); got != "pc-cy-ava,pc-ben-cy,pc-ava-ben" {
		t.Errorf("unfiltered = %s", got)
	}
	// Either side of the conversation: ava asked once and was asked once.
	if got := ids("crew", "peer-conversations", "backend", "--agent", "ava"); got != "pc-cy-ava,pc-ava-ben" {
		t.Errorf("--agent ava = %s", got)
	}
	// ben is on one side of two conversations and neither side of the third
	// — by id, not slug, to prove the resolver's fast path.
	if got := ids("crew", "peer-conversations", "backend", "--agent", "cpeerconvagentb000000001"); got != "pc-ben-cy,pc-ava-ben" {
		t.Errorf("--agent <id> = %s", got)
	}
	// The filter runs before the page is cut: one row for ava is ava's newest
	// conversation, not the crew's newest row filtered down to nothing.
	if got := ids("crew", "peer-conversations", "backend", "--agent", "ava", "--limit", "1"); got != "pc-cy-ava" {
		t.Errorf("--agent ava --limit 1 = %s", got)
	}

	table, err := run(tableCfg, "crew", "peer-conversations", "backend", "--agent", "ben")
	if err != nil {
		t.Fatalf("table: %v\n%s", err, table)
	}
	for _, cell := range []string{"FROM", "TO", "ben asks cy", "ava asks ben"} {
		if !strings.Contains(table, cell) {
			t.Errorf("table lacks %q:\n%s", cell, table)
		}
	}
	if strings.Contains(table, "cy asks ava") {
		t.Errorf("table shows a conversation ben is not part of:\n%s", table)
	}

	out, err := run(tableCfg, "crew", "peer-conversations", "backend", "--agent", "nobody")
	if err == nil || !strings.Contains(out, "agent not found") {
		t.Errorf("unknown --agent: err=%v\n%s", err, out)
	}
}
