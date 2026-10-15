package api

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/auth/sessions"
	"github.com/crewship-ai/crewship/internal/chataudience"
	"github.com/crewship-ai/crewship/internal/ws"
)

func documentRequestFixture(t *testing.T) (*PageHandler, string, string, string) {
	h, workspace, user, page, _ := artifactRunFixture(t)
	h.documentCall = func(context.Context, string, string, string, string, any) ([]byte, int, error) { return nil, 200, nil }
	execOrFatal(t, h.db, `INSERT INTO documents(workspace_id,page_id,schema_version,head_rev,snapshot,projection) VALUES(?,?,1,0,X'01','{}')`, workspace, page)
	return h, workspace, user, page
}
func createDocumentRequestTest(t *testing.T, h *PageHandler, workspace, user, body string, want int) string {
	t.Helper()
	r := pagesGrantRequest(t, "POST", "/", workspace, user, "OWNER", body)
	r.SetPathValue("slug", "report")
	session, err := sessions.NewDBStore(h.db).Create(t.Context(), user, "Docrequesttest", "127.0.0.1", auth.RefreshTokenTTL)
	if err != nil {
		t.Fatal(err)
	}
	UserFromContext(r.Context()).SessionID = session.ID
	w := httptest.NewRecorder()
	h.CreateDocumentRequest(w, r)
	artifactExpect(t, w, want)
	var id string
	if want == 202 {
		if err := h.db.QueryRow(`SELECT id FROM document_requests LIMIT 1`).Scan(&id); err != nil {
			t.Fatal(err)
		}
	}
	return id
}

const documentRequestBody = `{"chat_id":"artifact-chat","instruction":"Improve report","base_rev":0,"submission_id":"doc-first"}`

func TestDocumentRequestAdmissionIdempotencyAndRevocation(t *testing.T) {
	h, workspace, user, _ := documentRequestFixture(t)
	id := createDocumentRequestTest(t, h, workspace, user, documentRequestBody, 202)
	if retry := createDocumentRequestTest(t, h, workspace, user, documentRequestBody, 202); retry != id {
		t.Fatal("retry created a new request")
	}
	createDocumentRequestTest(t, h, workspace, user, strings.Replace(documentRequestBody, "Improve report", "Other", 1), 409)
	execOrFatal(t, h.db, `UPDATE document_requests SET status='running' WHERE id=?`, id)
	receipt, err := chataudience.CaptureTrusted(context.Background(), h.db, "artifact-chat", user, nil)
	if err != nil {
		t.Fatal(err)
	}
	execOrFatal(t, h.db, `DELETE FROM page_grants WHERE subject_type='agent'`)
	if bindDocumentRequestRun(httptest.NewRequest("POST", "/", nil), h.db, id, "artifact-run", receipt) == nil {
		t.Fatal("revoked grant admitted document run")
	}
}
func TestDocumentRequestCannotSucceedFromModelText(t *testing.T) {
	h, workspace, user, _ := documentRequestFixture(t)
	id := createDocumentRequestTest(t, h, workspace, user, documentRequestBody, 202)
	h.artifactDispatch = func(_ context.Context, _ string, _ string, prompt string, opts ws.ChatMessageOption) error {
		if opts.DocumentRequestID != id || !strings.Contains(prompt, id+"-checkpoint") {
			t.Fatal("lost explicit request/checkpoint authority")
		}
		// A model reporting completion cannot manufacture a durable saved result.
		return nil
	}
	h.dispatchNextDocumentRequest(context.Background())
	var status string
	if err := h.db.QueryRow(`SELECT status FROM document_requests WHERE id=?`, id).Scan(&status); err != nil || status != "failed" {
		t.Fatalf("status=%s err=%v", status, err)
	}
}

func TestDocumentRequestOriginSurvivesHistoricalRunReset(t *testing.T) {
	h, workspace, user, page := documentRequestFixture(t)
	id := createDocumentRequestTest(t, h, workspace, user, documentRequestBody, 202)
	execOrFatal(t, h.db, `UPDATE document_requests SET status='running' WHERE id=?`, id)
	receipt, err := chataudience.CaptureTrusted(t.Context(), h.db, "artifact-chat", user, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = bindDocumentRequestRun(httptest.NewRequest("POST", "/", nil), h.db, id, "artifact-run", receipt); err != nil {
		t.Fatal(err)
	}
	authority := &artifactRunAuthority{WorkspaceID: workspace, AgentID: "artifact-agent", CrewID: "artifact-crew", RunID: "artifact-run", HumanUserID: user, ChatID: "artifact-chat", MessageID: "artifact-message"}
	if err = h.checkArtifactRun(t.Context(), page, authority); err != nil {
		t.Fatal(err)
	}
	if err = h.checkDocumentRequestAuthority(t.Context(), page, authority); err != nil {
		t.Fatal(err)
	}
	if _, err = h.db.Exec(`UPDATE page_artifact_run_delegations SET document_request_id=NULL WHERE page_id=? AND run_id='artifact-run'`, page); err == nil {
		t.Fatal("request origin could be cleared")
	}
	// A reset may retain a live generic principal/delegation, but it must not
	// turn an explicit document job into a generic delegation by clearing run_id.
	execOrFatal(t, h.db, `UPDATE document_requests SET status='failed',run_id=NULL,receipt_json='{}' WHERE id=?`, id)
	if err = h.checkArtifactRun(t.Context(), page, authority); err != nil {
		t.Fatalf("fixture must retain generic live authority: %v", err)
	}
	if err = h.checkDocumentRequestAuthority(t.Context(), page, authority); err == nil {
		t.Fatal("reset request silently became a generic live delegation")
	}
	var origin string
	if err = h.db.QueryRow(`SELECT document_request_id FROM page_artifact_run_delegations WHERE page_id=? AND run_id='artifact-run'`, page).Scan(&origin); err != nil || origin != id {
		t.Fatalf("immutable origin=%q err=%v", origin, err)
	}
	// Removing the request removes its runtime delegation, never its durable
	// agent principal or the surrounding Page/checkpoint history.
	execOrFatal(t, h.db, `DELETE FROM document_requests WHERE id=?`, id)
	var principalCount int
	if err = h.db.QueryRow(`SELECT COUNT(*) FROM artifact_run_principals WHERE run_id='artifact-run'`).Scan(&principalCount); err != nil || principalCount != 1 {
		t.Fatalf("principal fixture lost: count=%d err=%v", principalCount, err)
	}
	if err = h.checkDocumentRequestAuthority(t.Context(), page, authority); err == nil {
		t.Fatal("missing request retained document authority")
	}
}
