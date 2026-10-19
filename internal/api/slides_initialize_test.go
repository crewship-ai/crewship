package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/documents"
	"github.com/crewship-ai/crewship/internal/slides"
)

// Reuse the real read Router/session fixture; initialize a DIFFERENT empty Page.
// Its deck is a canonical fixture, not evidence of agent or browser creation.
func newSlidesInitializeFixture(t *testing.T, enabled bool) (*slidesReadFixture, slides.InitializeRequest) {
	t.Helper()
	f := newSlidesReadFixture(t, enabled)
	page := generateCUID()
	execOrFatal(t, f.db, `INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json,private_audience) VALUES(?,?,'empty','Private Slides title',?,'{}',1)`, page, f.workspace, f.user)
	deck := f.deck
	deck.DeckID = page
	return f, slides.InitializeRequest{PageID: page, SchemaVersion: 1, MutationID: "initialize-original", Deck: deck}
}
func slidesInitializeJSON(t *testing.T, request slides.InitializeRequest) []byte {
	t.Helper()
	raw, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
func slidesInitializePost(t *testing.T, f *slidesReadFixture, token, target string, raw []byte, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest("POST", "/api/v1/pages/"+url.PathEscape(target)+"/slides/initialize?workspace_id="+url.QueryEscape(f.workspace), bytes.NewReader(raw))
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("Content-Type", "application/json")
	for key, value := range headers {
		r.Header.Set(key, value)
	}
	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, r)
	return w
}
func slidesInitializeDenied(t *testing.T, w *httptest.ResponseRecorder, status int) {
	t.Helper()
	if w.Code != status || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("denial status%d want%d cache%q body%s", w.Code, status, w.Header().Get("Cache-Control"), w.Body)
	}
	slidesReadNoContent(t, w.Body.Bytes())
}
func slidesInitializeRows(t *testing.T, f *slidesReadFixture, page string, want int) {
	t.Helper()
	for _, table := range []string{"slides_heads", "slides_revisions", "slides_receipts", "slides_object_ids"} {
		var count int
		if err := f.db.QueryRow("SELECT count(*) FROM "+table+" WHERE page_id=?", page).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if table == "slides_object_ids" {
			if (want == 0 && count != 0) || (want == 1 && count == 0) {
				t.Fatalf("object reservations%d", count)
			}
		} else if count != want {
			t.Fatalf("%s rows%d want%d", table, count, want)
		}
	}
	f.unchangedRows(t)
}

func TestSlidesHumanInitializeNativeReceiptAndActorBinding(t *testing.T) {
	f, request := newSlidesInitializeFixture(t, true)
	raw := slidesInitializeJSON(t, request)
	var first slides.Receipt
	for i, target := range []string{"empty", "@" + request.PageID} {
		w := slidesInitializePost(t, f, f.token, target, raw, nil)
		if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("initialize status%d cache%q %s", w.Code, w.Header().Get("Cache-Control"), w.Body)
		}
		var fields map[string]json.RawMessage
		var receipt slides.Receipt
		if err := json.Unmarshal(w.Body.Bytes(), &fields); err != nil || len(fields) != 5 {
			t.Fatalf("native receipt required %s %v", w.Body, err)
		}
		if err := json.Unmarshal(w.Body.Bytes(), &receipt); err != nil {
			t.Fatal(err)
		}
		hash, _ := slides.DeckHash(request.Deck)
		if receipt.MutationID != request.MutationID || receipt.Revision != 1 || receipt.OrderVersion != 1 || receipt.ContentHash != hash || receipt.Duplicate != (i == 1) {
			t.Fatalf("receipt %#v", receipt)
		}
		first = receipt
	}
	changed := request
	changed.Deck.Theme.Color = "#123456"
	slidesInitializeDenied(t, slidesInitializePost(t, f, f.token, "@"+request.PageID, slidesInitializeJSON(t, changed), nil), 409)
	// A later custodian may not claim the original actor's receipt.
	successor := generateCUID()
	pagesSeedUser(t, f.router.Pages(), f.workspace, successor, "initialize-successor@example.test", "MEMBER")
	token, _ := f.login(t, successor)
	execOrFatal(t, f.db, `UPDATE pages SET owner_user_id=? WHERE id=?`, successor, request.PageID)
	slidesInitializeDenied(t, slidesInitializePost(t, f, token, "@"+request.PageID, raw, nil), 409)
	slidesInitializeDenied(t, slidesInitializePost(t, f, f.token, "@"+request.PageID, raw, nil), 404)
	execOrFatal(t, f.db, `UPDATE pages SET owner_user_id=? WHERE id=?`, f.user, request.PageID)
	var hash string
	var pages int
	if err := f.db.QueryRow(`SELECT content_hash FROM slides_heads WHERE page_id=?`, request.PageID).Scan(&hash); err != nil || hash != first.ContentHash {
		t.Fatalf("confirmed head changed %q %v", hash, err)
	}
	if err := f.db.QueryRow(`SELECT count(*) FROM pages WHERE workspace_id=?`, f.workspace).Scan(&pages); err != nil || pages != 2 {
		t.Fatalf("initialization created a Page: %d %v", pages, err)
	}
	execOrFatal(t, f.db, `UPDATE user_sessions SET revoked_at='2026-10-19T00:00:00Z' WHERE id=?`, f.session)
	slidesInitializeDenied(t, slidesInitializePost(t, f, f.token, "@"+request.PageID, raw, nil), 401)
	slidesInitializeRows(t, f, request.PageID, 1)
}

func TestSlidesHumanInitializePrivacySchemaAndCompatibility(t *testing.T) {
	f, request := newSlidesInitializeFixture(t, true)
	raw := slidesInitializeJSON(t, request)
	for _, role := range []string{"OWNER", "ADMIN", "MANAGER", "MEMBER", "VIEWER"} {
		user := generateCUID()
		pagesSeedUser(t, f.router.Pages(), f.workspace, user, strings.ToLower(role)+"@initialize.example", role)
		token, _ := f.login(t, user)
		slidesInitializeDenied(t, slidesInitializePost(t, f, token, "@"+request.PageID, raw, nil), 404)
	}
	for _, headers := range []map[string]string{{"X-Internal-Token": "synthetic-internal"}, {actingAgentSlugHeader: "agent"}} {
		slidesInitializeDenied(t, slidesInitializePost(t, f, f.token, "@"+request.PageID, raw, headers), 403)
	}
	unsupported := request
	unsupported.SchemaVersion = 2
	unsupported.Deck.SchemaVersion = 2
	slidesInitializeDenied(t, slidesInitializePost(t, f, f.token, "@"+request.PageID, slidesInitializeJSON(t, unsupported), nil), 409)
	extra := append(append([]byte{}, raw[:len(raw)-1]...), []byte(`,"extra":true}`)...)
	slidesInitializeDenied(t, slidesInitializePost(t, f, f.token, "@"+request.PageID, extra, nil), 400)
	slidesInitializeRows(t, f, request.PageID, 0)
	// A trusted opaque Document fixture proves type exclusion without a Docs service.
	_, err := documents.New(f.db, documents.Options{}).Init(t.Context(), documents.Key{WorkspaceID: f.workspace, PageID: request.PageID}, 3, []byte("opaque-document-fixture"), []byte(`[]`), func(context.Context, *sql.Tx) (string, error) { return "user:" + f.user, nil })
	if err != nil {
		t.Fatal(err)
	}
	slidesInitializeDenied(t, slidesInitializePost(t, f, f.token, "@"+request.PageID, raw, nil), 404)
	slidesInitializeRows(t, f, request.PageID, 0)
	disabled, empty := newSlidesInitializeFixture(t, false)
	if w := slidesInitializePost(t, disabled, disabled.token, "@"+empty.PageID, slidesInitializeJSON(t, empty), nil); w.Code != 404 {
		t.Fatalf("disabled initialization registered: %d", w.Code)
	}
}

func TestSlidesHumanInitializeFinalAuthorityRollback(t *testing.T) {
	for _, credential := range []string{"session", "scoped-token"} {
		t.Run(credential, func(t *testing.T) {
			f, request := newSlidesInitializeFixture(t, true)
			token := f.token
			revoke := `UPDATE user_sessions SET revoked_at='2026-10-19T00:00:00Z' WHERE id='` + f.session + `';`
			if credential == "scoped-token" {
				token = "crewship_cli_initialize_fence"
				execOrFatal(t, f.db, `INSERT INTO cli_tokens(id,user_id,name,token_hash,tier,scopes) VALUES('initialize-fence',?,'Synthetic writer',?,'STANDARD','["pages:write"]')`, f.user, hashStandard(token))
				revoke = `UPDATE cli_tokens SET scopes='["pages:read"]' WHERE id='initialize-fence';`
			}
			// Actual native revision insertion occurs between the two authority callbacks.
			execOrFatal(t, f.db, `CREATE TRIGGER initialize_revoke AFTER INSERT ON slides_revisions WHEN NEW.page_id='`+request.PageID+`' BEGIN `+revoke+` END`)
			w := slidesInitializePost(t, f, token, "@"+request.PageID, slidesInitializeJSON(t, request), nil)
			want := 401
			if credential == "scoped-token" {
				want = 403
			}
			slidesInitializeDenied(t, w, want)
			slidesInitializeRows(t, f, request.PageID, 0)
		})
	}
}

func TestSlidesHumanInitializeActualCLIWriteOnlyScopeAndIdentity(t *testing.T) {
	f, request := newSlidesInitializeFixture(t, true)
	server := httptest.NewServer(f.router)
	defer server.Close()
	dir := t.TempDir()
	binary := filepath.Join(dir, "crewship-cli")
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	build := exec.CommandContext(t.Context(), "go", "build", "-o", binary, "./cmd/crewship")
	build.Dir = root
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("actual CLI build %v %s", err, out)
	}
	input := filepath.Join(dir, "initialize.json")
	if err := os.WriteFile(input, slidesInitializeJSON(t, request), 0600); err != nil {
		t.Fatal(err)
	}
	invoke := func(token, operation, target string) ([]byte, error) {
		t.Helper()
		config := filepath.Join(dir, "config.yaml")
		if err := os.WriteFile(config, []byte("server: "+server.URL+"\nworkspace: "+f.workspace+"\ntoken: "+token+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
		args := []string{"slide", operation, target, "--format", "json"}
		if operation == "initialize" {
			args = append(args, "--file", input)
		}
		cmd := exec.CommandContext(t.Context(), binary, args...)
		for _, entry := range os.Environ() {
			if !strings.HasPrefix(entry, "CREWSHIP_") {
				cmd.Env = append(cmd.Env, entry)
			}
		}
		cmd.Env = append(cmd.Env, "CREWSHIP_CONFIG="+config, "CREWSHIP_NO_SLUG_CACHE=1")
		return cmd.CombinedOutput()
	}
	token := "crewship_cli_initialize_write_only"
	execOrFatal(t, f.db, `INSERT INTO cli_tokens(id,user_id,name,token_hash,tier,scopes) VALUES('initialize-cli',?,'Synthetic writer',?,'STANDARD','["pages:write"]')`, f.user, hashStandard(token))
	out, err := invoke(token, "read", "@"+request.PageID)
	if err == nil || !bytes.Contains(out, []byte("403")) {
		t.Fatalf("write-only token acquired read scope %v %s", err, out)
	}
	slidesReadNoContent(t, out)
	out, err = invoke(token, "initialize", "@"+request.PageID)
	var receipt slides.Receipt
	if err != nil || json.Unmarshal(out, &receipt) != nil || receipt.Revision != 1 || receipt.Duplicate {
		t.Fatalf("actual CLI initialize %v %s", err, out)
	}
	execOrFatal(t, f.db, `UPDATE pages SET slug='renamed-initialize' WHERE id=?`, request.PageID)
	replacement := generateCUID()
	execOrFatal(t, f.db, `INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json) VALUES(?,?,'empty','Replacement marker',?,'{}')`, replacement, f.workspace, f.user)
	out, err = invoke(token, "initialize", "@"+request.PageID)
	if err != nil || json.Unmarshal(out, &receipt) != nil || !receipt.Duplicate || receipt.Revision != 1 {
		t.Fatalf("immutable retry %v %s", err, out)
	}
	// A reused address cannot repair the original request's Page identity.
	out, err = invoke(token, "initialize", "empty")
	if err == nil || !bytes.Contains(out, []byte("409")) {
		t.Fatalf("replacement initialized with old body %v %s", err, out)
	}
	slidesReadNoContent(t, out)
	execOrFatal(t, f.db, `UPDATE cli_tokens SET scopes='["pages:read"]' WHERE id='initialize-cli'`)
	out, err = invoke(token, "initialize", "@"+request.PageID)
	if err == nil || !bytes.Contains(out, []byte("403")) {
		t.Fatalf("removed write scope %v %s", err, out)
	}
	slidesReadNoContent(t, out)
	execOrFatal(t, f.db, `UPDATE cli_tokens SET scopes='["pages:write"]',revoked_at='2026-10-19T00:00:00Z' WHERE id='initialize-cli'`)
	out, err = invoke(token, "initialize", "@"+request.PageID)
	if err == nil || !bytes.Contains(out, []byte("401")) {
		t.Fatalf("revoked scoped token %v %s", err, out)
	}
	slidesReadNoContent(t, out)
	slidesInitializeRows(t, f, request.PageID, 1)
	execOrFatal(t, f.db, `DELETE FROM pages WHERE id=?`, request.PageID)
	out, err = invoke(f.token, "initialize", "@"+request.PageID)
	if err == nil || !bytes.Contains(out, []byte("404")) || bytes.Contains(out, []byte("Replacement marker")) {
		t.Fatalf("deleted ID substituted %v %s", err, out)
	}
	slidesReadNoContent(t, out)
}
