package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
)

func documentCreationFixture(t *testing.T) (*PageHandler, string, string, string, func(string, string) *httptest.ResponseRecorder) {
	t.Helper()
	f, intent := documentCreationAdmissionFixture(t)
	if err := bindCreation(f, intent); err != nil {
		t.Fatal(err)
	}
	h, workspace, user := f.pages, f.a.WorkspaceID, f.a.HumanUserID
	h.artifactMasterToken = "synthetic-document-create-master"
	token := internaltoken.DeriveAgentRunToken(internaltoken.DeriveAgentRunKey(h.artifactMasterToken, workspace, f.a.CrewID), workspace, f.a.AgentID, f.a.RunID)
	// Only trusted Node computation is synthetic here; admission and SQL are real.
	h.documentCall = func(context.Context, string, string, string, string, any) ([]byte, int, error) {
		return []byte(`{"schema":3,"snapshot":"dHJ1c3RlZC1Ob2RlLXByZXBhcmF0aW9uLWZpeHR1cmU=","blocks":[{"id":"server-leaf","hash":"server-fixture-hash"}]}`), 200, nil
	}
	call := func(name, body string) *httptest.ResponseRecorder {
		ctxDeadline, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		r := httptest.NewRequest("POST", "/api/v1/internal/documents/create", strings.NewReader(body)).WithContext(ctxDeadline)
		r.Header.Set("Authorization", "Bearer "+token)
		ctx := context.WithValue(r.Context(), ctxInternalTokenWS, workspace)
		ctx = context.WithValue(ctx, ctxInternalTokenCrew, f.a.CrewID)
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
	h.db.SetMaxOpenConns(1)
	first := call("first", documentCreationBody)
	creationExpect(t, first, 201)
	var created documentCreationResult
	if json.Unmarshal(first.Body.Bytes(), &created) != nil || created.PageID == "" || created.Revision != 0 || created.WorkspaceID != workspace || !strings.Contains(created.URL, "workspace_id=") {
		t.Fatal(first.Body.String())
	}
	retry := call("retry", documentCreationBody)
	creationExpect(t, retry, 200)
	var repeated documentCreationResult
	_ = json.Unmarshal(retry.Body.Bytes(), &repeated)
	if created != repeated || calls != 1 {
		t.Fatalf("retry rematerialized or changed result: %+v %+v calls%d", created, repeated, calls)
	}
	creationExpect(t, call("changed", strings.Replace(documentCreationBody, "First useful paragraph", "Changed paragraph", 1)), 409)
	creationExpect(t, call("new mutation", strings.Replace(documentCreationBody, "create-one", "create-two", 1)), 409)
	var owner, actor, creator string
	var grants int
	if err := h.db.QueryRow(`SELECT p.owner_user_id,r.actor,COALESCE(p.created_by_agent_id,''),(SELECT COUNT(*) FROM page_grants WHERE page_id=p.id AND subject_type='agent') FROM pages p JOIN document_receipts r ON r.page_id=p.id WHERE p.id=?`, created.PageID).Scan(&owner, &actor, &creator, &grants); err != nil || owner != user || actor != "agent:principal-agent:principal-run" || creator != "principal-agent" || grants != 0 {
		t.Fatalf("ownership, creator provenance or continuation changed: %s %s %s %d %v", owner, actor, creator, grants, err)
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
	for _, kind := range []string{"node-failure", "revoke-after-prepare", "fault-after-page-insert", "fault-before-consume", "HTML-only-permission"} {
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
				execOrFatal(t, h.db, `CREATE TRIGGER reject_created_document BEFORE INSERT ON documents BEGIN SELECT RAISE(ABORT,'synthetic create init fault'); END`)
			case "fault-before-consume":
				want = 500
				execOrFatal(t, h.db, `CREATE TRIGGER reject_creation_consume BEFORE UPDATE OF consumed_at ON document_creation_consents WHEN NEW.consumed_at IS NOT NULL BEGIN SELECT RAISE(ABORT,'synthetic consume fault'); END`)
			case "HTML-only-permission":
				execOrFatal(t, h.db, `DELETE FROM document_creation_consents`)
				execOrFatal(t, h.db, `CREATE TABLE artifact_creation_authorizations(run_id TEXT PRIMARY KEY,authorized_by_user_id TEXT,expires_at TEXT,granted_at TEXT); INSERT INTO artifact_creation_authorizations SELECT run_id,human_user_id,expires_at,'2026-10-16T00:00:00Z' FROM artifact_run_principals WHERE run_id='principal-run'`)
			}
			result := call(kind, documentCreationBody)
			creationExpect(t, result, want)
			for _, query := range []string{`SELECT COUNT(*) FROM pages WHERE slug='created-doc'`, `SELECT COUNT(*) FROM documents`, `SELECT COUNT(*) FROM document_receipts`, `SELECT COUNT(*) FROM document_creations`, `SELECT COUNT(*) FROM document_creation_consents WHERE consumed_at IS NOT NULL`} {
				var count int
				if err := h.db.QueryRow(query).Scan(&count); err != nil || count != 0 {
					t.Fatalf("failed creation left state: %s count%d err%v", query, count, err)
				}
			}
			if kind == "fault-before-consume" && !strings.Contains(audit.String(), "synthetic consume fault") {
				t.Fatalf("injected late consumption fault not reached: %s", audit.String())
			}
			if kind == "fault-after-page-insert" && !strings.Contains(audit.String(), "synthetic create init fault") {
				t.Fatalf("injected INSERT fault not reached: %s", audit.String())
			}
		})
	}
}

// Minimum installations have no delegation feature. A legacy installation must
// still receive no grant/delegation rows for the newly created Document.
func TestDocumentCreationNeverCreatesContinuationAuthority(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(fmt.Sprint("legacy=", legacy), func(t *testing.T) {
			h, _, _, _, call := documentCreationFixture(t)
			if legacy {
				execOrFatal(t, h.db, `CREATE TABLE page_artifact_run_delegations(page_id TEXT NOT NULL REFERENCES pages(id) ON DELETE CASCADE,run_id TEXT NOT NULL REFERENCES artifact_run_principals(run_id) ON DELETE CASCADE,granted_by_user_id TEXT NOT NULL REFERENCES users(id),expires_at TEXT NOT NULL,granted_at TEXT NOT NULL,revoked_at TEXT,revoked_by_user_id TEXT,request_id TEXT,PRIMARY KEY(page_id,run_id))`)
			}
			result := call("create", documentCreationBody)
			creationExpect(t, result, 201)
			var created documentCreationResult
			if err := json.Unmarshal(result.Body.Bytes(), &created); err != nil {
				t.Fatal(err)
			}
			var grants int
			if err := h.db.QueryRow(`SELECT COUNT(*) FROM page_grants WHERE page_id=? AND subject_type='agent'`, created.PageID).Scan(&grants); err != nil || grants != 0 {
				t.Fatalf("agent grants=%d %v", grants, err)
			}
			if legacy {
				var n int
				if err := h.db.QueryRow(`SELECT COUNT(*) FROM page_artifact_run_delegations WHERE page_id=?`, created.PageID).Scan(&n); err != nil || n != 0 {
					t.Fatalf("delegations=%d %v", n, err)
				}
			} else {
				var n int
				if err := h.db.QueryRow(`SELECT COUNT(*) FROM sqlite_schema WHERE name='page_artifact_run_delegations'`).Scan(&n); err != nil || n != 0 {
					t.Fatalf("minimum unexpectedly installed delegation feature: %d %v", n, err)
				}
			}
		})
	}
}

func TestDocumentCreationDeniesBeforePreparation(t *testing.T) {
	for _, change := range []string{"wrong workspace", "wrong crew", "forged HMAC", "missing consent", "expired principal", "expired consent", "revoked consent", "selected origin", "malformed Slides schema", "malformed Document schema", "malformed delegation schema"} {
		t.Run(change, func(t *testing.T) {
			h, ws, _, token, _ := documentCreationFixture(t)
			var calls int
			h.documentCall = func(context.Context, string, string, string, string, any) ([]byte, int, error) {
				calls++
				return nil, 503, errors.New("must not prepare")
			}
			crew := "principal-crew"
			switch change {
			case "wrong workspace":
				ws = "attacker-ws"
				token = internaltoken.DeriveAgentRunToken(internaltoken.DeriveAgentRunKey(h.artifactMasterToken, ws, crew), ws, "principal-agent", "principal-run")
			case "wrong crew":
				crew = "attacker-crew"
				token = internaltoken.DeriveAgentRunToken(internaltoken.DeriveAgentRunKey(h.artifactMasterToken, ws, crew), ws, "principal-agent", "principal-run")
			case "forged HMAC":
				token = internaltoken.DeriveAgentRunToken(internaltoken.DeriveAgentRunKey("attacker-master", ws, crew), ws, "principal-agent", "principal-run")
			case "missing consent":
				execOrFatal(t, h.db, `DELETE FROM document_creation_consents`)
			case "expired principal":
				execOrFatal(t, h.db, `UPDATE artifact_run_principals SET expires_at='2000-01-01T00:00:00Z'`)
			case "expired consent":
				execOrFatal(t, h.db, `DELETE FROM document_creation_consents; INSERT INTO document_creation_consents(message_id,workspace_id,human_user_id,chat_id,submission_id,run_id,expires_at,created_at) SELECT message_id,workspace_id,human_user_id,chat_id,'expired-synthetic-consent',run_id,'2000-01-01T00:00:00Z','2000-01-01T00:00:00Z' FROM artifact_run_principals`)
			case "revoked consent":
				execOrFatal(t, h.db, `UPDATE document_creation_consents SET revoked_at='2026-10-17T00:00:00Z'`)
			case "selected origin":
				execOrFatal(t, h.db, `UPDATE artifact_run_principals SET slides_request_origin='selected'`)
			case "malformed Slides schema":
				execOrFatal(t, h.db, `CREATE TABLE slides_requests(id TEXT)`)
			case "malformed Document schema":
				execOrFatal(t, h.db, `CREATE TABLE document_requests(id TEXT)`)
			case "malformed delegation schema":
				execOrFatal(t, h.db, `CREATE TABLE page_artifact_run_delegations(page_id TEXT)`)
			}
			r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(documentCreationBody))
			r.Header.Set("Authorization", "Bearer "+token)
			ctx := context.WithValue(r.Context(), ctxInternalTokenWS, ws)
			ctx = context.WithValue(ctx, ctxInternalTokenCrew, crew)
			response := httptest.NewRecorder()
			h.CreateDocumentAgent(response, r.WithContext(ctx))
			creationExpect(t, response, 403)
			if calls != 0 {
				t.Fatal("denied principal/consent reached Node preparation")
			}
			var n int
			if err := h.db.QueryRow(`SELECT COUNT(*) FROM document_creations`).Scan(&n); err != nil || n != 0 {
				t.Fatalf("denied request left provenance: %d %v", n, err)
			}
		})
	}
}

func creationExpect(t *testing.T, response *httptest.ResponseRecorder, status int) {
	t.Helper()
	if response.Code != status {
		t.Fatalf("status%d want%d: %s", response.Code, status, response.Body.String())
	}
}

func TestDocumentCreationDeletedResultCannotRecreateConsumedConsent(t *testing.T) {
	h, _, _, _, call := documentCreationFixture(t)
	response := call("create", documentCreationBody)
	creationExpect(t, response, 201)
	var result documentCreationResult
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	execOrFatal(t, h.db, `DELETE FROM pages WHERE id=?`, result.PageID)
	h.documentCall = func(context.Context, string, string, string, string, any) ([]byte, int, error) {
		t.Fatal("deleted result retried Node preparation")
		return nil, 0, nil
	}
	creationExpect(t, call("deleted retry", documentCreationBody), 409)
	var n int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM document_creation_consents WHERE consumed_at IS NOT NULL AND result_page_id IS NULL`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("deleted result revived allowance: %d %v", n, err)
	}
}
