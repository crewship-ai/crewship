//go:build linux && (docs_foundation || conformance)

package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
	"github.com/crewship-ai/crewship/internal/chataudience"
	"github.com/crewship-ai/crewship/internal/documents"
	"github.com/crewship-ai/crewship/internal/sidecar"
)

// Compiled CLI -> actual sidecar -> actual public Router -> matched Node ->
// independent Go authority/SQLite. No durable response or computation mocks.
func exerciseDocumentStructureCLI(t *testing.T, h *PageHandler, workspace, user, human, root, nodeSocket, tokenFile, jwtSecret, dir string, call func(string, string, string, map[string]any, int) map[string]any) {
	t.Helper()
	const page, slug = "structure-cli-page", "structure-cli"
	wmSeedPage(t, h.db, workspace, page, slug, "Structural document", user, "")
	execOrFatal(t, h.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id,granted_at) VALUES(?,'agent','artifact-agent','write',?,strftime('%Y-%m-%dT%H:%M:%SZ','now'))`, page, user)
	blocks := []any{}
	for _, id := range []string{"outside", "first", "second", "third", "tail"} {
		blocks = append(blocks, map[string]any{"type": "paragraph", "attrs": map[string]any{"id": id}, "content": []any{map[string]any{"type": "text", "text": id + " text"}}})
	}
	call(page, "initialize", human, map[string]any{"id": "structure-seed", "schema": 3, "page_id": page, "body": map[string]any{"type": "doc", "content": blocks}}, 200)
	router, err := NewRouter(h.db, jwtSecret, newTestLogger(), WithInternalToken(h.artifactMasterToken), WithDocumentAuthority(filepath.Join(dir, "cli-authority.sock"), tokenFile), WithDocumentService(nodeSocket))
	if err != nil {
		t.Fatal(err)
	}
	defer router.Shutdown()
	host := httptest.NewServer(router)
	defer host.Close()
	binary := filepath.Join(t.TempDir(), "crewship-structural-cli")
	build := exec.CommandContext(t.Context(), "go", "build", "-o", binary, "./cmd/crewship")
	build.Dir = root
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("actual structural CLI build: %v %s", err, output)
	}
	read := func() map[string]any { return call(page, "read", human, map[string]any{}, 200) }
	// A comment-bearing move must reject the entire preceding valid insert too.
	state := read()
	call(page, "comment", human, map[string]any{"id": "structure-comment", "schema": 3, "page_id": page, "epoch": state["epoch"], "ifRev": state["rev"], "block": "second", "from": 0, "to": 6, "text": "Keep this anchor"}, 200)
	for index, scenario := range []string{"comment move", "human change", "success"} {
		base := read()
		run := fmt.Sprintf("structure-cli-run-%d", index)
		execOrFatal(t, h.db, `INSERT INTO artifact_run_principals(run_id,workspace_id,agent_id,crew_id,human_user_id,chat_id,message_id,member_id,member_revision,chat_generation,chat_revision,expires_at) SELECT ?,workspace_id,agent_id,crew_id,human_user_id,chat_id,message_id,member_id,member_revision,chat_generation,chat_revision,expires_at FROM artifact_run_principals WHERE run_id='artifact-run'`, run)
		execOrFatal(t, h.db, `INSERT INTO journal_entries(id,workspace_id,crew_id,agent_id,entry_type,actor_type,summary,trace_id) VALUES(?,?,'artifact-crew','artifact-agent','run.started','system','fixture',?)`, run+"-started", workspace, run)
		key := internaltoken.DeriveAgentRunKey(h.artifactMasterToken, workspace, "artifact-crew")
		token := internaltoken.DeriveAgentRunToken(key, workspace, "artifact-agent", run)
		input := map[string]any{"page_id": page, "chat_id": "artifact-chat", "instruction": "Reorganize only these whole blocks", "base_rev": base["rev"], "submission_id": run, "structure": map[string]any{"block_ids": []string{"first", "second", "third"}, "allow_insert_after": true}}
		req := pagesGrantRequest(t, "POST", "/", workspace, user, "OWNER", string(mustDocumentsJSON(t, input)))
		req.SetPathValue("slug", slug)
		req.Header.Set("Authorization", "Bearer "+human)
		UserFromContext(req.Context()).SessionID = "docs-session"
		queued := httptest.NewRecorder()
		h.CreateDocumentRequest(queued, req)
		if queued.Code != 202 {
			t.Fatalf("actual structural admission: %d %s", queued.Code, queued.Body.String())
		}
		var job DocumentRequestCreateResponse
		if json.Unmarshal(queued.Body.Bytes(), &job) != nil {
			t.Fatal("invalid admission response")
		}
		execOrFatal(t, h.db, `UPDATE document_requests SET status='running' WHERE id=?`, job.ID)
		trusted, err := chataudience.CaptureTrusted(t.Context(), h.db, "artifact-chat", user, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err = bindDocumentRequestRun(httptest.NewRequest("POST", "/", nil), h.db, job.ID, run, trusted); err != nil {
			t.Fatal(err)
		}
		proxy := sidecar.NewServer(sidecar.ServerConfig{Addr: "127.0.0.1:9119", IPC: &sidecar.IPCConfig{BaseURL: host.URL, Token: internaltoken.DeriveCrewToken(h.artifactMasterToken, workspace, "artifact-crew"), AgentID: "artifact-agent", AgentSlug: "artifact-agent", CrewID: "artifact-crew", WorkspaceID: workspace, RunID: run, RunChatID: "artifact-chat", AgentRunKey: key}})
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan error, 1)
		go func() { done <- proxy.Start(ctx) }()
		select {
		case <-proxy.Ready():
		case err := <-done:
			cancel()
			t.Fatalf("owned fixed protected sidecar could not start: %v", err)
		case <-time.After(5 * time.Second):
			cancel()
			t.Fatal("owned sidecar startup timed out")
		}
		func() {
			defer func() {
				cancel()
				select {
				case <-done:
				case <-time.After(5 * time.Second):
					t.Error("owned sidecar did not stop")
				}
			}()
			cli := func(stdin string, wantSuccess bool, args ...string) map[string]any {
				t.Helper()
				cmd := exec.CommandContext(t.Context(), binary, append([]string{"doc", "agent"}, args...)...)
				cmd.Env = append(os.Environ(), "CREWSHIP_AGENT_TOKEN="+token)
				cmd.Stdin = strings.NewReader(stdin)
				output, err := cmd.CombinedOutput()
				if (err == nil) != wantSuccess {
					t.Fatalf("actual CLI %v: %v %s", args, err, output)
				}
				if !wantSuccess {
					return nil
				}
				var result map[string]any
				if json.Unmarshal(output, &result) != nil {
					t.Fatalf("actual CLI JSON: %s", output)
				}
				return result
			}
			mcp := func(name string) string {
				t.Helper()
				request, err := http.NewRequestWithContext(t.Context(), "POST", "http://127.0.0.1:9119/mcp/routines", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"`+name+`","arguments":{"slug":"`+slug+`"}}}`))
				if err != nil {
					t.Fatal(err)
				}
				request.Header.Set("Authorization", "Bearer "+token)
				response, err := http.DefaultClient.Do(request)
				if err != nil {
					t.Fatal(err)
				}
				defer response.Body.Close()
				output, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
				if err != nil || response.StatusCode != 200 || strings.Contains(string(output), `"isError":true`) {
					t.Fatalf("actual MCP %s: %d %v %s", name, response.StatusCode, err, output)
				}
				return string(output)
			}
			current := cli("", true, "read", slug)
			meta := current["agent_request"].(map[string]any)
			if meta["request_id"] != job.ID || meta["base_rev"] != base["rev"] || current["snapshot"] != nil {
				t.Fatal("CLI lost original context or exposed binary")
			}
			cli("", false, "checkpoint", slug)
			var premature int
			if err := h.db.QueryRow(`SELECT count(*) FROM document_checkpoints WHERE page_id=? AND id=?`, page, job.ID+"-checkpoint").Scan(&premature); err != nil || premature != 0 {
				t.Fatal("checkpoint created without successful own patch", err, premature)
			}
			insert := map[string]any{"op": "insert_after", "after_id": "first", "node": map[string]any{"type": "paragraph", "content": []any{map[string]any{"type": "text", "text": "Inserted through actual CLI"}}}}
			ops := []any{insert}
			switch scenario {
			case "comment move":
				ops = append(ops, map[string]any{"op": "move_before", "id": "second", "before_id": "first"})
				cli(string(mustDocumentsJSON(t, ops)), false, "apply", slug, "--operations-file", "-")
				if read()["rev"] != base["rev"] {
					t.Fatal("denied comment move committed first operation")
				}
				comments := call(page, "comments", human, map[string]any{}, 200)["comments"].([]any)
				if len(comments) != 1 || comments[0].(map[string]any)["status"] != "attached" {
					t.Fatal("denied move altered comment")
				}
			case "human change":
				fresh := read()
				target := fresh["outline"].([]any)[0].(map[string]any)
				call(page, "patch", human, map[string]any{"id": "structure-intervening-human", "schema": 3, "page_id": page, "epoch": fresh["epoch"], "ifRev": fresh["rev"], "target": "outside", "ifHash": target["hash"], "text": "Human changed outside"}, 200)
				cli(string(mustDocumentsJSON(t, ops)), false, "apply", slug, "--operations-file", "-")
				if read()["rev"] != fresh["rev"].(float64)+1 {
					t.Fatal("CLI refreshed immutable base after human edit")
				}
			default:
				if output := mcp("doc_read"); !strings.Contains(output, job.ID) || strings.Contains(output, "snapshot") {
					t.Fatal("actual MCP lost original context or exposed binary")
				}
				ops = append(ops, map[string]any{"op": "move_before", "id": "third", "before_id": "first"}, map[string]any{"op": "delete", "id": "second"})
				ack := cli(string(mustDocumentsJSON(t, ops)), true, "apply", slug, "--operations-file", "-")
				cli("", true, "checkpoint", slug)
				mcp("doc_checkpoint")
				// Force a real authority epoch renewal. Exact logical intent remains
				// durable; this does not grant a fresh write or a refreshed base.
				if err := documents.New(h.db, documents.Options{}).InvalidateLeases(t.Context()); err != nil {
					t.Fatal(err)
				}
				retry := cli(string(mustDocumentsJSON(t, ops)), true, "apply", slug, "--operations-file", "-")
				if retry["duplicate"] != true || retry["rev"] != ack["rev"] || read()["rev"] != ack["rev"] {
					t.Fatal("actual CLI retry duplicated write")
				}
				cli("", true, "checkpoint", slug)
				comments := call(page, "comments", human, map[string]any{}, 200)["comments"].([]any)
				if len(comments) != 1 || comments[0].(map[string]any)["status"] != "detached" {
					t.Fatal("deleted block's thread not retained detached")
				}
				content := read()["content"].(map[string]any)["content"].([]any)
				ids := []string{}
				for _, node := range content {
					ids = append(ids, node.(map[string]any)["attrs"].(map[string]any)["id"].(string))
				}
				if strings.Join(ids, ",") != "outside,third,first,"+job.ID+"-new-0,tail" {
					t.Fatal("actual structural order/IDs", ids)
				}
				execOrFatal(t, h.db, `UPDATE page_artifact_run_delegations SET revoked_at=strftime('%Y-%m-%dT%H:%M:%SZ','now') WHERE page_id=? AND run_id=?`, page, run)
				cli(string(mustDocumentsJSON(t, ops)), false, "apply", slug, "--operations-file", "-")
			}
		}()
	}
}
