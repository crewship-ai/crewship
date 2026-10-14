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
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/api"
	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
	"github.com/crewship-ai/crewship/internal/chataudience"
	"github.com/crewship-ai/crewship/internal/conversation"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/testutil"
)

// Use the real router, migrated database, host admission, and built CLI. A
// canned HTTP response cannot establish that the write and read contracts meet.
func TestAcceptance_ChatEntities_RealHostMutationAndCLI(t *testing.T) {
	db := testutil.MigratedSQLDB(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	const workspace = "cchatentityws00000000001"
	const master = "chat-entity-synthetic-host-master"
	const token = "crewship_cli_chatentity0000000000000000"
	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(t.Context(), q, args...); err != nil {
			t.Fatal(err)
		}
	}
	mustExec(`INSERT INTO workspaces(id,name,slug) VALUES(?,'Entities','entities')`, workspace)
	mustExec(`INSERT INTO users(id,email) VALUES('entity-owner','entity-owner@example.test')`)
	mustExec(`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('entity-member',?,'entity-owner','OWNER')`, workspace)
	mustExec(`INSERT INTO crews(id,workspace_id,name,slug) VALUES('entity-crew',?,'Entities','entities')`, workspace)
	mustExec(`INSERT INTO agents(id,workspace_id,crew_id,name,slug,agent_role,status) VALUES('entity-agent',?,'entity-crew','Jamie','jamie','LEAD','IDLE')`, workspace)
	mustExec(`INSERT INTO chats(id,workspace_id,agent_id,created_by,visibility) VALUES('entity-chat',?,'entity-agent','entity-owner','private')`, workspace)
	mustExec(`INSERT INTO cli_tokens(id,user_id,name,token_hash,created_at) VALUES('entity-token','entity-owner','acceptance',?,datetime('now'))`, sha256HexToken(token))
	store := conversation.NewStore(t.TempDir(), logger)
	t.Cleanup(store.Close)
	if err := store.Append(t.Context(), "entity-chat", conversation.Message{ID: "entity-trigger", Role: conversation.RoleUser, AuthorUserID: "entity-owner", Content: "Create an issue"}); err != nil {
		t.Fatal(err)
	}
	if err := store.SyncSession(t.Context(), "entity-chat"); err != nil {
		t.Fatal(err)
	}
	receipt, err := chataudience.CaptureTrusted(t.Context(), db, "entity-chat", "entity-owner", nil)
	if err != nil {
		t.Fatal(err)
	}
	writer := journal.NewWriter(db, logger, journal.WriterOptions{FlushSize: 1})
	t.Cleanup(func() { _ = writer.Close() })
	router, err := api.NewRouter(db, "this-is-a-32-char-test-secret-pad", logger, api.WithInternalToken(master), api.WithJournal(writer))
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(router)
	t.Cleanup(srv.Close)
	post := func(path, internalToken string, body any, want int) map[string]any {
		t.Helper()
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		req, err := http.NewRequestWithContext(t.Context(), "POST", srv.URL+path, bytes.NewReader(encoded))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Internal-Token", internalToken)
		res, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		data, _ := io.ReadAll(res.Body)
		if res.StatusCode != want {
			t.Fatalf("%s: %d want %d: %s", path, res.StatusCode, want, data)
		}
		var result map[string]any
		if err := json.Unmarshal(data, &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	post("/api/v1/internal/runs", master, map[string]any{"id": "entity-run", "workspace_id": workspace, "agent_id": "entity-agent", "chat_id": "entity-chat", "trigger_type": "USER", "human_authority": receipt, "trigger_message_id": "entity-trigger"}, 201)
	crewToken := internaltoken.DeriveCrewToken(master, workspace, "entity-crew")
	body := map[string]any{"workspace_id": workspace, "crew_id": "entity-crew", "title": "Real host issue", "author_agent_id": "entity-agent", "author_run_id": "entity-run", "operation_key": "create-real-1"}
	created := post("/api/v1/internal/issues", crewToken, body, 201)
	replay := post("/api/v1/internal/issues", crewToken, body, 200)
	if created["id"] != replay["id"] || replay["replayed"] != true {
		t.Fatalf("unstable replay: %v %v", created, replay)
	}
	cfgPath := filepath.Join(t.TempDir(), "cli.yaml")
	if err := os.WriteFile(cfgPath, []byte("server: "+srv.URL+"\nworkspace: "+workspace+"\ntoken: "+token+"\nformat: json\n"), 0600); err != nil {
		t.Fatal(err)
	}
	out, err := runFeedbackCLI(t, cfgPath, "chat", "refs", "entity-chat")
	if err != nil {
		t.Fatalf("real CLI refs: %v %s", err, out)
	}
	var page struct {
		Refs []struct {
			Entity struct {
				ID string `json:"id"`
			} `json:"entity"`
		} `json:"refs"`
	}
	if err := json.Unmarshal([]byte(out), &page); err != nil {
		t.Fatalf("CLI JSON: %v %s", err, out)
	}
	if len(page.Refs) != 1 || page.Refs[0].Entity.ID != created["id"] {
		t.Fatalf("wrong CLI projection: %s", out)
	}
	out, err = runFeedbackCLI(t, cfgPath, "operation", "get", created["operation_id"].(string))
	if err != nil || !strings.Contains(out, created["id"].(string)) {
		t.Fatalf("real CLI operation: %v %s", err, out)
	}
	for _, secretField := range []string{"human_authority", "request_hash", "create-real-1"} {
		if strings.Contains(out, secretField) {
			t.Fatalf("CLI leaked %s: %s", secretField, out)
		}
	}
	mustExec(`DELETE FROM workspace_members WHERE id='entity-member'`)
	out, err = runFeedbackCLI(t, cfgPath, "chat", "refs", "entity-chat")
	if err == nil {
		t.Fatalf("revoked viewer still read references: %s", out)
	}
}
