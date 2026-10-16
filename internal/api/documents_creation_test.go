package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func documentCreationFixture(t *testing.T) (*PageHandler, string, string, string, func(string, string) *httptest.ResponseRecorder) {
	t.Helper()
	h, workspace, user, _, token := artifactRunFixture(t)
	execOrFatal(t, h.db, `INSERT INTO document_creation_consents(message_id,workspace_id,human_user_id,chat_id,submission_id,run_id,expires_at,created_at) SELECT message_id,workspace_id,human_user_id,chat_id,'create-consent',run_id,expires_at,'2026-10-16T00:00:00Z' FROM artifact_run_principals WHERE run_id='artifact-run'`)
	h.documentCall = func(context.Context, string, string, string, string, any) ([]byte, int, error) {
		return []byte(`{"schema":3,"snapshot":"dHJ1c3RlZC1Ob2RlLXByZXBhcmF0aW9uLWZpeHR1cmU=","blocks":[{"id":"server-leaf","hash":"server-fixture-hash"}]}`), 200, nil
	}
	call := func(name, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/api/v1/internal/documents/create", strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+token)
		ctx := context.WithValue(r.Context(), ctxInternalTokenWS, workspace)
		ctx = context.WithValue(ctx, ctxInternalTokenCrew, "artifact-crew")
		rr := httptest.NewRecorder()
		h.CreateDocumentAgent(rr, r.WithContext(ctx))
		return rr
	}
	return h, workspace, user, token, call
}

const documentCreationBody = `{"slug":"created-doc","name":"Created report","mutation_id":"create-one","blocks":[{"type":"heading","level":1,"text":"A completed report"},{"type":"paragraph","text":"First useful paragraph"}]}`

func TestDocumentCreationAtomicResultAndOriginalPayloadRetry(t *testing.T) {
	h, workspace, user, _, call := documentCreationFixture(t)
	original := h.documentCall
	calls := 0
	h.documentCall = func(ctx context.Context, ws, bearer, page, operation string, body any) ([]byte, int, error) {
		calls++
		if operation != "prepare-create" {
			t.Fatal(operation)
		}
		return original(ctx, ws, bearer, page, operation, body)
	}
	first := call("first", documentCreationBody)
	artifactExpect(t, first, 201)
	var created documentCreationResult
	if json.Unmarshal(first.Body.Bytes(), &created) != nil || created.PageID == "" || created.Revision != 0 || created.WorkspaceID != workspace || !strings.Contains(created.URL, "workspace_id=") {
		t.Fatal(first.Body.String())
	}
	retry := call("retry", documentCreationBody)
	artifactExpect(t, retry, 200)
	var repeated documentCreationResult
	_ = json.Unmarshal(retry.Body.Bytes(), &repeated)
	if created != repeated || calls != 1 {
		t.Fatalf("retry rematerialized or changed result: %+v %+v calls%d", created, repeated, calls)
	}
	artifactExpect(t, call("changed", strings.Replace(documentCreationBody, "First useful paragraph", "Changed paragraph", 1)), 409)
	artifactExpect(t, call("new mutation", strings.Replace(documentCreationBody, "create-one", "create-two", 1)), 409)
	var owner, actor string
	var grants, delegations int
	if err := h.db.QueryRow(`SELECT p.owner_user_id,r.actor,(SELECT COUNT(*) FROM page_grants WHERE page_id=p.id AND subject_type='agent'),(SELECT COUNT(*) FROM page_artifact_run_delegations WHERE page_id=p.id) FROM pages p JOIN document_receipts r ON r.page_id=p.id WHERE p.id=?`, created.PageID).Scan(&owner, &actor, &grants, &delegations); err != nil || owner != user || actor != "agent:artifact-agent:artifact-run" || grants != 0 || delegations != 0 {
		t.Fatalf("ownership or continuation expanded: %s %s %d %d %v", owner, actor, grants, delegations, err)
	}
}

func TestDocumentCreationConcurrentExactRetryHasOnePage(t *testing.T) {
	h, _, _, _, call := documentCreationFixture(t)
	original := h.documentCall
	var arrivals atomic.Int32
	ready := make(chan struct{})
	h.documentCall = func(ctx context.Context, ws, bearer, page, operation string, body any) ([]byte, int, error) {
		if arrivals.Add(1) == 2 {
			close(ready)
		}
		select {
		case <-ready:
		case <-ctx.Done():
			return nil, 503, ctx.Err()
		}
		return original(ctx, ws, bearer, page, operation, body)
	}
	results := make(chan *httptest.ResponseRecorder, 2)
	var wait sync.WaitGroup
	for range 2 {
		wait.Add(1)
		go func() { defer wait.Done(); results <- call("concurrent", documentCreationBody) }()
	}
	wait.Wait()
	close(results)
	ids := map[string]bool{}
	statuses := map[int]int{}
	for result := range results {
		if result.Code != 200 && result.Code != 201 {
			t.Fatalf("concurrent: %d %s", result.Code, result.Body.String())
		}
		var value documentCreationResult
		_ = json.Unmarshal(result.Body.Bytes(), &value)
		ids[value.PageID] = true
		statuses[result.Code]++
	}
	if len(ids) != 1 || statuses[201] != 1 || statuses[200] != 1 {
		t.Fatalf("duplicate publication: %v %v", ids, statuses)
	}
}

func TestDocumentCreationPreparationRevocationAndSQLFaultRollback(t *testing.T) {
	for _, kind := range []string{"node-failure", "revoke-after-prepare", "fault-after-page-insert", "HTML-only-permission"} {
		t.Run(kind, func(t *testing.T) {
			h, _, _, _, call := documentCreationFixture(t)
			original := h.documentCall
			var audit strings.Builder
			h.logger = slog.New(slog.NewTextHandler(&audit, nil))
			want := 403
			switch kind {
			case "node-failure":
				want = 503
				h.documentCall = func(context.Context, string, string, string, string, any) ([]byte, int, error) {
					return nil, 503, errors.New("synthetic preparation unavailable")
				}
			case "revoke-after-prepare":
				h.documentCall = func(ctx context.Context, ws, bearer, page, operation string, body any) ([]byte, int, error) {
					raw, status, err := original(ctx, ws, bearer, page, operation, body)
					execOrFatal(t, h.db, `UPDATE document_creation_consents SET revoked_at='2026-10-16T00:00:00Z'`)
					return raw, status, err
				}
			case "fault-after-page-insert":
				want = 500
				execOrFatal(t, h.db, `CREATE TRIGGER reject_created_document BEFORE INSERT ON documents WHEN NEW.page_id!=(SELECT id FROM pages WHERE slug='report') BEGIN SELECT RAISE(ABORT,'synthetic create init fault'); END`)
			case "HTML-only-permission":
				execOrFatal(t, h.db, `DELETE FROM document_creation_consents`)
				execOrFatal(t, h.db, `INSERT INTO artifact_creation_authorizations(run_id,authorized_by_user_id,expires_at,granted_at) SELECT run_id,human_user_id,expires_at,'2026-10-16T00:00:00Z' FROM artifact_run_principals WHERE run_id='artifact-run'`)
			}
			result := call(kind, documentCreationBody)
			artifactExpect(t, result, want)
			for _, query := range []string{`SELECT COUNT(*) FROM pages WHERE slug='created-doc'`, `SELECT COUNT(*) FROM document_creations`, `SELECT COUNT(*) FROM document_creation_consents WHERE consumed_at IS NOT NULL`} {
				var count int
				if err := h.db.QueryRow(query).Scan(&count); err != nil || count != 0 {
					t.Fatalf("failed creation left state: %s count%d err%v", query, count, err)
				}
			}
			if kind == "fault-after-page-insert" && !strings.Contains(audit.String(), "synthetic create init fault") {
				t.Fatalf("injected INSERT fault not reached: %s", audit.String())
			}
		})
	}
}

func TestDocumentCreationCardRequiresCurrentChatAndPageReach(t *testing.T) {
	h, workspace, owner, _, create := documentCreationFixture(t)
	response := create("create", documentCreationBody)
	artifactExpect(t, response, 201)
	var saved documentCreationResult
	if err := json.Unmarshal(response.Body.Bytes(), &saved); err != nil {
		t.Fatal(err)
	}
	cards := artifactResults(t, artifactResultsCall(t, h, workspace, owner, "OWNER", "artifact-chat"))
	if len(cards) != 1 || cards[0].Type != "document" || cards[0].PageID != saved.PageID || cards[0].Revision != 0 {
		t.Fatalf("durable document card %+v", cards)
	}
	pagesSeedUser(t, h, workspace, "doc-card-reader", "doc-card-reader@example.com", "MEMBER")
	execOrFatal(t, h.db, `UPDATE chats SET visibility='group' WHERE id='artifact-chat'`)
	execOrFatal(t, h.db, `INSERT INTO chat_participants(chat_id,user_id,role) VALUES('artifact-chat','doc-card-reader','member')`)
	hidden := artifactResultsCall(t, h, workspace, "doc-card-reader", "OWNER", "artifact-chat")
	if len(artifactResults(t, hidden)) != 0 || strings.Contains(hidden.Body.String(), saved.PageID) || strings.Contains(hidden.Body.String(), "Created report") {
		t.Fatal("chat reach disclosed private Doc")
	}
	pagesGrant(t, h, workspace, owner, saved.Slug, `{"subject_type":"user","subject":"doc-card-reader@example.com","level":"read"}`)
	if len(artifactResults(t, artifactResultsCall(t, h, workspace, "doc-card-reader", "MEMBER", "artifact-chat"))) != 1 {
		t.Fatal("authorized Doc card absent")
	}
	execOrFatal(t, h.db, `DELETE FROM page_grants WHERE page_id=? AND subject_id='doc-card-reader'`, saved.PageID)
	if len(artifactResults(t, artifactResultsCall(t, h, workspace, "doc-card-reader", "MEMBER", "artifact-chat"))) != 0 {
		t.Fatal("Doc card survives Page revocation")
	}
	execOrFatal(t, h.db, `DELETE FROM chat_participants WHERE user_id='doc-card-reader'`)
	artifactExpect(t, artifactResultsCall(t, h, workspace, "doc-card-reader", "MEMBER", "artifact-chat"), 404)
}
