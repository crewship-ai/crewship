//go:build linux && (artifact_project_browser || conformance)

package api

// This acceptance fixture uses the production chat/run/sidecar/storage/browser
// pipeline. Only container execution is replaced by a synthetic subprocess;
// no model endpoint is called and no paid-LLM acceptance is claimed.
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
	"sync"
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

type artifactSyntheticRuntime struct {
	mockContainerExec
	sidecarURL string
	starts     atomic.Int32
}

func (p *artifactSyntheticRuntime) Exec(ctx context.Context, cfg provider.ExecConfig) (*provider.ExecResult, error) {
	if len(cfg.Env) == 0 {
		return &provider.ExecResult{ExecID: "synthetic-setup", Reader: io.NopCloser(strings.NewReader(""))}, nil
	}
	binary, err := os.Executable()
	if err != nil {
		return nil, err
	}
	phase := p.starts.Add(1)
	cmd := exec.CommandContext(ctx, binary, "-test.run=^TestArtifactProjectSyntheticAgentProcess$")
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "CREWSHIP_ARTIFACT_SYNTHETIC_CHILD=1", "CREWSHIP_ARTIFACT_SYNTHETIC_MCP=" + p.sidecarURL, fmt.Sprintf("CREWSHIP_ARTIFACT_SYNTHETIC_PHASE=%d", phase)}
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

// Capture the production sidecar's selected ephemeral address without adding a
// production-only test accessor or reserving/racing an unrelated listener.
type artifactSidecarAddress struct{ address chan string }

func (h *artifactSidecarAddress) Enabled(context.Context, slog.Level) bool { return true }
func (h *artifactSidecarAddress) Handle(_ context.Context, r slog.Record) error {
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
func (h *artifactSidecarAddress) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *artifactSidecarAddress) WithGroup(string) slog.Handler      { return h }

func TestArtifactProjectAgentBrowser(t *testing.T) {
	if os.Getenv("CREWSHIP_ARTIFACT_PROJECT_BROWSER") != "1" {
		t.Fatal("requires explicit CREWSHIP_ARTIFACT_PROJECT_BROWSER=1")
	}
	t.Setenv("CREWSHIP_ARTIFACT_PROJECTS_ENABLED", "true")
	f := artifactProjectOriginalRunFixture(t)
	db := f.rig.db
	owner, workspace := f.a.HumanUserID, f.a.WorkspaceID
	mustExec(t, db, `UPDATE crews SET cached_image='synthetic-owned-agent-image' WHERE id=?`, f.a.CrewID)
	const reader = "artifact-agent-browser-reader"
	mustExec(t, db, `INSERT INTO users(id,email,full_name) VALUES(?,?,'Synthetic reader')`, reader, reader+"@example.test")
	mustExec(t, db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES(?,?,?,'MEMBER')`, generateCUID(), workspace, reader)
	// Credentials are synthetic and never leave localhost. They satisfy the real
	// production resolver/preflight; the subprocess receives only its run bearer.
	setTestEncryptionKey(t)
	encrypted, err := encryption.Encrypt("synthetic-never-dial-provider")
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, db, `INSERT INTO credentials(id,workspace_id,name,encrypted_value,type,provider,created_by) VALUES('synthetic-agent-key',?,'Synthetic fixture',?,'API_KEY','ANTHROPIC',?)`, workspace, encrypted, owner)
	mustExec(t, db, `INSERT INTO agent_credentials(id,agent_id,credential_id,env_var_name,priority) VALUES('synthetic-agent-binding',?,'synthetic-agent-key','ANTHROPIC_API_KEY',1)`, f.a.AgentID)
	const secret = "synthetic-artifact-agent-browser-secret-long-enough"
	const master = "synthetic-artifact-agent-browser-master"
	validator, err := auth.NewJWTValidator(secret)
	if err != nil {
		t.Fatal(err)
	}
	sessionStore := sessions.NewDBStore(db)
	tokens := map[string]string{}
	for _, user := range []string{owner, reader} {
		session, e := sessionStore.Create(t.Context(), user, "synthetic", "127.0.0.1", auth.RefreshTokenTTL)
		if e != nil {
			t.Fatal(e)
		}
		tokens[user], e = validator.IssueAccessToken(user, session.ID, user, user+"@example.test")
		if e != nil {
			t.Fatal(e)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	hub := ws.NewHub(newTestLogger(), nil, validator, sessionStore)
	hub.SetChannelAuthorizer(ws.NewDBChannelAuthorizer(db))
	hubDone := make(chan struct{})
	go func() { hub.Run(ctx); close(hubDone) }()
	t.Cleanup(func() { cancel(); <-hubDone })
	runtime := &artifactSyntheticRuntime{}
	orch := orchestrator.New(runtime, newInbandAsgState(), newTestLogger())
	orch.SetSidecarEnabled(true)
	writer := journal.NewWriter(db, newTestLogger(), journal.WriterOptions{FlushSize: 1})
	t.Cleanup(func() { _ = writer.Close() })
	dir := t.TempDir()
	store := conversation.NewStore(filepath.Join(dir, "transcript"), newTestLogger(), conversation.WithDB(db))
	t.Cleanup(store.Close)
	logs := logcollector.NewWriter(filepath.Join(dir, "logs"), newTestLogger())
	makeRouter := func() *Router {
		r, e := NewRouter(db, secret, newTestLogger(), WithInternalToken(master), WithHub(hub), WithJournal(writer), WithOrchestrator(orch), WithLogWriter(logs), WithStoragePath(filepath.Join(dir, "storage")))
		if e != nil {
			t.Fatal(e)
		}
		return r
	}
	current := makeRouter()
	var mu sync.RWMutex
	t.Cleanup(func() { mu.Lock(); defer mu.Unlock(); current.Shutdown() })
	repo, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(repo, "out", "pages", "_.html")); err != nil {
		t.Fatal("build production static frontend first", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /ws", hub.HandleUpgrade)
	mux.HandleFunc("POST /__fixture/reader", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Fixture-Control") != "synthetic-project-control" {
			w.WriteHeader(403)
			return
		}
		_, e := db.ExecContext(r.Context(), `INSERT OR REPLACE INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id,granted_at) SELECT id,'user',?,'read',?,datetime('now') FROM pages WHERE workspace_id=? AND slug='browser-artifact'`, reader, owner, workspace)
		if e != nil {
			w.WriteHeader(500)
			return
		}
		w.WriteHeader(204)
	})
	mux.HandleFunc("POST /__fixture/restart", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Fixture-Control") != "synthetic-project-control" {
			w.WriteHeader(403)
			return
		}
		next := makeRouter()
		mu.Lock()
		previous := current
		current = next
		mu.Unlock()
		previous.Shutdown()
		w.WriteHeader(204)
	})
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		mu.RLock()
		router := current
		mu.RUnlock()
		router.ServeHTTP(w, r)
	})
	mux.Handle("/", StaticFileHandler(os.DirFS(filepath.Join(repo, "out"))))
	host := httptest.NewServer(mux)
	t.Cleanup(host.Close)
	orch.SetIPCConfig(host.URL, master)
	bridge := chatbridge.New(orch, runtime, store, logs, chatbridge.NewIPCResolver(host.URL, master, newTestLogger()), chatbridge.BridgeConfig{ArtifactProjectsEnabled: true}, newTestLogger())
	bridge.SetPageChatContextResolver(current.Pages())
	bridge.SetArtifactProjectIntentRecorder(current.Pages())
	hub.SetChatHandler(bridge)
	address := &artifactSidecarAddress{address: make(chan string, 1)}
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
	fixture, _ := json.Marshal(map[string]any{"server": host.URL, "workspace": workspace, "chat": f.a.ChatID, "owner": owner, "reader": reader, "tokens": tokens})
	fixturePath := filepath.Join(dir, "fixture.json")
	if err = os.WriteFile(fixturePath, fixture, 0600); err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(ctx, "node", filepath.Join(repo, "e2e", "artifact-project-agent.browser.mjs"))
	evidence := filepath.Join(dir, "synthetic-agent-browser")
	if base := os.Getenv("CREWSHIP_ARTIFACT_AGENT_EVIDENCE"); base != "" {
		evidence = filepath.Join(base, "synthetic-agent-browser")
	}
	command.Env = append(os.Environ(), "CREWSHIP_ARTIFACT_PROJECT_FIXTURE="+fixturePath, "CREWSHIP_ARTIFACT_PROJECT_OUTPUT="+evidence)
	command.Stdout, command.Stderr = os.Stdout, os.Stderr
	if err = command.Run(); err != nil {
		t.Fatal("synthetic signed-agent browser acceptance", err)
	}
	if runtime.starts.Load() != 2 {
		t.Fatalf("want exactly two signed subprocesses, got %d", runtime.starts.Load())
	}
	// Prove the observed versions were produced by the admitted runs, and those
	// runs trace to mirrored original human messages and typed immutable intents.
	var n int
	err = db.QueryRow(`SELECT count(*) FROM artifact_project_message_intents i JOIN artifact_project_run_consents c ON c.run_id=i.run_id AND c.source_kind='chat' JOIN artifact_run_principals p ON p.run_id=i.run_id AND p.message_id=i.message_id JOIN conversation_messages m ON m.id=i.message_id AND m.author_user_id=p.human_user_id WHERE i.chat_id=? AND i.run_id IS NOT NULL`, f.a.ChatID).Scan(&n)
	if err != nil || n != 2 {
		t.Fatalf("two real mirrored/admitted human intents: count %d: %v", n, err)
	}
	err = db.QueryRow(`SELECT count(*) FROM artifact_versions v JOIN pages pg ON pg.id=v.page_id JOIN artifact_project_message_intents i ON v.actor='agent:'||?||':'||i.run_id WHERE pg.workspace_id=? AND pg.slug='browser-artifact' AND v.version IN (1,2)`, f.a.AgentID, workspace).Scan(&n)
	if err != nil || n != 2 {
		t.Fatalf("v1/v2 must carry the exact admitted agent-run actors: count %d: %v", n, err)
	}

}

func TestArtifactProjectSyntheticAgentProcess(t *testing.T) {
	if os.Getenv("CREWSHIP_ARTIFACT_SYNTHETIC_CHILD") != "1" {
		// Helper assertions run only in the child selected by the parent via env.
		return
	}
	token := os.Getenv("CREWSHIP_AGENT_TOKEN")
	if !strings.HasPrefix(token, "agtv2.") {
		t.Fatal("orchestrator did not supply a signed per-run token")
	}
	call := func(name string, args map[string]any) json.RawMessage {
		raw, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": name, "arguments": args}})
		req, err := http.NewRequest("POST", os.Getenv("CREWSHIP_ARTIFACT_SYNTHETIC_MCP"), bytes.NewReader(raw))
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
	files := func(heading string) []map[string]string {
		return []map[string]string{
			{"path": "index.html", "media_type": "text/html", "content": `<main><h1 id="hero-heading">` + heading + `</h1><p>One project, three connected screens.</p><a href="details.html">View details</a></main>`},
			{"path": "details.html", "media_type": "text/html", "content": `<h1>Details</h1><a href="settings.html">Open settings</a>`},
			{"path": "settings.html", "media_type": "text/html", "content": `<h1>Settings</h1><a href="index.html">Back to overview</a>`},
		}
	}
	if os.Getenv("CREWSHIP_ARTIFACT_SYNTHETIC_PHASE") == "1" {
		call("artifact_create", map[string]any{"slug": "browser-artifact", "name": "Signed agent browser artifact", "entrypoint": "index.html", "files": files("Version one"), "message": "Synthetic runner: initial three-screen project"})
	} else {
		call("artifact_versions", map[string]any{"slug": "browser-artifact", "version": 1})
		raw := call("artifact_comments", map[string]any{"slug": "browser-artifact", "version": 1})
		var comments struct {
			Comments []struct {
				ID       string `json:"id"`
				Quote    string `json:"quote"`
				Selector string `json:"selector"`
				Path     string `json:"path"`
			} `json:"comments"`
		}
		if json.Unmarshal(raw, &comments) != nil || len(comments.Comments) != 1 {
			t.Fatalf("expected exactly one anchored comment: %s", raw)
		}
		c := comments.Comments[0]
		if c.Quote != "Version one" || c.Selector != "#hero-heading" || c.Path != "index.html" {
			t.Fatalf("wrong selected anchor: %+v", c)
		}
		call("artifact_publish", map[string]any{"slug": "browser-artifact", "base_version": 1, "entrypoint": "index.html", "files": files("Version two"), "message": "Address selected heading comment"})
		call("artifact_reply", map[string]any{"slug": "browser-artifact", "comment_id": c.ID, "version": 2, "text": "Updated the selected heading in version 2."})
	}
	fmt.Println(`{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"Synthetic runner saved the project with real artifact MCP tools."}}}`)
	fmt.Println(`{"type":"result","subtype":"success","is_error":false,"num_turns":1,"total_cost_usd":0}`)
	// Return normally so TestMain closes its owned temporary database pool.
	// The synthetic runtime strips the Go harness PASS line from the JSONL.
}
