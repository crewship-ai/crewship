package api

import (
	"bytes"
	"context"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/chatbridge"
	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/crewship-ai/crewship/internal/webhook"
)

// ---------------------------------------------------------------------------
// Branch-coverage top-up for webhook.go, proxy_attachments.go, and
// mission_handler_mutate.go.
//
// Helpers introduced here are prefixed covWPM* to avoid colliding with the
// existing fakeChatResolver (webhook_test.go), newUnixIPCServer /
// newProxyHandlerForTest (proxy_test.go), seedMission* (missions_test.go /
// eval_handler_test.go), and the shared setupTestDB / seedTestUser /
// seedTestWorkspace / withWorkspaceUser / newTestLogger harness.
//
// SKIPPED (require live orchestrator/container/sidecar-filesystem stack):
//   - webhook.go trigger happy path: spawns a goroutine that calls
//     *orchestrator.Orchestrator.RunAgent (concrete type, not stubbable) +
//     log writer + ws hub. We cover the synchronous pre-goroutine surface
//     (ResolveAgent error via the inner handler, EnsureCrewRuntime error).
//   - proxy_attachments.go file-write happy path needs a sidecar serving the
//     /crews/{id}/files/save IPC endpoint; we stand up a Unix-socket stub
//     for it (no real container needed) and also cover the IPC-error and
//     IPC-unreachable forwarding branches.
// ---------------------------------------------------------------------------

// covWPMContainerProvider is a minimal provider.ContainerProvider whose only
// behaviour that matters here is EnsureCrewRuntime returning a fixed
// id/error. Every other method is a no-op so the webhook trigger's synchronous
// "ensure crew runtime" branch can be exercised without a Docker daemon.
type covWPMContainerProvider struct {
	ensureErr error
}

func (m *covWPMContainerProvider) EnsureCrewRuntime(_ context.Context, _ provider.CrewConfig) (string, error) {
	if m.ensureErr != nil {
		return "", m.ensureErr
	}
	return "container-cov", nil
}
func (m *covWPMContainerProvider) StopCrewRuntime(_ context.Context, _ string) error   { return nil }
func (m *covWPMContainerProvider) RemoveCrewRuntime(_ context.Context, _ string) error { return nil }
func (m *covWPMContainerProvider) ContainerStatus(_ context.Context, _ string) (*provider.ContainerStatus, error) {
	return nil, nil
}
func (m *covWPMContainerProvider) ContainerStats(_ context.Context, _ string) (*provider.ContainerMetrics, error) {
	return nil, nil
}
func (m *covWPMContainerProvider) Exec(_ context.Context, _ provider.ExecConfig) (*provider.ExecResult, error) {
	return nil, nil
}
func (m *covWPMContainerProvider) ExecInspect(_ context.Context, _ string) (bool, int, error) {
	return false, 0, nil
}
func (m *covWPMContainerProvider) CrewContainerName(_ string, slug string) string {
	return "crew-" + slug
}
func (m *covWPMContainerProvider) CopyToContainer(_ context.Context, _, _ string, _ io.Reader) error {
	return nil
}

var _ provider.ContainerProvider = (*covWPMContainerProvider)(nil)

// covWPMServeWebhook drives the WebhookHandler's public ServeHTTP with the
// given headers + body, returning the recorder. It sets the crewId/agentId
// path values the inner webhook.Handler reads.
func covWPMServeWebhook(h *WebhookHandler, crewID, agentID string, body []byte, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/webhooks/"+crewID+"/"+agentID, bytes.NewReader(body))
	req.SetPathValue("crewId", crewID)
	req.SetPathValue("agentId", agentID)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

// ---- webhook.go via ServeHTTP ----

func TestCovWPMWebhookMissingSignatureUnauthorized(t *testing.T) {
	resolver := &fakeChatResolver{lookupReturnSecret: "s3cr3t"}
	h := NewWebhookHandler(setupTestDB(t), newTestLogger(), resolver, nil, nil, nil, nil)

	rr := covWPMServeWebhook(h, "crew-1", "agent-1", []byte(`{"event":"x"}`), nil)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 (no X-Signature / X-Webhook-Secret)", rr.Code)
	}
}

func TestCovWPMWebhookSecretLookupFailureNotFound(t *testing.T) {
	// lookupSecret bubbles the resolver error; the inner handler maps it to
	// 404. Exercises webhook.go lookupSecret's error pass-through.
	resolver := &fakeChatResolver{lookupReturnErr: errors.New("no such agent")}
	h := NewWebhookHandler(setupTestDB(t), newTestLogger(), resolver, nil, nil, nil, nil)

	rr := covWPMServeWebhook(h, "crew-1", "agent-unknown", []byte(`{}`),
		map[string]string{"X-Signature": "deadbeef"})
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 (secret lookup failed)", rr.Code)
	}
}

func TestCovWPMWebhookInvalidHMACUnauthorized(t *testing.T) {
	resolver := &fakeChatResolver{lookupReturnSecret: "correct-secret"}
	h := NewWebhookHandler(setupTestDB(t), newTestLogger(), resolver, nil, nil, nil, nil)

	rr := covWPMServeWebhook(h, "crew-1", "agent-1", []byte(`{"event":"x"}`),
		map[string]string{"X-Signature": "not-the-right-hmac"})
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 (bad HMAC)", rr.Code)
	}
}

func TestCovWPMWebhookValidHMACTriggerResolveAgentError500(t *testing.T) {
	// Valid signature → handler calls trigger → trigger's ResolveAgent
	// fails → "resolve agent: ..." → inner handler maps trigger error to
	// 500. Covers webhook.go trigger up to the ResolveAgent branch through
	// the real HTTP path (not a direct trigger() call).
	body := []byte(`{"event":"deploy","source":"github"}`)
	secret := "shared-secret"
	resolver := &fakeChatResolver{
		lookupReturnSecret: secret,
		resolveReturnErr:   errors.New("agent vanished"),
	}
	h := NewWebhookHandler(setupTestDB(t), newTestLogger(), resolver, nil, nil, nil, nil)

	rr := covWPMServeWebhook(h, "crew-1", "agent-1", body,
		map[string]string{"X-Signature": webhook.ComputeHMAC(body, secret)})
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500 (trigger ResolveAgent error)", rr.Code)
	}
}

func TestCovWPMWebhookEnsureCrewRuntimeError500(t *testing.T) {
	// ResolveAgent succeeds, so trigger proceeds to EnsureCrewRuntime which
	// errors → "ensure crew runtime: ..." returned synchronously (no
	// goroutine spawned). Inner handler maps it to 500. This covers the
	// CreateChat + EnsureCrewRuntime-error span of trigger.
	body := []byte(`{"event":"deploy"}`)
	secret := "shared-secret"
	resolver := &fakeChatResolver{
		lookupReturnSecret: secret,
		resolveReturnInfo: &chatbridge.ChatInfo{
			AgentID:     "agent-1",
			AgentSlug:   "ag",
			CrewID:      "crew-1",
			CrewSlug:    "crew",
			WorkspaceID: "ws-1",
		},
	}
	container := &covWPMContainerProvider{ensureErr: errors.New("docker down")}
	h := NewWebhookHandler(setupTestDB(t), newTestLogger(), resolver, nil, nil, container, nil)

	rr := covWPMServeWebhook(h, "crew-1", "agent-1", body,
		map[string]string{"X-Signature": webhook.ComputeHMAC(body, secret)})
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500 (EnsureCrewRuntime error)", rr.Code)
	}
}

// ---- proxy_attachments.go ----

// covWPMSeedAttachmentAgent inserts an agent (with crew) and, optionally, a
// chat owned by that agent. Returns nothing; IDs are passed in.
func covWPMSeedAttachmentAgent(t *testing.T, h *ProxyHandler, wsID, agentID, slug, crewID string) {
	t.Helper()
	seedCrewRow(t, h.db, crewID, wsID, "C-"+crewID, "c-"+crewID)
	if _, err := h.db.Exec(`INSERT INTO agents (id, workspace_id, crew_id, name, slug, agent_role, status,
		cli_adapter, tool_profile, timeout_seconds, memory_enabled)
		VALUES (?, ?, ?, ?, ?, 'AGENT', 'IDLE', 'CLAUDE_CODE', 'CODING', 1800, 0)`,
		agentID, wsID, crewID, "N-"+agentID, slug); err != nil {
		t.Fatalf("seed agent: %v", err)
	}
}

func covWPMSeedChat(t *testing.T, h *ProxyHandler, chatID, agentID, wsID string) {
	t.Helper()
	if _, err := h.db.Exec(`INSERT INTO chats (id, agent_id, workspace_id, title, mode, status, started_at, created_at, updated_at)
		VALUES (?, ?, ?, 'T', 'CHAT', 'ACTIVE', datetime('now'), datetime('now'), datetime('now'))`,
		chatID, agentID, wsID); err != nil {
		t.Fatalf("seed chat: %v", err)
	}
}

// covWPMMultipartBody builds a multipart/form-data body with a single field.
// fieldName == "" omits the file field entirely (for the missing-field case).
func covWPMMultipartBody(t *testing.T, fieldName, filename, content string) (*bytes.Buffer, string) {
	t.Helper()
	buf := &bytes.Buffer{}
	mw := multipart.NewWriter(buf)
	if fieldName != "" {
		fw, err := mw.CreateFormFile(fieldName, filename)
		if err != nil {
			t.Fatalf("create form file: %v", err)
		}
		if _, err := fw.Write([]byte(content)); err != nil {
			t.Fatalf("write form file: %v", err)
		}
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	return buf, mw.FormDataContentType()
}

func covWPMAttachmentRequest(agentID, chatID, contentType string, body io.Reader, userID, wsID, role string) *http.Request {
	req := httptest.NewRequest("POST", "/api/v1/agents/"+agentID+"/chats/"+chatID+"/attachments", body)
	req.SetPathValue("agentId", agentID)
	req.SetPathValue("chatId", chatID)
	req.Header.Set("Content-Type", contentType)
	return withWorkspaceUser(req, userID, wsID, role)
}

func TestCovWPMAttachmentForbiddenRole(t *testing.T) {
	h := newProxyHandlerForTest(t, "/tmp/cov-no-socket")
	userID := seedTestUser(t, h.db)
	wsID := seedTestWorkspace(t, h.db, userID)

	body, ct := covWPMMultipartBody(t, "file", "a.txt", "x")
	req := covWPMAttachmentRequest("ag", "ch", ct, body, userID, wsID, "VIEWER")
	rr := httptest.NewRecorder()
	h.AgentChatAttachment(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403 (VIEWER cannot create)", rr.Code)
	}
}

func TestCovWPMAttachmentMissingIDs(t *testing.T) {
	h := newProxyHandlerForTest(t, "/tmp/cov-no-socket")
	userID := seedTestUser(t, h.db)
	wsID := seedTestWorkspace(t, h.db, userID)

	body, ct := covWPMMultipartBody(t, "file", "a.txt", "x")
	// Empty agentId/chatId path values.
	req := covWPMAttachmentRequest("", "", ct, body, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.AgentChatAttachment(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (agentId/chatId required)", rr.Code)
	}
}

func TestCovWPMAttachmentAgentNotFound(t *testing.T) {
	h := newProxyHandlerForTest(t, "/tmp/cov-no-socket")
	userID := seedTestUser(t, h.db)
	wsID := seedTestWorkspace(t, h.db, userID)

	body, ct := covWPMMultipartBody(t, "file", "a.txt", "x")
	req := covWPMAttachmentRequest("ghost", "ch", ct, body, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.AgentChatAttachment(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 (unknown agent)", rr.Code)
	}
}

func TestCovWPMAttachmentChatNotFound(t *testing.T) {
	h := newProxyHandlerForTest(t, "/tmp/cov-no-socket")
	userID := seedTestUser(t, h.db)
	wsID := seedTestWorkspace(t, h.db, userID)
	covWPMSeedAttachmentAgent(t, h, wsID, "ag", "ag-slug", "crew-att")

	body, ct := covWPMMultipartBody(t, "file", "a.txt", "x")
	req := covWPMAttachmentRequest("ag", "no-chat", ct, body, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.AgentChatAttachment(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 (chat not found)", rr.Code)
	}
}

func TestCovWPMAttachmentChatNotScoped403(t *testing.T) {
	h := newProxyHandlerForTest(t, "/tmp/cov-no-socket")
	userID := seedTestUser(t, h.db)
	wsID := seedTestWorkspace(t, h.db, userID)
	covWPMSeedAttachmentAgent(t, h, wsID, "ag", "ag-slug", "crew-att")
	covWPMSeedAttachmentAgent(t, h, wsID, "other-ag", "other-slug", "crew-att2")
	// Chat belongs to other-ag, not ag.
	covWPMSeedChat(t, h, "ch-other", "other-ag", wsID)

	body, ct := covWPMMultipartBody(t, "file", "a.txt", "x")
	req := covWPMAttachmentRequest("ag", "ch-other", ct, body, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.AgentChatAttachment(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403 (chat not scoped to this agent)", rr.Code)
	}
}

func TestCovWPMAttachmentInvalidMultipart400(t *testing.T) {
	h := newProxyHandlerForTest(t, "/tmp/cov-no-socket")
	userID := seedTestUser(t, h.db)
	wsID := seedTestWorkspace(t, h.db, userID)
	covWPMSeedAttachmentAgent(t, h, wsID, "ag", "ag-slug", "crew-att")
	covWPMSeedChat(t, h, "ch", "ag", wsID)

	// Claim multipart but send a non-multipart body → ParseMultipartForm errors.
	req := covWPMAttachmentRequest("ag", "ch", "multipart/form-data; boundary=xyz",
		strings.NewReader("this is not a valid multipart payload"), userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.AgentChatAttachment(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (invalid multipart)", rr.Code)
	}
}

func TestCovWPMAttachmentMissingFileField400(t *testing.T) {
	h := newProxyHandlerForTest(t, "/tmp/cov-no-socket")
	userID := seedTestUser(t, h.db)
	wsID := seedTestWorkspace(t, h.db, userID)
	covWPMSeedAttachmentAgent(t, h, wsID, "ag", "ag-slug", "crew-att")
	covWPMSeedChat(t, h, "ch", "ag", wsID)

	// Valid multipart but no "file" field → FormFile error.
	body, ct := covWPMMultipartBody(t, "", "", "")
	req := covWPMAttachmentRequest("ag", "ch", ct, body, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.AgentChatAttachment(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (file field required)", rr.Code)
	}
}

func TestCovWPMAttachmentInvalidFilename400(t *testing.T) {
	h := newProxyHandlerForTest(t, "/tmp/cov-no-socket")
	userID := seedTestUser(t, h.db)
	wsID := seedTestWorkspace(t, h.db, userID)
	covWPMSeedAttachmentAgent(t, h, wsID, "ag", "ag-slug", "crew-att")
	covWPMSeedChat(t, h, "ch", "ag", wsID)

	// filepath.Base(".") == "." → rejected as invalid filename.
	body, ct := covWPMMultipartBody(t, "file", ".", "x")
	req := covWPMAttachmentRequest("ag", "ch", ct, body, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.AgentChatAttachment(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (invalid filename)", rr.Code)
	}
}

func TestCovWPMAttachmentFilenameTooLong400(t *testing.T) {
	h := newProxyHandlerForTest(t, "/tmp/cov-no-socket")
	userID := seedTestUser(t, h.db)
	wsID := seedTestWorkspace(t, h.db, userID)
	covWPMSeedAttachmentAgent(t, h, wsID, "ag", "ag-slug", "crew-att")
	covWPMSeedChat(t, h, "ch", "ag", wsID)

	longName := strings.Repeat("a", 256) + ".txt"
	body, ct := covWPMMultipartBody(t, "file", longName, "x")
	req := covWPMAttachmentRequest("ag", "ch", ct, body, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.AgentChatAttachment(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (filename too long)", rr.Code)
	}
}

func TestCovWPMAttachmentUnreachableIPC502(t *testing.T) {
	// Valid request all the way to the IPC PUT, which fails because the
	// socket doesn't exist → 502.
	h := newProxyHandlerForTest(t, "/tmp/cov-no-such-socket-attach")
	userID := seedTestUser(t, h.db)
	wsID := seedTestWorkspace(t, h.db, userID)
	covWPMSeedAttachmentAgent(t, h, wsID, "ag", "ag-slug", "crew-att")
	covWPMSeedChat(t, h, "ch", "ag", wsID)

	body, ct := covWPMMultipartBody(t, "file", "photo.png", "binary-bytes")
	req := covWPMAttachmentRequest("ag", "ch", ct, body, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.AgentChatAttachment(rr, req)
	if rr.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want 502 (IPC unreachable)", rr.Code)
	}
}

func TestCovWPMAttachmentIPCErrorForwarded(t *testing.T) {
	// Sidecar stub returns 4xx → handler forwards the status verbatim with
	// the error body wrapped in JSON.
	sock := newUnixIPCServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte("disk full"))
	}))
	h := newProxyHandlerForTest(t, sock)
	userID := seedTestUser(t, h.db)
	wsID := seedTestWorkspace(t, h.db, userID)
	covWPMSeedAttachmentAgent(t, h, wsID, "ag", "ag-slug", "crew-att")
	covWPMSeedChat(t, h, "ch", "ag", wsID)

	body, ct := covWPMMultipartBody(t, "file", "photo.png", "data")
	req := covWPMAttachmentRequest("ag", "ch", ct, body, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.AgentChatAttachment(rr, req)
	if rr.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409 (IPC error forwarded verbatim)", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "disk full") {
		t.Errorf("body = %q, want to contain forwarded IPC error", rr.Body.String())
	}
}

func TestCovWPMAttachmentHappyPath201(t *testing.T) {
	// Sidecar stub accepts the save → 201 Created with the relative +
	// agent-side paths. Covers the success tail of AgentChatAttachment.
	var gotPath, gotBody string
	sock := newUnixIPCServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path + "?" + r.URL.RawQuery
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(http.StatusOK)
	}))
	h := newProxyHandlerForTest(t, sock)
	userID := seedTestUser(t, h.db)
	wsID := seedTestWorkspace(t, h.db, userID)
	covWPMSeedAttachmentAgent(t, h, wsID, "ag", "ag-slug", "crew-att")
	covWPMSeedChat(t, h, "ch", "ag", wsID)

	body, ct := covWPMMultipartBody(t, "file", "report.pdf", "pdf-bytes")
	req := covWPMAttachmentRequest("ag", "ch", ct, body, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.AgentChatAttachment(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", rr.Code, rr.Body.String())
	}
	if gotBody != "pdf-bytes" {
		t.Errorf("IPC received body = %q, want forwarded upload bytes", gotBody)
	}
	if !strings.HasPrefix(gotPath, "/crews/crew-att/files/save") {
		t.Errorf("IPC path = %q, want /crews/crew-att/files/save prefix", gotPath)
	}
	if !strings.Contains(rr.Body.String(), `"report.pdf"`) {
		t.Errorf("response = %q, want filename echoed", rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "/output/ag-slug/attachments/ch/report.pdf") {
		t.Errorf("response = %q, want agent_path", rr.Body.String())
	}
}

// ---- mission_handler_mutate.go: Update / Delete remaining branches ----

func covWPMNewMissionReq(method, crewID, missionID, role, userID, wsID, body string) *http.Request {
	req := httptest.NewRequest(method, "/api/v1/crews/"+crewID+"/missions/"+missionID, strings.NewReader(body))
	req.SetPathValue("crewId", crewID)
	req.SetPathValue("missionId", missionID)
	ctx := withUser(req.Context(), &AuthUser{ID: userID})
	ctx = withWorkspace(ctx, wsID, role)
	return req.WithContext(ctx)
}

func TestCovWPMMissionUpdateInvalidJSON400(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedMissionCrew(t, db, wsID)
	h := NewMissionHandler(db, nil, nil, newTestLogger())

	req := covWPMNewMissionReq("PATCH", crewID, "m1", "MANAGER", userID, wsID, `{not json`)
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (invalid JSON)", rr.Code)
	}
}

func TestCovWPMMissionUpdateNotFound404(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedMissionCrew(t, db, wsID)
	h := NewMissionHandler(db, nil, nil, newTestLogger())

	req := covWPMNewMissionReq("PATCH", crewID, "ghost", "MANAGER", userID, wsID, `{"title":"x"}`)
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 (mission not found)", rr.Code)
	}
}

func TestCovWPMMissionUpdateTitleDescriptionPlan(t *testing.T) {
	// Exercises the non-status Title / Description / Plan update branches in
	// a single call — none of these are covered by the status-only
	// TestMissionUpdate.
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedMissionCrew(t, db, wsID)
	leadID := seedMissionAgent(t, db, wsID, crewID, "lead-1", "LEAD")
	if _, err := db.Exec(`INSERT INTO missions (id, workspace_id, crew_id, lead_agent_id, trace_id, title, status, created_at, updated_at)
		VALUES ('mu', ?, ?, ?, 'trace-mu', 'Old', 'PLANNING', datetime('now'), datetime('now'))`, wsID, crewID, leadID); err != nil {
		t.Fatalf("insert mission: %v", err)
	}
	h := NewMissionHandler(db, nil, nil, newTestLogger())

	req := covWPMNewMissionReq("PATCH", crewID, "mu", "MANAGER", userID, wsID,
		`{"title":"New Title","description":"New Desc","plan":"Step 1\nStep 2"}`)
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	var title, desc, plan string
	if err := db.QueryRow(`SELECT title, description, plan FROM missions WHERE id = 'mu'`).Scan(&title, &desc, &plan); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if title != "New Title" || desc != "New Desc" || !strings.Contains(plan, "Step 1") {
		t.Errorf("persisted (%q,%q,%q), want title/desc/plan updated", title, desc, plan)
	}
}

func TestCovWPMMissionUpdateTerminalStatusEmitsHook(t *testing.T) {
	// PLANNING → CANCELLED is a valid terminal transition: covers the
	// completedAt branch + committedTerminalStatus path (storagePath unset,
	// so the async hook is a no-op but the branch executes).
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedMissionCrew(t, db, wsID)
	leadID := seedMissionAgent(t, db, wsID, crewID, "lead-1", "LEAD")
	if _, err := db.Exec(`INSERT INTO missions (id, workspace_id, crew_id, lead_agent_id, trace_id, title, status, created_at, updated_at)
		VALUES ('mc', ?, ?, ?, 'trace-mc', 'M', 'PLANNING', datetime('now'), datetime('now'))`, wsID, crewID, leadID); err != nil {
		t.Fatalf("insert mission: %v", err)
	}
	h := NewMissionHandler(db, nil, nil, newTestLogger())

	req := covWPMNewMissionReq("PATCH", crewID, "mc", "MANAGER", userID, wsID, `{"status":"CANCELLED"}`)
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	var completedAt *string
	if err := db.QueryRow(`SELECT completed_at FROM missions WHERE id = 'mc'`).Scan(&completedAt); err != nil {
		t.Fatalf("read completed_at: %v", err)
	}
	if completedAt == nil || *completedAt == "" {
		t.Error("completed_at not set on terminal transition")
	}
}

func TestCovWPMMissionUpdateForbidden403(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedMissionCrew(t, db, wsID)
	h := NewMissionHandler(db, nil, nil, newTestLogger())

	req := covWPMNewMissionReq("PATCH", crewID, "m1", "VIEWER", userID, wsID, `{"title":"x"}`)
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rr.Code)
	}
}

func TestCovWPMMissionUpdateDBClosed500(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedMissionCrew(t, db, wsID)
	h := NewMissionHandler(db, nil, nil, newTestLogger())
	db.Close() // BeginTx now fails → 500.

	req := covWPMNewMissionReq("PATCH", crewID, "m1", "MANAGER", userID, wsID, `{"title":"x"}`)
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500 (db closed)", rr.Code)
	}
}

func TestCovWPMMissionDeleteNotFound404(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedMissionCrew(t, db, wsID)
	h := NewMissionHandler(db, nil, nil, newTestLogger())

	req := covWPMNewMissionReq("DELETE", crewID, "ghost", "MANAGER", userID, wsID, "")
	rr := httptest.NewRecorder()
	h.Delete(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 (mission not found)", rr.Code)
	}
}

func TestCovWPMMissionDeleteWrongStatus400(t *testing.T) {
	// Mission in IN_PROGRESS: the guarded DELETE affects 0 rows, the
	// follow-up query finds it → 400 "wrong status".
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedMissionCrew(t, db, wsID)
	leadID := seedMissionAgent(t, db, wsID, crewID, "lead-1", "LEAD")
	if _, err := db.Exec(`INSERT INTO missions (id, workspace_id, crew_id, lead_agent_id, trace_id, title, status, created_at, updated_at)
		VALUES ('md', ?, ?, ?, 'trace-md', 'M', 'IN_PROGRESS', datetime('now'), datetime('now'))`, wsID, crewID, leadID); err != nil {
		t.Fatalf("insert mission: %v", err)
	}
	h := NewMissionHandler(db, nil, nil, newTestLogger())

	req := covWPMNewMissionReq("DELETE", crewID, "md", "MANAGER", userID, wsID, "")
	rr := httptest.NewRecorder()
	h.Delete(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (only PLANNING/CANCELLED deletable)", rr.Code)
	}
}

func TestCovWPMMissionDeleteForbidden403(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedMissionCrew(t, db, wsID)
	h := NewMissionHandler(db, nil, nil, newTestLogger())

	req := covWPMNewMissionReq("DELETE", crewID, "m1", "VIEWER", userID, wsID, "")
	rr := httptest.NewRecorder()
	h.Delete(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rr.Code)
	}
}

func TestCovWPMMissionDeleteDBClosed500(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedMissionCrew(t, db, wsID)
	h := NewMissionHandler(db, nil, nil, newTestLogger())
	db.Close() // initial DELETE Exec fails → 500.

	req := covWPMNewMissionReq("DELETE", crewID, "m1", "MANAGER", userID, wsID, "")
	rr := httptest.NewRecorder()
	h.Delete(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500 (db closed)", rr.Code)
	}
}
