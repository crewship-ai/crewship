package api

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/artifacts"
	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
	"github.com/crewship-ai/crewship/internal/auth/sessions"
	"github.com/crewship-ai/crewship/internal/chataudience"
	"github.com/crewship-ai/crewship/internal/chatbridge"
	"github.com/crewship-ai/crewship/internal/conversation"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/mooring"
	"github.com/crewship-ai/crewship/internal/orchestrator"
	"github.com/crewship-ai/crewship/internal/runauthority"
	"github.com/crewship-ai/crewship/internal/sidecar"
	"github.com/crewship-ai/crewship/internal/ws"
	"golang.org/x/net/websocket"
)

// The whole path of one request, from the chat message to the stored revision.
//
// Real: the chat bridge, the message store, the reference recorder, the run
// admission over the internal HTTP API, the sidecar's MCP server, its
// credentials and transport, the router fence, the Sheet tools, the lease, the
// version store and the human's document API. Replaced: the model (this test
// calls the tools in its place), the container runtime, and the engine process,
// whose stand-in calculates one formula on another sheet. It proves the wiring
// and the authority, not the engine's arithmetic.

// scenarioSheet is the stored document: Data holds one typed number and Summary
// one formula, =Data!A1*2.
type scenarioSheet struct {
	Sheets map[string]map[string]string `json:"sheets"`
}

func scenarioSheetBytes(input string) []byte {
	var n int
	_, _ = fmt.Sscan(input, &n)
	raw, _ := json.Marshal(scenarioSheet{Sheets: map[string]map[string]string{"Data": {"A1": input}, "Summary": {"B1": fmt.Sprint(n * 2)}}})
	return raw
}

// scenarioEngine opens exactly the bytes it is given, like the engine process:
// nothing survives between two calls except what was saved.
type scenarioEngine struct {
	mu       sync.Mutex
	applies  int
	retained []byte
	inspects chan struct{}
}

func (e *scenarioEngine) Open(_ context.Context, base []byte) (CraftNativeSheetSession, func(), error) {
	var doc scenarioSheet
	if err := json.Unmarshal(base, &doc); err != nil {
		return nil, nil, err
	}
	return &scenarioEngineSession{engine: e, input: doc.Sheets["Data"]["A1"]}, func() {}, nil
}
func (e *scenarioEngine) Retain(_ CraftNativeSheetSession, snapshot []byte) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.retained = snapshot
}
func (e *scenarioEngine) Discard(CraftNativeSheetSession) {}

func (e *scenarioEngine) applied() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.applies
}

type scenarioEngineSession struct {
	engine *scenarioEngine
	input  string
}

func (s *scenarioEngineSession) value(address string) (CraftNativeSheetCell, error) {
	var doc scenarioSheet
	_ = json.Unmarshal(scenarioSheetBytes(s.input), &doc)
	switch strings.ReplaceAll(address, "'", "") {
	case "Data!A1":
		return CraftNativeSheetCell{Cell: address, Input: s.input, Value: doc.Sheets["Data"]["A1"]}, nil
	case "Summary!B1":
		return CraftNativeSheetCell{Cell: address, Input: "=Data!A1*2", Formula: "=Data!A1*2", Value: doc.Sheets["Summary"]["B1"]}, nil
	}
	return CraftNativeSheetCell{}, errors.New("no such cell")
}
func (s *scenarioEngineSession) GetCell(_ context.Context, cell string) (CraftNativeSheetCell, error) {
	return s.value(cell)
}
func (s *scenarioEngineSession) ReadRange(context.Context, string, bool) (CraftNativeSheetValues, error) {
	return CraftNativeSheetValues{}, errors.New("not used")
}
func (s *scenarioEngineSession) SetCell(_ context.Context, cell, input string) error {
	if strings.ReplaceAll(cell, "'", "") != "Data!A1" {
		return errors.New("no such cell")
	}
	s.input = input
	return nil
}
func (s *scenarioEngineSession) WriteRange(context.Context, string, [][]any) (int, error) {
	return 0, errors.New("not used")
}
func (s *scenarioEngineSession) Apply(ctx context.Context, ops []CraftNativeSheetWrite) (int, error) {
	s.engine.mu.Lock()
	s.engine.applies++
	s.engine.mu.Unlock()
	for i, op := range ops {
		if err := s.SetCell(ctx, op.Cell, op.Input); err != nil {
			return i, err
		}
	}
	return len(ops), nil
}
func (s *scenarioEngineSession) ClearRange(context.Context, string) error {
	return errors.New("scenario engine does not clear")
}
func (s *scenarioEngineSession) ColumnsByHeader(context.Context, string, string, bool) (json.RawMessage, error) {
	return nil, errors.New("scenario engine does not find headers")
}
func (s *scenarioEngineSession) Sheets(context.Context) ([]CraftNativeSheetInfo, error) {
	select {
	case s.engine.inspects <- struct{}{}:
	default:
	}
	return []CraftNativeSheetInfo{{Name: "Data", Used: "A1"}, {Name: "Summary", Used: "B1"}}, nil
}
func (s *scenarioEngineSession) Snapshot(context.Context) ([]byte, error) {
	return scenarioSheetBytes(s.input), nil
}

// errScenarioModel is where the model would start: the run is admitted, the
// message is stored, and the test takes the agent's place.
var errScenarioModel = errors.New("scenario stops before the model")

// scenarioResolver is the production IPC resolver except for the chat lookup
// (the receipt is captured exactly as the host captures it) and the stop after
// a committed run admission.
type scenarioResolver struct {
	*chatbridge.IPCResolver
	f                *artifactPrincipalFixture
	internal         *InternalHandler
	source           *auth.OriginalCredential
	runID, messageID string
	admitErr         error
	admitted         chan struct{}
}

func (r *scenarioResolver) ResolveHumanChat(ctx context.Context, user, chat string) (*chatbridge.ChatInfo, error) {
	// The socket's own credential when the message came through the hub.
	source := r.source
	if sent := auth.OriginalCredentialFromContext(ctx); sent != nil && sent.Valid() {
		source = sent
	}
	receipt, err := r.internal.originalHumanChatReceipt(ctx, chat, user, nil, source)
	if err != nil || receipt == nil {
		return nil, fmt.Errorf("receipt %v: %w", receipt, err)
	}
	return &chatbridge.ChatInfo{HumanAuthority: receipt, AgentID: r.f.a.AgentID, AgentSlug: "principal-agent", CrewID: r.f.a.CrewID, CrewSlug: "principal-crew", WorkspaceID: r.f.a.WorkspaceID, Visibility: "private"}, nil
}

func (r *scenarioResolver) CreateRun(ctx context.Context, runID, agentID, chatID, workspaceID, trigger string, metadata map[string]any) error {
	if chataudience.ReceiptFromContext(ctx) == nil {
		return errors.New("the run is admitted without the human's receipt")
	}
	r.runID, r.messageID = runID, runauthority.TriggerMessageFromContext(ctx)
	defer func() { r.admitted <- struct{}{} }()
	if r.admitErr = r.IPCResolver.CreateRun(ctx, runID, agentID, chatID, workspaceID, trigger, metadata); r.admitErr != nil {
		return r.admitErr
	}
	return errScenarioModel
}

type chatRunScenario struct {
	t        *testing.T
	f        *artifactPrincipalFixture
	m        *craftNativeAgentManager
	engine   *scenarioEngine
	resolver *scenarioResolver
	human    context.Context
	bridge   *chatbridge.Bridge
	host     *httptest.Server
}

// fullSynchronousPool sets synchronous=FULL on every connection the pool will
// hand out, as the server's DSN does. The pragma is per connection: set with one
// Exec, a second connection (the socket path opens one) stays NORMAL and the
// agent's writer refuses it, which read as a Sheet that was never attached.
func fullSynchronousPool(t *testing.T, db *sql.DB) {
	t.Helper()
	const size = 8
	db.SetMaxOpenConns(size)
	db.SetMaxIdleConns(size)
	db.SetConnMaxLifetime(0)
	db.SetConnMaxIdleTime(0)
	conns := make([]*sql.Conn, 0, size)
	for range size {
		c, err := db.Conn(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if _, err = c.ExecContext(t.Context(), `PRAGMA synchronous=FULL`); err != nil {
			t.Fatal(err)
		}
		conns = append(conns, c)
	}
	for _, c := range conns {
		_ = c.Close()
	}
}

func seedScenarioSheet(t *testing.T, f *artifactPrincipalFixture, id, owner string) {
	t.Helper()
	mustExec(t, f.rig.db, `INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json,private_audience) VALUES(?,?,?,?,?,'{}',1)`, id, f.a.WorkspaceID, id, id, owner)
	mustExec(t, f.rig.db, `INSERT INTO craft_native_documents(page_id,workspace_id,application_id,engine_revision,engine_sha256,editor_bundle) VALUES(?,?,'gridcraft',?,?,?)`, id, f.a.WorkspaceID, strings.Repeat("a", 40), strings.Repeat("b", 64), strings.Repeat("c", 64))
}

func (s *chatRunScenario) seedSheet(id, owner string) {
	s.t.Helper()
	seedScenarioSheet(s.t, s.f, id, owner)
}

func newChatRunScenario(t *testing.T, flagOn bool) *chatRunScenario {
	t.Helper()
	const master = "synthetic-chatrun-scenario-master"
	f, source := originalSessionFixture(t)
	db, workspace := f.rig.db, f.a.WorkspaceID
	fullSynchronousPool(t, db)
	mustExec(t, db, `UPDATE chats SET visibility='private' WHERE id=?`, f.a.ChatID)
	mustExec(t, db, `DELETE FROM chat_participants WHERE chat_id=?`, f.a.ChatID)
	mustExec(t, db, `INSERT INTO workspace_artifact_creation_policy(workspace_id,enabled,version,updated_at) VALUES(?,1,1,'2026-10-17T00:00:00Z')`, workspace)
	mustExec(t, db, `INSERT INTO workspace_artifact_creation_crews(workspace_id,crew_id) VALUES(?,?)`, workspace, f.a.CrewID)
	seedScenarioSheet(t, f, "sheet-own", f.a.HumanUserID)
	if flagOn {
		mustExec(t, db, `INSERT INTO feature_flag_overrides(id,flag_id,workspace_id,enabled) VALUES('scenario-flag-on','ff_artifact_agent_access_modes',?,1)`, workspace)
	}
	craftNativeEnableVersionHistory(t, db, true)
	versions := artifacts.NewVersionStore(db, artifacts.VersionOptions{Enabled: true, ContentType: "gridcraft"})
	if _, err := versions.Apply(t.Context(), artifacts.VersionKey{WorkspaceID: workspace, PageID: "sheet-own"}, artifacts.VersionRequest{EntryPoint: "document.json", Files: map[string]artifacts.VersionFile{"document.json": {MediaType: "application/json", Content: scenarioSheetBytes("10")}}}, func(context.Context, *sql.Tx) (string, error) { return "user:" + f.a.HumanUserID, nil }); err != nil {
		t.Fatal(err)
	}

	f.pages.artifactMasterToken = master
	h := newCraftNativeHTTP(f.pages, true, &collaborationAdmissionTestClock{boot: "chatrun-scenario", now: time.Second})
	store := mooring.NativeStoreConfig{Application: "gridcraft", EngineRevision: strings.Repeat("a", 40), EngineSHA256: strings.Repeat("b", 64), EditorBundle: strings.Repeat("c", 64), Validate: func(context.Context, []byte) error { return nil }}
	d := newCraftNativeDocumentsHTTP(h, map[string]craftNativeApplication{"gridcraft": {Store: store}})
	d.profiles = map[string]CraftNativeDocumentProfile{"gridcraft": {Application: "gridcraft"}}
	engine := &scenarioEngine{inspects: make(chan struct{}, 1)}
	adapter := NewCraftNativeSheetAgentAdapter(engine)
	m := newCraftNativeAgentManager(h, d, CraftNativeHTTPConfig{AgentsEnabled: true})
	// The server hands a chat run's calls, and only those, to the sessions.
	m.config.AgentApply = func(ctx context.Context, p CraftNativeDocumentProfile, base []byte, commands json.RawMessage) ([]byte, error) {
		if !CraftNativeChatRun(ctx) {
			return nil, errors.New("not a chat run")
		}
		return adapter.Apply(ctx, p, base, commands)
	}
	m.config.AgentInspect = func(ctx context.Context, p CraftNativeDocumentProfile, base []byte, query json.RawMessage) (json.RawMessage, error) {
		if !CraftNativeChatRun(ctx) {
			return nil, errors.New("not a chat run")
		}
		return adapter.Inspect(ctx, p, base, query)
	}
	h.agents = m

	writer := journal.NewWriter(db, newTestLogger(), journal.WriterOptions{ChainKey: []byte("synthetic-artifact-refs-chain"), FlushSize: 1})
	t.Cleanup(func() { _ = writer.Close() })
	internal := NewInternalHandler(db, master, newTestLogger())
	internal.SetJournal(writer)
	router := &Router{db: db, logger: newTestLogger(), mux: newRouteMux()}
	guard := func(next http.HandlerFunc) http.Handler {
		return internal.requireInternal(withHostChatOrigin(next))
	}
	router.mux.Handle("POST /api/v1/internal/craft-native/chat-run", guard(m.chatRunTool))
	router.mux.Handle("POST /api/v1/internal/runs", guard(internal.CreateRun))
	host := httptest.NewServer(router)
	t.Cleanup(host.Close)

	transcript := conversation.NewStore(filepath.Join(t.TempDir(), "transcript"), newTestLogger(), conversation.WithDB(db))
	t.Cleanup(transcript.Close)
	resolver := &scenarioResolver{IPCResolver: chatbridge.NewIPCResolver(host.URL, master, newTestLogger()), f: f, internal: internal, source: source, admitted: make(chan struct{}, 4)}
	runtime := &replayRuntime{}
	bridge := chatbridge.New(orchestrator.New(runtime, nil, newTestLogger()), runtime, transcript, nil, resolver, chatbridge.BridgeConfig{}, newTestLogger())
	bridge.SetArtifactRefRecorder(f.pages)

	u := &AuthUser{ID: source.UserID, SessionID: source.ID, OriginalCredential: source}
	human := auth.WithOriginalCredential(context.WithValue(context.WithValue(context.WithValue(t.Context(), ctxUser, u), ctxAuthKind, AuthKindSession), ctxWorkspaceID, workspace), *source)
	return &chatRunScenario{t: t, f: f, m: m, engine: engine, resolver: resolver, human: human, bridge: bridge, host: host}
}

// scenarioRefs is the metadata the web composer sends for its attachment chips.
func scenarioRefs(pages ...string) map[string]any {
	if len(pages) == 0 {
		return nil
	}
	refs := make([]any, 0, len(pages))
	for _, page := range pages {
		refs = append(refs, map[string]any{"page_id": page})
	}
	return map[string]any{chatbridge.ArtifactRefsMetadataKey: refs}
}

func (s *chatRunScenario) trySend(content string, pages ...string) error {
	return s.bridge.HandleChatMessage(s.t.Context(), s.f.a.HumanUserID, s.f.a.ChatID, content, func(ws.ChatEvent) {}, ws.ChatMessageOption{Metadata: scenarioRefs(pages...)})
}

func (s *chatRunScenario) access() craftNativeAgentAccessResponse {
	s.t.Helper()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/pages/@sheet-own/craft/agent-access?chat_id="+s.f.a.ChatID, nil).WithContext(s.human)
	request.SetPathValue("slug", "@sheet-own")
	w := httptest.NewRecorder()
	s.m.human.agentAccess(w, request)
	var out craftNativeAgentAccessResponse
	if w.Code == http.StatusForbidden { // the person may not even read it
		out.Reason, out.Message = CraftSheetReasonNotAttached, w.Body.String()
		return out
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &out) != nil {
		s.t.Fatalf("agent access: %d %s", w.Code, w.Body)
	}
	return out
}

// send is the person's message, with the Sheet as an attachment chip or not.
func (s *chatRunScenario) send(content string, attach bool) {
	s.t.Helper()
	var pages []string
	if attach {
		pages = []string{"sheet-own"}
	}
	err := s.trySend(content, pages...)
	if !errors.Is(err, errScenarioModel) {
		s.t.Fatalf("the message did not reach a run: %v", err)
	}
	var stored string
	if err = s.f.rig.db.QueryRow(`SELECT content FROM conversation_messages WHERE id=? AND author_user_id=? AND role='user'`, s.resolver.messageID, s.f.a.HumanUserID).Scan(&stored); err != nil || stored != content {
		s.t.Fatalf("stored message %q: %v", stored, err)
	}
}

// agent starts the crew's sidecar for the admitted run and returns the MCP call
// the agent's CLI would make.
func (s *chatRunScenario) agent() func(method string, params any) (text string, isError bool, raw json.RawMessage) {
	s.t.Helper()
	return s.agentWithTimeout(15 * time.Second)
}

func (s *chatRunScenario) agentWithTimeout(timeout time.Duration) func(method string, params any) (text string, isError bool, raw json.RawMessage) {
	s.t.Helper()
	master := s.f.pages.artifactMasterToken
	runKey := internaltoken.DeriveAgentRunKey(master, s.f.a.WorkspaceID, s.f.a.CrewID)
	bearer := internaltoken.DeriveAgentRunToken(runKey, s.f.a.WorkspaceID, s.f.a.AgentID, s.resolver.runID)
	reservation, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		s.t.Fatal(err)
	}
	addr := reservation.Addr().String()
	if err = reservation.Close(); err != nil {
		s.t.Fatal(err)
	}
	s.t.Setenv("CREWSHIP_SIDECAR_STATE_DIR", s.t.TempDir())
	s.t.Setenv("CREWSHIP_SIDECAR_RUN_AUTHORITY", "0") // the identity fixture of the task test, not a host runtime
	server := sidecar.NewServer(sidecar.ServerConfig{Addr: addr, Logger: newTestLogger(), IPC: &sidecar.IPCConfig{
		BaseURL: s.host.URL, Token: internaltoken.DeriveCrewToken(master, s.f.a.WorkspaceID, s.f.a.CrewID), WorkspaceID: s.f.a.WorkspaceID, CrewID: s.f.a.CrewID, AgentID: s.f.a.AgentID, AgentSlug: "principal-agent",
		ChatID: s.f.a.ChatID, RunID: s.resolver.runID, RunChatID: s.f.a.ChatID, AgentRunKey: runKey, ContainerID: "chatrun-scenario-owned", CraftSheetEnabled: true,
	}})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	var serveErr error
	go func() { serveErr = server.Start(ctx); close(done) }()
	s.t.Cleanup(func() {
		cancel()
		select {
		case <-done:
			if serveErr != nil {
				s.t.Error("sidecar shutdown", serveErr)
			}
		case <-time.After(7 * time.Second):
			s.t.Error("the sidecar did not shut down")
		}
	})
	select {
	case <-server.Ready():
	case <-done:
		s.t.Fatal("sidecar startup", serveErr)
	case <-time.After(5 * time.Second):
		s.t.Fatal("the sidecar never became ready")
	}
	client := &http.Client{Timeout: timeout}
	return func(method string, params any) (string, bool, json.RawMessage) {
		s.t.Helper()
		body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
		request, err := http.NewRequestWithContext(s.t.Context(), http.MethodPost, "http://"+addr+"/mcp/routines", strings.NewReader(string(body)))
		if err != nil {
			s.t.Fatal(err)
		}
		request.Header.Set("Authorization", "Bearer "+bearer)
		request.Header.Set("Content-Type", "application/json")
		response, err := client.Do(request)
		if err != nil {
			s.t.Fatal(err)
		}
		defer response.Body.Close()
		raw, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
		if err != nil || response.StatusCode != 200 {
			s.t.Fatalf("MCP %s: %d %v %s", method, response.StatusCode, err, raw)
		}
		var envelope struct {
			Error  json.RawMessage `json:"error"`
			Result json.RawMessage `json:"result"`
		}
		if json.Unmarshal(raw, &envelope) != nil || len(envelope.Error) > 0 {
			s.t.Fatalf("MCP envelope: %s", raw)
		}
		var result struct {
			IsError bool `json:"isError"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		}
		_ = json.Unmarshal(envelope.Result, &result)
		text := ""
		if len(result.Content) > 0 {
			text = result.Content[0].Text
		}
		return text, result.IsError, envelope.Result
	}
}

func (s *chatRunScenario) tool(call func(string, any) (string, bool, json.RawMessage), name string, arguments map[string]any) string {
	s.t.Helper()
	text, isError, _ := call("tools/call", map[string]any{"name": name, "arguments": arguments})
	if isError {
		s.t.Fatalf("%s: %s", name, text)
	}
	return text
}

// humanRequest is the person's own request in the browser session that sent the message.
func (s *chatRunScenario) humanRequest(method, body, token string, values map[string]string, handler http.HandlerFunc) *httptest.ResponseRecorder {
	s.t.Helper()
	r := httptest.NewRequest(method, "/", strings.NewReader(body)).WithContext(s.human)
	r.SetPathValue("slug", "sheet-own")
	for k, v := range values {
		r.SetPathValue(k, v)
	}
	if token != "" {
		r.Header.Set("X-Craft-Session", token)
	}
	w := httptest.NewRecorder()
	handler(w, r)
	return w
}

func (s *chatRunScenario) humanSession() string {
	s.t.Helper()
	w := s.humanRequest(http.MethodPost, `{}`, "", nil, s.m.human.sessions)
	var admitted struct {
		Token string `json:"session_token"`
	}
	if w.Code != 201 || json.Unmarshal(w.Body.Bytes(), &admitted) != nil || admitted.Token == "" {
		s.t.Fatalf("human session: %d %s", w.Code, w.Body)
	}
	return admitted.Token
}

func (s *chatRunScenario) head() int64 {
	s.t.Helper()
	var version int64
	if err := s.f.rig.db.QueryRow(`SELECT latest_version FROM artifact_version_heads WHERE page_id='sheet-own'`).Scan(&version); err != nil {
		s.t.Fatal(err)
	}
	return version
}

func TestChatRunSheetScenarioFromMessageToStoredRevision(t *testing.T) {
	s := newChatRunScenario(t, true)
	const request = "Change the number in Data A1 to 25"
	s.send(request, true)

	// The message's reference is bound to the admitted run, nothing else is.
	var attached, trigger string
	if err := s.f.rig.db.QueryRow(`SELECT group_concat(page_id) FROM artifact_run_scope WHERE run_id=?`, s.resolver.runID).Scan(&attached); err != nil || attached != "sheet-own" {
		t.Fatalf("run scope %q: %v", attached, err)
	}
	if err := s.f.rig.db.QueryRow(`SELECT json_extract(payload,'$.trigger_message_id') FROM journal_entries WHERE trace_id=? AND entry_type='run.started'`, s.resolver.runID).Scan(&trigger); err != nil || trigger != s.resolver.messageID {
		t.Fatalf("run started by message %q, want %q: %v", trigger, s.resolver.messageID, err)
	}

	// The person has the Sheet open without editing it: they do not hold it.
	human := s.humanSession()
	before := s.humanRequest(http.MethodGet, "", human, nil, s.m.documents.readSnapshot)
	var document craftNativeDocumentResponse
	if before.Code != 200 || json.Unmarshal(before.Body.Bytes(), &document) != nil || document.Version != "1" || string(document.Bytes) != string(scenarioSheetBytes("10")) {
		t.Fatalf("the person's Sheet before the change: %d %s", before.Code, before.Body)
	}

	call := s.agent()
	_, _, listed := call("tools/list", map[string]any{})
	for _, name := range []string{"read", "acquire", "renew", "apply", "propose", "release"} {
		if !strings.Contains(string(listed), `"craft_sheet_`+name+`"`) {
			t.Fatalf("the agent is not offered craft_sheet_%s", name)
		}
	}
	select {
	case <-s.engine.inspects:
	case <-time.After(10 * time.Second):
		t.Fatal("listing the tools did not open the attached Sheet ahead of the first call")
	}

	read := func(sheet, cell string) string {
		return s.tool(call, "craft_sheet_read", map[string]any{"query": map[string]any{"command": "cell.get", "params": map[string]any{"sheet": sheet, "cell": cell}}})
	}
	if got := read("Summary", "B1"); !strings.Contains(got, `"value":"20"`) {
		t.Fatalf("dependent value before the change: %s", got)
	}
	var lease struct {
		Generation string `json:"generation"`
	}
	if got := s.tool(call, "craft_sheet_acquire", map[string]any{}); json.Unmarshal([]byte(got), &lease) != nil || lease.Generation == "" {
		t.Fatalf("acquire: %s", got)
	}
	// While the agent holds the Sheet the person cannot take it from under it.
	if w := craftNativeLeaseCall(s.m.human, s.human, human, "sheet-own", "acquire", `{}`); w.Code != 409 {
		t.Fatalf("the person took the Sheet while the agent held it: %d %s", w.Code, w.Body)
	}
	apply := map[string]any{"generation": lease.Generation, "base_version": "1", "request_id": "scenario-first", "commands": []any{map[string]any{"command": "cell.set", "params": map[string]any{"sheet": "Data", "cell": "A1", "input": "25"}}}}
	applied := s.tool(call, "craft_sheet_apply", apply)
	var result craftNativeChatApplyResult
	var receipt mooring.NativeSaveReceipt
	stored := sha256.Sum256(scenarioSheetBytes("25"))
	if json.Unmarshal([]byte(applied), &result) != nil || result.PageID != "sheet-own" || result.BaseVersion != 1 || result.Version != 2 || result.ChangedCells != 1 || result.ContentHash != "sha256:"+hex.EncodeToString(stored[:]) ||
		json.Unmarshal(result.Receipt, &receipt) != nil || receipt.Phase != "committed" {
		t.Fatalf("apply result: %s", applied)
	}
	// A lost answer is retried with the exact same request: one revision, one engine run.
	if again := s.tool(call, "craft_sheet_apply", apply); again != applied || s.engine.applied() != 1 || s.head() != 2 {
		t.Fatalf("an exact retry changed something: engine %d, version %d", s.engine.applied(), s.head())
	}
	if got := read("Summary", "B1"); !strings.Contains(got, `"value":"50"`) {
		t.Fatalf("dependent value after the change: %s", got)
	}
	s.tool(call, "craft_sheet_release", map[string]any{"generation": lease.Generation})

	// The person sees the new revision, byte for byte, and who made it.
	after := s.humanRequest(http.MethodGet, "", human, nil, s.m.documents.readSnapshot)
	if after.Code != 200 || json.Unmarshal(after.Body.Bytes(), &document) != nil || document.Version != "2" || string(document.Bytes) != string(scenarioSheetBytes("25")) {
		t.Fatalf("the person's Sheet after the change: %d %s", after.Code, after.Body)
	}
	history := decodeCraftNativeVersions(t, s.humanRequest(http.MethodGet, "", human, nil, s.m.documents.listVersions))
	if history.CurrentVersion != "2" || len(history.Versions) != 2 || history.Versions[0].Author.Kind != "agent" || history.Versions[1].Author.Kind == "agent" {
		t.Fatalf("history: %+v", history)
	}
	first := s.humanRequest(http.MethodGet, "", human, map[string]string{"version": "1"}, s.m.documents.readVersion)
	if first.Code != 200 {
		t.Fatalf("the revision before the change is gone: %d %s", first.Code, first.Body)
	}
	// The run's access is audited with the person it ran for.
	var audited int
	if err := s.f.rig.db.QueryRow(`SELECT COUNT(*) FROM audit_logs WHERE action='artifact.agent_run_accessed' AND entity_id='sheet-own'`).Scan(&audited); err != nil || audited == 0 {
		t.Fatalf("audited %d accesses: %v", audited, err)
	}
	// And the Sheet is free again for a manual change.
	if w := craftNativeLeaseCall(s.m.human, s.human, human, "sheet-own", "acquire", `{}`); w.Code != 200 {
		t.Fatalf("the person cannot take the Sheet after the agent released it: %d %s", w.Code, w.Body)
	}
}

// The same conversation without one of its two consents: the workspace flag, or
// the attachment on this very message. The message is stored, the run starts,
// and the Sheet is neither read nor changed.
func TestChatRunSheetScenarioWithoutConsentChangesNothing(t *testing.T) {
	for name, tc := range map[string]struct{ flagOn, attach bool }{
		"the workspace flag is off":                {false, true},
		"the Sheet is not attached to the message": {true, false},
	} {
		t.Run(name, func(t *testing.T) {
			s := newChatRunScenario(t, tc.flagOn)
			s.send("Change the number in Data A1 to 25", tc.attach)
			call := s.agent()
			for tool, arguments := range map[string]map[string]any{
				"craft_sheet_read":    {"query": map[string]any{"command": "cell.get", "params": map[string]any{"sheet": "Data", "cell": "A1"}}},
				"craft_sheet_acquire": {},
				"craft_sheet_apply":   {"generation": "1", "base_version": "1", "request_id": "refused", "commands": []any{map[string]any{"command": "cell.set", "params": map[string]any{"sheet": "Data", "cell": "A1", "input": "25"}}}},
			} {
				text, isError, _ := call("tools/call", map[string]any{"name": tool, "arguments": arguments})
				if !isError || strings.Contains(text, "sheet-own") {
					t.Errorf("%s: refused=%v %s", tool, isError, text)
				}
			}
			if s.engine.applied() != 0 || s.head() != 1 {
				t.Fatalf("engine ran %d times, version %d", s.engine.applied(), s.head())
			}
			var leases int
			if err := s.f.rig.db.QueryRow(`SELECT COUNT(*) FROM mooring_edit_leases`).Scan(&leases); err != nil || leases != 0 {
				t.Fatalf("%d leases: %v", leases, err)
			}
		})
	}
}

// What the person and the workspace have set, and what the agent's first tool
// call answers. The outcome is measured on the real path: the message through
// the bridge, the call through the sidecar. Attaching is what decides, not a
// grant the agent holds on the page.
func TestChatRunSheetScenarioWhoMayChangeTheSheet(t *testing.T) {
	agentGrant := func(level string) func(*chatRunScenario) {
		return func(s *chatRunScenario) {
			mustExec(s.t, s.f.rig.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES('sheet-own','agent',?,?,?)`, s.f.a.AgentID, level, s.f.a.HumanUserID)
		}
	}
	own := []string{"sheet-own"}
	for name, tc := range map[string]struct {
		flagOff bool
		pages   []string
		before  func(*chatRunScenario)
		// refused: the message itself is refused, before it is stored.
		refused bool
		// reason is what the agent is told; "" means it may change the Sheet.
		reason string
		// ownLease: the person holds the edit lease themselves. The access API keeps the chip on and
		// names the holder as theirs, while the agent is still told the reason.
		ownLease bool
	}{
		"attached, the agent holds no grant":            {pages: own},
		"attached, the agent holds a read grant":        {pages: own, before: agentGrant("read")},
		"attached, the agent holds a propose grant":     {pages: own, before: agentGrant("propose")},
		"not attached, the agent holds no grant":        {reason: CraftSheetReasonNotAttached},
		"not attached, the agent holds a read grant":    {before: agentGrant("read"), reason: CraftSheetReasonNotAttached},
		"not attached, the agent holds a propose grant": {before: agentGrant("propose"), reason: CraftSheetReasonNotAttached},
		"the workspace flag is off":                     {flagOff: true, pages: own, reason: CraftSheetReasonFeatureOff},
		"two Sheets on one message":                     {pages: []string{"sheet-own", "sheet-second"}, before: func(s *chatRunScenario) { s.seedSheet("sheet-second", s.f.a.HumanUserID) }, reason: CraftSheetReasonNotAttached},
		"the crew is not in the agent policy": {pages: own, before: func(s *chatRunScenario) {
			mustExec(s.t, s.f.rig.db, `DELETE FROM workspace_artifact_creation_crews`)
		}, reason: CraftSheetReasonAgentPolicy},
		"the Sheet left the personal space": {pages: own, before: func(s *chatRunScenario) {
			mustExec(s.t, s.f.rig.db, `UPDATE pages SET private_audience=0 WHERE id='sheet-own'`)
		}, reason: CraftSheetReasonNotAttached},
		"a person is editing the Sheet": {pages: own, before: func(s *chatRunScenario) {
			if w := craftNativeLeaseCall(s.m.human, s.human, s.humanSession(), "sheet-own", "acquire", `{}`); w.Code != 200 {
				s.t.Fatalf("human acquire: %d %s", w.Code, w.Body)
			}
		}, reason: CraftSheetReasonHolderHuman, ownLease: true},
		"the chat is shared": {pages: own, before: func(s *chatRunScenario) {
			mustExec(s.t, s.f.rig.db, `UPDATE chats SET visibility='group' WHERE id=?`, s.f.a.ChatID)
		}, refused: true, reason: CraftSheetReasonNotAttached},
		"someone else's Sheet, not shared with the person": {pages: own, before: func(s *chatRunScenario) {
			mustExec(s.t, s.f.rig.db, `UPDATE pages SET owner_user_id=? WHERE id='sheet-own'`, s.f.rig.adminID)
		}, refused: true, reason: CraftSheetReasonNotAttached},
	} {
		t.Run(name, func(t *testing.T) {
			s := newChatRunScenario(t, !tc.flagOff)
			if tc.before != nil {
				tc.before(s)
			}
			// The access API is asked what the attached Sheet's agent will be told,
			// before anything is sent, and must say exactly that.
			if len(tc.pages) == 1 {
				if got := s.access(); tc.ownLease {
					if !got.Allowed || got.Reason != "" || got.Holder == nil || !got.Holder.Mine {
						t.Errorf("access API with the person's own lease %+v: want allowed, holder theirs", got)
					}
				} else if got.Reason != tc.reason || got.Allowed != (tc.reason == "") || (got.Message == "") != (tc.reason == "") {
					t.Errorf("access API %+v, the agent gets %q", got, tc.reason)
				}
			}
			const content = "Change the number in Data A1 to 25"
			err := s.trySend(content, tc.pages...)
			if tc.refused {
				var stored int
				if scanErr := s.f.rig.db.QueryRow(`SELECT COUNT(*) FROM conversation_messages WHERE content=?`, content).Scan(&stored); scanErr != nil || stored != 0 {
					t.Fatalf("a refused message was stored %d times: %v", stored, scanErr)
				}
				if !errors.Is(err, chatbridge.ErrArtifactRefUnavailable) || s.resolver.runID != "" {
					t.Fatalf("the message was not refused: %v", err)
				}
				return
			}
			if !errors.Is(err, errScenarioModel) {
				t.Fatalf("the message did not reach a run: %v", err)
			}
			text, isError, _ := s.agent()("tools/call", map[string]any{"name": "craft_sheet_acquire", "arguments": map[string]any{}})
			var answer struct {
				Reason     string `json:"reason"`
				Generation string `json:"generation"`
			}
			if json.Unmarshal([]byte(text), &answer) != nil || answer.Reason != tc.reason || isError != (tc.reason != "") || (answer.Generation != "") != (tc.reason == "") {
				t.Fatalf("acquire: error=%v %s, want reason %q", isError, text, tc.reason)
			}
		})
	}
}

// The frame the web composer sends, through the WebSocket hub: what the browser
// puts in metadata reaches the run as the attached Sheet, and the agent changes it.
func TestChatRunSheetScenarioThroughTheWebSocket(t *testing.T) {
	s := newChatRunScenario(t, true)
	const secret = "synthetic-chatrun-scenario-jwt-secret"
	validator, err := auth.NewJWTValidator(secret)
	if err != nil {
		t.Fatal(err)
	}
	sessionStore := sessions.NewDBStore(s.f.rig.db)
	hub := ws.NewHub(newTestLogger(), nil, validator, sessionStore)
	hub.SetChannelAuthorizer(ws.NewDBChannelAuthorizer(s.f.rig.db))
	hub.SetChatHandler(s.bridge)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { hub.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	router, err := NewRouter(s.f.rig.db, secret, newTestLogger(), WithHub(hub))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(router.Shutdown)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /ws", hub.HandleUpgrade)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	session, err := sessionStore.Create(t.Context(), s.f.a.HumanUserID, "fixture", "127.0.0.1", auth.RefreshTokenTTL)
	if err != nil {
		t.Fatal(err)
	}
	bearer, err := validator.IssueAccessToken(s.f.a.HumanUserID, session.ID, "Fixture", "fixture@example.test")
	if err != nil {
		t.Fatal(err)
	}
	ask := httptest.NewRequest(http.MethodGet, "/api/v1/ws-token", nil)
	ask.Header.Set("Authorization", "Bearer "+bearer)
	ask.Header.Set("X-Workspace-ID", s.f.a.WorkspaceID)
	answer := httptest.NewRecorder()
	router.ServeHTTP(answer, ask)
	var ticket struct {
		Token string `json:"token"`
	}
	if answer.Code != 200 || json.Unmarshal(answer.Body.Bytes(), &ticket) != nil || ticket.Token == "" {
		t.Fatalf("WS ticket: %d %s", answer.Code, answer.Body)
	}
	conn, err := websocket.Dial(strings.Replace(server.URL, "http://", "ws://", 1)+"/ws", "", server.URL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	_ = conn.SetDeadline(time.Now().Add(15 * time.Second))
	for _, frame := range []map[string]any{{"type": "auth", "token": ticket.Token}, {"type": "ping"}} {
		if err = websocket.JSON.Send(conn, frame); err != nil {
			t.Fatal(err)
		}
	}
	var pong map[string]any
	if err = websocket.JSON.Receive(conn, &pong); err != nil || pong["type"] != "pong" {
		t.Fatalf("WS authentication: %v %v", pong, err)
	}
	if err = websocket.JSON.Send(conn, map[string]any{"type": "send_message", "channel": "session:" + s.f.a.ChatID, "payload": map[string]any{
		"session_id": s.f.a.ChatID, "content": "Change the number in Data A1 to 25", "metadata": scenarioRefs("sheet-own")}}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-s.resolver.admitted:
	case <-time.After(15 * time.Second):
		t.Fatal("the message from the socket started no run")
	}
	if s.resolver.admitErr != nil {
		t.Fatalf("run admission: %v", s.resolver.admitErr)
	}
	call := s.agent()
	var lease struct {
		Generation string `json:"generation"`
	}
	if got, isError, _ := call("tools/call", map[string]any{"name": "craft_sheet_acquire", "arguments": map[string]any{}}); isError || json.Unmarshal([]byte(got), &lease) != nil || lease.Generation == "" {
		var refs, scope, principals int
		_ = s.f.rig.db.QueryRow(`SELECT count(*) FROM artifact_message_refs WHERE chat_id=?`, s.f.a.ChatID).Scan(&refs)
		_ = s.f.rig.db.QueryRow(`SELECT count(*) FROM artifact_run_scope WHERE run_id=?`, s.resolver.runID).Scan(&scope)
		_ = s.f.rig.db.QueryRow(`SELECT count(*) FROM artifact_run_principals WHERE run_id=?`, s.resolver.runID).Scan(&principals)
		t.Errorf("socket run %q message %q refs-in-chat %d run-scope %d principal %d", s.resolver.runID, s.resolver.messageID, refs, scope, principals)
		t.Fatalf("acquire: %s", got)
	}
	s.tool(call, "craft_sheet_apply", map[string]any{"generation": lease.Generation, "base_version": "1", "request_id": "socket", "commands": []any{map[string]any{"command": "cell.set", "params": map[string]any{"sheet": "Data", "cell": "A1", "input": "25"}}}})
	if s.head() != 2 {
		t.Fatalf("version %d after the change", s.head())
	}
}

// Taking the consent or any of the rights it stands on away while the agent
// works: the next write is refused and the Sheet keeps its version.
func TestChatRunSheetScenarioRevokedMidRun(t *testing.T) {
	for name, revoke := range map[string]string{
		"the person leaves the workspace":     `DELETE FROM workspace_members WHERE user_id=(SELECT human_user_id FROM artifact_run_principals LIMIT 1)`,
		"the person becomes a guest":          `UPDATE workspace_members SET access_mode='restricted' WHERE user_id=(SELECT human_user_id FROM artifact_run_principals LIMIT 1)`,
		"the person is suspended":             `UPDATE users SET suspended_at='2026-01-01T00:00:00Z' WHERE id=(SELECT human_user_id FROM artifact_run_principals LIMIT 1)`,
		"the person's session ends":           `UPDATE user_sessions SET revoked_at='2026-01-01T00:00:00Z' WHERE user_id=(SELECT human_user_id FROM artifact_run_principals LIMIT 1)`,
		"the crew leaves the agent policy":    `DELETE FROM workspace_artifact_creation_crews`,
		"the workspace flag is turned off":    `UPDATE feature_flag_overrides SET enabled=0 WHERE id='scenario-flag-on'`,
		"the attachment is taken back":        `DELETE FROM artifact_message_refs`,
		"the chat becomes shared":             `UPDATE chats SET visibility='group'`,
		"the Sheet changes hands":             `UPDATE pages SET owner_user_id='admin' WHERE id='sheet-own'`,
		"the Sheet leaves the personal space": `UPDATE pages SET private_audience=0 WHERE id='sheet-own'`,
		"the Sheet is archived":               `UPDATE pages SET archived_at='2026-01-01T00:00:00Z' WHERE id='sheet-own'`,
	} {
		t.Run(name, func(t *testing.T) {
			s := newChatRunScenario(t, true)
			s.send("Change the number in Data A1", true)
			call := s.agent()
			var lease struct {
				Generation string `json:"generation"`
			}
			if got := s.tool(call, "craft_sheet_acquire", map[string]any{}); json.Unmarshal([]byte(got), &lease) != nil || lease.Generation == "" {
				t.Fatalf("acquire: %s", got)
			}
			if strings.Contains(revoke, "'admin'") {
				revoke = strings.ReplaceAll(revoke, "'admin'", "'"+s.f.rig.adminID+"'")
			}
			mustExec(t, s.f.rig.db, revoke)
			text, isError, _ := call("tools/call", map[string]any{"name": "craft_sheet_apply", "arguments": map[string]any{"generation": lease.Generation, "base_version": "1", "request_id": "revoked", "commands": []any{map[string]any{"command": "cell.set", "params": map[string]any{"sheet": "Data", "cell": "A1", "input": "25"}}}}})
			if !isError {
				t.Errorf("the agent still wrote: %s", text)
			}
			if s.head() != 1 {
				t.Errorf("the Sheet moved to version %d", s.head())
			}
			if text, isError, _ = call("tools/call", map[string]any{"name": "craft_sheet_read", "arguments": map[string]any{}}); !isError {
				t.Errorf("the agent still reads the Sheet: %s", text)
			}
		})
	}
}
