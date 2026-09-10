package sidecar

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/api"
	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
	"github.com/crewship-ai/crewship/internal/testutil"
)

// Real MCP transport -> sidecar identity -> crew-bound API middleware -> Git ->
// offline Docker compiler. No running installation or user API token is used.
func TestPageProjectMCPDockerIntegration(t *testing.T) {
	image := os.Getenv("CREWSHIP_TEST_PAGE_BUILD_IMAGE")
	if image == "" {
		// SKIP-WAIVER(#2472): pages-apps CI supplies the pinned image and requires this MCP integration test.
		t.Skip("requires a pinned local Pages tools image")
	}
	db := testutil.MigratedDB(t).DB
	for _, query := range []string{
		`INSERT INTO workspaces(id,name,slug) VALUES('ws-page','Pages','pages')`,
		`INSERT INTO crews(id,workspace_id,name,slug,autonomy_level) VALUES('crew-page','ws-page','Engineering','engineering','full')`,
		`INSERT INTO agents(id,workspace_id,crew_id,name,slug,agent_role,status,cli_adapter,tool_profile,timeout_seconds,memory_enabled) VALUES('agent-page','ws-page','crew-page','Lead','lead','LEAD','IDLE','CLAUDE_CODE','CODING',1800,0)`,
		`INSERT INTO agents(id,workspace_id,crew_id,name,slug,agent_role,status,cli_adapter,tool_profile,timeout_seconds,memory_enabled) VALUES('agent-peer','ws-page','crew-page','Peer','peer','WORKER','IDLE','CLAUDE_CODE','CODING',1800,0)`,
	} {
		if _, err := db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	master := strings.Repeat("m", 32)
	router, err := api.NewRouter(db, strings.Repeat("s", 32), pipelinesSilentLogger(), api.WithInternalToken(master), api.WithPageProjectsPath(t.TempDir()), api.WithPageBuildImage(image), api.WithPageRuntime("https://pages.example.net", "https://studio.example.com"))
	if err != nil {
		t.Fatal(err)
	}
	upstream := httptest.NewServer(router)
	defer upstream.Close()
	token := internaltoken.DeriveAgentToken(master, "ws-page", "agent-page")
	peerToken := internaltoken.DeriveAgentToken(master, "ws-page", "agent-peer")
	s := newRoutineMCPTestServer(t, &IPCConfig{BaseURL: upstream.URL, Token: internaltoken.DeriveCrewToken(master, "ws-page", "crew-page"), WorkspaceID: "ws-page", CrewID: "crew-page", AgentID: "agent-page", AgentSlug: "lead", AgentToken: token})
	s.crewMembers = []CrewMember{{ID: "agent-peer", Slug: "peer", AuthToken: peerToken}}
	mcp := httptest.NewServer(http.HandlerFunc(s.handleRoutinesMCP))
	defer mcp.Close()
	call := func(name string, args map[string]any, bearer string, wantError bool) map[string]any {
		t.Helper()
		body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": name, "arguments": args}})
		req, _ := http.NewRequest("POST", mcp.URL, strings.NewReader(string(body)))
		req.Header.Set("Content-Type", "application/json")
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var rpc struct {
			Result memoryMCPToolCallResult `json:"result"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&rpc); err != nil {
			t.Fatal(err)
		}
		if len(rpc.Result.Content) != 1 || rpc.Result.IsError != wantError {
			t.Fatalf("%s: HTTP %d %+v", name, resp.StatusCode, rpc.Result)
		}
		var result map[string]any
		if err := json.Unmarshal([]byte(rpc.Result.Content[0].Text), &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	args := map[string]any{"operation": "init", "slug": "health", "expected_revision": 0}
	call("page_project", args, "", true)
	call("page_project", args, "forged", true)
	call("save_page", map[string]any{"name": "Health", "panels": []map[string]any{{"id": "p1", "schema": "status.v1", "owner": "crew/engineering", "producer": "agent/lead", "sla_seconds": 30, "span": 4}}}, token, false)
	saved := call("page_project", args, token, false)
	if saved["revision"] != float64(1) || saved["git_commit"] == "" {
		t.Fatal(saved)
	}
	call("page_project", map[string]any{"operation": "save", "slug": "health", "expected_revision": 1, "files": []map[string]any{{"path": "src/note.txt", "encoding": "utf8", "content": "authored by peer"}}}, peerToken, false)
	call("page_project", map[string]any{"operation": "save", "slug": "health", "expected_revision": 1}, token, true)
	job := call("page_project", map[string]any{"operation": "build", "slug": "health", "expected_revision": 2}, peerToken, false)
	ready := false
	deadline := time.Now().Add(40 * time.Second)
	for time.Now().Before(deadline) {
		result := call("page_project", map[string]any{"operation": "status", "slug": "health"}, token, false)
		if _, ok := result["artifact"]; ok {
			t.Fatal("compiled code returned to agent")
		}
		build, ok := result["build"].(map[string]any)
		if !ok {
			t.Fatal(result)
		}
		if build["state"] == "ready" {
			ready = true
			break
		}
		if build["state"] != "running" {
			t.Fatal(build)
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !ready {
		t.Fatal("compiler did not finish")
	}
	call("page_project", map[string]any{"operation": "check", "slug": "health", "expected_revision": 2, "build_id": job["id"]}, token, false)
	call("page_project", map[string]any{"operation": "publish", "slug": "health", "expected_revision": 2, "build_id": job["id"]}, token, true)
	for _, query := range []string{
		`SELECT json_extract(actor_json,'$.agent_id') FROM page_project_revisions WHERE revision=2 AND actor_user_id IS NULL`,
		`SELECT json_extract(actor_json,'$.agent_id') FROM page_project_builds WHERE requested_by IS NULL`,
	} {
		var actor string
		if err := db.QueryRow(query).Scan(&actor); err != nil || actor != "agent-peer" {
			t.Fatalf("acting identity %q: %v", actor, err)
		}
	}
	var live int
	if err := db.QueryRow(`SELECT count(*) FROM page_project_live`).Scan(&live); err != nil || live != 0 {
		t.Fatalf("unexpected publication %d: %v", live, err)
	}
}
