//go:build linux && docs_foundation && docs_canvas_browser

package api

// Browser acceptance for agent proposals on a Canvas (schema 6) Doc (#5149).
// It uses the production chat/run/sidecar/Docs engine/browser pipeline. Only
// container execution is replaced by a synthetic subprocess that calls the
// real sidecar doc_draft MCP tools with its signed per-run bearer; no model
// endpoint is called and no model acceptance is claimed.
//
//	CREWSHIP_DOCS_CANVAS_BROWSER=1 go test -tags docs_foundation,docs_canvas_browser ./internal/api \
//	  -run '^TestDocumentCanvasProposalBrowser$' -count=1
//
// It needs Node 24, the frozen Docs installation and a production static
// frontend in ./out (pnpm build). CREWSHIP_DOCS_CANVAS_EVIDENCE names where
// the browser writes screenshots and videos.
import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
	"github.com/crewship-ai/crewship/internal/auth/sessions"
	"github.com/crewship-ai/crewship/internal/chatbridge"
	"github.com/crewship-ai/crewship/internal/conversation"
	"github.com/crewship-ai/crewship/internal/encryption"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/logcollector"
	"github.com/crewship-ai/crewship/internal/orchestrator"
	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/crewship-ai/crewship/internal/sidecar"
	"github.com/crewship-ai/crewship/internal/ws"
)

type canvasProposalSyntheticRuntime struct {
	mockContainerExec
	sidecarURL string
	starts     atomic.Int32
}

func (p *canvasProposalSyntheticRuntime) Exec(ctx context.Context, cfg provider.ExecConfig) (*provider.ExecResult, error) {
	if len(cfg.Env) == 0 {
		return &provider.ExecResult{ExecID: "synthetic-setup", Reader: io.NopCloser(strings.NewReader(""))}, nil
	}
	binary, err := os.Executable()
	if err != nil {
		return nil, err
	}
	p.starts.Add(1)
	cmd := exec.CommandContext(ctx, binary, "-test.run=^TestDocumentCanvasProposalSyntheticAgentProcess$")
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "CREWSHIP_DOCS_CANVAS_SYNTHETIC_CHILD=1", "CREWSHIP_DOCS_CANVAS_SYNTHETIC_MCP=" + p.sidecarURL}
	for _, env := range cfg.Env {
		if strings.HasPrefix(env, "CREWSHIP_AGENT_TOKEN=") {
			cmd.Env = append(cmd.Env, env)
		}
	}
	output, err := cmd.CombinedOutput()
	for _, env := range cmd.Env {
		if token, ok := strings.CutPrefix(env, "CREWSHIP_AGENT_TOKEN="); ok && token != "" && bytes.Contains(output, []byte(token)) {
			return nil, fmt.Errorf("synthetic subprocess leaked its run bearer; output withheld")
		}
	}
	if err != nil {
		return nil, fmt.Errorf("synthetic agent subprocess failed: %w: %s", err, output)
	}
	return &provider.ExecResult{ExecID: "synthetic-agent", Reader: io.NopCloser(bytes.NewReader(bytes.TrimSuffix(output, []byte("PASS\n"))))}, nil
}

type canvasSidecarAddress struct{ address chan string }

func (h *canvasSidecarAddress) Enabled(context.Context, slog.Level) bool { return true }
func (h *canvasSidecarAddress) Handle(_ context.Context, r slog.Record) error {
	if r.Message == "sidecar proxy started" {
		r.Attrs(func(a slog.Attr) bool {
			if a.Key == "addr" {
				h.address <- a.Value.String()
			}
			return true
		})
	}
	return nil
}
func (h *canvasSidecarAddress) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *canvasSidecarAddress) WithGroup(string) slog.Handler      { return h }

func TestDocumentCanvasProposalBrowser(t *testing.T) {
	if os.Getenv("CREWSHIP_DOCS_CANVAS_BROWSER") != "1" {
		t.Fatal("requires explicit CREWSHIP_DOCS_CANVAS_BROWSER=1")
	}
	repo, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	if version, e := exec.Command("node", "--version").Output(); e != nil || !strings.HasPrefix(string(version), "v24.") {
		t.Fatalf("Node24 required: %s %v", version, e)
	}
	if _, err = os.Stat(filepath.Join(repo, "out", "pages", "_.html")); err != nil {
		t.Fatal("build production static frontend first", err)
	}
	f := artifactProjectOriginalRunFixture(t)
	db := f.rig.db
	owner, workspace := f.a.HumanUserID, f.a.WorkspaceID
	mustExec(t, db, `UPDATE crews SET cached_image='synthetic-owned-agent-image' WHERE id=?`, f.a.CrewID)
	// Synthetic credentials never leave localhost. They satisfy the production
	// resolver/preflight; the subprocess receives only its run bearer.
	setTestEncryptionKey(t)
	encrypted, err := encryption.Encrypt("synthetic-never-dial-provider")
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, db, `INSERT INTO credentials(id,workspace_id,name,encrypted_value,type,provider,created_by) VALUES('synthetic-agent-key',?,'Synthetic fixture',?,'API_KEY','ANTHROPIC',?)`, workspace, encrypted, owner)
	mustExec(t, db, `INSERT INTO agent_credentials(id,agent_id,credential_id,env_var_name,priority) VALUES('synthetic-agent-binding',?,'synthetic-agent-key','ANTHROPIC_API_KEY',1)`, f.a.AgentID)
	const secret = "synthetic-docs-canvas-browser-secret-long-enough"
	const master = "synthetic-docs-canvas-browser-master"
	validator, err := auth.NewJWTValidator(secret)
	if err != nil {
		t.Fatal(err)
	}
	sessionStore := sessions.NewDBStore(db)
	session, err := sessionStore.Create(t.Context(), owner, "synthetic", "127.0.0.1", auth.RefreshTokenTTL)
	if err != nil {
		t.Fatal(err)
	}
	ownerToken, err := validator.IssueAccessToken(owner, session.ID, owner, owner+"@example.test")
	if err != nil {
		t.Fatal(err)
	}

	dir, err := os.MkdirTemp("", "doc-canvas-browser-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	serviceSecretFile := filepath.Join(dir, "secret")
	const serviceSecret = "synthetic-docs-canvas-browser-service-secret"
	if err := os.WriteFile(serviceSecretFile, []byte(serviceSecret), 0600); err != nil {
		t.Fatal(err)
	}
	authoritySocket, nodeSocket := filepath.Join(dir, "a.sock"), filepath.Join(dir, "n.sock")

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	hub := ws.NewHub(newTestLogger(), nil, validator, sessionStore)
	hub.SetChannelAuthorizer(ws.NewDBChannelAuthorizer(db))
	hubDone := make(chan struct{})
	go func() { hub.Run(ctx); close(hubDone) }()
	t.Cleanup(func() { cancel(); <-hubDone })
	runtime := &canvasProposalSyntheticRuntime{}
	orch := orchestrator.New(runtime, newInbandAsgState(), newTestLogger())
	orch.SetSidecarEnabled(true)
	writer := journal.NewWriter(db, newTestLogger(), journal.WriterOptions{FlushSize: 1})
	t.Cleanup(func() { _ = writer.Close() })
	store := conversation.NewStore(filepath.Join(dir, "transcript"), newTestLogger(), conversation.WithDB(db))
	t.Cleanup(store.Close)
	logs := logcollector.NewWriter(filepath.Join(dir, "logs"), newTestLogger())
	router, err := NewRouter(db, secret, newTestLogger(), WithInternalToken(master), WithHub(hub), WithJournal(writer), WithOrchestrator(orch), WithLogWriter(logs), WithStoragePath(filepath.Join(dir, "storage")), WithDocumentAuthority(authoritySocket, serviceSecretFile), WithDocumentService(nodeSocket))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(router.Shutdown)
	node := exec.Command("node", filepath.Join(repo, "tools/docs-service/service.mjs"))
	node.Env = append(os.Environ(), "CREWSHIP_DOCS_SOCKET="+nodeSocket, "CREWSHIP_DOCS_AUTHORITY_SOCKET="+authoritySocket, "CREWSHIP_DOCS_SERVICE_TOKEN_FILE="+serviceSecretFile)
	node.Stderr = os.Stderr
	if err := node.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = node.Process.Kill(); _ = node.Wait() })
	documentsWaitSocket(t, nodeSocket)
	documentsWaitReady(t, nodeSocket, serviceSecret)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /ws", hub.HandleUpgrade)
	mux.Handle("/api/", router)
	mux.Handle("/", StaticFileHandler(os.DirFS(filepath.Join(repo, "out"))))
	host := httptest.NewServer(mux)
	t.Cleanup(host.Close)
	orch.SetIPCConfig(host.URL, master)
	bridge := chatbridge.New(orch, runtime, store, logs, chatbridge.NewIPCResolver(host.URL, master, newTestLogger()), chatbridge.BridgeConfig{DocumentCreationEnabled: true}, newTestLogger())
	bridge.SetPageChatContextResolver(router.Pages())
	bridge.SetDocumentIterationRecorder(router.Pages())
	hub.SetChatHandler(bridge)
	address := &canvasSidecarAddress{address: make(chan string, 1)}
	proxy := sidecar.NewServer(sidecar.ServerConfig{Addr: "127.0.0.1:0", Logger: slog.New(address), IPC: &sidecar.IPCConfig{BaseURL: host.URL, Token: internaltoken.DeriveCrewToken(master, workspace, f.a.CrewID), WorkspaceID: workspace, CrewID: f.a.CrewID, AgentID: f.a.AgentID, AgentSlug: "principal-agent", AgentToken: internaltoken.DeriveAgentToken(master, workspace, f.a.AgentID), AgentRunKey: internaltoken.DeriveAgentRunKey(master, workspace, f.a.CrewID)}})
	proxyDone := make(chan error, 1)
	go func() { proxyDone <- proxy.Start(ctx) }()
	select {
	case addr := <-address.address:
		runtime.sidecarURL = "http://" + addr + "/mcp/routines"
	case err := <-proxyDone:
		t.Fatal(err)
	case <-time.After(10 * time.Second):
		t.Fatal("sidecar did not start")
	}
	t.Cleanup(func() { cancel(); <-proxyDone })

	fixture, _ := json.Marshal(map[string]any{"server": host.URL, "workspace": workspace, "chat": f.a.ChatID, "owner": owner, "agent": "principal-agent", "token": ownerToken})
	fixturePath := filepath.Join(dir, "fixture.json")
	if err = os.WriteFile(fixturePath, fixture, 0600); err != nil {
		t.Fatal(err)
	}
	evidence := filepath.Join(dir, "evidence")
	if base := os.Getenv("CREWSHIP_DOCS_CANVAS_EVIDENCE"); base != "" {
		evidence = base
	}
	command := exec.CommandContext(ctx, "node", filepath.Join(repo, "e2e", "docs-canvas-proposal.browser.mjs"))
	command.Dir = repo
	command.Env = append(os.Environ(), "CREWSHIP_DOCS_CANVAS_FIXTURE="+fixturePath, "CREWSHIP_DOCS_CANVAS_OUTPUT="+evidence)
	command.Stdout, command.Stderr = os.Stdout, os.Stderr
	if err = command.Run(); err != nil {
		t.Fatal("Canvas proposal browser acceptance", err)
	}
	if runtime.starts.Load() != 1 {
		t.Fatalf("want exactly one signed subprocess, got %d", runtime.starts.Load())
	}
	// The proposals trace to one admitted run bound to the mirrored original
	// human message and its schema-6 mandate.
	var n int
	err = db.QueryRow(`SELECT count(*) FROM document_proposals dp JOIN document_iteration_intents i ON i.run_id=dp.run_id AND i.schema_version=6 JOIN artifact_run_principals p ON p.run_id=i.run_id AND p.message_id=i.message_id JOIN conversation_messages m ON m.id=i.message_id AND m.author_user_id=p.human_user_id WHERE i.chat_id=? AND dp.schema_version=6`, f.a.ChatID).Scan(&n)
	if err != nil || n != 2 {
		t.Fatalf("two schema-6 proposals from the admitted human-bound run: count %d: %v", n, err)
	}
}

// The synthetic agent: one proposal per mandated block, through the real
// sidecar doc_draft MCP tools with the orchestrator's signed run bearer.
func TestDocumentCanvasProposalSyntheticAgentProcess(t *testing.T) {
	if os.Getenv("CREWSHIP_DOCS_CANVAS_SYNTHETIC_CHILD") != "1" {
		return // Only the signed subprocess invocation exercises this helper.
	}
	token := os.Getenv("CREWSHIP_AGENT_TOKEN")
	if !strings.HasPrefix(token, "agtv2.") {
		t.Fatal("orchestrator did not supply a signed per-run token")
	}
	call := func(name string, args any) json.RawMessage {
		raw, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": name, "arguments": args}})
		req, err := http.NewRequest("POST", os.Getenv("CREWSHIP_DOCS_CANVAS_SYNTHETIC_MCP"), bytes.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		client := &http.Client{Timeout: 20 * time.Second, Transport: &http.Transport{Proxy: nil}}
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var packet struct {
			Result struct {
				Content []struct {
					Text string `json:"text"`
				} `json:"content"`
				IsError bool `json:"isError"`
			} `json:"result"`
			Error json.RawMessage `json:"error"`
		}
		body, _ := io.ReadAll(res.Body)
		if res.StatusCode != 200 || json.Unmarshal(body, &packet) != nil || packet.Result.IsError || len(packet.Result.Content) != 1 || len(packet.Error) > 0 {
			t.Fatalf("MCP %s: status %d: %s", name, res.StatusCode, body)
		}
		return json.RawMessage(packet.Result.Content[0].Text)
	}
	view := call("doc_draft_view", map[string]any{})
	var parsed struct {
		Mode   string `json:"mode"`
		Schema int    `json:"schema"`
	}
	if json.Unmarshal(view, &parsed) != nil || parsed.Mode != "propose" || parsed.Schema != 6 {
		t.Fatalf("schema-6 propose view required: %s", view)
	}
	for _, p := range []struct{ target, text, id string }{
		{"lead", "Agree the launch date with sales by Friday.", "canvas-browser-lead"},
		{"other", "Second paragraph, rejected in the browser.", "canvas-browser-other"},
	} {
		prepared := call("doc_draft_prepare", map[string]any{"view": view, "target": p.target, "text": p.text, "mutation_id": p.id})
		var out struct {
			Packet json.RawMessage `json:"packet"`
		}
		if json.Unmarshal(prepared, &out) != nil || len(out.Packet) == 0 {
			t.Fatalf("prepare %s: %s", p.target, prepared)
		}
		proposed := call("doc_draft_propose", map[string]any{"packet": out.Packet})
		if !bytes.Contains(proposed, []byte(`"proposed"`)) {
			t.Fatalf("propose %s: %s", p.target, proposed)
		}
	}
	fmt.Println(`{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"Synthetic runner sent two proposals with the real doc_draft tools."}}}`)
	fmt.Println(`{"type":"result","subtype":"success","is_error":false,"num_turns":1,"total_cost_usd":0}`)
}
