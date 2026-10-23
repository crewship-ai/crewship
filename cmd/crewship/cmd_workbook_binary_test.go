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
	command := func(config, input string, args ...string) *exec.Cmd {
		cmd := exec.CommandContext(t.Context(), binary, args...)
		cmd.Stdin = strings.NewReader(input)
		for _, v := range os.Environ() {
			if !strings.HasPrefix(v, "CREWSHIP_") {
				cmd.Env = append(cmd.Env, v)
			}
		}
		cmd.Env = append(cmd.Env, "CREWSHIP_CONFIG="+config, "CREWSHIP_NO_SLUG_CACHE=1", "NO_COLOR=1")
		return cmd
	}
	runAs := func(config string, wantErr bool, input string, args ...string) []byte {
		t.Helper()
		out, err := command(config, input, args...).CombinedOutput()
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
	refusedAs := func(config string, wantExit int, input string, args ...string) string {
		t.Helper()
		var stdout, stderr strings.Builder
		cmd := command(config, input, args...)
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		err := cmd.Run()
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != wantExit {
			t.Fatalf("CLI %v exit=%v want %d: %s %s", args, err, wantExit, stdout.String(), stderr.String())
		}
		return stdout.String() + stderr.String()
	}
	refused := func(wantExit int, input string, args ...string) string {
		t.Helper()
		return refusedAs(cfg, wantExit, input, args...)
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
	if out := refused(5, editPacket(w1, 2, "D5", "late"), "workbook", "edit", "@workbook-binary-page", "--file", "-"); !strings.Contains(out, `"editor_session_expired"`) {
		t.Fatalf("closed session: %s", out)
	}
	if _, err = db.Exec(`UPDATE feature_flag_overrides SET enabled=0 WHERE id='workbook-editor-on'`); err != nil {
		t.Fatal(err)
	}
	if out := refused(7, "", "workbook", "session", "open", "@workbook-binary-page"); !strings.Contains(out, "gridcraft_editor_disabled") {
		t.Fatalf("flag off: %s", out)
	}

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
	// The named editor also edits live: the editor is switched on again, the
	// editor opens its own session and edits, and its commits feed starts at
	// the published revision (3), never in the private history before it.
	if _, err = db.Exec(`UPDATE feature_flag_overrides SET enabled=1 WHERE id='workbook-editor-on'`); err != nil {
		t.Fatal(err)
	}
	var peerSession struct {
		SessionID string `json:"session_id"`
	}
	if out := runAs(peerCfg, false, "", "workbook", "session", "open", "@workbook-binary-page"); json.Unmarshal(out, &peerSession) != nil || peerSession.SessionID == "" {
		t.Fatalf("editor session %s", out)
	}
	// Its base is the published head: a base below the publication floor is refused.
	if out := refusedAs(peerCfg, 5, editPacket(peerSession.SessionID, 1, "E5", "peer"), "workbook", "edit", "@workbook-binary-page", "--file", "-"); !strings.Contains(out, `"snapshot_required": true`) || !strings.Contains(out, `"conflicts": []`) {
		t.Fatalf("editor edit below the floor: %s", out)
	}
	peerEdit := fmt.Sprintf(`{"session_id":%q,"mutation_id":"%s:2","base_rev":3,"generation":1,"ops":[{"kind":"cell.set","sheet":"Sheet1","cell":"E5","input":"peer"}]}`, peerSession.SessionID, peerSession.SessionID)
	peerEdited := runAs(peerCfg, false, peerEdit, "workbook", "edit", "@workbook-binary-page", "--file", "-")
	if json.Unmarshal(peerEdited, &receipt) != nil || receipt.Rev != 4 {
		t.Fatalf("editor edit %s", peerEdited)
	}
	if out := refusedAs(peerCfg, 5, "", "workbook", "commits", "@workbook-binary-page", "--since", "0"); !strings.Contains(out, `"snapshot_required"`) || !strings.Contains(out, `"min_since": 3`) {
		t.Fatalf("editor read private history: %s", out)
	}
	if out := runAs(peerCfg, false, "", "workbook", "commits", "@workbook-binary-page", "--since", "3"); json.Unmarshal(out, &feed) != nil || feed.HeadRev != 4 || len(feed.Commits) != 1 || !feed.Commits[0].Own || feed.Commits[0].Ops[0].Input != "peer" {
		t.Fatalf("editor feed %s", out)
	}
	runAs(peerCfg, false, `{"mutation_id":"peer-import","mandate_id":"human-import","base_rev":4,"generation":1,"command":{"kind":"import","snapshot":{"sheets":[{}]}}}`, "workbook", "import", "@workbook-binary-page", "--file", "-")
	// Shared workbook agent changes require proposals, even for the owner.
	sharedMandate := func(id, target string) string {
		return fmt.Sprintf(`{"mandate_id":%q,"run_id":"workbook-run","base_rev":5,"generation":1,"targets":[%q]}`, id, target)
	}
	if out := refusedAs(peerCfg, 5, sharedMandate("peer-mandate", "Sheet1!B2"), "workbook", "admit", "@workbook-binary-page", "--file", "-"); !strings.Contains(out, "proposal_required") {
		t.Fatalf("editor admit: %s", out)
	}
	if out := refused(5, sharedMandate("wildcard-mandate", "*"), "workbook", "admit", "@workbook-binary-page", "--file", "-"); !strings.Contains(out, "proposal_required") {
		t.Fatalf("wildcard admit on shared: %s", out)
	}
	if out := refused(5, sharedMandate("shared-mandate", "Sheet1!B2"), "workbook", "admit", "@workbook-binary-page", "--file", "-"); !strings.Contains(out, "proposal_required") {
		t.Fatalf("shared owner mandate bypassed proposal requirement: %s", out)
	}
	run("", "page", "revoke", "@workbook-binary-page", "--user", "workbook-peer@example.test")
	runAs(peerCfg, true, "", "workbook", "read", "@workbook-binary-page")
}
