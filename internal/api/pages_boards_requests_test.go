package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/chataudience"
	"github.com/crewship-ai/crewship/internal/ws"
)

const boardDiagramRequest = `{"kind":"diagram","chat_id":"artifact-chat","instruction":"Draw a simple process","base_seq":0,"submission_id":"diagram-request","selected_element_ids":[],"selected_placement_ids":[],"target":{"x":0,"y":0,"width":400,"height":300},"dialect":"flowchart-v1","converter_version":"2.2.2"}`

func boardJobCall(t *testing.T, h *PageHandler, workspace, user, id, body string, handler http.HandlerFunc) *httptest.ResponseRecorder {
	t.Helper()
	r := pagesGrantRequest(t, "POST", "/", workspace, user, "OWNER", body)
	r.SetPathValue("slug", "report")
	r.SetPathValue("requestId", id)
	w := httptest.NewRecorder()
	handler(w, r)
	return w
}
func boardJobAgentCall(t *testing.T, h *PageHandler, workspace, token, id, body string, handler http.HandlerFunc) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest("POST", "/", strings.NewReader(body))
	r.SetPathValue("requestId", id)
	r.Header.Set("Authorization", "Bearer "+token)
	ctx := context.WithValue(r.Context(), ctxInternalTokenWS, workspace)
	ctx = context.WithValue(ctx, ctxInternalTokenCrew, "artifact-crew")
	w := httptest.NewRecorder()
	handler(w, r.WithContext(ctx))
	return w
}
func boardJobFixture(t *testing.T) (*PageHandler, string, string, string, string, string) {
	t.Helper()
	h, workspace, user, page, token := boardRunFixture(t)
	h.artifactDispatch = func(context.Context, string, string, string, ws.ChatMessageOption) error { return nil }
	w := boardJobCall(t, h, workspace, user, "", boardDiagramRequest, h.CreateBoardRequest)
	artifactExpect(t, w, 202)
	var out struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(w.Body.Bytes(), &out) != nil || out.ID == "" {
		t.Fatal(w.Body.String())
	}
	return h, workspace, user, page, token, out.ID
}
func TestBoardMermaidStrictProfile(t *testing.T) {
	for _, source := range []string{"flowchart TD\nA[Hello] --> B[World]", "flowchart LR\nA[Žluťoučký kůň]\nA -.-> B"} {
		if _, err := validateBoardMermaid(source); err != nil {
			t.Fatal(err)
		}
	}
	for _, source := range []string{"flowchart TD\nA[<img src=x>]", "flowchart TD\nclick A https://example.com", "flowchart TD\nA[https://example.com]", "%%{init: {}}%%\nflowchart TD\nA", "flowchart TD\nA[one]\nA[two]", "flowchart TD\nA --> B --> C", "flowchart TD\nA[&lt;script&gt;]"} {
		if _, err := validateBoardMermaid(source); err == nil {
			t.Fatalf("unsafe source accepted %q", source)
		}
	}
}
func TestBoardRequestIdempotencyCancellationAndArrays(t *testing.T) {
	h, workspace, user, _, _, id := boardJobFixture(t)
	w := boardJobCall(t, h, workspace, user, "", boardDiagramRequest, h.CreateBoardRequest)
	artifactExpect(t, w, 202)
	if !strings.Contains(w.Body.String(), id) {
		t.Fatal("retry changed request")
	}
	artifactExpect(t, boardJobCall(t, h, workspace, user, "", strings.Replace(boardDiagramRequest, "simple process", "different", 1), h.CreateBoardRequest), 409)
	w = boardJobCall(t, h, workspace, user, id, "", h.ListBoardRequests)
	artifactExpect(t, w, 200)
	if !strings.Contains(w.Body.String(), `"selected_element_ids":[]`) || strings.Contains(w.Body.String(), `"eligible_chats":null`) {
		t.Fatal(w.Body.String())
	}
	for i := 0; i < 2; i++ {
		artifactExpect(t, boardJobCall(t, h, workspace, user, id, "", h.CancelBoardRequest), 200)
	}
}
func TestBoardRequestDiagramRequiresLiveRunThenExplicitAtomicInsert(t *testing.T) {
	h, workspace, user, page, token, id := boardJobFixture(t)
	publish := `{"mutation_id":"source","source":"flowchart TD\nA[Hello] --> B[World]","dialect":"flowchart-v1","converter_version":"2.2.2"}`
	artifactExpect(t, boardJobAgentCall(t, h, workspace, token, id, publish, h.PublishBoardRequestMermaid), 403)
	if _, err := h.db.Exec(`UPDATE page_board_requests SET status='running',run_id='artifact-run' WHERE id=?`, id); err != nil {
		t.Fatal(err)
	}
	artifactExpect(t, boardJobAgentCall(t, h, workspace, token, id, publish, h.PublishBoardRequestMermaid), 201)
	artifactExpect(t, boardJobAgentCall(t, h, workspace, token, id, publish, h.PublishBoardRequestMermaid), 200)
	var hash string
	var seq int64
	if h.db.QueryRow(`SELECT source_hash FROM page_board_requests WHERE id=?`, id).Scan(&hash) != nil {
		t.Fatal("hash missing")
	}
	if h.db.QueryRow(`SELECT seq FROM page_boards WHERE page_id=?`, page).Scan(&seq) != nil || seq != 0 {
		t.Fatal("agent source modified board")
	}
	insert := func(base int64, elementID string) string {
		raw, _ := json.Marshal(map[string]any{"mutation_id": "insert", "base_seq": base, "source_hash": hash, "converter_version": "2.2.2", "upserts": []map[string]any{{"element": boardAgentElement(t, elementID, nil), "expected_rev": 0}}})
		return string(raw)
	}
	artifactExpect(t, boardJobCall(t, h, workspace, user, id, insert(99, "converted"), h.InsertBoardRequest), 409)
	artifactExpect(t, boardJobCall(t, h, workspace, user, id, insert(0, "converted"), h.InsertBoardRequest), 201)
	artifactExpect(t, boardJobCall(t, h, workspace, user, id, insert(0, "second-tab"), h.InsertBoardRequest), 200)
	var count int
	if h.db.QueryRow(`SELECT COUNT(*) FROM page_board_elements WHERE board_page_id=? AND origin_request_id=? AND author_agent_id IS NULL AND author_user_id=?`, page, id, user).Scan(&count) != nil || count != 1 {
		t.Fatal("conversion provenance or once-only fence broken")
	}
}

func TestBoardRequestTrustedDispatchAndScopedCreationSurviveBoardDeletion(t *testing.T) {
	h, workspace, user, page, token, _ := boardJobFixture(t)
	artifactExpect(t, boardCall(t, h, workspace, user, "OWNER", "report", "batch", boardAgentBatch(t, "wireframe", 0, 0, boardAgentElement(t, "wire", nil))), 201)
	body := strings.Replace(boardDiagramRequest, `"kind":"diagram"`, `"kind":"artifact"`, 1)
	body = strings.Replace(body, `"base_seq":0`, `"base_seq":1`, 1)
	body = strings.Replace(body, `"submission_id":"diagram-request"`, `"submission_id":"artifact-job"`, 1)
	body = strings.Replace(body, `"selected_element_ids":[]`, `"selected_element_ids":["wire"]`, 1)
	response := boardJobCall(t, h, workspace, user, "", body, h.CreateBoardRequest)
	artifactExpect(t, response, 202)
	var out struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(response.Body.Bytes(), &out)
	execOrFatal(t, h.db, `UPDATE page_board_requests SET status='cancelled' WHERE kind='diagram'`)
	called := false
	h.artifactDispatch = func(ctx context.Context, actualUser, chat, prompt string, opts ws.ChatMessageOption) error {
		called = true
		if actualUser != user || opts.BoardRequestID != out.ID || !strings.Contains(prompt, "immutable selected snapshot") {
			t.Fatal("lost server job context")
		}
		r := httptest.NewRequest("POST", "/", nil)
		if err := bindBoardRequestRun(r, h.db, out.ID, "artifact-run", opts.HumanAuthority); err != nil {
			t.Fatal(err)
		}
		// The admitted artifact creation capability remains useful after source
		// deletion, while selected source/asset reads fail closed.
		execOrFatal(t, h.db, `DELETE FROM pages WHERE id=?`, page)
		artifactExpect(t, boardJobAgentCall(t, h, workspace, token, out.ID, "", h.GetBoardRequestAgent), 403)
		artifactExpect(t, artifactCreateAgent(t, h, workspace, token, creationBody), 201)
		artifactExpect(t, artifactCreateAgent(t, h, workspace, token, creationBody), 200)
		artifactExpect(t, artifactCreateAgent(t, h, workspace, token, strings.Replace(creationBody, "create-one", "second", 1)), 409)
		return nil
	}
	h.dispatchNextBoardRequest(context.Background())
	if !called {
		t.Fatal("job not dispatched")
	}
	var status, pin string
	var result sql.NullString
	var detached sql.NullString
	if err := h.db.QueryRow(`SELECT status,pin_status,result_page_id,board_page_id FROM page_board_requests WHERE id=?`, out.ID).Scan(&status, &pin, &result, &detached); err != nil || status != "succeeded" || pin != "created_not_pinned" || !result.Valid || detached.Valid {
		t.Fatalf("detached output not retained %s %s %v %v %v", status, pin, result, detached, err)
	}
}
func TestBoardRequestCancellationRevokesCreationAndAgentForgedIdentity(t *testing.T) {
	h, workspace, user, page, token, id := boardJobFixture(t)
	receipt, _ := chataudience.CaptureTrusted(context.Background(), h.db, "artifact-chat", user, nil)
	execOrFatal(t, h.db, `UPDATE page_board_requests SET kind='artifact',status='running' WHERE id=?`, id)
	r := httptest.NewRequest("POST", "/", nil)
	scoped := r.WithContext(context.WithValue(r.Context(), ctxInternalTokenWS, workspace))
	if bindBoardRequestRun(scoped, h.db, id, "artifact-run", receipt) == nil {
		t.Fatal("crew IPC minted job authority")
	}
	if err := bindBoardRequestRun(r, h.db, id, "artifact-run", receipt); err != nil {
		t.Fatal(err)
	}
	artifactExpect(t, boardJobCall(t, h, workspace, user, id, "", h.CancelBoardRequest), 200)
	artifactExpect(t, artifactCreateAgent(t, h, workspace, token, creationBody), 403)
	var count int
	if h.db.QueryRow(`SELECT COUNT(*) FROM pages WHERE id!=?`, page).Scan(&count) != nil || count != 0 {
		t.Fatal("cancelled job created artifact")
	}
	artifactExpect(t, boardJobAgentCall(t, h, workspace, token, id, `{"mutation_id":"x","source":"flowchart TD\nA","dialect":"flowchart-v1","converter_version":"2.2.2","human_user_id":"forged"}`, h.PublishBoardRequestMermaid), 400)
}

func TestBoardRequestPromptDoesNotPersistSnapshotBeforeAdmission(t *testing.T) {
	h, workspace, user, _, _, _ := boardJobFixture(t)
	artifactExpect(t, boardCall(t, h, workspace, user, "OWNER", "report", "batch", boardAgentBatch(t, "secret", 0, 0, boardAgentElement(t, "secret-element", map[string]any{"x": 493123}))), 201)
	body := strings.Replace(boardDiagramRequest, `"base_seq":0`, `"base_seq":1`, 1)
	body = strings.Replace(body, `"submission_id":"diagram-request"`, `"submission_id":"secret-job"`, 1)
	body = strings.Replace(body, `"selected_element_ids":[]`, `"selected_element_ids":["secret-element"]`, 1)
	response := boardJobCall(t, h, workspace, user, "", body, h.CreateBoardRequest)
	artifactExpect(t, response, 202)
	var out struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(response.Body.Bytes(), &out)
	execOrFatal(t, h.db, `UPDATE page_board_requests SET status='cancelled' WHERE id!=?`, out.ID)
	called := false
	h.artifactDispatch = func(_ context.Context, _ string, _ string, prompt string, opts ws.ChatMessageOption) error {
		called = true
		if strings.Contains(prompt, "493123") || strings.Contains(prompt, "secret-element") {
			t.Fatal("source copied into chat before admission")
		}
		execOrFatal(t, h.db, `DELETE FROM workspace_members WHERE workspace_id=? AND user_id=?`, workspace, user)
		if bindBoardRequestRun(httptest.NewRequest("POST", "/", nil), h.db, out.ID, "artifact-run", opts.HumanAuthority) == nil {
			t.Fatal("revoked access admitted source run")
		}
		return errors.New("admission refused")
	}
	h.dispatchNextBoardRequest(context.Background())
	if !called {
		t.Fatal("dispatch not called")
	}
	var status string
	if h.db.QueryRow(`SELECT status FROM page_board_requests WHERE id=?`, out.ID).Scan(&status) != nil || status != "failed" {
		t.Fatal("refused job remains live")
	}
}

func TestBoardRequestEditorCanShareOnlyExplicitSnapshot(t *testing.T) {
	h, workspace, user, page, _, _ := boardJobFixture(t)
	pagesSeedUser(t, h, workspace, "board-owner", "board-owner@example.test", "MEMBER")
	execOrFatal(t, h.db, `UPDATE pages SET owner_user_id='board-owner' WHERE id=?`, page)
	execOrFatal(t, h.db, `UPDATE workspace_members SET role='MEMBER' WHERE workspace_id=? AND user_id=?`, workspace, user)
	execOrFatal(t, h.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES(?,'user',?,'write','board-owner')`, page, user)
	body := strings.Replace(boardDiagramRequest, "diagram-request", "editor-request", 1)
	artifactExpect(t, boardJobCall(t, h, workspace, user, "", body, h.CreateBoardRequest), 202)
	execOrFatal(t, h.db, `UPDATE page_grants SET level='read' WHERE page_id=? AND subject_type='user' AND subject_id=?`, page, user)
	// Even a request context carrying an old OWNER role cannot replay admission
	// after the live write grant is gone.
	artifactExpect(t, boardJobCall(t, h, workspace, user, "", body, h.CreateBoardRequest), 403)
}
