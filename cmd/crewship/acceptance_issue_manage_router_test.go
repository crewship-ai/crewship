package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/access"
	"github.com/crewship-ai/crewship/internal/api"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/testutil"
)

// All tokens, accounts and active-work records are synthetic test fixtures.
// The child CLI uses the real Router, credential scope checks and delete
// transaction; no live instance, model or runtime is involved.
func TestAcceptanceIssueManageRouterCLI(t *testing.T) {
	t.Setenv("CREWSHIP_RATELIMIT_DISABLED", "true")
	db := testutil.MigratedDB(t).DB
	sql := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(query, args...); err != nil {
			t.Fatal(err)
		}
	}
	sql(`INSERT INTO workspaces(id,name,slug) VALUES('delete-ws','Delete fixture','delete-ws')`)
	sql(`INSERT INTO users(id,email) VALUES('delete-owner','owner@delete.invalid'),('delete-a','a@delete.invalid'),('delete-b','b@delete.invalid')`)
	sql(`INSERT INTO workspace_members(id,user_id,workspace_id,role) VALUES('delete-owner','delete-owner','delete-ws','OWNER'),('delete-a','delete-a','delete-ws','VIEWER'),('delete-b','delete-b','delete-ws','VIEWER')`)
	sql(`INSERT INTO crews(id,workspace_id,name,slug) VALUES('delete-crew','delete-ws','Delete fixture','delete-crew')`)
	sql(`INSERT INTO agents(id,workspace_id,crew_id,name,slug,agent_role) VALUES('delete-lead','delete-ws','delete-crew','Lead fixture','lead','LEAD')`)
	store := access.Store{DB: db}
	grant := func(user, project string, operations ...string) {
		t.Helper()
		member, err := store.Membership(t.Context(), user, "delete-ws")
		if err != nil {
			t.Fatal(err)
		}
		var rights []access.Right
		for _, operation := range operations {
			rights = append(rights, access.Right{Kind: "project", ID: project, Operation: operation})
		}
		if _, err := store.Replace(t.Context(), "delete-owner", user, "delete-ws", "restricted", member, rights); err != nil {
			t.Fatal(err)
		}
	}
	tokens := map[string]string{}
	for index, suffix := range []string{"a", "b"} {
		project, issue := "delete-project-"+suffix, "delete-issue-"+suffix
		sql(`INSERT INTO projects(id,workspace_id,name,slug) VALUES(?,'delete-ws',?,?)`, project, project, project)
		sql(`INSERT INTO missions(id,workspace_id,crew_id,lead_agent_id,trace_id,title,status,number,identifier,priority,sort_order,mission_type,project_id,created_at,updated_at) VALUES(?,'delete-ws','delete-crew','delete-lead',?,?,'BACKLOG',?,?,'medium',0,'issue',?,datetime('now'),datetime('now'))`, issue, "trace-"+issue, "CLIENT_PRIVATE_"+suffix, index+1, "DELETE-"+strings.ToUpper(suffix), project)
		user := "delete-" + suffix
		tokens[user] = "crewship_cli_" + strings.Repeat(suffix, 48)
		sql(`INSERT INTO cli_tokens(id,user_id,name,token_hash,created_at) VALUES(?,?,?, ?,datetime('now'))`, user, user, "fixture", sha256HexToken(tokens[user]))
		grant(user, project, "read", "manage")
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	writer := journal.NewWriter(db, logger, journal.WriterOptions{})
	t.Cleanup(func() { _ = writer.Close() })
	router, err := api.NewRouter(db, "delete-cli-router-fixture-secret-long", logger, api.WithJournal(writer), api.WithStoragePath(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(router.Shutdown)
	var mu sync.Mutex
	var observed []struct {
		method, path string
		status       int
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, r)
		mu.Lock()
		observed = append(observed, struct {
			method, path string
			status       int
		}{r.Method, r.URL.Path, response.Code})
		mu.Unlock()
		for key, values := range response.Header() {
			for _, value := range values {
				w.Header().Add(key, value)
			}
		}
		w.WriteHeader(response.Code)
		_, _ = w.Write(response.Body.Bytes())
	}))
	t.Cleanup(server.Close)
	binary := buildCrewshipBinary(t)
	run := func(user string, want int, args ...string) string {
		t.Helper()
		mu.Lock()
		observed = nil
		mu.Unlock()
		config := filepath.Join(t.TempDir(), "cli.yaml")
		if err := os.WriteFile(config, []byte("server: "+server.URL+"\nworkspace: delete-ws\ntoken: "+tokens[user]+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
		defer cancel()
		command := exec.CommandContext(ctx, binary, append(args, "--server", server.URL, "--workspace", "delete-ws", "--format", "json")...)
		command.Dir = t.TempDir()
		command.Env = append(os.Environ(), "CREWSHIP_CONFIG="+config, "CREWSHIP_SERVER=", "CREWSHIP_WORKSPACE=", "CREWSHIP_PROFILE=", "CREWSHIP_TOKEN=", "NO_COLOR=1")
		out, err := command.CombinedOutput()
		if (err == nil) != (want < 400) {
			t.Fatalf("%s %v: error=%v output=%s", user, args, err, out)
		}
		mu.Lock()
		defer mu.Unlock()
		if len(observed) == 0 || observed[len(observed)-1].status != want {
			t.Fatalf("%v: observed=%v, want final HTTP %d", args, observed, want)
		}
		if args[1] == "shared-delete" {
			writes := 0
			for _, call := range observed {
				if strings.Contains(call.path, "/restricted-issues/") {
					if call.method != "DELETE" {
						t.Fatal("deletion fetched a new snapshot implicitly")
					}
					writes++
				}
			}
			if writes != 1 {
				t.Fatalf("deletion issued %d writes", writes)
			}
		}
		if user == "delete-a" && strings.Contains(string(out), "CLIENT_PRIVATE_b") {
			t.Fatal("foreign issue title leaked")
		}
		return string(out)
	}
	preview := func(user, issue string) sharedIssueDeletion {
		t.Helper()
		var result sharedIssueDeletion
		if err := json.Unmarshal([]byte(run(user, 200, "issue", "shared-delete-preview", issue)), &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	remove := func(version string, want int) {
		t.Helper()
		run("delete-a", want, "issue", "shared-delete", "delete-issue-a", "--project", "delete-project-a", "--version", version, "--confirm")
	}
	// Manage without the separate destructive share cannot preview or delete.
	run("delete-a", 404, "issue", "shared-delete-preview", "delete-issue-a")
	remove(strings.Repeat("a", 64), 404)
	for _, suffix := range []string{"a", "b"} {
		grant("delete-"+suffix, "delete-project-"+suffix, "read", "manage", "delete_issues")
	}
	initial := preview("delete-a", "delete-issue-a")
	if !initial.CanDelete || initial.ProjectID != "delete-project-a" {
		t.Fatal(initial)
	}
	if !preview("delete-b", "delete-issue-b").CanDelete {
		t.Fatal("positive other-client control failed")
	}
	run("delete-a", 404, "issue", "shared-delete-preview", "delete-issue-b")
	sql(`UPDATE missions SET aggregate_revision=aggregate_revision+1 WHERE id='delete-issue-a'`)
	remove(initial.Version, 409)
	// Credential narrowing changes the affordance and rejects a stale save.
	current := preview("delete-a", "delete-issue-a")
	sql(`UPDATE cli_tokens SET scopes='["agents:write"]' WHERE user_id='delete-a'`)
	if preview("delete-a", "delete-issue-a").CanDelete {
		t.Fatal("narrow credential advertised destructive authority")
	}
	remove(current.Version, 403)
	sql(`UPDATE cli_tokens SET scopes=NULL WHERE user_id='delete-a'`)
	// A superseded issue execution does not make its linked live run idle.
	sql(`INSERT INTO pipelines(id,workspace_id,slug,name,definition_json,definition_hash) VALUES('delete-pipeline','delete-ws','delete-pipeline','Fixture','{"name":"fixture","steps":[]}','fixture-hash')`)
	sql(`INSERT INTO pipeline_runs(id,workspace_id,pipeline_id,pipeline_slug,status,mode,started_at,step_outputs_json,cost_usd,duration_ms,triggered_via,inputs_json,created_at,updated_at) VALUES('delete-run','delete-ws','delete-pipeline','delete-pipeline','queued','run',datetime('now'),'{}',0,0,'manual','{}',datetime('now'),datetime('now'))`)
	sql(`INSERT INTO issue_executions(id,mission_id,work_revision,brief_revision,stage,reviewer_agent_id,routine_run_id,created_at,updated_at) VALUES('delete-execution','delete-issue-a',0,0,'superseded','delete-lead','delete-run',datetime('now'),datetime('now'))`)
	for _, status := range []string{"queued", "running", "waiting"} {
		sql(`UPDATE pipeline_runs SET status=? WHERE id='delete-run'`, status)
		active := preview("delete-a", "delete-issue-a")
		if active.CanDelete {
			t.Fatalf("run %s offered deletion", status)
		}
		remove(active.Version, 409)
		var retained string
		if err := db.QueryRow(`SELECT status FROM pipeline_runs WHERE id='delete-run'`).Scan(&retained); err != nil || retained != status {
			t.Fatalf("denied deletion altered run: %s %v", retained, err)
		}
	}
	sql(`UPDATE pipeline_runs SET status='completed' WHERE id='delete-run'`)
	current = preview("delete-a", "delete-issue-a")
	if !current.CanDelete {
		t.Fatal("completed work still blocked disposable issue")
	}
	sql(`CREATE TRIGGER reject_cli_issue_delete_audit BEFORE INSERT ON journal_entries WHEN NEW.summary='Guest deleted a project issue' BEGIN SELECT RAISE(ABORT,'fixture audit failure'); END`)
	remove(current.Version, 500)
	if preview("delete-a", "delete-issue-a").Version != current.Version {
		t.Fatal("audit failure changed deletion snapshot")
	}
	sql(`DROP TRIGGER reject_cli_issue_delete_audit`)
	remove(current.Version, 204)
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM missions WHERE id='delete-issue-a'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("deleted issue retained: %d %v", count, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM missions WHERE id='delete-issue-b' AND title='CLIENT_PRIVATE_b'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("other client changed: %d %v", count, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM journal_entries WHERE actor_id='delete-a' AND summary='Guest deleted a project issue'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("unexpected destructive audit count: %d %v", count, err)
	}
	// None of these known-ID operations introduced List or runtime authority.
	if err := store.Check(t.Context(), "delete-a", "delete-ws", access.Right{Kind: "project", ID: "delete-project-a", Operation: "list"}); err == nil {
		t.Fatal("destructive share implicitly granted List")
	}
	request, err := http.NewRequest(http.MethodGet, server.URL+"/api/v1/journal", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+tokens["delete-a"])
	request.Header.Set("X-Workspace-ID", "delete-ws")
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 404 {
		t.Fatalf("destructive share opened private journal: %d", response.StatusCode)
	}
}
