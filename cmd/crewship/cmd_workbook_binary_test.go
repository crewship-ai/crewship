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
		`INSERT INTO feature_flag_overrides(id,flag_id,workspace_id,enabled) VALUES('workbook-editor-on','ffl_gridcraft_editor','cworkbookworkspace000000001',1)`,
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
	router.RegisterGridCraft(appengine.Options{Enabled: true, Editor: true}, binaryWorkbookEngine{})
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	binary := buildCrewshipBinary(t)
	cfg := filepath.Join(t.TempDir(), "cli.yaml")
	if err = os.WriteFile(cfg, []byte(fmt.Sprintf("server: %s\ntoken: %s\nworkspace: cworkbookworkspace000000001\nformat: json\n", server.URL, token)), 0600); err != nil {
		t.Fatal(err)
	}
	command := func(input string, args ...string) *exec.Cmd {
		cmd := exec.CommandContext(t.Context(), binary, args...)
		cmd.Stdin = strings.NewReader(input)
		for _, v := range os.Environ() {
			if !strings.HasPrefix(v, "CREWSHIP_") {
				cmd.Env = append(cmd.Env, v)
			}
		}
		cmd.Env = append(cmd.Env, "CREWSHIP_CONFIG="+cfg, "CREWSHIP_NO_SLUG_CACHE=1", "NO_COLOR=1")
		return cmd
	}
	run := func(input string, args ...string) []byte {
		t.Helper()
		out, err := command(input, args...).CombinedOutput()
		if err != nil {
			t.Fatalf("CLI %v: %v %s", args, err, out)
		}
		return out
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

	// Human cell edits from two editor sessions of the owner.
	session := func() string {
		t.Helper()
		var opened struct {
			SessionID string `json:"session_id"`
			Rev       int64  `json:"rev"`
			Protocol  int    `json:"protocol"`
		}
		out := run("", "workbook", "session", "open", "@workbook-binary-page")
		if json.Unmarshal(out, &opened) != nil || opened.SessionID == "" || opened.Protocol != 1 {
			t.Fatalf("session %s", out)
		}
		return opened.SessionID
	}
	w1, w2 := session(), session()
	refused := func(wantExit int, input string, args ...string) string {
		t.Helper()
		var stdout, stderr strings.Builder
		cmd := command(input, args...)
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		err := cmd.Run()
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != wantExit {
			t.Fatalf("CLI %v exit=%v want %d: %s %s", args, err, wantExit, stdout.String(), stderr.String())
		}
		return stdout.String() + stderr.String()
	}
	editPacket := func(session string, seq int, cell, input string) string {
		return fmt.Sprintf(`{"session_id":%q,"mutation_id":"%s:%d","base_rev":1,"generation":1,"ops":[{"kind":"cell.set","sheet":"Sheet1","cell":%q,"input":%q}]}`, session, session, seq, cell, input)
	}
	if out := refused(3, editPacket("ws_neveropened", 1, "B5", "x"), "workbook", "edit", "@workbook-binary-page", "--file", "-"); !strings.Contains(out, "not found") {
		t.Fatalf("unopened session: %s", out)
	}
	edit := editPacket(w1, 1, "B5", "draft")
	edited := run(edit, "workbook", "edit", "@workbook-binary-page", "--file", "-")
	if retried := run(edit, "workbook", "edit", "@workbook-binary-page", "--file", "-"); string(retried) != string(edited) {
		t.Fatalf("edit receipt changed: %s %s", edited, retried)
	}
	if json.Unmarshal(edited, &receipt) != nil || receipt.Rev != 2 || receipt.OperationKey.MutationID != w1+":1" {
		t.Fatalf("edit receipt %s", edited)
	}
	if out := refused(5, editPacket(w2, 1, "B5", "mine"), "workbook", "edit", "@workbook-binary-page", "--file", "-"); !strings.Contains(out, `"conflicts"`) || !strings.Contains(out, `"draft"`) {
		t.Fatalf("conflict out=%s", out)
	}
	run(editPacket(w2, 2, "C5", "other"), "workbook", "edit", "@workbook-binary-page", "--file", "-")
	run("", "workbook", "status", "@workbook-binary-page", w2+":2")

	// The commits feed, own revisions with their session, and paging.
	var feed struct {
		HeadRev int64 `json:"head_rev"`
		Commits []struct {
			Rev       int64  `json:"rev"`
			Kind      string `json:"kind"`
			Own       bool   `json:"own"`
			SessionID string `json:"session_id"`
			Ops       []struct {
				Cell, Input string
			} `json:"ops"`
		} `json:"commits"`
	}
	out := run("", "workbook", "commits", "@workbook-binary-page", "--since", "1")
	if json.Unmarshal(out, &feed) != nil || feed.HeadRev != 3 || len(feed.Commits) != 2 || !feed.Commits[0].Own || feed.Commits[0].SessionID != w1 || feed.Commits[1].SessionID != w2 || feed.Commits[1].Ops[0].Input != "other" {
		t.Fatalf("feed %s", out)
	}
	out = run("", "workbook", "commits", "@workbook-binary-page", "--since", "0", "--limit", "1")
	if json.Unmarshal(out, &feed) != nil || len(feed.Commits) != 1 || feed.Commits[0].Kind != "snapshot" {
		t.Fatalf("paged feed %s", out)
	}
	if out := refused(5, "", "workbook", "commits", "@workbook-binary-page", "--since", "9"); !strings.Contains(out, `"snapshot_required"`) || !strings.Contains(out, `"min_since"`) {
		t.Fatalf("history gone: %s", out)
	}
	refused(2, "", "workbook", "commits", "@workbook-binary-page", "--since", "0", "--limit", "500")

	// Heartbeat, close and expiry of a session.
	run("", "workbook", "session", "open", "@workbook-binary-page", "--renew", w2)
	run("", "workbook", "session", "close", "@workbook-binary-page", w1)
	run("", "workbook", "session", "close", "@workbook-binary-page", w1)
	if out := refused(4, editPacket(w1, 2, "D5", "late"), "workbook", "edit", "@workbook-binary-page", "--file", "-"); !strings.Contains(out, "session_expired") {
		t.Fatalf("closed session: %s", out)
	}
	if _, err = db.Exec(`UPDATE feature_flag_overrides SET enabled=0 WHERE id='workbook-editor-on'`); err != nil {
		t.Fatal(err)
	}
	if out := refused(7, "", "workbook", "session", "open", "@workbook-binary-page"); !strings.Contains(out, "gridcraft_editor_disabled") {
		t.Fatalf("flag off: %s", out)
	}
}
