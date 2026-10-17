package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/auth/sessions"
)

func documentResultsRequest(t *testing.T, h *PageHandler, workspace, user string) *http.Request {
	t.Helper()
	session, err := sessions.NewDBStore(h.db).Create(t.Context(), user, "document-results", "127.0.0.1", auth.RefreshTokenTTL)
	if err != nil {
		t.Fatal(err)
	}
	r := pagesRequest(t, "GET", "/api/v1/chats/principal-chat/artifact-results", workspace, user, "OWNER", "")
	r.SetPathValue("chatId", "principal-chat")
	ctx := context.WithValue(r.Context(), ctxAuthKind, AuthKindSession)
	ctx = context.WithValue(ctx, ctxUser, &AuthUser{ID: user, SessionID: session.ID})
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	t.Cleanup(cancel)
	return r.WithContext(ctx)
}

func documentResultsCall(h *PageHandler, r *http.Request) *httptest.ResponseRecorder {
	response := httptest.NewRecorder()
	h.ListChatDocumentResults(response, r)
	return response
}

func documentResultsDecode(t *testing.T, response *httptest.ResponseRecorder) []DocumentChatResult {
	t.Helper()
	creationExpect(t, response, 200)
	var wire DocumentChatResultsResponse
	if err := json.Unmarshal(response.Body.Bytes(), &wire); err != nil || wire.Results == nil {
		t.Fatalf("results envelope: %s %v", response.Body.String(), err)
	}
	return wire.Results
}

func documentResultsFixture(t *testing.T) (*PageHandler, string, string, documentCreationResult) {
	t.Helper()
	h, workspace, owner, _, create := documentCreationFixture(t)
	response := create("create", documentCreationBody)
	creationExpect(t, response, 201)
	var saved documentCreationResult
	if err := json.Unmarshal(response.Body.Bytes(), &saved); err != nil {
		t.Fatal(err)
	}
	return h, workspace, owner, saved
}

func TestDocumentChatResultsDurableProvenanceAndCurrentReach(t *testing.T) {
	h, workspace, owner, saved := documentResultsFixture(t)
	h.db.SetMaxOpenConns(1)
	read := func(user string) *httptest.ResponseRecorder {
		return documentResultsCall(h, documentResultsRequest(t, h, workspace, user))
	}
	cards := documentResultsDecode(t, read(owner))
	if len(cards) != 1 || cards[0].Type != "document" || cards[0].PageID != saved.PageID || cards[0].Revision != 0 || cards[0].OnBehalfOfUserID != owner {
		t.Fatalf("durable card: %+v", cards)
	}
	// Durable provenance remains useful after run completion, expiry and restore.
	mustExec(t, h.db, `INSERT INTO journal_entries(id,workspace_id,entry_type,actor_type,summary,trace_id) VALUES('result-run-ended',?,'run.completed','system','done','principal-run')`, workspace)
	mustExec(t, h.db, `UPDATE artifact_run_principals SET expires_at='2000-01-01T00:00:00Z'`)
	mustExec(t, h.db, `DELETE FROM artifact_run_principals`)
	mustExec(t, h.db, `DELETE FROM document_creation_consents`)
	if len(documentResultsDecode(t, read(owner))) != 1 {
		t.Fatal("result depends on transient admission")
	}
	mustExec(t, h.db, `UPDATE document_creations SET human_user_id=NULL`)
	if rows := documentResultsDecode(t, read(owner)); len(rows) != 1 || rows[0].OnBehalfOfUserID != "" {
		t.Fatal("unavailable former human erased durable result")
	}
	pagesSeedUser(t, h, workspace, "result-reader", "result-reader@example.invalid", "MEMBER")
	mustExec(t, h.db, `INSERT INTO chat_participants(chat_id,user_id,role) VALUES('principal-chat','result-reader','member')`)
	hidden := read("result-reader")
	if len(documentResultsDecode(t, hidden)) != 0 || strings.Contains(hidden.Body.String(), saved.PageID) || strings.Contains(hidden.Body.String(), saved.Title) {
		t.Fatal("chat reach disclosed private Document")
	}
	mustExec(t, h.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES(?,'user','result-reader','read',?)`, saved.PageID, owner)
	if len(documentResultsDecode(t, read("result-reader"))) != 1 {
		t.Fatal("authorized result absent")
	}
	mustExec(t, h.db, `DELETE FROM page_grants WHERE page_id=? AND subject_id='result-reader'`, saved.PageID)
	if len(documentResultsDecode(t, read("result-reader"))) != 0 {
		t.Fatal("revoked Page grant retained result")
	}
	mustExec(t, h.db, `DELETE FROM chat_participants WHERE user_id='result-reader'`)
	creationExpect(t, read("result-reader"), 404)
	mustExec(t, h.db, `DELETE FROM document_creations`)
	if len(documentResultsDecode(t, read(owner))) != 0 {
		t.Fatal("agent completion or message fabricated result")
	}
}

func TestDocumentChatResultsCurrentCredentialAndLiveMembership(t *testing.T) {
	for _, change := range []string{"session-revoked", "cli-revoked", "cli-narrowed", "restricted", "suspended", "membership-removed", "agent", "run-principal", "unknown-credential", "disabled"} {
		t.Run(change, func(t *testing.T) {
			h, workspace, owner, _ := documentResultsFixture(t)
			r := documentResultsRequest(t, h, workspace, owner)
			want := 401
			switch change {
			case "session-revoked":
				mustExec(t, h.db, `UPDATE user_sessions SET revoked_at='2026-01-01T00:00:00Z'`)
			case "cli-revoked", "cli-narrowed":
				const token = "crewship_cli_document_results_test"
				mustExec(t, h.db, `INSERT INTO cli_tokens(id,user_id,name,token_hash,tier,scopes) VALUES('results-cli',?,'results',?,'STANDARD','["pages:read"]')`, owner, hashStandard(token))
				r.Header.Set("Authorization", "Bearer "+token)
				r = r.WithContext(context.WithValue(r.Context(), ctxAuthKind, AuthKindCLIToken))
				if change == "cli-revoked" {
					mustExec(t, h.db, `UPDATE cli_tokens SET revoked_at='2026-01-01T00:00:00Z' WHERE id='results-cli'`)
				} else {
					mustExec(t, h.db, `UPDATE cli_tokens SET scopes='["chat"]' WHERE id='results-cli'`)
					want = 403
				}
			case "restricted":
				mustExec(t, h.db, `UPDATE workspace_members SET access_mode='restricted' WHERE user_id=?`, owner)
				want = 404
			case "suspended":
				mustExec(t, h.db, `UPDATE users SET suspended_at='2026-01-01T00:00:00Z' WHERE id=?`, owner)
				want = 404
			case "membership-removed":
				mustExec(t, h.db, `DELETE FROM workspace_members WHERE user_id=?`, owner)
				want = 404
			case "agent":
				r.Header.Set("X-Internal-Token", "synthetic-crew-ipc")
			case "run-principal":
				r = r.WithContext(context.WithValue(r.Context(), artifactRunKey{}, &artifactRunAuthority{AgentID: "principal-agent"}))
			case "unknown-credential":
				r = r.WithContext(context.WithValue(r.Context(), ctxAuthKind, "unknown"))
			case "disabled":
				h.documentCall = nil
				want = 503
			}
			response := documentResultsCall(h, r)
			creationExpect(t, response, want)
			if strings.Contains(response.Body.String(), "Created report") {
				t.Fatal("denial exposed title")
			}
		})
	}
}

func TestDocumentChatResultsDeletedAndMalformedPersistence(t *testing.T) {
	for _, change := range []string{"page-deleted", "doc-deleted", "view", "missing-column"} {
		t.Run(change, func(t *testing.T) {
			h, workspace, owner, saved := documentResultsFixture(t)
			want := 200
			switch change {
			case "page-deleted":
				mustExec(t, h.db, `DELETE FROM pages WHERE id=?`, saved.PageID)
			case "doc-deleted":
				mustExec(t, h.db, `DELETE FROM documents WHERE page_id=?`, saved.PageID)
			case "view":
				mustExec(t, h.db, `CREATE VIEW page_artifacts AS SELECT 'unrelated' AS page_id`)
				want = 500
			case "missing-column":
				mustExec(t, h.db, `CREATE TABLE page_artifacts(unrelated TEXT)`)
				want = 500
			}
			response := documentResultsCall(h, documentResultsRequest(t, h, workspace, owner))
			creationExpect(t, response, want)
			if want == 200 && len(documentResultsDecode(t, response)) != 0 {
				t.Fatal("deleted result remained visible")
			}
		})
	}
}
