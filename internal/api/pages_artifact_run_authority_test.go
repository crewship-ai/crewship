package api

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
	"github.com/crewship-ai/crewship/internal/chataudience"
	"github.com/crewship-ai/crewship/internal/tsformat"
)

func artifactRunFixture(t *testing.T) (*PageHandler, string, string, string, string) {
	t.Helper()
	h, _, _, ws, user, page := artifactFixture(t)
	execOrFatal(t, h.db, `INSERT INTO crews(id,workspace_id,name,slug) VALUES('artifact-crew',?,'Crew','artifact-crew')`, ws)
	seedAgentRow(t, h.db, "artifact-agent", ws, "artifact-crew", "Agent", "artifact-agent", "AGENT")
	execOrFatal(t, h.db, `INSERT INTO chats(id,workspace_id,agent_id,created_by,visibility) VALUES('artifact-chat',?,'artifact-agent',?,'private')`, ws, user)
	execOrFatal(t, h.db, `INSERT INTO conversation_messages(id,session_id,agent_id,role,content,ts,author_user_id) VALUES('artifact-message','artifact-chat','artifact-agent','user','make report',?,?)`, tsformat.Format(time.Now()), user)
	receipt, err := chataudience.CaptureTrusted(context.Background(), h.db, "artifact-chat", user, nil)
	if err != nil || receipt == nil {
		t.Fatalf("receipt %v %v", receipt, err)
	}
	req := httptest.NewRequest("POST", "/", nil)
	if err = captureArtifactChatPrincipal(req, h.db, "artifact-run", ws, "artifact-agent", "artifact-crew", "artifact-chat", "artifact-message", receipt); err != nil {
		t.Fatal(err)
	}
	execOrFatal(t, h.db, `INSERT INTO journal_entries(id,workspace_id,crew_id,agent_id,entry_type,actor_type,summary,trace_id) VALUES('artifact-start',?,'artifact-crew','artifact-agent','run.started','system','test','artifact-run')`, ws)
	execOrFatal(t, h.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id,granted_at) VALUES(?,'agent','artifact-agent','write',?,?)`, page, user, tsformat.Format(time.Now()))
	r := pagesGrantRequest(t, "POST", "/", ws, user, "OWNER", `{"run_id":"artifact-run"}`)
	r.SetPathValue("slug", "report")
	rr := httptest.NewRecorder()
	h.GrantArtifactRun(rr, r)
	artifactExpect(t, rr, 201)
	h.artifactMasterToken = "artifact-test-master"
	return h, ws, user, page, internaltoken.DeriveAgentRunToken(internaltoken.DeriveAgentRunKey(h.artifactMasterToken, ws, "artifact-crew"), ws, "artifact-agent", "artifact-run")
}
func artifactAgentSave(t *testing.T, h *PageHandler, ws, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest("POST", "/", strings.NewReader(body))
	r.SetPathValue("slug", "report")
	r.Header.Set("Authorization", "Bearer "+token)
	ctx := context.WithValue(r.Context(), ctxInternalTokenWS, ws)
	ctx = context.WithValue(ctx, ctxInternalTokenCrew, "artifact-crew")
	rr := httptest.NewRecorder()
	h.PublishArtifactAgent(rr, r.WithContext(ctx))
	return rr
}
func TestArtifactRunPublicationAndProvenance(t *testing.T) {
	h, ws, user, page, token := artifactRunFixture(t)
	body := `{"html":"<h1>agent</h1>","base_rev":0,"mutation_id":"agent-first"}`
	artifactExpect(t, artifactAgentSave(t, h, ws, token, body), 201)
	artifactExpect(t, artifactAgentSave(t, h, ws, token, body), 200)
	var agent, run, human string
	if err := h.db.QueryRow(`SELECT author_agent_id,author_run_id,author_user_id FROM page_artifact_revisions WHERE page_id=?`, page).Scan(&agent, &run, &human); err != nil {
		t.Fatal(err)
	}
	if agent != "artifact-agent" || run != "artifact-run" || human != user {
		t.Fatalf("provenance %q %q %q", agent, run, human)
	}
}
func TestArtifactRunPublicationRefusesStaleAuthority(t *testing.T) {
	for _, tc := range []struct{ name, sql string }{
		{"terminal", `INSERT INTO journal_entries(id,workspace_id,agent_id,entry_type,actor_type,summary,trace_id) SELECT 'artifact-end',workspace_id,'artifact-agent','run.completed','system','test','artifact-run' FROM pages WHERE slug='report'`},
		{"revoked delegation", `UPDATE page_artifact_run_delegations SET revoked_at='2026-01-01T00:00:00Z'`},
		{"expired delegation", `UPDATE page_artifact_run_delegations SET expires_at='2026-01-01T00:00:00Z'`},
		{"revoked grant", `DELETE FROM page_grants WHERE subject_type='agent'`},
		{"membership epoch", `UPDATE workspace_members SET access_revision=access_revision+1`},
		{"chat epoch", `UPDATE chats SET authority_revision=authority_revision+1`},
		{"removed human", `DELETE FROM workspace_members`},
		{"deleted workspace", `UPDATE workspaces SET deleted_at='2026-01-01T00:00:00Z'`},
		{"deleted crew", `UPDATE crews SET deleted_at='2026-01-01T00:00:00Z' WHERE id='artifact-crew'`},
		{"deleted agent", `UPDATE agents SET deleted_at='2026-01-01T00:00:00Z' WHERE id='artifact-agent'`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, ws, _, _, token := artifactRunFixture(t)
			execOrFatal(t, h.db, tc.sql)
			artifactExpect(t, artifactAgentSave(t, h, ws, token, `{"html":"x","base_rev":0,"mutation_id":"denied"}`), 403)
		})
	}
}
func TestArtifactRunRejectsForeignOrMissingCredential(t *testing.T) {
	h, ws, _, _, token := artifactRunFixture(t)
	for _, bad := range []string{"", token + "tampered", internaltoken.DeriveAgentRunToken(internaltoken.DeriveAgentRunKey(h.artifactMasterToken, ws, "other-crew"), ws, "artifact-agent", "artifact-run"), internaltoken.DeriveAgentRunToken(internaltoken.DeriveAgentRunKey(h.artifactMasterToken, ws, "artifact-crew"), ws, "other-agent", "artifact-run")} {
		artifactExpect(t, artifactAgentSave(t, h, ws, bad, `{"html":"x","base_rev":0,"mutation_id":"denied"}`), 403)
	}
	r := httptest.NewRequest("POST", "/", nil)
	var receipt chataudience.Receipt
	b, _ := json.Marshal(map[string]any{"user_id": "unknown"})
	_ = json.Unmarshal(b, &receipt)
	if captureArtifactChatPrincipal(r, h.db, "unknown", ws, "artifact-agent", "artifact-crew", "artifact-chat", "artifact-message", &receipt) == nil {
		t.Fatal("accepted forged principal")
	}
}

func TestArtifactRunRetryCannotCrossActorOrRun(t *testing.T) {
	h, ws, user, page, token := artifactRunFixture(t)
	body := `{"html":"x","base_rev":0,"mutation_id":"same"}`
	artifactExpect(t, artifactAgentSave(t, h, ws, token, body), 201)
	artifactExpect(t, artifactSave(t, h, ws, user, "x", "same", 0), 409)
	execOrFatal(t, h.db, `INSERT INTO artifact_run_principals SELECT 'second-run',workspace_id,agent_id,crew_id,human_user_id,chat_id,message_id,member_id,member_revision,chat_generation,chat_revision,expires_at FROM artifact_run_principals WHERE run_id='artifact-run'`)
	execOrFatal(t, h.db, `INSERT INTO page_artifact_run_delegations SELECT page_id,'second-run',granted_by_user_id,expires_at,granted_at,NULL,NULL,NULL FROM page_artifact_run_delegations WHERE page_id=?`, page)
	execOrFatal(t, h.db, `INSERT INTO journal_entries(id,workspace_id,crew_id,agent_id,entry_type,actor_type,summary,trace_id) VALUES('second-start',?,'artifact-crew','artifact-agent','run.started','system','test','second-run')`, ws)
	second := internaltoken.DeriveAgentRunToken(internaltoken.DeriveAgentRunKey(h.artifactMasterToken, ws, "artifact-crew"), ws, "artifact-agent", "second-run")
	artifactExpect(t, artifactAgentSave(t, h, ws, second, body), 409)
}
func TestArtifactRunLedgerGenerationFence(t *testing.T) {
	h, ws, _, _, token := artifactRunFixture(t)
	execOrFatal(t, h.db, `INSERT INTO work_items(id,workspace_id,source,agent_id,crew_id,state,generation,eligible_at,created_at,updated_at) VALUES('artifact-work',?,'chat','artifact-agent','artifact-crew','running',2,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`, ws)
	execOrFatal(t, h.db, `INSERT INTO work_attempts(run_id,work_id,attempt,generation,lease_owner,lease_expires_at,heartbeat_at,started_at) VALUES('artifact-run','artifact-work',1,1,'worker','2126-01-01T00:00:00Z','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`)
	artifactExpect(t, artifactAgentSave(t, h, ws, token, `{"html":"x","base_rev":0,"mutation_id":"stale"}`), 403)
}

func TestArtifactRunDelegationAuditAndRevocation(t *testing.T) {
	h, ws, user, page, token := artifactRunFixture(t)
	var by, at string
	if err := h.db.QueryRow(`SELECT granted_by_user_id,granted_at FROM page_artifact_run_delegations WHERE page_id=?`, page).Scan(&by, &at); err != nil || by != user || at == "" {
		t.Fatalf("grant audit %q %q %v", by, at, err)
	}
	r := pagesGrantRequest(t, "DELETE", "/", ws, user, "OWNER", "")
	r.SetPathValue("slug", "report")
	r.SetPathValue("runId", "artifact-run")
	rr := httptest.NewRecorder()
	h.RevokeArtifactRun(rr, r)
	artifactExpect(t, rr, 204)
	if err := h.db.QueryRow(`SELECT revoked_by_user_id,revoked_at FROM page_artifact_run_delegations WHERE page_id=?`, page).Scan(&by, &at); err != nil || by != user || at == "" {
		t.Fatalf("revoke audit %q %q %v", by, at, err)
	}
	artifactExpect(t, artifactAgentSave(t, h, ws, token, `{"html":"x","base_rev":0,"mutation_id":"denied"}`), 403)
	receipt, err := chataudience.CaptureTrusted(context.Background(), h.db, "artifact-chat", user, nil)
	if err != nil {
		t.Fatal(err)
	}
	forged := httptest.NewRequest("POST", "/", nil)
	forged = forged.WithContext(context.WithValue(forged.Context(), ctxInternalTokenCrew, "artifact-crew"))
	if captureArtifactChatPrincipal(forged, h.db, "forged", ws, "artifact-agent", "artifact-crew", "artifact-chat", "artifact-message", receipt) == nil {
		t.Fatal("crew transport nominated human")
	}
}

func TestArtifactRunRevocationDuringUpload(t *testing.T) {
	h, ws, _, page, token := artifactRunFixture(t)
	body := `{"html":"x","base_rev":0,"mutation_id":"during-upload"}`
	r := httptest.NewRequest("POST", "/", nil)
	r.SetPathValue("slug", "report")
	r.Header.Set("Authorization", "Bearer "+token)
	ctx := context.WithValue(r.Context(), ctxInternalTokenWS, ws)
	ctx = context.WithValue(ctx, ctxInternalTokenCrew, "artifact-crew")
	r = r.WithContext(ctx)
	r.Body = &artifactRevokingBody{reader: strings.NewReader(body), revoke: func() {
		execOrFatal(t, h.db, `UPDATE page_artifact_run_delegations SET revoked_at=? WHERE page_id=?`, tsformat.Format(time.Now()), page)
	}}
	rr := httptest.NewRecorder()
	h.PublishArtifactAgent(rr, r)
	artifactExpect(t, rr, 403)
	var count int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM page_artifact_revisions WHERE page_id=?`, page).Scan(&count); err != nil || count != 0 {
		t.Fatalf("revoked upload committed: %d %v", count, err)
	}
}
