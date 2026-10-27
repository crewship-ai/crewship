package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
)

func TestArtifactProjectToolCannotUseHumanImpersonation(t *testing.T) {
	f := originalUserPrincipalFixture(t)
	f.admit(t)
	r := pagesRequest(t, "POST", "/api/v1/internal/pages/artifact-tool", f.a.WorkspaceID, f.a.HumanUserID, "OWNER", `{"operation":"artifact_create","slug":"demo","payload":{"name":"Demo"}}`)
	w := httptest.NewRecorder()
	f.pages.ArtifactProjectTool(w, r)
	if w.Code != 403 {
		t.Fatalf("human without crew transport/run capability reached internal tool: %d %s", w.Code, w.Body.String())
	}
}

func TestArtifactProjectToolClosedCanonicalEnvelope(t *testing.T) {
	for _, raw := range []string{`{"operation":"artifact_create","Operation":"artifact_publish"}`, `{"Operation":"artifact_create"}`, `{"payload":{"files":[{"path":"index.html","Path":"other.html"}]}}`} {
		var packet struct {
			Operation string          `json:"operation"`
			Payload   json.RawMessage `json:"payload"`
		}
		if err := decodeArtifactProjectJSONBytes([]byte(raw), &packet); err == nil {
			t.Errorf("ambiguous field casing accepted: %s", raw)
		}
	}
}

func TestArtifactProjectRunConsentRejectsMissingAndOtherPage(t *testing.T) {
	f := originalUserPrincipalFixture(t)
	f.admit(t)
	if err := f.pages.checkArtifactProjectRunConsent(t.Context(), &f.a, ""); err == nil {
		t.Fatal("provenance without explicit consent became creation authority")
	}
	f.rig.seedPage(t, "exact-page", "exact-page", "Exact", f.a.HumanUserID, "")
	f.rig.seedPage(t, "other-page", "other-page", "Other", f.a.HumanUserID, "")
	mustExec(t, f.rig.db, `INSERT INTO artifact_project_run_consents(run_id,workspace_id,human_user_id,chat_id,message_id,expires_at,created_at,result_page_id) SELECT run_id,workspace_id,human_user_id,chat_id,message_id,expires_at,expires_at,'exact-page' FROM artifact_run_principals WHERE run_id=?`, f.a.RunID)
	if err := f.pages.checkArtifactProjectRunConsent(t.Context(), &f.a, "other-page"); err == nil {
		t.Fatal("consent expanded to another human-owned Page")
	}
	if _, err := f.rig.db.Exec(`UPDATE artifact_project_run_consents SET result_page_id='other-page' WHERE run_id=?`, f.a.RunID); err == nil || !strings.Contains(err.Error(), "immutable") {
		t.Fatal("result Page binding could be replaced", err)
	}
}

func TestArtifactProjectToolsConsentCreatePublishReplyAndRevocation(t *testing.T) {
	t.Setenv("CREWSHIP_ARTIFACT_PROJECTS_ENABLED", "true")
	f := artifactProjectOriginalRunFixture(t)
	f.admit(t)
	f.pages.artifactMasterToken = "synthetic-artifact-project-master"
	humanToken := mintTokenFor(t, f.rig.db, f.a.HumanUserID, "artifact-project-consent")
	human := func(path string, payload string, handler http.HandlerFunc) *httptest.ResponseRecorder {
		r := pagesRequest(t, "POST", path, f.a.WorkspaceID, f.a.HumanUserID, "OWNER", payload)
		r.Header.Set("Authorization", "Bearer "+humanToken)
		r = r.WithContext(context.WithValue(r.Context(), ctxAuthKind, AuthKindCLIToken))
		r.SetPathValue("slug", "agent-screens")
		w := httptest.NewRecorder()
		handler(w, r)
		return w
	}
	runToken := internaltoken.DeriveAgentRunToken(internaltoken.DeriveAgentRunKey(f.pages.artifactMasterToken, f.a.WorkspaceID, f.a.CrewID), f.a.WorkspaceID, f.a.AgentID, f.a.RunID)
	tool := func(operation, slug, payload string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/api/v1/internal/pages/artifact-tool", strings.NewReader(fmt.Sprintf(`{"operation":%q,"slug":%q,"payload":%s}`, operation, slug, payload)))
		r.Header.Set("Authorization", "Bearer "+runToken)
		ctx := context.WithValue(r.Context(), ctxInternalTokenWS, f.a.WorkspaceID)
		ctx = context.WithValue(ctx, ctxInternalTokenCrew, f.a.CrewID)
		w := httptest.NewRecorder()
		f.pages.ArtifactProjectTool(w, r.WithContext(ctx))
		return w
	}
	createBody := `{"name":"Agent screens",` + strings.TrimPrefix(artifactProjectBody(0, "First"), "{")
	if w := tool("artifact_create", "agent-screens", createBody); w.Code != 403 {
		t.Fatalf("creation without consent %d %s", w.Code, w.Body)
	}
	grant := human("/api/v1/pages/artifacts/consent", `{"run_id":"principal-run"}`, f.pages.GrantArtifactProjectRunConsent)
	if grant.Code != 200 {
		t.Fatalf("human consent %d %s", grant.Code, grant.Body)
	}
	created := tool("artifact_create", "agent-screens", createBody)
	if created.Code != 201 {
		t.Fatalf("signed run creation %d %s", created.Code, created.Body)
	}
	var page string
	if err := f.rig.db.QueryRow(`SELECT id FROM pages WHERE slug='agent-screens'`).Scan(&page); err != nil {
		t.Fatal(err)
	}
	if err := f.pages.checkArtifactProjectRunConsent(t.Context(), &f.a, page); err != nil {
		t.Fatal("positive consent guard calibration", err)
	}
	mustExec(t, f.rig.db, `INSERT INTO artifact_project_message_intents(message_id,workspace_id,human_user_id,chat_id,submission_id,base_version,run_id,created_at) VALUES(?,?,?,?,?,0,?,datetime('now'))`, f.a.MessageID, f.a.WorkspaceID, f.a.HumanUserID, f.a.ChatID, "explicit-original-project", f.a.RunID)
	if w := tool("artifact_create", "second-project", createBody); w.Code != 403 {
		t.Fatalf("second create %d %s", w.Code, w.Body)
	}
	read := tool("artifact_versions", "agent-screens", `{}`)
	if read.Code != 200 || !strings.Contains(read.Body.String(), "details.html") || !strings.Contains(read.Body.String(), "settings.html") {
		t.Fatalf("read three screens %d %s", read.Code, read.Body)
	}
	comment := human("/api/v1/pages/agent-screens/artifact/comments", `{"version":1,"path":"index.html","selector":"#hero","quote":"First","text":"Change heading","send_to_agent":true}`, f.pages.CreateArtifactProjectComment)
	if comment.Code != 201 {
		t.Fatalf("comment %d %s", comment.Code, comment.Body)
	}
	var commentBody struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(comment.Body.Bytes(), &commentBody) != nil {
		t.Fatal("invalid comment response")
	}
	if commentBody.ID == "" {
		if err := f.rig.db.QueryRow(`SELECT id FROM artifact_project_comments WHERE page_id=?`, page).Scan(&commentBody.ID); err != nil {
			t.Fatal(err)
		}
	}
	if w := tool("artifact_publish", "agent-screens", artifactProjectBody(1, "Second")); w.Code != 200 {
		t.Fatalf("publish v2 %d %s", w.Code, w.Body)
	}
	if w := tool("artifact_reply", "agent-screens", fmt.Sprintf(`{"comment_id":%q,"version":2,"text":"Changed the heading"}`, commentBody.ID)); w.Code != 201 && w.Code != 200 {
		t.Fatalf("reply %d %s", w.Code, w.Body)
	}
	if w := tool("artifact_publish", "agent-screens", artifactProjectBody(1, "Stale")); w.Code != 409 {
		t.Fatalf("stale manifest %d %s", w.Code, w.Body)
	}
	// A complete reread is information, not a renewed mandate. If the human
	// commits after this run's own v2, the run must not adopt the new base and
	// overwrite a different selected element under its original permission.
	if w := human("/api/v1/pages/agent-screens/artifact/versions", artifactProjectBody(2, "Human concurrent change"), f.pages.PublishArtifactProject); w.Code != 200 {
		t.Fatalf("concurrent human save %d %s", w.Code, w.Body)
	}
	if w := tool("artifact_versions", "agent-screens", `{}`); w.Code != 200 || !strings.Contains(w.Body.String(), "Human concurrent change") {
		t.Fatalf("latest reread %d %s", w.Code, w.Body)
	}
	if w := tool("artifact_publish", "agent-screens", artifactProjectBody(3, "Blindly rebased old selection")); w.Code != 409 {
		t.Fatalf("reread silently expanded original mandate %d %s", w.Code, w.Body)
	}
	if w := tool("artifact_restore", "agent-screens", `{"base_version":3,"version":1}`); w.Code != 409 {
		t.Fatalf("restore silently expanded original mandate %d %s", w.Code, w.Body)
	}
	f.rig.seedPage(t, "unrelated-project", "unrelated-project", "Unrelated", f.a.HumanUserID, "")
	if w := tool("artifact_publish", "unrelated-project", artifactProjectBody(0, "Wrong page")); w.Code != 403 && w.Code != 404 {
		t.Fatalf("wrong page %d %s", w.Code, w.Body)
	}
	mustExec(t, f.rig.db, `UPDATE artifact_project_message_intents SET revoked_at='2026-10-27T00:00:00Z' WHERE run_id=?`, f.a.RunID)
	if _, err := f.rig.db.Exec(`UPDATE artifact_project_message_intents SET revoked_at=NULL WHERE run_id=?`, f.a.RunID); err == nil {
		t.Fatal("revoked typed intent revived through ABA")
	}
	for _, operation := range []string{"artifact_versions", "artifact_comments", "artifact_publish", "artifact_restore", "artifact_reply"} {
		payload := `{}`
		switch operation {
		case "artifact_publish":
			payload = artifactProjectBody(2, "Revoked typed intent")
		case "artifact_restore":
			payload = `{"base_version":2,"version":1}`
		case "artifact_reply":
			payload = fmt.Sprintf(`{"comment_id":%q,"version":2,"text":"Revoked reply"}`, commentBody.ID)
		}
		if w := tool(operation, "agent-screens", payload); w.Code != 403 {
			t.Fatalf("revoked typed intent %s fell through to manual consent %d %s", operation, w.Code, w.Body)
		}
	}
	mustExec(t, f.rig.db, `UPDATE artifact_project_run_consents SET revoked_at='2026-10-27T00:00:00Z' WHERE run_id=?`, f.a.RunID)
	if _, err := f.rig.db.Exec(`UPDATE artifact_project_run_consents SET revoked_at=NULL WHERE run_id=?`, f.a.RunID); err == nil {
		t.Fatal("revoked run consent revived through ABA")
	}
	if w := tool("artifact_publish", "agent-screens", artifactProjectBody(2, "Revoked")); w.Code != 403 {
		t.Fatalf("revoked consent %d %s", w.Code, w.Body)
	}
	if w := human("/api/v1/pages/artifacts/consent", `{"run_id":"principal-run"}`, f.pages.GrantArtifactProjectRunConsent); w.Code != 403 {
		t.Fatalf("consent revival %d %s", w.Code, w.Body)
	}
}

func artifactProjectOriginalRunFixture(t *testing.T) *artifactPrincipalFixture {
	t.Helper()
	f, source := originalSessionFixture(t)
	f.rig.db = artifactProjectFullDurability(t, f.rig.db)
	f.pages.db = f.rig.db
	mustExec(t, f.rig.db, `UPDATE chats SET visibility='private' WHERE id=?`, f.a.ChatID)
	mustExec(t, f.rig.db, `UPDATE workspace_members SET capabilities='["chat","page.create"]' WHERE user_id=?`, f.a.HumanUserID)
	mustExec(t, f.rig.db, `INSERT INTO workspace_artifact_creation_policy(workspace_id,enabled,version,updated_at) VALUES(?,1,1,'2026-10-27T00:00:00Z')`, f.a.WorkspaceID)
	mustExec(t, f.rig.db, `INSERT INTO workspace_artifact_creation_crews(workspace_id,crew_id) VALUES(?,?)`, f.a.WorkspaceID, f.a.CrewID)
	expiry, err := source.Expiry()
	if err != nil {
		t.Fatal(err)
	}
	source, err = captureOriginalHumanCredential(t.Context(), f.rig.db, &AuthUser{ID: f.a.HumanUserID, SessionID: "original-session", CredentialExpiresAt: expiry}, AuthKindSession, "")
	if err != nil {
		t.Fatal("fresh original source after fixture authority setup", err)
	}
	h := NewInternalHandler(f.rig.db, "synthetic-artifact-original-master", newTestLogger())
	receipt, err := h.originalHumanChatReceipt(t.Context(), f.a.ChatID, f.a.HumanUserID, nil, source)
	if err != nil || receipt == nil {
		t.Fatal("original artifact receipt", err)
	}
	f.receipt = receipt
	return f
}
