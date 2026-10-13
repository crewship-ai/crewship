package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
	"github.com/crewship-ai/crewship/internal/tsformat"
	"github.com/crewship-ai/crewship/internal/ws"
)

func TestArtifactRequestTypedReplyAndProgress(t *testing.T) {
	for _, kind := range []string{"revision", "reply"} {
		t.Run(kind, func(t *testing.T) {
			h, workspace, user, page := requestFixture(t)
			body := strings.TrimSuffix(requestBody, "}") + fmt.Sprintf(`,"result_kind":%q}`, kind)
			id := createArtifactRequestForTest(t, h, workspace, user, body, 202)
			// Explicit revision and omission must retain legacy submission fingerprint.
			if kind == "revision" {
				createArtifactRequestForTest(t, h, workspace, user, requestBody, 202)
			}
			h.artifactDispatch = func(ctx context.Context, actualUser, chat, prompt string, opts ws.ChatMessageOption) error {
				execOrFatal(t, h.db, `INSERT INTO artifact_run_principals SELECT 'reply-run',workspace_id,agent_id,crew_id,human_user_id,chat_id,message_id,member_id,member_revision,chat_generation,chat_revision,expires_at FROM artifact_run_principals WHERE run_id='artifact-run'`)
				if err := bindArtifactRequestRun(httptest.NewRequest("POST", "/", nil), h.db, id, "reply-run", opts.HumanAuthority); err != nil {
					t.Fatal(err)
				}
				execOrFatal(t, h.db, `INSERT INTO journal_entries(id,workspace_id,crew_id,agent_id,entry_type,actor_type,summary,trace_id) VALUES('reply-start',?,'artifact-crew','artifact-agent','run.started','system','test','reply-run')`, workspace)
				token := internaltoken.DeriveAgentRunToken(internaltoken.DeriveAgentRunKey(h.artifactMasterToken, workspace, "artifact-crew"), workspace, "artifact-agent", "reply-run")
				update := func(token, body string, want int) string {
					t.Helper()
					r := artifactAgentHTTPRequest(t, h, workspace, token, body)
					r.SetPathValue("requestId", id)
					w := httptest.NewRecorder()
					h.UpdateArtifactRequestAgent(w, r)
					artifactExpect(t, w, want)
					return w.Body.String()
				}
				wrongToken := internaltoken.DeriveAgentRunToken(internaltoken.DeriveAgentRunKey(h.artifactMasterToken, workspace, "artifact-crew"), workspace, "artifact-agent", "artifact-run")
				update(wrongToken, `{"mutation_id":"forged","operation":"reply","body":"Other run"}`, 403)
				progress := `{"mutation_id":"phase","operation":"progress","phase":"preparing","message":"Untrusted <script> claim"}`
				firstProgress := update(token, progress, 200)
				update(token, progress, 200)
				update(token, strings.Replace(progress, "claim", "changed", 1), 409)
				update(token, strings.Replace(progress, `"phase"`, `"phase2"`, 1), 429)
				var deadline string
				execOrFatal(t, h.db, `UPDATE page_artifact_requests SET deadline_at=? WHERE id=?`, tsformat.Format(time.Now().Add(time.Minute)), id)
				if err := h.db.QueryRow(`SELECT deadline_at FROM page_artifact_requests WHERE id=?`, id).Scan(&deadline); err != nil {
					t.Fatal(err)
				}
				artifactExpect(t, artifactPolicyCall(t, h, workspace, user, "OWNER", "PUT", `{"enabled":false,"crew_ids":["artifact-crew"],"base_version":1}`), 200)
				reply := `{"mutation_id":"reply","operation":"reply","body":"Answer, not a publication"}`
				if kind == "revision" {
					update(token, reply, 409)
					return nil
				}
				artifactExpect(t, artifactAgentSave(t, h, workspace, token, fmt.Sprintf(`{"html":"blocked","base_rev":1,"mutation_id":"no-publication","request_id":%q}`, id)), 403)
				update(token, reply, 200)
				update(token, reply, 200)
				if again := update(token, progress, 200); again != firstProgress {
					t.Fatalf("progress receipt changed after reply: %s -> %s", firstProgress, again)
				}
				execOrFatal(t, h.db, `UPDATE page_artifact_requests SET deadline_at=? WHERE id=?`, tsformat.Format(time.Now().Add(-time.Minute)), id)
				update(token, reply, 403)
				execOrFatal(t, h.db, `UPDATE page_artifact_requests SET deadline_at=? WHERE id=?`, deadline, id)
				var state, after string
				var result *int64
				if err := h.db.QueryRow(`SELECT status,deadline_at,result_revision FROM page_artifact_requests WHERE id=?`, id).Scan(&state, &after, &result); err != nil || state != "succeeded" || after != deadline || result != nil {
					t.Fatalf("wrong typed outcome %s %s %v %v", state, after, result, err)
				}
				outcome, err := loadArtifactRequestOutcome(ctx, h.db, id)
				if err != nil || outcome.Reply == nil || outcome.Reply.RunID != "reply-run" || outcome.Reply.HumanID != user {
					t.Fatalf("lost provenance %+v %v", outcome, err)
				}
				if _, err = h.db.Exec(`UPDATE page_artifact_request_updates SET body='forged' WHERE request_id=?`, id); err == nil {
					t.Fatal("audit mutable")
				}
				execOrFatal(t, h.db, `UPDATE page_artifact_run_delegations SET revoked_at='revoked' WHERE page_id=? AND run_id='reply-run'`, page)
				update(token, reply, 403)
				return nil
			}
			h.dispatchNextArtifactRequest(context.Background())
		})
	}
}

func artifactAgentHTTPRequest(t *testing.T, h *PageHandler, workspace, token, body string) *http.Request {
	t.Helper()
	r := httptest.NewRequest("POST", "/", strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+token)
	ctx := context.WithValue(r.Context(), ctxInternalTokenWS, workspace)
	ctx = context.WithValue(ctx, ctxInternalTokenCrew, "artifact-crew")
	return r.WithContext(ctx)
}

func TestArtifactRequestUpdateStrictJSON(t *testing.T) {
	for _, raw := range []string{`null`, `[]`, `{"operation":"reply","operation":"progress"}`, `{"operation":null}`, `{"Operation":"reply"}`, `{"run_id":"forged"}`, `{"body":12}`, `{} {}`} {
		if validateArtifactRequestUpdateJSON([]byte(raw)) == nil {
			t.Errorf("accepted %s", raw)
		}
	}
	if err := validateArtifactRequestUpdateJSON([]byte(`{"operation":"reply","body":"safe <html> text"}`)); err != nil {
		t.Fatal(err)
	}
}
