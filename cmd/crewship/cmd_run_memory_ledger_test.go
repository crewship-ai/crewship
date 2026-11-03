//go:build linux

package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/crewship-ai/crewship/internal/api"
	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
	"github.com/crewship-ai/crewship/internal/auth/sessions"
	"github.com/crewship-ai/crewship/internal/chatbridge"
	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/conversation"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/logcollector"
	"github.com/crewship-ai/crewship/internal/orchestrator"
	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/crewship-ai/crewship/internal/provider/bbolt"
	"github.com/crewship-ai/crewship/internal/testutil"
	"github.com/crewship-ai/crewship/internal/work"
	"github.com/crewship-ai/crewship/internal/ws"
)

const cliMemoryMaster = "synthetic-cli-memory-host-token"

// The provider is the only fake: normal CLI command -> real authenticated WS
// -> real Bridge -> real IPC API -> real SQLite ledger and memory mutation.
// It attempts the actual host write while the simulated CLI process is live;
// no work authorization rows are inserted by the fixture.
type cliMemoryProvider struct {
	db       *sql.DB
	url      string
	mu       sync.Mutex
	run      string
	writeErr error
}

func (p *cliMemoryProvider) EnsureCrewRuntime(context.Context, provider.CrewConfig) (string, error) {
	return "memory-container", nil
}
func (p *cliMemoryProvider) StopCrewRuntime(context.Context, string) error   { return nil }
func (p *cliMemoryProvider) RemoveCrewRuntime(context.Context, string) error { return nil }
func (p *cliMemoryProvider) ContainerStatus(context.Context, string) (*provider.ContainerStatus, error) {
	return &provider.ContainerStatus{ID: "memory-container", State: "running"}, nil
}
func (p *cliMemoryProvider) Exec(_ context.Context, c provider.ExecConfig) (*provider.ExecResult, error) {
	if len(c.Env) == 0 {
		return &provider.ExecResult{ExecID: "setup", Reader: io.NopCloser(strings.NewReader(""))}, nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	var generation int64
	var phase string
	if err := p.db.QueryRow(`SELECT a.run_id,a.generation,a.runtime_phase FROM work_attempts a JOIN work_items w ON w.id=a.work_id WHERE w.agent_id='memory-agent' AND a.ended_at IS NULL`).Scan(&p.run, &generation, &phase); err != nil {
		p.writeErr = fmt.Errorf("normal CLI missing live memory attempt: %w", err)
		return nil, p.writeErr
	}
	if phase != "requested" && phase != "confirmed" {
		p.writeErr = fmt.Errorf("provider creation preceded durable request: %s", phase)
		return nil, p.writeErr
	}
	p.writeErr = p.mutate(p.run, generation, "live-write")
	if p.writeErr != nil {
		return nil, p.writeErr
	}
	transcript := `{"type":"system","subtype":"init","model":"test-model"}` + "\n" + `{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"memory written"}}}` + "\n" + `{"type":"result","subtype":"success","is_error":false,"num_turns":1}` + "\n"
	return &provider.ExecResult{ExecID: "memory-exec", Reader: io.NopCloser(strings.NewReader(transcript))}, nil
}
func (p *cliMemoryProvider) ExecInspect(context.Context, string) (bool, int, error) {
	return false, 0, nil
}
func (p *cliMemoryProvider) ContainerStats(context.Context, string) (*provider.ContainerMetrics, error) {
	return nil, fmt.Errorf("no stats")
}
func (p *cliMemoryProvider) CrewContainerName(_ string, slug string) string {
	return "crewship-team-" + slug
}
func (p *cliMemoryProvider) CopyToContainer(context.Context, string, string, io.Reader) error {
	return nil
}
func (p *cliMemoryProvider) mutate(run string, generation int64, operation string) error {
	body, _ := json.Marshal(map[string]any{"operation_id": operation, "scope": "agent", "file": "AGENT.md", "op": "append", "content": "cli-memory-proof\n", "run_id": run, "generation": generation})
	request, err := http.NewRequest("POST", p.url+"/api/v1/internal/memory/mutation", bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Internal-Token", internaltoken.DeriveCrewToken(cliMemoryMaster, "memory-w", "memory-crew"))
	request.Header.Set("X-Acting-Agent-Slug", "memory-agent")
	capability := internaltoken.DeriveAgentRunToken(internaltoken.DeriveAgentRunKey(cliMemoryMaster, "memory-w", "memory-crew"), "memory-w", "memory-agent", run)
	request.Header.Set("Authorization", "Bearer "+capability)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(response.Body, 8192))
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("memory mutation status=%d: %s", response.StatusCode, data)
	}
	return nil
}

func TestRunCmdNormalMemoryWritesUseCurrentLedgerAttempt(t *testing.T) {
	db := testutil.MigratedSQLDB(t)
	seed := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(query, args...); err != nil {
			t.Fatal(err)
		}
	}
	seed(`INSERT INTO users(id,email) VALUES('memory-owner','owner@memory.test')`)
	seed(`INSERT INTO workspaces(id,name,slug) VALUES('memory-w','Memory','memory-w')`)
	seed(`INSERT INTO workspace_members(id,user_id,workspace_id,role) VALUES('memory-member','memory-owner','memory-w','OWNER')`)
	seed(`INSERT INTO crews(id,workspace_id,name,slug,network_mode,container_memory_mb,container_cpus,cached_image) VALUES('memory-crew','memory-w','Memory','memory-crew','free',4096,2.0,'synthetic-memory-image')`)
	seed(`INSERT INTO agents(id,workspace_id,crew_id,name,slug,agent_role,status,cli_adapter,tool_profile,memory_enabled) VALUES('memory-agent','memory-w','memory-crew','Memory','memory-agent','AGENT','IDLE','CLAUDE_CODE','CODING',1)`)
	seed(`INSERT INTO chats(id,workspace_id,agent_id,created_by,visibility,origin) VALUES('memory-chat','memory-w','memory-agent','memory-owner','private','CLI')`)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	const secret = "synthetic-cli-memory-jwt-secret-long-enough"
	validator, err := auth.NewJWTValidator(secret)
	if err != nil {
		t.Fatal(err)
	}
	sessionStore := sessions.NewDBStore(db)
	session, err := sessionStore.Create(t.Context(), "memory-owner", "test", "127.0.0.1", auth.RefreshTokenTTL)
	if err != nil {
		t.Fatal(err)
	}
	token, err := validator.IssueAccessToken("memory-owner", session.ID, "Owner", "owner@memory.test")
	if err != nil {
		t.Fatal(err)
	}
	hub := ws.NewHub(logger, nil, validator, sessionStore)
	writer := journal.NewWriter(db, logger, journal.WriterOptions{FlushSize: 1})
	t.Cleanup(func() { _ = writer.Close() })
	root := t.TempDir()
	router, err := api.NewRouter(db, secret, logger, api.WithInternalToken(cliMemoryMaster), api.WithHub(hub), api.WithJournal(writer), api.WithStoragePath(root), api.WithMemoryVersionsBlobRoot(filepath.Join(root, "blobs")))
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", hub.HandleUpgrade)
	mux.Handle("/", router)
	server := httptest.NewServer(mux)
	defer server.Close()
	state, err := bbolt.New(filepath.Join(root, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	container := &cliMemoryProvider{db: db, url: server.URL}
	orch := orchestrator.New(container, state, logger)
	resolver := chatbridge.NewIPCResolver(server.URL, cliMemoryMaster, logger)
	bridge := chatbridge.New(orch, container, conversation.NewStore(root, logger), logcollector.NewWriter(root, logger), resolver, chatbridge.BridgeConfig{}, logger)
	hub.SetChatHandler(bridge)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	hubDone := make(chan struct{})
	go func() { defer close(hubDone); hub.Run(ctx) }()
	defer func() { cancel(); <-hubDone }()
	saveCLIState(t)
	t.Cleanup(ResetAIFirstLatches)
	t.Setenv("CREWSHIP_SERVER", "")
	t.Setenv("CREWSHIP_WORKSPACE", "")
	flagServer = ""
	flagWorkspace = ""
	cliCfg = &cli.CLIConfig{Token: token, Workspace: "memory-w", Server: server.URL}
	setFlagCov(t, runCmd, "chat", "memory-chat")
	setFlagCov(t, runCmd, "no-stream", "true")
	setFormatCov(t, "json")
	output, err := captureStdoutCov(t, func() error { return runCmd.RunE(runCmd, []string{"memory-agent", "write a memory note"}) })
	container.mu.Lock()
	run, writeErr := container.run, container.writeErr
	container.mu.Unlock()
	if err != nil || writeErr != nil || !strings.Contains(output, "memory written") {
		t.Fatalf("CLI err=%v write=%v output=%s", err, writeErr, output)
	}
	p, owned, err := work.NewStore(db).RunProjection(t.Context(), run)
	if err != nil || !owned || !p.Ready || p.Status != "COMPLETED" {
		t.Fatalf("actual normal turn not settled: %+v %v", p, err)
	}
	var mutations int
	if err = db.QueryRow(`SELECT COUNT(*) FROM memory_mutations WHERE run_id=?`, run).Scan(&mutations); err != nil || mutations != 1 {
		t.Fatalf("durable writes=%d err=%v", mutations, err)
	}
	if err = container.mutate(run, 1, "late-write"); err == nil {
		t.Fatal("ended CLI turn retained memory authority")
	}
}
