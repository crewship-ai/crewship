//go:build linux && (docs_foundation || conformance)

package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
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
	"github.com/crewship-ai/crewship/internal/chataudience"
	"github.com/crewship-ai/crewship/internal/chatbridge"
	"github.com/crewship-ai/crewship/internal/conversation"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/orchestrator"
	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/crewship-ai/crewship/internal/sidecar"
	"github.com/crewship-ai/crewship/internal/ws"
)

// Only container/model execution is simulated. The trusted bridge, mirrored
// human message, host issuer, HTTP run admission, real Node engine, draft tool
// implementation and SQLite commit/checkpoint are production code.
type iterationRuntime struct{ provider.ContainerProvider }

func (*iterationRuntime) EnsureCrewRuntime(context.Context, provider.CrewConfig) (string, error) {
	return "owned-test-runtime", nil
}

type iterationLiveResolver struct {
	*chatbridge.IPCResolver
	f    *artifactPrincipalFixture
	run  string
	stop error
}

func (r *iterationLiveResolver) ResolveHumanChat(ctx context.Context, user, chat string) (*chatbridge.ChatInfo, error) {
	receipt, err := chataudience.CaptureTrusted(ctx, r.f.rig.db, chat, user, chataudience.ReceiptFromContext(ctx))
	if err != nil {
		return nil, err
	}
	return &chatbridge.ChatInfo{HumanAuthority: receipt, AgentID: r.f.a.AgentID, AgentSlug: "principal-agent", CrewID: r.f.a.CrewID, CrewSlug: "principal-crew", WorkspaceID: r.f.a.WorkspaceID, Visibility: "private"}, nil
}
func (r *iterationLiveResolver) CreateRun(ctx context.Context, run, agent, chat, workspace, trigger string, meta map[string]any) error {
	if err := r.IPCResolver.CreateRun(ctx, run, agent, chat, workspace, trigger, meta); err != nil {
		return err
	}
	r.run = run
	return r.stop
}

type iterationChatHandlerFunc func(context.Context, string, string, string, func(ws.ChatEvent), ...ws.ChatMessageOption) error

func (f iterationChatHandlerFunc) HandleChatMessage(ctx context.Context, user, chat, text string, emit func(ws.ChatEvent), opts ...ws.ChatMessageOption) error {
	return f(ctx, user, chat, text, emit, opts...)
}

func TestDocumentIterationManagedBridgeDraftSamePage(t *testing.T) {
	if os.Getenv("CREWSHIP_DOCUMENT_CREATE") != "1" {
		t.Fatal("requires explicit owned Node24/frozen Docs installation opt-in")
	}
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	version, err := exec.Command("node", "--version").Output()
	if err != nil || !strings.HasPrefix(string(version), "v24.") {
		t.Fatalf("Node24 required: %s %v", version, err)
	}
	f, intent := documentCreationAdmissionFixture(t, true)
	mustExec(t, f.rig.db, `UPDATE workspaces SET slug='targeted-iteration' WHERE id=?`, f.a.WorkspaceID)
	if err := bindCreation(f, intent); err != nil {
		t.Fatal(err)
	}
	const master = "synthetic-iteration-integration-master"
	dir, err := os.MkdirTemp("", "doc-iterate-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	secret := filepath.Join(dir, "secret")
	const serviceSecret = "synthetic-iteration-integration-service-secret"
	if err := os.WriteFile(secret, []byte(serviceSecret), 0600); err != nil {
		t.Fatal(err)
	}
	authoritySocket, nodeSocket := filepath.Join(dir, "a.sock"), filepath.Join(dir, "n.sock")
	writer := journal.NewWriter(f.rig.db, newTestLogger(), journal.WriterOptions{FlushSize: 1})
	t.Cleanup(func() { _ = writer.Close() })
	const jwtSecret = "synthetic-iteration-jwt-secret-pad"
	validator, err := auth.NewJWTValidator(jwtSecret)
	if err != nil {
		t.Fatal(err)
	}
	hub := ws.NewHub(newTestLogger(), nil, validator, sessions.NewDBStore(f.rig.db))
	hub.SetChannelAuthorizer(ws.NewDBChannelAuthorizer(f.rig.db))
	hubCtx, stopHub := context.WithCancel(t.Context())
	defer stopHub()
	hubDone := make(chan struct{})
	go func() { hub.Run(hubCtx); close(hubDone) }()
	t.Cleanup(func() { stopHub(); <-hubDone })
	router, err := NewRouter(f.rig.db, jwtSecret, newTestLogger(), WithInternalToken(master), WithHub(hub), WithJournal(writer), WithStoragePath(filepath.Join(dir, "storage")), WithDocumentAuthority(authoritySocket, secret), WithDocumentService(nodeSocket))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(router.Shutdown)
	node := exec.Command("node", filepath.Join(root, "tools/docs-service/service.mjs"))
	node.Env = append(os.Environ(), "CREWSHIP_DOCS_SOCKET="+nodeSocket, "CREWSHIP_DOCS_AUTHORITY_SOCKET="+authoritySocket, "CREWSHIP_DOCS_SERVICE_TOKEN_FILE="+secret)
	node.Stderr = os.Stderr
	if err := node.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = node.Process.Kill(); _ = node.Wait() })
	documentsWaitSocket(t, nodeSocket)
	documentsWaitReady(t, nodeSocket, serviceSecret)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /ws", hub.HandleUpgrade)
	mux.Handle("/", router)
	host := httptest.NewServer(mux)
	t.Cleanup(host.Close)
	request := func(method, path, token string, body []byte) (int, []byte, error) {
		req, err := http.NewRequestWithContext(t.Context(), method, host.URL+path, bytes.NewReader(body))
		if err != nil {
			return 0, nil, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("X-Workspace-ID", f.a.WorkspaceID)
		req.Header.Set("Content-Type", "application/json")
		if strings.HasPrefix(token, "agtv2.") {
			req.Header.Set("X-Internal-Token", internaltoken.DeriveCrewToken(master, f.a.WorkspaceID, f.a.CrewID))
		}
		client := http.Client{Timeout: 20 * time.Second}
		res, err := client.Do(req)
		if err != nil {
			return 0, nil, err
		}
		defer res.Body.Close()
		raw, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
		return res.StatusCode, raw, err
	}
	originalToken := internaltoken.DeriveAgentRunToken(internaltoken.DeriveAgentRunKey(master, f.a.WorkspaceID, f.a.CrewID), f.a.WorkspaceID, f.a.AgentID, f.a.RunID)
	creation := []byte(`{"slug":"iteration-source","name":"Existing document","mutation_id":"initial-create","blocks":[{"type":"paragraph","text":"Original introduction."},{"type":"paragraph","text":"Keep this paragraph."}]}`)
	status, raw, err := request(http.MethodPost, "/api/v1/internal/documents/create", originalToken, creation)
	if err != nil || status != 201 {
		t.Fatalf("real initial document: %d %s %v", status, raw, err)
	}
	var created documentCreationResult
	if err := json.Unmarshal(raw, &created); err != nil {
		t.Fatal(err)
	}
	var rev, generation int64
	if err := f.rig.db.QueryRow(`SELECT head_rev,generation FROM documents WHERE page_id=?`, created.PageID).Scan(&rev, &generation); err != nil {
		t.Fatal(err)
	}
	resolver := &iterationLiveResolver{IPCResolver: chatbridge.NewIPCResolver(host.URL, master, newTestLogger()), f: f, stop: errors.New("stop after actual HTTP admission")}
	runtime := &iterationRuntime{}
	orch := orchestrator.New(runtime, nil, newTestLogger())
	store := conversation.NewStore(filepath.Join(dir, "transcript"), newTestLogger(), conversation.WithDB(f.rig.db))
	t.Cleanup(store.Close)
	bridge := chatbridge.New(orch, runtime, store, nil, resolver, chatbridge.BridgeConfig{DocumentCreationEnabled: true}, newTestLogger())
	bridge.SetDocumentIterationRecorder(router.Pages())

	type result struct {
		run    string
		packet sidecar.DocumentDraftPacket
		err    error
	}
	completed := make(chan result, 1)
	hub.SetChatHandler(iterationChatHandlerFunc(func(ctx context.Context, user, chat, text string, emit func(ws.ChatEvent), opts ...ws.ChatMessageOption) error {
		// Stop after the real run admission, then replace only model execution
		// with deterministic calls to the real draft tool. The synthetic stop's
		// error frame is not a product outcome and must not reach the CLI.
		var admissionEvents []ws.ChatEvent
		if err := bridge.HandleChatMessage(ctx, user, chat, text, func(e ws.ChatEvent) { admissionEvents = append(admissionEvents, e) }, opts...); !errors.Is(err, resolver.stop) {
			completed <- result{err: err}
			return err
		}
		for _, event := range admissionEvents {
			if event.Type != "error" {
				emit(event)
			}
		}
		run := resolver.run
		token := internaltoken.DeriveAgentRunToken(internaltoken.DeriveAgentRunKey(master, f.a.WorkspaceID, f.a.CrewID), f.a.WorkspaceID, f.a.AgentID, run)
		exchange := func(method string, body []byte) (int, []byte, error) {
			return request(method, "/api/v1/internal/documents/draft", token, body)
		}
		status, raw, err := exchange(http.MethodGet, nil)
		if err != nil || status != 200 {
			err = fmt.Errorf("draft view: %d %s %v", status, raw, err)
			completed <- result{err: err}
			return err
		}
		var view sidecar.DocumentDraftView
		if err := json.Unmarshal(raw, &view); err != nil {
			completed <- result{err: err}
			return err
		}
		if view.PageID != created.PageID || view.Rev != rev || len(view.Targets) != 2 {
			err = fmt.Errorf("target changed: %+v", view)
			completed <- result{err: err}
			return err
		}
		input, _ := json.Marshal(sidecar.DocumentDraftPrepare{View: view, Target: view.Targets[0].ID, Text: "Revised introduction.", MutationID: "followup-change"})
		packet, err := sidecar.PrepareDocumentDraft(input)
		if err == nil {
			var outcome sidecar.DocumentDraftOutcome
			outcome, err = sidecar.ApplyDocumentDraft(packet, false, exchange)
			if err == nil && outcome.Status != "checkpointed" {
				err = fmt.Errorf("unconfirmed draft: %+v", outcome)
			}
		}
		completed <- result{run: run, packet: packet, err: err}
		if err != nil {
			return err
		}
		emit(ws.ChatEvent{Type: "text", Content: "Saved revision of the existing document."})
		emit(ws.ChatEvent{Type: "done"})
		return nil
	}))
	const humanToken = "crewship_cli_targeted_iteration_test"
	digest := sha256.Sum256([]byte(humanToken))
	mustExec(t, f.rig.db, `INSERT INTO cli_tokens(id,user_id,name,token_hash,tier,scopes) VALUES('iteration-cli-token',?,'Iteration test',?,'STANDARD','["*"]')`, f.a.HumanUserID, fmt.Sprintf("%x", digest))
	target := filepath.Join(dir, "target.json")
	targetBytes, _ := json.Marshal(map[string]any{"submission_id": "followup", "page_id": created.PageID, "schema": 3, "rev": rev, "generation": generation})
	if err := os.WriteFile(target, targetBytes, 0600); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(dir, "cli.yaml")
	if err := os.WriteFile(config, []byte(fmt.Sprintf("server: %s\ntoken: %s\nworkspace: %s\n", host.URL, humanToken, "targeted-iteration")), 0600); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(dir, "crewship")
	build := exec.CommandContext(t.Context(), "go", "build", "-o", binary, "./cmd/crewship")
	build.Dir = root
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("CLI build: %s %v", output, err)
	}
	command := exec.CommandContext(t.Context(), binary, "run", "principal-agent", "--chat", f.a.ChatID, "--document-target", target, "--no-stream", "Rewrite the introduction; preserve the other paragraph")
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "CREWSHIP_") {
			command.Env = append(command.Env, entry)
		}
	}
	command.Env = append(command.Env, "CREWSHIP_CONFIG="+config, "CREWSHIP_NO_SLUG_CACHE=1", "NO_COLOR=1")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("actual binary→Router/WS→bridge→draft tools: %s %v", output, err)
	}
	var saved result
	select {
	case saved = <-completed:
	case <-time.After(3 * time.Second):
		t.Fatal("no durable admission/tool result")
	}
	if saved.err != nil || saved.run == "" {
		t.Fatalf("draft failed: %+v", saved)
	}
	var head, cursor int64
	var snapshot []byte
	var count int
	if err := f.rig.db.QueryRow(`SELECT d.head_rev,d.snapshot,i.draft_rev FROM documents d JOIN document_iteration_intents i ON i.page_id=d.page_id WHERE i.run_id=?`, saved.run).Scan(&head, &snapshot, &cursor); err != nil {
		t.Fatal(err)
	}
	if head != rev+1 || cursor != head || len(snapshot) == 0 {
		t.Fatal("same Page durable update missing")
	}
	if err := f.rig.db.QueryRow(`SELECT count(*) FROM document_creations WHERE source_chat_id=?`, f.a.ChatID).Scan(&count); err != nil || count != 1 {
		t.Fatal("iteration created replacement document")
	}
	status, raw, err = request(http.MethodGet, "/api/v1/pages/@"+created.PageID+"/document", humanToken, nil)
	if err != nil || status != 200 || !bytes.Contains(raw, []byte("Revised introduction.")) || !bytes.Contains(raw, []byte("Keep this paragraph.")) {
		t.Fatalf("Latest panel human reader: %d %s %v", status, raw, err)
	}
	token := internaltoken.DeriveAgentRunToken(internaltoken.DeriveAgentRunKey(master, f.a.WorkspaceID, f.a.CrewID), f.a.WorkspaceID, f.a.AgentID, saved.run)
	exchange := func(method string, body []byte) (int, []byte, error) {
		return request(method, "/api/v1/internal/documents/draft", token, body)
	}
	replay, err := sidecar.ApplyDocumentDraft(saved.packet, false, exchange)
	if err != nil || replay.Status != "checkpointed" {
		t.Fatalf("exact receipt retry failed: %+v %v", replay, err)
	}
}
