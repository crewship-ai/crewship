package main

import (
	"context"
	"encoding/json"
	"errors"
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
func (binaryWorkbookEngine) Prepare(_ context.Context, b appengine.Binding, s appengine.State, c appengine.Command) (appengine.Candidate, error) {
	if c.Kind == "cells.set" {
		var effects []string
		for _, cell := range c.Cells {
			effects = append(effects, cell.Sheet+"!"+cell.Cell)
		}
		return appengine.NewCandidate(b, s.Snapshot, effects), nil
	}
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
	runAs := func(config string, wantErr bool, input string, args ...string) []byte {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), binary, args...)
		cmd.Stdin = strings.NewReader(input)
		for _, v := range os.Environ() {
			if !strings.HasPrefix(v, "CREWSHIP_") {
				cmd.Env = append(cmd.Env, v)
			}
		}
		cmd.Env = append(cmd.Env, "CREWSHIP_CONFIG="+config, "CREWSHIP_NO_SLUG_CACHE=1", "NO_COLOR=1")
		out, err := cmd.CombinedOutput()
		if (err != nil) != wantErr {
			t.Fatalf("CLI %v: err=%v want error=%v %s", args, err, wantErr, out)
		}
		return out
	}
	run := func(input string, args ...string) []byte {
		t.Helper()
		return runAs(cfg, false, input, args...)
	}
	created := run("", "workbook", "create", "created-workbook", "--name", "Created workbook")
	createdRetry := run("", "workbook", "create", "created-workbook", "--name", "Created workbook")
	var page struct {
		PageID string `json:"page_id"`
	}
	if json.Unmarshal(createdRetry, &page) != nil || page.PageID == "" || !strings.Contains(string(created), page.PageID) {
		t.Fatalf("create %s retry %s", created, createdRetry)
	}
	run(`{"mutation_id":"created-import","mandate_id":"human-import","base_rev":0,"generation":0,"command":{"kind":"import","snapshot":{"sheets":[{}]}}}`, "workbook", "import", "created-workbook", "--file", "-")
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

	// Human cell edits from two client sessions of the owner.
	edit := `{"session_id":"w1","mutation_id":"w1:1","base_rev":1,"generation":1,"ops":[{"kind":"cell.set","sheet":"Sheet1","cell":"B5","input":"draft"}]}`
	edited := run(edit, "workbook", "edit", "@workbook-binary-page", "--file", "-")
	if retried := run(edit, "workbook", "edit", "@workbook-binary-page", "--file", "-"); string(retried) != string(edited) {
		t.Fatalf("edit receipt changed: %s %s", edited, retried)
	}
	if json.Unmarshal(edited, &receipt) != nil || receipt.Rev != 2 || receipt.OperationKey.MutationID != "w1:1" {
		t.Fatalf("edit receipt %s", edited)
	}
	cmd := exec.CommandContext(t.Context(), binary, "workbook", "edit", "@workbook-binary-page", "--file", "-")
	cmd.Stdin = strings.NewReader(`{"session_id":"w2","mutation_id":"w2:1","base_rev":1,"generation":1,"ops":[{"kind":"cell.set","sheet":"Sheet1","cell":"B5","input":"mine"}]}`)
	for _, v := range os.Environ() {
		if !strings.HasPrefix(v, "CREWSHIP_") {
			cmd.Env = append(cmd.Env, v)
		}
	}
	cmd.Env = append(cmd.Env, "CREWSHIP_CONFIG="+cfg, "CREWSHIP_NO_SLUG_CACHE=1", "NO_COLOR=1")
	var stdout strings.Builder
	cmd.Stdout = &stdout
	err = cmd.Run()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 5 || !strings.Contains(stdout.String(), `"conflicts"`) || !strings.Contains(stdout.String(), `"draft"`) {
		t.Fatalf("conflict exit=%v out=%s", err, stdout.String())
	}
	run(`{"session_id":"w2","mutation_id":"w2:2","base_rev":1,"generation":1,"ops":[{"kind":"cell.set","sheet":"Sheet1","cell":"C5","input":"other"}]}`, "workbook", "edit", "@workbook-binary-page", "--file", "-")
	run("", "workbook", "status", "@workbook-binary-page", "w2:2")

	// A second human editor: explicit publication, then the ordinary page revoke.
	const peerToken = "crewship_cli_workbook_synthetic_peer"
	for _, q := range []string{
		`INSERT INTO users(id,email) VALUES('workbook-peer','workbook-peer@example.test')`,
		`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('workbook-peer-member','cworkbookworkspace000000001','workbook-peer','MEMBER')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO cli_tokens(id,user_id,name,token_hash,tier) VALUES('workbook-peer-token','workbook-peer','peer',?,'STANDARD')`, sha256HexToken(peerToken)); err != nil {
		t.Fatal(err)
	}
	peerCfg := filepath.Join(t.TempDir(), "peer.yaml")
	if err = os.WriteFile(peerCfg, []byte(fmt.Sprintf("server: %s\ntoken: %s\nworkspace: cworkbookworkspace000000001\nformat: json\n", server.URL, peerToken)), 0600); err != nil {
		t.Fatal(err)
	}
	runAs(peerCfg, true, "", "workbook", "read", "@workbook-binary-page")
	if out := runAs(cfg, true, "", "workbook", "share", "@workbook-binary-page", "--editor", "workbook-peer@example.test"); !strings.Contains(string(out), "publication_required") {
		t.Fatalf("implicit publication: %s", out)
	}
	shared := run("", "workbook", "share", "@workbook-binary-page", "--editor", "workbook-peer@example.test", "--publish")
	var sharedOut struct {
		Published    bool   `json:"published"`
		EditorUserID string `json:"editor_user_id"`
	}
	if json.Unmarshal(shared, &sharedOut) != nil || !sharedOut.Published || sharedOut.EditorUserID != "workbook-peer" {
		t.Fatalf("share %s", shared)
	}
	runAs(peerCfg, false, "", "workbook", "read", "@workbook-binary-page")
	runAs(peerCfg, false, `{"mutation_id":"peer-import","mandate_id":"human-import","base_rev":3,"generation":1,"command":{"kind":"import","snapshot":{"sheets":[{}]}}}`, "workbook", "import", "@workbook-binary-page", "--file", "-")
	run("", "page", "revoke", "@workbook-binary-page", "--user", "workbook-peer@example.test")
	runAs(peerCfg, true, "", "workbook", "read", "@workbook-binary-page")
}
