//go:build linux && (document_creation_browser || conformance)

package api

import (
	"bytes"
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

	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
	"github.com/crewship-ai/crewship/internal/auth/sessions"
	"github.com/crewship-ai/crewship/internal/chatbridge"
	"github.com/crewship-ai/crewship/internal/conversation"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/logcollector"
	"github.com/crewship-ai/crewship/internal/orchestrator"
	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/crewship-ai/crewship/internal/provider/bbolt"
	"github.com/crewship-ai/crewship/internal/ws"
)

// Only the provider's model process is synthetic. Browser, WebSocket audience,
// Bridge persistence/admission, IPC, Node validation and durable result are real.
// This is deliberately not a model or Docker qualification.
type documentCreationBrowserProvider struct {
	create func(context.Context, string) error
}

func (*documentCreationBrowserProvider) EnsureCrewRuntime(context.Context, provider.CrewConfig) (string, error) {
	return "synthetic-create-container", nil
}
func (*documentCreationBrowserProvider) StopCrewRuntime(context.Context, string) error   { return nil }
func (*documentCreationBrowserProvider) RemoveCrewRuntime(context.Context, string) error { return nil }
func (*documentCreationBrowserProvider) ContainerStatus(context.Context, string) (*provider.ContainerStatus, error) {
	return &provider.ContainerStatus{State: "running"}, nil
}
func (*documentCreationBrowserProvider) ContainerStats(context.Context, string) (*provider.ContainerMetrics, error) {
	return nil, fmt.Errorf("synthetic provider has no metrics")
}
func (*documentCreationBrowserProvider) ExecInspect(context.Context, string) (bool, int, error) {
	return false, 0, nil
}
func (*documentCreationBrowserProvider) CrewContainerName(string, string) string {
	return "synthetic-create-container"
}
func (*documentCreationBrowserProvider) CopyToContainer(context.Context, string, string, io.Reader) error {
	return nil
}
func (p *documentCreationBrowserProvider) Exec(ctx context.Context, cfg provider.ExecConfig) (*provider.ExecResult, error) {
	output := ""
	bearer := ""
	for _, variable := range cfg.Env {
		if strings.HasPrefix(variable, "CREWSHIP_AGENT_TOKEN=") {
			bearer = strings.TrimPrefix(variable, "CREWSHIP_AGENT_TOKEN=")
		}
	}
	if bearer != "" {
		if !strings.HasPrefix(bearer, "agtv2.") {
			return nil, fmt.Errorf("runtime must propagate original run capability")
		}
		if err := p.create(ctx, bearer); err != nil {
			return nil, err
		}
		output = "{\"type\":\"stream_event\",\"event\":{\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"Your launch plan document is ready.\"}}}\n{\"type\":\"result\",\"subtype\":\"success\",\"num_turns\":1,\"is_error\":false}\n"
	}
	return &provider.ExecResult{ExecID: "synthetic-exec", Reader: io.NopCloser(strings.NewReader(output))}, nil
}

func TestDocumentCreationChatBrowser(t *testing.T) {
	if os.Getenv("CREWSHIP_DOCUMENT_CREATE_BROWSER") != "1" {
		t.Fatal("requires explicit current static export + Chromium + Node24 gate")
	}
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(root, "out", "index.html")); err != nil {
		t.Fatal("real production export required", err)
	}
	h, workspace, owner, _, _ := artifactRunFixture(t)
	// No precreated chat is selected: the browser must create its first row.
	execOrFatal(t, h.db, `UPDATE agents SET cli_adapter='CLAUDE_CODE',status='IDLE' WHERE id='artifact-agent'`)
	// The scripted provider stands in for a previously provisioned runtime.
	// This marker is never pulled or executed and is not image qualification.
	execOrFatal(t, h.db, `UPDATE crews SET cached_image='synthetic-browser-provider-image' WHERE id='artifact-crew'`)
	dir := t.TempDir()
	const jwtSecret = "synthetic-document-create-browser-jwt-secret"
	validator, err := auth.NewJWTValidator(jwtSecret)
	if err != nil {
		t.Fatal(err)
	}
	session, err := sessions.NewDBStore(h.db).Create(t.Context(), owner, "synthetic", "127.0.0.1", auth.RefreshTokenTTL)
	if err != nil {
		t.Fatal(err)
	}
	token, err := validator.IssueAccessToken(owner, session.ID, owner, owner+"@example.test")
	if err != nil {
		t.Fatal(err)
	}
	hub := ws.NewHub(newTestLogger(), nil, validator, sessions.NewDBStore(h.db))
	hub.SetChannelAuthorizer(ws.NewDBChannelAuthorizer(h.db))
	hubCtx, stopHub := context.WithCancel(t.Context())
	hubDone := make(chan struct{})
	go func() { defer close(hubDone); hub.Run(hubCtx) }()
	t.Cleanup(func() { stopHub(); <-hubDone })
	secretFile := filepath.Join(dir, "secret")
	const secret = "synthetic-document-create-browser-service"
	if err = os.WriteFile(secretFile, []byte(secret), 0600); err != nil {
		t.Fatal(err)
	}
	authoritySocket, nodeSocket := filepath.Join(dir, "a.sock"), filepath.Join(dir, "n.sock")
	writer := journal.NewWriter(h.db, newTestLogger(), journal.WriterOptions{FlushSize: 1})
	t.Cleanup(func() { _ = writer.Close() })
	router, err := NewRouter(h.db, jwtSecret, newTestLogger(), WithHub(hub), WithJournal(writer), WithStoragePath(h.artifactStoragePath), WithInternalToken(h.artifactMasterToken), WithDocumentAuthority(authoritySocket, secretFile), WithDocumentService(nodeSocket))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(router.Shutdown)
	node := exec.Command("node", filepath.Join(root, "tools/docs-service/service.mjs"))
	node.Env = append(os.Environ(), "CREWSHIP_DOCS_SOCKET="+nodeSocket, "CREWSHIP_DOCS_AUTHORITY_SOCKET="+authoritySocket, "CREWSHIP_DOCS_SERVICE_TOKEN_FILE="+secretFile)
	node.Stderr = os.Stderr
	if err = node.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = node.Process.Kill(); _ = node.Wait() })
	documentsWaitSocket(t, nodeSocket)
	documentsWaitReady(t, nodeSocket, secret)
	mux := http.NewServeMux()
	mux.Handle("/api/", router)
	mux.HandleFunc("/ws", hub.HandleUpgrade)
	mux.Handle("/", StaticFileHandler(os.DirFS(filepath.Join(root, "out"))))
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	transcript := conversation.NewStore(filepath.Join(dir, "transcript"), newTestLogger(), conversation.WithDB(h.db))
	t.Cleanup(transcript.Close)
	state, err := bbolt.New(filepath.Join(dir, "runtime.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = state.Close() })
	ctr := &documentCreationBrowserProvider{}
	ctr.create = func(ctx context.Context, bearer string) error {
		var run, chat string
		if err := h.db.QueryRowContext(ctx, `SELECT run_id,chat_id FROM document_creation_consents WHERE workspace_id=? AND consumed_at IS NULL AND run_id IS NOT NULL`, workspace).Scan(&run, &chat); err != nil {
			return err
		}
		// This callback represents the model's tool invocation, never consent.
		expected := internaltoken.DeriveAgentRunToken(internaltoken.DeriveAgentRunKey(h.artifactMasterToken, workspace, "artifact-crew"), workspace, "artifact-agent", run)
		if bearer != expected {
			return fmt.Errorf("runtime run capability differs from bound creation consent")
		}
		body := []byte(`{"slug":"browser-launch-plan","name":"Launch plan","mutation_id":"browser-create-once","blocks":[{"type":"heading","level":1,"text":"Launch plan"},{"type":"paragraph","text":"This useful document came from the first chat message."},{"type":"bulletList","items":["Confirm the launch owner","Publish the checklist"]},{"type":"codeBlock","text":"status: ready"}]}`)
		req, err := http.NewRequestWithContext(ctx, "POST", server.URL+"/api/v1/internal/documents/create", bytes.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+bearer)
		req.Header.Set("X-Internal-Token", internaltoken.DeriveCrewToken(h.artifactMasterToken, workspace, "artifact-crew"))
		req.Header.Set("Content-Type", "application/json")
		response, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
		if err != nil {
			return err
		}
		defer response.Body.Close()
		raw, err := io.ReadAll(io.LimitReader(response.Body, 64<<10))
		if err != nil {
			return err
		}
		if response.StatusCode != 201 {
			return fmt.Errorf("synthetic tool status%d: %s", response.StatusCode, raw)
		}
		var saved documentCreationResult
		if err = json.Unmarshal(raw, &saved); err != nil {
			return err
		}
		if saved.PageID == "" || chat == "artifact-chat" {
			return fmt.Errorf("expected new chat durable creation")
		}
		return nil
	}
	orch := orchestrator.New(ctr, state, newTestLogger())
	orch.SetIPCConfig(server.URL, h.artifactMasterToken)
	orch.SetSidecarEnabled(true) // Exercise the actual runtime capability environment builder.
	bridge := chatbridge.New(orch, ctr, transcript, logcollector.NewWriter(filepath.Join(dir, "logs"), newTestLogger()), chatbridge.NewIPCResolver(server.URL, h.artifactMasterToken, newTestLogger()), chatbridge.BridgeConfig{}, newTestLogger())
	hub.SetChatHandler(bridge)
	fixture, err := json.Marshal(map[string]any{"server": server.URL, "workspace": workspace, "owner": owner, "token": token, "agent": "artifact-agent"})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "browser.json")
	if err = os.WriteFile(path, fixture, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Minute)
	defer cancel()
	browser := exec.CommandContext(ctx, "node", filepath.Join(root, "scripts/acceptance-document-creation-browser.mjs"))
	browser.Env = append(os.Environ(), "CREWSHIP_DOCUMENT_CREATE_FIXTURE="+path)
	browser.Stdout, browser.Stderr = os.Stdout, os.Stderr
	if err = browser.Run(); err != nil {
		t.Fatal("actual chat creation browser", err)
	}
	var count int
	if err = h.db.QueryRow(`SELECT COUNT(*) FROM document_creations c JOIN document_creation_consents s ON s.result_page_id=c.page_id WHERE c.workspace_id=? AND s.chat_id<>'artifact-chat' AND s.consumed_at IS NOT NULL`, workspace).Scan(&count); err != nil || count != 1 {
		t.Fatalf("first Send durable provenance count%d %v", count, err)
	}
}
