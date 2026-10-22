package main

import (
	"context"
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
	"github.com/crewship-ai/crewship/internal/appengine"
	"github.com/crewship-ai/crewship/internal/testutil"
)

type binaryWorkbookEngine struct{}

func (binaryWorkbookEngine) Identity() string { return "binary-fixture@1" }
func (binaryWorkbookEngine) Prepare(_ context.Context, b appengine.Binding, _ appengine.State, c appengine.Command) (appengine.Candidate, error) {
	return appengine.NewCandidate(b, c.Snapshot, []string{"*"}), nil
}
func TestWorkbookHumanBinary(t *testing.T) {
	db := testutil.MigratedDB(t).DB
	db.SetMaxOpenConns(1)
	for _, q := range []string{
		`PRAGMA synchronous=FULL`,
		`INSERT INTO users(id,email) VALUES('workbook-human','workbook-human@example.test')`,
		`INSERT INTO workspaces(id,name,slug) VALUES('cworkbookworkspace000000001','Workbook','workbook')`,
		`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('workbook-member','cworkbookworkspace000000001','workbook-human','OWNER')`,
		`INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,private_audience,spec_json) VALUES('workbook-binary-page','cworkbookworkspace000000001','binary-workbook','Workbook','workbook-human',1,'{}')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	const token = "crewship_cli_workbook_synthetic_token"
	if _, err := db.Exec(`INSERT INTO cli_tokens(id,user_id,name,token_hash,tier) VALUES('workbook-token','workbook-human','human',?,'STANDARD')`, sha256HexToken(token)); err != nil {
		t.Fatal(err)
	}
	router, err := api.NewRouter(db, strings.Repeat("workbook-secret-", 3), slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(router.Shutdown)
	router.RegisterGridCraft(appengine.Options{Enabled: true}, binaryWorkbookEngine{})
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	binary := buildCrewshipBinary(t)
	cfg := filepath.Join(t.TempDir(), "cli.yaml")
	if err = os.WriteFile(cfg, []byte(fmt.Sprintf("server: %s\ntoken: %s\nworkspace: cworkbookworkspace000000001\nformat: json\n", server.URL, token)), 0600); err != nil {
		t.Fatal(err)
	}
	run := func(input string, args ...string) []byte {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), binary, args...)
		cmd.Stdin = strings.NewReader(input)
		for _, v := range os.Environ() {
			if !strings.HasPrefix(v, "CREWSHIP_") {
				cmd.Env = append(cmd.Env, v)
			}
		}
		cmd.Env = append(cmd.Env, "CREWSHIP_CONFIG="+cfg, "CREWSHIP_NO_SLUG_CACHE=1", "NO_COLOR=1")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("CLI %v: %v %s", args, err, out)
		}
		return out
	}
	body := `{"mutation_id":"binary-import","mandate_id":"human-import","base_rev":0,"generation":0,"command":{"kind":"import","snapshot":{"sheets":[{}]}}}`
	first := run(body, "workbook", "import", "@workbook-binary-page", "--file", "-")
	retry := run(body, "workbook", "import", "@workbook-binary-page", "--file", "-")
	if string(first) != string(retry) {
		t.Fatalf("receipt changed: %s %s", first, retry)
	}
	var receipt appengine.Receipt
	if json.Unmarshal(retry, &receipt) != nil || receipt.Rev != 1 {
		t.Fatalf("receipt %s", retry)
	}
	run("", "workbook", "read", "@workbook-binary-page")
	run("", "workbook", "status", "@workbook-binary-page", "binary-import")
	// Seed the existing signed-run authority substrate, not a workbook ACL.
	for _, q := range []string{
		`INSERT INTO crews(id,workspace_id,name,slug) VALUES('workbook-crew','cworkbookworkspace000000001','Workbook crew','workbook-crew')`,
		`INSERT INTO agents(id,workspace_id,crew_id,name,slug,agent_role,status,cli_adapter,tool_profile,timeout_seconds,memory_enabled) VALUES('workbook-agent','cworkbookworkspace000000001','workbook-crew','Agent','workbook-agent','AGENT','IDLE','CLAUDE_CODE','CODING',1800,0)`,
		`INSERT INTO chats(id,workspace_id,agent_id,created_by,visibility) VALUES('workbook-chat','cworkbookworkspace000000001','workbook-agent','workbook-human','private')`,
		`INSERT INTO conversation_messages(id,session_id,agent_id,role,content,ts,author_user_id) VALUES('workbook-message','workbook-chat','workbook-agent','user','Set B2 to 42','2026-01-01T00:00:00Z','workbook-human')`,
		`INSERT INTO artifact_run_principals(run_id,workspace_id,agent_id,crew_id,human_user_id,chat_id,message_id,member_id,member_revision,chat_generation,chat_revision,expires_at) SELECT 'workbook-run',m.workspace_id,'workbook-agent','workbook-crew',m.user_id,c.id,'workbook-message',m.id,m.access_revision,c.authority_generation,c.authority_revision,'2999-01-01T00:00:00Z' FROM workspace_members m JOIN chats c ON c.workspace_id=m.workspace_id AND c.created_by=m.user_id WHERE m.id='workbook-member' AND c.id='workbook-chat'`,
		`INSERT INTO journal_entries(id,workspace_id,crew_id,agent_id,entry_type,actor_type,summary,trace_id) VALUES('workbook-start','cworkbookworkspace000000001','workbook-crew','workbook-agent','run.started','system','test','workbook-run')`,
		`INSERT INTO workspace_artifact_creation_policy(workspace_id,enabled,version,updated_at) VALUES('cworkbookworkspace000000001',1,1,'2026-01-01T00:00:00Z')`,
		`INSERT INTO workspace_artifact_creation_crews(workspace_id,crew_id) VALUES('cworkbookworkspace000000001','workbook-crew')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	run(`{"mandate_id":"binary-mandate","run_id":"workbook-run","base_rev":1,"generation":1,"targets":["Sheet1!B2"]}`, "workbook", "admit", "@workbook-binary-page", "--file", "-")
	run("", "workbook", "revoke", "@workbook-binary-page", "binary-mandate")
}
