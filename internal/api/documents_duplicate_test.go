package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

type duplicateFixture struct {
	h                       *PageHandler
	workspace, user, source string
	prepare                 func()
	call                    func(string, string) *httptest.ResponseRecorder
}

// Only canonical Node computation is synthetic here. Router authentication,
// current credential/ownership, source tuple, writer fence and SQLite are real.
// A separate integration test exercises the actual Node process and CLI binary.
func newDuplicateFixture(t *testing.T) *duplicateFixture {
	t.Helper()
	h, workspace, user, saved := documentResultsFixture(t)
	f := &duplicateFixture{h: h, workspace: workspace, user: user, source: saved.PageID}
	mustExec(t, h.db, `UPDATE workspace_members SET role='MEMBER' WHERE workspace_id=? AND user_id=?`, workspace, user)
	const token = "crewship_cli_duplicate_personal_document"
	mustExec(t, h.db, `INSERT INTO cli_tokens(id,user_id,name,token_hash,tier,scopes) VALUES('duplicate-token',?,'duplicate',?,'STANDARD','["pages:read","pages:write"]')`, user, hashStandard(token))
	dir, err := os.MkdirTemp("", "doc-copy-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	secret := filepath.Join(dir, "secret")
	if err = os.WriteFile(secret, []byte(strings.Repeat("copy-service-secret-", 3)), 0600); err != nil {
		t.Fatal(err)
	}
	router, err := NewRouter(h.db, "this-is-a-32-char-test-secret-pad", newTestLogger(), WithDocumentAuthority(filepath.Join(dir, "a.sock"), secret), WithDocumentService(filepath.Join(dir, "n.sock")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(router.Shutdown)
	router.pages.documentCall = func(ctx context.Context, ws, bearer, page, operation string, body any) ([]byte, int, error) {
		if operation == "read" {
			if page != saved.PageID {
				t.Fatalf("read wrong source %q", page)
			}
			var schema int
			var rev, generation int64
			if err := h.db.QueryRowContext(ctx, `SELECT schema_version,head_rev,generation FROM documents WHERE page_id=?`, page).Scan(&schema, &rev, &generation); err != nil {
				t.Fatal(err)
			}
			return []byte(fmt.Sprintf(`{"schema":%d,"rev":%d,"generation":%d,"content":{"type":"doc","content":[{"type":"paragraph","attrs":{"id":"source-block"},"content":[{"type":"text","text":"confirmed source"}]}]}}`, schema, rev, generation)), 200, nil
		}
		if operation != "prepare-copy" {
			t.Fatalf("unexpected computation %q", operation)
		}
		if f.prepare != nil {
			f.prepare()
		}
		return []byte(`{"schema":3,"snapshot":"dHJ1c3RlZC1jbGVhbi1jb3B5LXNuYXBzaG90","blocks":[]}`), 200, nil
	}
	f.call = func(key, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/api/v1/pages/"+saved.PageID+"/duplicate?workspace_id="+workspace, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Idempotency-Key", key)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w
	}
	f.h = router.pages
	return f
}

func (f *duplicateFixture) packet(key string) string {
	return fmt.Sprintf(`{"mutation_id":%q,"expected_source":{"id":%q,"pages_version":null}}`, key, f.source)
}

func TestDuplicatePersonalDocumentAtomicIdentityAndRetry(t *testing.T) {
	f := newDuplicateFixture(t)
	mustExec(t, f.h.db, `INSERT INTO document_checkpoints(workspace_id,page_id,id,rev,schema_version,snapshot,projection,actor) VALUES(?,?,'extra-source-history',0,3,'old source snapshot','[]','user:source')`, f.workspace, f.source)
	first := f.call("one-copy", f.packet("one-copy"))
	creationExpect(t, first, 201)
	var result struct {
		Page struct{ ID, Slug string } `json:"page"`
	}
	if err := json.Unmarshal(first.Body.Bytes(), &result); err != nil || result.Page.ID == "" || result.Page.ID == f.source {
		t.Fatalf("independent identity %s %v", first.Body, err)
	}
	copyID := result.Page.ID
	// A later source edit does not re-run a previously committed copy.
	mustExec(t, f.h.db, `UPDATE documents SET head_rev=head_rev+1 WHERE page_id=?`, f.source)
	for range 3 {
		retry := f.call("one-copy", f.packet("one-copy"))
		creationExpect(t, retry, 200)
		if retry.Body.String() != first.Body.String() {
			t.Fatalf("retry changed copy %s / %s", first.Body, retry.Body)
		}
	}
	changed := strings.Replace(f.packet("one-copy"), `"pages_version":null`, `"pages_version":123`, 1)
	creationExpect(t, f.call("one-copy", changed), 409)
	var owner, crew, folder string
	var private bool
	if err := f.h.db.QueryRow(`SELECT owner_user_id,COALESCE(owner_crew_id,''),COALESCE(folder_id,''),private_audience FROM pages WHERE id=?`, copyID).Scan(&owner, &crew, &folder, &private); err != nil || owner != f.user || crew != "" || folder != "" || !private {
		t.Fatalf("copy audience %s %s %s %v %v", owner, crew, folder, private, err)
	}
	for _, table := range []string{"page_grants", "document_creations"} {
		var n int
		if err := f.h.db.QueryRow(`SELECT COUNT(*) FROM `+table+` WHERE page_id=?`, copyID).Scan(&n); err != nil || n != 0 {
			t.Errorf("inherited %s: %d %v", table, n, err)
		}
	}
	var consents int
	if err := f.h.db.QueryRow(`SELECT COUNT(*) FROM document_creation_consents WHERE result_page_id=?`, copyID).Scan(&consents); err != nil || consents != 0 {
		t.Fatalf("inherited creation consent: %d %v", consents, err)
	}
	var copies, history int
	if err := f.h.db.QueryRow(`SELECT COUNT(*) FROM pages WHERE id<>?`, f.source).Scan(&copies); err != nil || copies != 1 {
		t.Fatalf("duplicate count %d %v", copies, err)
	}
	if err := f.h.db.QueryRow(`SELECT COUNT(*) FROM document_checkpoints WHERE page_id=?`, copyID).Scan(&history); err != nil || history != 1 {
		t.Fatalf("source history copied instead of one initial checkpoint: %d %v", history, err)
	}
}

func TestDuplicatePersonalDocumentRechecksBeforeCommit(t *testing.T) {
	for _, change := range []string{"head", "generation", "page-version", "custody", "token", "membership", "sql-rollback"} {
		t.Run(change, func(t *testing.T) {
			f := newDuplicateFixture(t)
			want := 409
			f.prepare = func() {
				switch change {
				case "head":
					mustExec(t, f.h.db, `UPDATE documents SET head_rev=head_rev+1 WHERE page_id=?`, f.source)
				case "generation":
					mustExec(t, f.h.db, `UPDATE documents SET generation=generation+1 WHERE page_id=?`, f.source)
				case "page-version":
					mustExec(t, f.h.db, `UPDATE pages SET pages_version=pages_version+1 WHERE id=?`, f.source)
				case "custody":
					pagesSeedUser(t, f.h, f.workspace, "copy-other-owner", "copy-other@example.invalid", "MEMBER")
					mustExec(t, f.h.db, `UPDATE pages SET owner_user_id='copy-other-owner' WHERE id=?`, f.source)
					want = 404
				case "token":
					mustExec(t, f.h.db, `UPDATE cli_tokens SET revoked_at=datetime('now') WHERE id='duplicate-token'`)
					want = 401
				case "membership":
					mustExec(t, f.h.db, `DELETE FROM workspace_members WHERE workspace_id=? AND user_id=?`, f.workspace, f.user)
					want = 404
				case "sql-rollback":
					mustExec(t, f.h.db, `CREATE TRIGGER refuse_copy_receipt BEFORE INSERT ON document_receipts WHEN NEW.page_id<>'`+f.source+`' BEGIN SELECT RAISE(ABORT,'copy receipt unavailable'); END`)
					want = 500
				}
			}
			response := f.call("race-copy", f.packet("race-copy"))
			creationExpect(t, response, want)
			var pages, docs int
			if err := f.h.db.QueryRow(`SELECT (SELECT COUNT(*) FROM pages),(SELECT COUNT(*) FROM documents)`).Scan(&pages, &docs); err != nil || pages != 1 || docs != 1 {
				t.Fatalf("partial copy: pages%d docs%d %v", pages, docs, err)
			}
		})
	}
}

func TestDuplicatePersonalDocumentConcurrentExactRetry(t *testing.T) {
	f := newDuplicateFixture(t)
	f.h.db.SetMaxOpenConns(1)
	start := make(chan struct{})
	results := make(chan *httptest.ResponseRecorder, 2)
	var group sync.WaitGroup
	for range 2 {
		group.Add(1)
		go func() { defer group.Done(); <-start; results <- f.call("concurrent-copy", f.packet("concurrent-copy")) }()
	}
	close(start)
	group.Wait()
	close(results)
	var identity string
	for response := range results {
		if response.Code != 200 && response.Code != 201 {
			t.Fatalf("concurrent retry: %d %s", response.Code, response.Body)
		}
		if identity == "" {
			identity = response.Body.String()
		} else if response.Body.String() != identity {
			t.Fatalf("concurrent copy identities diverged: %s / %s", identity, response.Body)
		}
	}
	var count int
	if err := f.h.db.QueryRow(`SELECT COUNT(*) FROM pages WHERE id<>?`, f.source).Scan(&count); err != nil || count != 1 {
		t.Fatalf("concurrent copies: %d %v", count, err)
	}
}

func TestDuplicatePersonalDocumentRejectsUnknownPacketAndUnsupportedSource(t *testing.T) {
	for _, change := range []string{"root-field", "root-alias", "source-field", "duplicate-source-field", "header", "private-audience", "read-scope", "write-scope", "disabled", "unknown-schema"} {
		t.Run(change, func(t *testing.T) {
			f := newDuplicateFixture(t)
			body := f.packet("closed-copy")
			key := "closed-copy"
			want := 400
			switch change {
			case "root-field":
				body = strings.TrimSuffix(body, "}") + `,"owner":"forged"}`
			case "root-alias":
				body = strings.Replace(body, `"mutation_id"`, `"MUTATION_ID"`, 1)
			case "source-field":
				body = strings.Replace(body, `"pages_version":null`, `"pages_version":null,"revision":0`, 1)
			case "duplicate-source-field":
				body = strings.Replace(body, `"pages_version":null`, `"pages_version":null,"pages_version":0`, 1)
			case "header":
				key = "different-header"
			case "private-audience":
				mustExec(t, f.h.db, `UPDATE pages SET private_audience=0 WHERE id=?`, f.source)
				want = 404
			case "read-scope":
				mustExec(t, f.h.db, `UPDATE cli_tokens SET scopes='["pages:write"]' WHERE id='duplicate-token'`)
				want = 403
			case "write-scope":
				mustExec(t, f.h.db, `UPDATE cli_tokens SET scopes='["pages:read"]' WHERE id='duplicate-token'`)
				want = 403
			case "disabled":
				f.h.documentCall = nil
				want = 503
			case "unknown-schema":
				mustExec(t, f.h.db, `UPDATE documents SET schema_version=99 WHERE page_id=?`, f.source)
				want = 422
			}
			response := f.call(key, body)
			creationExpect(t, response, want)
			var count int
			if err := f.h.db.QueryRow(`SELECT COUNT(*) FROM pages WHERE id<>?`, f.source).Scan(&count); err != nil || count != 0 {
				t.Fatalf("refusal created a Page: %d %v", count, err)
			}
		})
	}
}

func TestDuplicatePersonalDocumentBoundsAndDeletedReceipt(t *testing.T) {
	for _, mode := range []string{"canonical-limit", "snapshot-limit", "deleted-copy", "transferred-copy"} {
		t.Run(mode, func(t *testing.T) {
			f := newDuplicateFixture(t)
			if strings.HasSuffix(mode, "limit") {
				real := f.h.documentCall
				f.h.documentCall = func(ctx context.Context, ws, bearer, page, operation string, body any) ([]byte, int, error) {
					raw, status, err := real(ctx, ws, bearer, page, operation, body)
					if mode == "canonical-limit" && operation == "read" {
						raw = []byte(strings.Replace(string(raw), "confirmed source", strings.Repeat("x", 1<<20), 1))
					}
					if mode == "snapshot-limit" && operation == "prepare-copy" {
						raw, _ = json.Marshal(map[string]any{"schema": 3, "snapshot": []byte(strings.Repeat("x", (1<<20)+1)), "blocks": []any{}})
					}
					return raw, status, err
				}
				creationExpect(t, f.call("bounded-copy", f.packet("bounded-copy")), 413)
				var count int
				if err := f.h.db.QueryRow(`SELECT COUNT(*) FROM pages WHERE id<>?`, f.source).Scan(&count); err != nil || count != 0 {
					t.Fatalf("oversized copy was partly saved: %d %v", count, err)
				}
				return
			}
			first := f.call("retained-key", f.packet("retained-key"))
			creationExpect(t, first, 201)
			var result struct {
				Page struct{ ID string } `json:"page"`
			}
			if err := json.Unmarshal(first.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if mode == "deleted-copy" {
				mustExec(t, f.h.db, `DELETE FROM pages WHERE id=?`, result.Page.ID)
			} else {
				pagesSeedUser(t, f.h, f.workspace, "copy-new-custodian", "copy-custodian@example.invalid", "MEMBER")
				mustExec(t, f.h.db, `UPDATE pages SET owner_user_id='copy-new-custodian' WHERE id=?`, result.Page.ID)
			}
			refused := f.call("retained-key", f.packet("retained-key"))
			creationExpect(t, refused, 404)
			if strings.Contains(refused.Body.String(), result.Page.ID) {
				t.Fatalf("denied retry disclosed its old copy: %s", refused.Body)
			}
			var receipts int
			if err := f.h.db.QueryRow(`SELECT COUNT(*) FROM document_copy_receipts WHERE mutation_id='retained-key'`).Scan(&receipts); err != nil || receipts != 1 {
				t.Fatalf("old key was freed: %d %v", receipts, err)
			}
			var count int
			want := 0
			if mode == "transferred-copy" {
				want = 1
			}
			if err := f.h.db.QueryRow(`SELECT COUNT(*) FROM pages WHERE id<>?`, f.source).Scan(&count); err != nil || count != want {
				t.Fatalf("retry created another copy: %d %v", count, err)
			}
		})
	}
}
