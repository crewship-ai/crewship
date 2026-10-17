//go:build unix

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
	"time"

	"github.com/crewship-ai/crewship/internal/api"
	"github.com/crewship-ai/crewship/internal/testutil"
)

// The real CLI reaches registered Router auth and migrated persistence. Snapshot
// bytes are fixtures: result listing must not require an engine data RPC, a live
// run principal, or a restored consent. Creation/Node qualification is separate.
func TestAcceptance_DocumentChatResultsBinary(t *testing.T) {
	db := testutil.MigratedDB(t).DB
	const workspace = "cdocresultworkspace001"
	execSQL := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(query, args...); err != nil {
			t.Fatal(err)
		}
	}
	for _, query := range []string{
		`INSERT INTO workspaces(id,name,slug) VALUES('cdocresultworkspace001','Doc results','doc-results')`,
		`INSERT INTO users(id,email) VALUES('result-owner','result-owner@example.invalid'),('result-reader','result-reader@example.invalid'),('result-outsider','result-outsider@example.invalid')`,
		`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('rm1','cdocresultworkspace001','result-owner','MEMBER'),('rm2','cdocresultworkspace001','result-reader','MEMBER'),('rm3','cdocresultworkspace001','result-outsider','MEMBER')`,
		`INSERT INTO crews(id,workspace_id,name,slug) VALUES('result-crew','cdocresultworkspace001','Result crew','result-crew')`,
		`INSERT INTO agents(id,workspace_id,crew_id,name,slug,agent_role,status,cli_adapter,tool_profile,timeout_seconds,memory_enabled) VALUES('result-agent','cdocresultworkspace001','result-crew','Ada','ada','AGENT','IDLE','CLAUDE_CODE','CODING',1800,0)`,
		`INSERT INTO chats(id,workspace_id,agent_id,created_by,visibility) VALUES('result-chat','cdocresultworkspace001','result-agent','result-owner','group')`,
		`INSERT INTO chat_participants(chat_id,user_id,role) VALUES('result-chat','result-reader','member')`,
		`INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json) VALUES('result-page','cdocresultworkspace001','finished-report','Finished report','result-owner','{}')`,
		`INSERT INTO documents(workspace_id,page_id,schema_version,snapshot,projection) VALUES('cdocresultworkspace001','result-page',3,'PRIVATE_DOC_SNAPSHOT','[]')`,
		`INSERT INTO document_creations(page_id,workspace_id,run_id,agent_id,human_user_id,source_chat_id,submission_id,mutation_id,request_digest,revision,created_at) VALUES('result-page','cdocresultworkspace001','recovered-ended-run','result-agent','result-owner','result-chat','original-human-message','one-result','0000000000000000000000000000000000000000000000000000000000000000',0,'2026-10-17T00:00:00Z')`,
	} {
		execSQL(query)
	}
	var principals int
	if err := db.QueryRow(`SELECT COUNT(*) FROM artifact_run_principals`).Scan(&principals); err != nil || principals != 0 {
		t.Fatalf("fixture has live authority: %d %v", principals, err)
	}
	tokens := map[string]string{}
	for _, user := range []string{"result-owner", "result-reader", "result-outsider", "narrow-owner"} {
		token := "crewship_cli_documentresults_" + user
		owner, scopes := user, `["pages:read"]`
		if user == "narrow-owner" {
			owner = "result-owner"
			scopes = `["agents:read"]`
		}
		execSQL(`INSERT INTO cli_tokens(id,user_id,name,token_hash,tier,scopes) VALUES(?,?,?,?,'STANDARD',?)`, "token-"+user, owner, "result test", sha256HexToken(token), scopes)
		tokens[user] = token
	}
	dir, err := os.MkdirTemp("", "doc-result-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	secret := filepath.Join(dir, "secret")
	if err := os.WriteFile(secret, []byte(strings.Repeat("result-service-secret-", 3)), 0600); err != nil {
		t.Fatal(err)
	}
	router, err := api.NewRouter(db, "this-is-a-32-char-test-secret-pad", slog.Default(), api.WithDocumentAuthority(filepath.Join(dir, "a.sock"), secret), api.WithDocumentService(filepath.Join(dir, "e.sock")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(router.Shutdown)
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	binary := buildCrewshipBinary(t)
	run := func(user string) (string, error) {
		t.Helper()
		cfg := filepath.Join(t.TempDir(), "cli.yaml")
		if err := os.WriteFile(cfg, []byte(fmt.Sprintf("server: %s\ntoken: %s\nworkspace: %s\nformat: json\n", server.URL, tokens[user], workspace)), 0600); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		command := exec.CommandContext(ctx, binary, "doc", "chat-results", "result-chat")
		command.Dir = dir
		for _, value := range os.Environ() {
			if !strings.HasPrefix(value, "CREWSHIP_") {
				command.Env = append(command.Env, value)
			}
		}
		command.Env = append(command.Env, "CREWSHIP_CONFIG="+cfg, "CREWSHIP_NO_SLUG_CACHE=1", "NO_COLOR=1")
		output, err := command.CombinedOutput()
		return string(output), err
	}
	list := func(user string) int {
		t.Helper()
		output, err := run(user)
		if err != nil {
			t.Fatalf("actual result command: %v %s", err, output)
		}
		var wire struct {
			Results []struct {
				PageID   string `json:"page_id"`
				Type     string `json:"type"`
				Revision int64  `json:"revision"`
				RunID    string `json:"run_id"`
			} `json:"results"`
		}
		if err := json.Unmarshal([]byte(output), &wire); err != nil || wire.Results == nil {
			t.Fatalf("actual result response: %v %s", err, output)
		}
		for _, result := range wire.Results {
			if result.PageID != "result-page" || result.Type != "document" || result.Revision != 0 || result.RunID != "recovered-ended-run" {
				t.Fatalf("result identity changed: %+v", result)
			}
		}
		if strings.Contains(output, "PRIVATE_DOC_SNAPSHOT") {
			t.Fatal("metadata endpoint disclosed content")
		}
		return len(wire.Results)
	}
	if list("result-owner") != 1 || list("result-reader") != 0 {
		t.Fatal("result Page ACL ignored")
	}
	execSQL(`INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES('result-page','user','result-reader','read','result-owner')`)
	if list("result-reader") != 1 {
		t.Fatal("granted reader missing result")
	}
	execSQL(`DELETE FROM page_grants WHERE page_id='result-page'`)
	if list("result-reader") != 0 {
		t.Fatal("Page revocation retained result")
	}
	for _, test := range []struct{ user, status string }{{"result-outsider", "404"}, {"narrow-owner", "403"}} {
		if output, err := run(test.user); err == nil || !strings.Contains(output, test.status) || strings.Contains(output, "Finished report") {
			t.Fatalf("denied current scope/chat %s: %v %s", test.user, err, output)
		}
	}
	execSQL(`UPDATE cli_tokens SET revoked_at='2026-01-01T00:00:00Z' WHERE id='token-result-owner'`)
	if output, err := run("result-owner"); err == nil || !strings.Contains(output, "401") || strings.Contains(output, "Finished report") {
		t.Fatalf("revoked exact credential: %v %s", err, output)
	}
}
