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
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/api"
	"github.com/crewship-ai/crewship/internal/testutil"
)

// Real binary -> registered Router -> current credential and migrated SQLite.
// The configured Docs service socket has no listener: preview is metadata-only.
// This does not qualify model authoring, Node or the dormant iteration grant.
func TestAcceptance_ChatArtifactPreviewBinary(t *testing.T) {
	db := testutil.MigratedDB(t).DB
	const workspace = "cpreviewworkspace001"
	execSQL := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(query, args...); err != nil {
			t.Fatal(query, err)
		}
	}
	for _, query := range []string{
		`INSERT INTO workspaces(id,name,slug) VALUES('cpreviewworkspace001','Preview fixture','preview-fixture')`,
		`INSERT INTO users(id,email) VALUES('preview-author','preview-author@example.invalid'),('preview-outsider','preview-outsider@example.invalid'),('preview-admin','preview-admin@example.invalid'),('preview-owner','preview-owner@example.invalid')`,
		`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('pm1','cpreviewworkspace001','preview-author','MEMBER'),('pm2','cpreviewworkspace001','preview-outsider','MEMBER'),('pm3','cpreviewworkspace001','preview-admin','ADMIN'),('pm4','cpreviewworkspace001','preview-owner','OWNER')`,
		`INSERT INTO crews(id,workspace_id,name,slug) VALUES('preview-crew','cpreviewworkspace001','Preview crew','preview-crew')`,
		`INSERT INTO agents(id,workspace_id,crew_id,name,slug,agent_role,status,cli_adapter,tool_profile,timeout_seconds,memory_enabled) VALUES('preview-agent','cpreviewworkspace001','preview-crew','Preview agent','preview-agent','AGENT','IDLE','CLAUDE_CODE','CODING',1800,0)`,
		`INSERT INTO chats(id,workspace_id,agent_id,created_by,visibility,private_artifact_author_user_id) VALUES('preview-chat','cpreviewworkspace001','preview-agent','preview-author','private','preview-author')`,
		`INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,private_audience,spec_json) VALUES('preview-page','cpreviewworkspace001','secret-preview-slug','PRIVATE_PREVIEW_TITLE','preview-author',1,'{}')`,
		`INSERT INTO documents(workspace_id,page_id,schema_version,snapshot,projection,head_rev,generation) VALUES('cpreviewworkspace001','preview-page',3,'PRIVATE_PREVIEW_CONTENT','[]',19,7)`,
	} {
		execSQL(query)
	}
	tokens := map[string]string{}
	for _, name := range []string{"preview-author", "preview-outsider", "preview-admin", "preview-owner", "narrow-author"} {
		user, scopes := name, `["pages:read"]`
		if name == "narrow-author" {
			user, scopes = "preview-author", `["agents:read"]`
		}
		token := "crewship_cli_artifactpreview_" + name
		execSQL(`INSERT INTO cli_tokens(id,user_id,name,token_hash,tier,scopes) VALUES(?,?,?,?,'STANDARD',?)`, "token-"+name, user, "preview fixture", sha256HexToken(token), scopes)
		tokens[name] = token
	}
	dir, err := os.MkdirTemp("", "chat-preview-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	secret := filepath.Join(dir, "secret")
	if err := os.WriteFile(secret, []byte(strings.Repeat("preview-service-secret-", 3)), 0600); err != nil {
		t.Fatal(err)
	}
	router, err := api.NewRouter(db, "this-is-a-32-char-test-secret-pad", slog.Default(), api.WithDocumentAuthority(filepath.Join(dir, "a.sock"), secret), api.WithDocumentService(filepath.Join(dir, "unserved.sock")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(router.Shutdown)
	// Router startup fences retained Documents before serving requests. Capture
	// that legitimate new generation, then require preview to preserve it.
	var confirmedRev, confirmedGeneration int64
	if err := db.QueryRow(`SELECT head_rev,generation FROM documents WHERE page_id='preview-page'`).Scan(&confirmedRev, &confirmedGeneration); err != nil || confirmedRev != 19 || confirmedGeneration != 8 {
		t.Fatalf("startup must preserve revision and fence seed generation 7 exactly once: rev=%d generation=%d err=%v", confirmedRev, confirmedGeneration, err)
	}
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	binary := buildCrewshipBinary(t)
	run := func(user string) (string, error) {
		t.Helper()
		cfg := filepath.Join(t.TempDir(), "cli.yaml")
		if err := os.WriteFile(cfg, []byte(fmt.Sprintf("server: %s\ntoken: %s\nworkspace: %s\nformat: json\n", server.URL, tokens[user], workspace)), 0600); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()
		command := exec.CommandContext(ctx, binary, "chat", "artifact-preview", "preview-chat", "--page-id", "preview-page")
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
	assertNoAuthority := func() {
		t.Helper()
		for _, table := range []string{"document_iteration_intents", "artifact_run_principals", "document_creation_consents", "page_grants", "document_receipts", "document_checkpoints"} {
			var count int
			if err := db.QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil || count != 0 {
				t.Fatalf("preview created authority in %s: count=%d err=%v", table, count, err)
			}
		}
		var rev, generation int64
		if err := db.QueryRow(`SELECT head_rev,generation FROM documents WHERE page_id='preview-page'`).Scan(&rev, &generation); err != nil || rev != confirmedRev || generation != confirmedGeneration {
			t.Fatalf("preview mutated confirmed content: rev=%d generation=%d err=%v", rev, generation, err)
		}
	}
	assertNoAuthority()
	output, err := run("preview-author")
	if err != nil {
		t.Fatalf("actual preview command: %v %s", err, output)
	}
	var actual any
	if err := json.Unmarshal([]byte(output), &actual); err != nil {
		t.Fatalf("actual preview JSON: %v %s", err, output)
	}
	want := map[string]any{
		"chat":       map[string]any{"id": "preview-chat", "visibility": "private", "audience": "author_and_workspace_owners"},
		"target":     map[string]any{"page_id": "preview-page", "type": "document", "audience": "author_and_workspace_owners", "expected": map[string]any{"schema": float64(3), "rev": float64(confirmedRev), "generation": float64(confirmedGeneration)}},
		"can_update": false, "reason": "targeted_iteration_unavailable",
	}
	if !reflect.DeepEqual(actual, want) {
		t.Fatalf("actual closed DTO: got=%#v want=%#v", actual, want)
	}
	for _, secret := range []string{"PRIVATE_PREVIEW_TITLE", "PRIVATE_PREVIEW_CONTENT", "secret-preview-slug"} {
		if strings.Contains(output, secret) {
			t.Fatalf("metadata command leaked %q", secret)
		}
	}
	assertNoAuthority()
	for _, denied := range []struct{ user, status string }{{"preview-outsider", "404"}, {"preview-admin", "404"}, {"preview-owner", "404"}, {"narrow-author", "403"}} {
		t.Run(denied.user, func(t *testing.T) {
			output, err := run(denied.user)
			if err == nil || !strings.Contains(output, denied.status) {
				t.Fatalf("current audience/scope %s: err=%v output=%s", denied.user, err, output)
			}
			for _, secret := range []string{"PRIVATE_PREVIEW_TITLE", "PRIVATE_PREVIEW_CONTENT", "secret-preview-slug", `"expected"`, `"target"`} {
				if strings.Contains(output, secret) {
					t.Fatalf("denial leaked %q: %s", secret, output)
				}
			}
			assertNoAuthority()
		})
	}
	execSQL(`UPDATE cli_tokens SET revoked_at='2026-01-01T00:00:00Z' WHERE id='token-preview-author'`)
	if output, err := run("preview-author"); err == nil || !strings.Contains(output, "401") || strings.Contains(output, "PRIVATE_PREVIEW_") {
		t.Fatalf("revoked original token: err=%v output=%s", err, output)
	}
	assertNoAuthority()
}
