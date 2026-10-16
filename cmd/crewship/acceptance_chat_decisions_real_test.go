package main

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/api"
	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
	"github.com/crewship-ai/crewship/internal/chataudience"
	"github.com/crewship-ai/crewship/internal/conversation"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/testutil"
)

func TestAcceptance_ChatDecisions_RealSourceAndCLI(t *testing.T) {
	db := testutil.MigratedSQLDB(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	const workspace = "cdecisionws0000000000001"
	const master = "decision-synthetic-master"
	const token = "crewship_cli_decision0000000000000000"
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(t.Context(), q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO workspaces(id,name,slug) VALUES(?,'Decision','decision')`, workspace)
	exec(`INSERT INTO users(id,email) VALUES('decision-user','decision@example.test')`)
	exec(`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('decision-member',?,'decision-user','OWNER')`, workspace)
	exec(`INSERT INTO crews(id,workspace_id,name,slug) VALUES('decision-crew',?,'Decision','decision')`, workspace)
	exec(`INSERT INTO agents(id,workspace_id,crew_id,name,slug,agent_role,status) VALUES('decision-agent',?,'decision-crew','Jamie','jamie','LEAD','IDLE')`, workspace)
	exec(`INSERT INTO chats(id,workspace_id,agent_id,created_by,visibility) VALUES('decision-chat',?,'decision-agent','decision-user','private')`, workspace)
	exec(`INSERT INTO cli_tokens(id,user_id,name,token_hash,created_at) VALUES('decision-token','decision-user','acceptance',?,datetime('now'))`, sha256HexToken(token))
	store := conversation.NewStore(t.TempDir(), logger)
	t.Cleanup(store.Close)
	if err := store.Append(t.Context(), "decision-chat", conversation.Message{ID: "decision-trigger", Role: conversation.RoleUser, AuthorUserID: "decision-user", Content: "Ask for information"}); err != nil {
		t.Fatal(err)
	}
	if err := store.SyncSession(t.Context(), "decision-chat"); err != nil {
		t.Fatal(err)
	}
	receipt, err := chataudience.CaptureTrusted(t.Context(), db, "decision-chat", "decision-user", nil)
	if err != nil || receipt == nil {
		t.Fatalf("receipt %v %v", receipt, err)
	}
	writer := journal.NewWriter(db, logger, journal.WriterOptions{FlushSize: 1})
	t.Cleanup(func() { _ = writer.Close() })
	router, err := api.NewRouter(db, "this-is-a-32-char-test-secret-pad", logger, api.WithInternalToken(master), api.WithJournal(writer))
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(router)
	t.Cleanup(srv.Close)
	post := func(path, internal, runToken string, body any) map[string]any {
		t.Helper()
		raw, _ := json.Marshal(body)
		req, err := http.NewRequestWithContext(t.Context(), "POST", srv.URL+path, bytes.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Internal-Token", internal)
		if runToken != "" {
			req.Header.Set("X-Crewship-Run-Token", runToken)
		}
		res, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		data, _ := io.ReadAll(res.Body)
		if res.StatusCode != 201 {
			t.Fatalf("%s %d %s", path, res.StatusCode, data)
		}
		var result map[string]any
		if err := json.Unmarshal(data, &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	post("/api/v1/internal/runs", master, "", map[string]any{"id": "decision-run", "workspace_id": workspace, "agent_id": "decision-agent", "chat_id": "decision-chat", "trigger_type": "USER", "human_authority": receipt, "trigger_message_id": "decision-trigger"})
	created := post("/api/v1/internal/escalations", internaltoken.DeriveCrewToken(master, workspace, "decision-crew"), internaltoken.DeriveAgentRunToken(internaltoken.DeriveAgentRunKey(master, workspace, "decision-crew"), workspace, "decision-agent", "decision-run"), map[string]any{"workspace_id": workspace, "crew_id": "decision-crew", "chat_id": "decision-chat", "source_run_id": "decision-run", "actor_agent_id": "decision-agent", "reason": "When should we run?"})
	source := created["escalation_id"].(string)
	cfg := filepath.Join(t.TempDir(), "cli.yaml")
	if err := os.WriteFile(cfg, []byte("server: "+srv.URL+"\nworkspace: "+workspace+"\ntoken: "+token+"\nformat: json\n"), 0600); err != nil {
		t.Fatal(err)
	}
	out, err := runFeedbackCLI(t, cfg, "chat", "decision", "list", "decision-chat")
	if err != nil || !strings.Contains(out, source) {
		t.Fatalf("list %v %s", err, out)
	}
	out, err = runFeedbackCLI(t, cfg, "chat", "decision", "show", "decision-chat", "escalation", source)
	if err != nil {
		t.Fatalf("show %v %s", err, out)
	}
	var card api.ChatCardResponse
	if err = json.Unmarshal([]byte(out), &card); err != nil {
		t.Fatal(err)
	}
	args := []string{"chat", "decision", "answer", "decision-chat", "escalation", source, "--expected-version", card.Decision.Source.Version, "--idempotency-key", "cli-answer-key", "--text", "Tomorrow"}
	exec(`UPDATE cli_tokens SET scopes='["agents:write"]' WHERE id='decision-token'`)
	for _, readArgs := range [][]string{{"chat", "decision", "show", "decision-chat", "escalation", source}, {"chat", "decision", "list", "decision-chat"}} {
		out, err = runFeedbackCLI(t, cfg, readArgs...)
		if err != nil {
			t.Fatalf("scoped source read: %v %s", err, out)
		}
		var references []api.ChatCardResponse
		if readArgs[2] == "show" {
			var current api.ChatCardResponse
			_ = json.Unmarshal([]byte(out), &current)
			references = []api.ChatCardResponse{current}
		} else {
			var page api.ChatCardPage
			_ = json.Unmarshal([]byte(out), &page)
			references = page.Refs
		}
		if len(references) != 1 || references[0].Decision == nil || slices.Contains(references[0].Decision.AllowedActions, "answer") {
			t.Fatalf("narrow token advertised forbidden write: %s", out)
		}
	}
	out, err = runFeedbackCLI(t, cfg, args...)
	if err == nil {
		t.Fatalf("narrow token answered source: %s", out)
	}
	var pendingStatus string
	if err = db.QueryRow(`SELECT status FROM escalations WHERE id=?`, source).Scan(&pendingStatus); err != nil || pendingStatus != "PENDING" {
		t.Fatalf("denied scope left source effects: status=%s err=%v", pendingStatus, err)
	}
	exec(`UPDATE cli_tokens SET scopes='["workspace:admin"]' WHERE id='decision-token'`)
	out, err = runFeedbackCLI(t, cfg, "chat", "decision", "show", "decision-chat", "escalation", source)
	var scopedWritable api.ChatCardResponse
	if err != nil || json.Unmarshal([]byte(out), &scopedWritable) != nil || scopedWritable.Decision == nil || !slices.Contains(scopedWritable.Decision.AllowedActions, "answer") {
		t.Fatalf("canonical scoped token lost legitimate action: %v %s", err, out)
	}

	for i := 0; i < 2; i++ {
		out, err = runFeedbackCLI(t, cfg, args...)
		if err != nil || !strings.Contains(out, `"answered_by": "decision-user"`) {
			t.Fatalf("answer/replay %v %s", err, out)
		}
	}
	var actor, value string
	if err = db.QueryRow(`SELECT resolved_by_user_id,resolution FROM escalations WHERE id=?`, source).Scan(&actor, &value); err != nil || actor != "decision-user" || value != "Tomorrow" {
		t.Fatalf("source actor=%s value=%s err=%v", actor, value, err)
	}
	exec(`UPDATE workspace_members SET role='VIEWER' WHERE id='decision-member'`)
	out, err = runFeedbackCLI(t, cfg, args...)
	if err != nil || !strings.Contains(out, `"answered_by": "decision-user"`) {
		t.Fatalf("read-authorized replay after write revocation: %v %s", err, out)
	}
	exec(`DELETE FROM workspace_members WHERE id='decision-member'`)
	out, err = runFeedbackCLI(t, cfg, args...)
	if err == nil {
		t.Fatalf("revoked CLI replay accepted: %s", out)
	}
}
