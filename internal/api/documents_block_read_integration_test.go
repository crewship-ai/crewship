//go:build linux && (docs_foundation || conformance)

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
	"github.com/crewship-ai/crewship/internal/auth/sessions"
	"github.com/crewship-ai/crewship/internal/chataudience"
	"github.com/crewship-ai/crewship/internal/sidecar"
)

func TestDocumentBlockReadManagedRouterAndSidecar(t *testing.T) {
	if os.Getenv("CREWSHIP_DOCUMENT_BLOCK_READ") != "1" {
		t.Fatal("requires explicit owned Node24/frozen Docs installation opt-in")
	}
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(root, "tools/docs-service/node_modules/yjs")); err != nil {
		t.Fatal(err)
	}
	version, err := exec.Command("node", "--version").Output()
	if err != nil || !strings.HasPrefix(string(version), "v24.") {
		t.Fatalf("Node24 required: %s %v", version, err)
	}
	h, workspace, owner, page, agent := artifactRunFixture(t)
	dir, err := os.MkdirTemp("", "doc-block-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	secretFile := filepath.Join(dir, "secret")
	const secret = "synthetic-document-block-service-secret-only"
	if err = os.WriteFile(secretFile, []byte(secret), 0600); err != nil {
		t.Fatal(err)
	}
	authoritySocket, nodeSocket := filepath.Join(dir, "a.sock"), filepath.Join(dir, "n.sock")
	const jwtSecret = "synthetic-document-block-jwt-secret-only"
	router, err := NewRouter(h.db, jwtSecret, newTestLogger(), WithInternalToken(h.artifactMasterToken), WithStoragePath(h.artifactStoragePath), WithDocumentAuthority(authoritySocket, secretFile), WithDocumentService(nodeSocket))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(router.Shutdown)
	child := exec.Command("node", filepath.Join(root, "tools/docs-service/service.mjs"))
	child.Env = append(os.Environ(), "CREWSHIP_DOCS_SOCKET="+nodeSocket, "CREWSHIP_DOCS_AUTHORITY_SOCKET="+authoritySocket, "CREWSHIP_DOCS_SERVICE_TOKEN_FILE="+secretFile)
	child.Stderr = os.Stderr
	if err = child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = child.Process.Kill(); _ = child.Wait() })
	documentsWaitSocket(t, nodeSocket)
	documentsWaitReady(t, nodeSocket, secret)
	session, err := sessions.NewDBStore(h.db).Create(t.Context(), owner, "synthetic", "127.0.0.1", auth.RefreshTokenTTL)
	if err != nil {
		t.Fatal(err)
	}
	validator, err := auth.NewJWTValidator(jwtSecret)
	if err != nil {
		t.Fatal(err)
	}
	human, err := validator.IssueAccessToken(owner, session.ID, "Block read owner", "block@example.test")
	if err != nil {
		t.Fatal(err)
	}
	host := httptest.NewServer(router)
	t.Cleanup(host.Close)
	client := &http.Client{Timeout: 20 * time.Second}
	request := func(base, token, method, path string, body any) ([]byte, int, error) {
		var data []byte
		if body != nil {
			var e error
			data, e = json.Marshal(body)
			if e != nil {
				return nil, 0, e
			}
		}
		req, e := http.NewRequestWithContext(t.Context(), method, base+path, bytes.NewReader(data))
		if e != nil {
			return nil, 0, e
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Workspace-ID", workspace)
		response, e := client.Do(req)
		if e != nil {
			return nil, 0, e
		}
		defer response.Body.Close()
		raw, e := io.ReadAll(io.LimitReader(response.Body, (4<<20)+1))
		return raw, response.StatusCode, e
	}
	call := func(base, token, method, path string, body any, want int) []byte {
		t.Helper()
		raw, status, e := request(base, token, method, path, body)
		if e != nil || status != want {
			t.Fatalf("%s %s: status%d want%d err%v body%s", method, path, status, want, e, raw)
		}
		return raw
	}
	endpoint := "/api/v1/pages/report/document"
	body := map[string]any{"type": "doc", "content": []any{
		map[string]any{"type": "paragraph", "attrs": map[string]any{"id": "leaf"}, "content": []any{map[string]any{"type": "text", "text": "Original selected plaintext"}}},
		map[string]any{"type": "paragraph", "attrs": map[string]any{"id": "other"}, "content": []any{map[string]any{"type": "text", "text": "unrelated-private-canary"}}},
		map[string]any{"type": "codeBlock", "attrs": map[string]any{"id": "code"}, "content": []any{map[string]any{"type": "text", "text": "const literal = '<script>';"}}},
	}}
	body["content"] = append(body["content"].([]any), map[string]any{"type": "bulletList", "attrs": map[string]any{"id": "list"}, "content": []any{map[string]any{"type": "listItem", "attrs": map[string]any{"id": "item"}, "content": []any{map[string]any{"type": "paragraph", "attrs": map[string]any{"id": "list-leaf"}, "content": []any{map[string]any{"type": "text", "text": "List text"}}}}}}})
	call(host.URL, human, "POST", endpoint+"/initialize", map[string]any{"id": "block-init", "schema": 3, "page_id": page, "body": body}, 200)
	read := call(host.URL, human, "GET", endpoint, nil, 200)
	var state DocumentStateResponse
	if json.Unmarshal(read, &state) != nil {
		t.Fatal("invalid initial state")
	}
	var originalHash string
	for _, block := range state.Outline {
		if block.ID == "leaf" {
			originalHash = block.Hash
		}
	}
	raw := call(host.URL, human, "GET", endpoint+"?block_id=leaf", nil, 200)
	var selected DocumentBlockReadResponse
	if json.Unmarshal(raw, &selected) != nil || selected.PageID != page || selected.Schema != 3 || selected.Rev != 0 || selected.Epoch != state.Epoch || selected.Block.Hash != originalHash || selected.Block.Text != "Original selected plaintext" || bytes.Contains(raw, []byte("unrelated-private-canary")) || bytes.Contains(raw, []byte(`"snapshot"`)) {
		t.Fatalf("unbound/unrelated block response: %s", raw)
	}
	call(host.URL, human, "GET", endpoint+"?block_id=code", nil, 200)
	call(host.URL, human, "GET", endpoint+"?block_id=missing", nil, 404)
	call(host.URL, human, "GET", endpoint+"?block_id=list", nil, 422)
	for _, query := range []string{"?block_id=", "?block_id=leaf&block_id=other", "?block_id=leaf&atRev=0"} {
		call(host.URL, human, "GET", endpoint+query, nil, 400)
	}
	call(host.URL, human, "POST", endpoint+"/patch?block_id=leaf", map[string]any{}, 400)
	// The real sidecar forwards the authenticated run token to this actual Router.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	_ = listener.Close()
	side := sidecar.NewServer(sidecar.ServerConfig{Addr: addr, Logger: newTestLogger(), IPC: &sidecar.IPCConfig{BaseURL: host.URL, Token: internaltoken.DeriveCrewToken(h.artifactMasterToken, workspace, "artifact-crew"), WorkspaceID: workspace, CrewID: "artifact-crew", AgentID: "artifact-agent", AgentSlug: "artifact-agent", AgentToken: internaltoken.DeriveAgentToken(h.artifactMasterToken, workspace, "artifact-agent"), AgentRunKey: internaltoken.DeriveAgentRunKey(h.artifactMasterToken, workspace, "artifact-crew"), RunID: "artifact-run", RunChatID: "artifact-chat"}})
	sideCtx, stopSide := context.WithCancel(context.Background())
	sideDone := make(chan error, 1)
	go func() { sideDone <- side.Start(sideCtx) }()
	t.Cleanup(func() {
		stopSide()
		select {
		case e := <-sideDone:
			if e != nil {
				t.Errorf("sidecar stop: %v", e)
			}
		case <-time.After(5 * time.Second):
			t.Error("sidecar stop timed out")
		}
	})
	select {
	case <-side.Ready():
	case e := <-sideDone:
		t.Fatalf("sidecar start: %v", e)
	case <-time.After(5 * time.Second):
		t.Fatal("sidecar ready timed out")
	}
	sideURL := "http://" + addr
	call(sideURL, agent, "GET", endpoint+"?block_id=leaf&workspace_id="+workspace, nil, 200)
	call(sideURL, agent, "GET", endpoint+"?block_id=leaf&workspace_id=wrong", nil, 400)
	// A valid run capability never substitutes for its authenticated crew realm.
	execOrFatal(t, h.db, `INSERT INTO crews(id,workspace_id,name,slug) VALUES('block-other-crew',?,'Other crew','block-other-crew')`, workspace)
	execOrFatal(t, h.db, `INSERT INTO workspaces(id,name,slug) VALUES('block-other-workspace','Other workspace','block-other-workspace')`)
	execOrFatal(t, h.db, `INSERT INTO crews(id,workspace_id,name,slug) VALUES('block-foreign-crew','block-other-workspace','Foreign crew','block-foreign-crew')`)
	for _, realm := range []struct{ workspace, crew string }{{workspace, "block-other-crew"}, {"block-other-workspace", "block-foreign-crew"}} {
		req, e := http.NewRequestWithContext(t.Context(), "GET", host.URL+"/api/v1/internal/documents/report?block_id=leaf", nil)
		if e != nil {
			t.Fatal(e)
		}
		req.Header.Set("Authorization", "Bearer "+agent)
		req.Header.Set("X-Internal-Token", internaltoken.DeriveCrewToken(h.artifactMasterToken, realm.workspace, realm.crew))
		req.Header.Set("X-Workspace-ID", workspace)
		response, e := client.Do(req)
		if e != nil {
			t.Fatal(e)
		}
		denied, e := io.ReadAll(io.LimitReader(response.Body, 4096))
		response.Body.Close()
		if e != nil || response.StatusCode != 403 || bytes.Contains(denied, []byte("Original selected")) {
			t.Fatalf("cross-realm run read=%d %v %s", response.StatusCode, e, denied)
		}
	}
	// Exercise configured CLI HTTP forwarding with this synthetic run bearer.
	// This is not the separate protected agent-runtime credential loader.
	// The existing general client injects workspace_id for POST too.
	cliBinary := filepath.Join(dir, "crewship-cli")
	build := exec.CommandContext(t.Context(), "go", "build", "-o", cliBinary, "./cmd/crewship")
	build.Dir = root
	if output, e := build.CombinedOutput(); e != nil {
		t.Fatalf("owned CLI build: %v %s", e, output)
	}
	cliConfig := filepath.Join(dir, "cli.yaml")
	if e := os.WriteFile(cliConfig, []byte(fmt.Sprintf("server: %s\ntoken: %s\nworkspace: %s\n", sideURL, agent, workspace)), 0600); e != nil {
		t.Fatal(e)
	}
	cli := func(payload any, args ...string) []byte {
		t.Helper()
		data, e := json.Marshal(payload)
		if e != nil {
			t.Fatal(e)
		}
		command := exec.CommandContext(t.Context(), cliBinary, args...)
		command.Dir = root
		command.Stdin = bytes.NewReader(data)
		for _, value := range os.Environ() {
			if !strings.HasPrefix(value, "CREWSHIP_") {
				command.Env = append(command.Env, value)
			}
		}
		command.Env = append(command.Env, "CREWSHIP_CONFIG="+cliConfig, "CREWSHIP_NO_SLUG_CACHE=1")
		output, e := command.CombinedOutput()
		if e != nil {
			t.Fatalf("actual CLI %s: %v %s", strings.Join(args, " "), e, output)
		}
		return output
	}
	cli(map[string]any{"id": "block-cli-patch", "schema": 3, "page_id": page, "epoch": state.Epoch, "target": "leaf", "text": "Original selected plaintext!", "ifRev": state.Rev, "ifHash": originalHash}, "doc", "patch", "report", "--file", "-")
	read = call(host.URL, human, "GET", endpoint, nil, 200)
	if json.Unmarshal(read, &state) != nil {
		t.Fatal("invalid state after CLI patch")
	}
	for _, block := range state.Outline {
		if block.ID == "leaf" {
			originalHash = block.Hash
		}
	}
	if state.Rev != 1 || originalHash == selected.Block.Hash {
		t.Fatal("actual CLI patch did not persist a changed revision/hash")
	}
	cli(map[string]any{"id": "block-cli-checkpoint", "schema": 3, "page_id": page, "epoch": state.Epoch, "ifRev": state.Rev}, "doc", "checkpoint", "report", "--file", "-")
	cliRead := cli(nil, "doc", "read", "report", "--block", "leaf")
	var cliBlock DocumentBlockReadResponse
	if json.Unmarshal(cliRead, &cliBlock) != nil || cliBlock.PageID != page || cliBlock.Rev != state.Rev || cliBlock.Block.Hash != originalHash || cliBlock.Block.Text != "Original selected plaintext!" {
		t.Fatal("actual CLI current block lost source binding")
	}
	// Admit a real immutable selection for the same existing live run.
	admission := map[string]any{"page_id": page, "chat_id": "artifact-chat", "instruction": "Change only selected text", "base_rev": state.Rev, "submission_id": "block-selected-request", "targets": []any{map[string]any{"id": "leaf", "hash": originalHash, "from": 0, "to": 8, "quote": "Original"}}}
	created := call(host.URL, human, "POST", endpoint+"/requests", admission, 202)
	var job DocumentRequestCreateResponse
	if json.Unmarshal(created, &job) != nil || job.ID == "" {
		t.Fatalf("request admission: %s", created)
	}
	execOrFatal(t, h.db, `UPDATE document_requests SET status='running' WHERE id=?`, job.ID)
	trusted, err := chataudience.CaptureTrusted(t.Context(), h.db, "artifact-chat", owner, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = bindDocumentRequestRun(httptest.NewRequest("POST", "/", nil), h.db, job.ID, "artifact-run", trusted); err != nil {
		t.Fatal(err)
	}
	call(sideURL, agent, "GET", endpoint+"?block_id=leaf", nil, 200)
	// Existing selected-read ceiling is unchanged: a read does not grant mutation.
	call(sideURL, agent+"bad", "GET", endpoint+"?block_id=leaf", nil, 403)
	original := router.pages.documentCall
	var injection func()
	router.pages.documentCall = func(ctx context.Context, ws, bearer, id, operation string, payload any) ([]byte, int, error) {
		raw, status, e := original(ctx, ws, bearer, id, operation, payload)
		if e == nil && status == 200 && operation == "read" && injection != nil {
			run := injection
			injection = nil
			run()
		}
		return raw, status, e
	}
	// Change canonical content through a real human CRDT HTTP commit after read.
	injection = func() {
		fresh := call(host.URL, human, "GET", endpoint, nil, 200)
		var current DocumentStateResponse
		_ = json.Unmarshal(fresh, &current)
		update, e := documentBlockHumanUpdate(t.Context(), root, fresh)
		if e != nil {
			t.Fatal(e)
		}
		call(host.URL, human, "POST", endpoint+"/patch", map[string]any{"id": "block-human-race", "schema": 3, "page_id": page, "epoch": current.Epoch, "kind": "human", "update": update}, 200)
	}
	raced := call(host.URL, human, "GET", endpoint+"?block_id=leaf", nil, 409)
	if bytes.Contains(raced, []byte("Original selected")) {
		t.Fatal("stale block body leaked")
	}
	call(host.URL, human, "GET", endpoint+"?block_id=leaf", nil, 200)
	// textOf returns complete Unicode plaintext, never a summarized outline.
	for _, size := range []int{maxDocumentBlockTextBytes, maxDocumentBlockTextBytes + 1} {
		slug := "block-limit-" + fmt.Sprint(size)
		created := call(host.URL, human, "POST", "/api/v1/pages", map[string]any{"slug": slug, "name": "Block text limit", "panels": []any{}, "allow_empty": true}, 201)
		var pageInfo struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(created, &pageInfo) != nil {
			t.Fatal("invalid boundary page")
		}
		text := strings.Repeat("é", maxDocumentBlockTextBytes/2-2) + "TAIL"
		if size > maxDocumentBlockTextBytes {
			text += "x"
		}
		body := map[string]any{"type": "doc", "content": []any{map[string]any{"type": "paragraph", "attrs": map[string]any{"id": "bounded"}, "content": []any{map[string]any{"type": "text", "text": text}}}}}
		path := "/api/v1/pages/" + slug + "/document"
		call(host.URL, human, "POST", path+"/initialize", map[string]any{"id": "init-" + slug, "schema": 3, "page_id": pageInfo.ID, "body": body}, 200)
		want := 200
		if size > maxDocumentBlockTextBytes {
			want = 422
		}
		result := call(host.URL, human, "GET", path+"?block_id=bounded", nil, want)
		if want == 200 {
			var block DocumentBlockReadResponse
			if json.Unmarshal(result, &block) != nil || block.Block.Text != text || len(block.Block.Text) != size {
				t.Fatal("full UTF8 boundary block was summarized or truncated")
			}
		}
	}
	// A valid source with repeated ancestor text can exceed the existing full
	// managed read envelope even when the selected block itself is very small.
	largeCreated := call(host.URL, human, "POST", "/api/v1/pages", map[string]any{"slug": "block-large-envelope", "name": "Large outline", "panels": []any{}, "allow_empty": true}, 201)
	var largePage struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(largeCreated, &largePage) != nil {
		t.Fatal("invalid envelope page")
	}
	var nested any = map[string]any{"type": "paragraph", "attrs": map[string]any{"id": "large-leaf"}, "content": []any{map[string]any{"type": "text", "text": strings.Repeat("x", 330000)}}}
	for i := 0; i < 10; i++ {
		nested = map[string]any{"type": "bulletList", "attrs": map[string]any{"id": fmt.Sprintf("large-list-%d", i)}, "content": []any{map[string]any{"type": "listItem", "attrs": map[string]any{"id": fmt.Sprintf("large-item-%d", i)}, "content": []any{map[string]any{"type": "paragraph", "attrs": map[string]any{"id": fmt.Sprintf("large-first-%d", i)}}, nested}}}}
	}
	largeBody := map[string]any{"type": "doc", "content": []any{map[string]any{"type": "paragraph", "attrs": map[string]any{"id": "tiny"}, "content": []any{map[string]any{"type": "text", "text": "Small selected block"}}}, nested}}
	largePath := "/api/v1/pages/block-large-envelope/document"
	call(host.URL, human, "POST", largePath+"/initialize", map[string]any{"id": "init-large-envelope", "schema": 3, "page_id": largePage.ID, "body": largeBody}, 200)
	tooLarge := call(host.URL, human, "GET", largePath+"?block_id=tiny", nil, 502)
	if !bytes.Contains(tooLarge, []byte("4 MiB")) || bytes.Contains(tooLarge, []byte("Small selected block")) {
		t.Fatal("oversized full envelope was hidden or truncated into success")
	}
	// Materialization cannot retain a selected run after its delegation is revoked.
	injection = func() {
		call(host.URL, human, "DELETE", "/api/v1/pages/report/artifact/delegations/artifact-run", nil, 204)
	}
	runDenied := call(sideURL, agent, "GET", endpoint+"?block_id=leaf", nil, 403)
	if bytes.Contains(runDenied, []byte("human replacement")) {
		t.Fatal("revoked run received materialized text")
	}
	var livePrincipal, revokedDelegation int
	if e := h.db.QueryRow(`SELECT count(*) FROM artifact_run_principals WHERE run_id='artifact-run'`).Scan(&livePrincipal); e != nil || livePrincipal != 1 {
		t.Fatal("run revocation also removed its principal")
	}
	if e := h.db.QueryRow(`SELECT count(*) FROM page_artifact_run_delegations WHERE page_id=? AND run_id='artifact-run' AND revoked_at IS NOT NULL`, page).Scan(&revokedDelegation); e != nil || revokedDelegation != 1 {
		t.Fatal("actual delegation revocation was not persisted")
	}
	call(host.URL, human, "GET", endpoint+"?block_id=leaf", nil, 200)
	// Revoke only the exact current human session after successful materialization.
	injection = func() {
		execOrFatal(t, h.db, `UPDATE user_sessions SET revoked_at=? WHERE id=?`, time.Now().UTC().Format(time.RFC3339Nano), session.ID)
	}
	denied := call(host.URL, human, "GET", endpoint+"?block_id=leaf", nil, 403)
	if bytes.Contains(denied, []byte("human replacement")) {
		t.Fatal("revoked session received block text")
	}
	var membership int
	if e := h.db.QueryRow(`SELECT count(*) FROM workspace_members WHERE workspace_id=? AND user_id=?`, workspace, owner).Scan(&membership); e != nil || membership != 1 {
		t.Fatal("session revocation also removed membership")
	}
	t.Log("actual managed Node24 + Router + real sidecar: original full block/hash/schema/epoch, code leaf, generic and selected run reads, invalid selectors, human CRDT source race, exact-session revocation; no model run")
}

func documentBlockHumanUpdate(ctx context.Context, root string, raw []byte) (string, error) {
	script := `import * as Y from 'yjs';import{load,decode,patch}from'./schema.mjs';let bytes='';for await(const c of process.stdin)bytes+=c;const state=JSON.parse(bytes),doc=load(state.snapshot);try{for(const update of state.updates||[])Y.applyUpdate(doc,decode(typeof update==='string'?update:update.update));const vector=Y.encodeStateVector(doc);patch(doc,{op:'replaceText',id:'leaf',text:'human replacement plaintext'},state.schema===1?2:state.schema);process.stdout.write(Buffer.from(Y.encodeStateAsUpdate(doc,vector)).toString('base64'));}finally{doc.destroy();}`
	command := exec.CommandContext(ctx, "node", "--input-type=module", "-e", script)
	command.Dir = filepath.Join(root, "tools/docs-service")
	command.Stdin = bytes.NewReader(raw)
	data, err := command.Output()
	if err != nil {
		return "", err
	}
	if len(data) > 1400000 || len(data) == 0 {
		return "", fmt.Errorf("invalid bounded synthetic human update")
	}
	return string(data), nil
}
