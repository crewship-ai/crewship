package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/auth/sessions"
	"github.com/crewship-ai/crewship/internal/documents"
	"github.com/crewship-ai/crewship/internal/slides"
)

const slidesReadSecret = "synthetic-slides-human-read-jwt-secret"

type slidesReadFixture struct {
	db                                    *sql.DB
	router                                *Router
	workspace, page, user, token, session string
	deck                                  slides.Deck
}

func newSlidesReadFixture(t *testing.T, enabled bool) *slidesReadFixture {
	t.Helper()
	db := setupTestDB(t)
	router, err := NewRouter(db, slidesReadSecret, newTestLogger())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(router.Shutdown)
	router.registerSlidesReadRoutes(enabled)
	f := &slidesReadFixture{db: db, router: router, workspace: generateCUID(), page: generateCUID(), user: generateCUID()}
	execOrFatal(t, db, "INSERT INTO workspaces(id,name,slug) VALUES(?, 'Slides','slides-read')", f.workspace)
	pagesSeedUser(t, router.Pages(), f.workspace, f.user, "slides-reader@example.test", "MEMBER")
	execOrFatal(t, db, "INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json,private_audience) VALUES(?,?,'deck','Private Slides title',?,'{}',1)", f.page, f.workspace, f.user)
	raw, err := os.ReadFile("../../testdata/slides-schema-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct{ Deck slides.Deck }
	if err = json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	f.deck = fixture.Deck
	f.deck.DeckID = f.page
	for i := range f.deck.Slides {
		for j := range f.deck.Slides[i].Elements {
			if text := f.deck.Slides[i].Elements[j].Text; text != nil {
				text.Value = "Private Slides body"
			}
		}
	}
	// Trusted fixture initialization is not a public creation or agent path.
	_, err = slides.New(db, slides.Options{}).Initialize(t.Context(), slides.Key{WorkspaceID: f.workspace, PageID: f.page}, slides.InitializeRequest{PageID: f.page, SchemaVersion: 1, MutationID: "fixture", Deck: f.deck}, func(context.Context, *sql.Tx) (string, error) { return "user:" + f.user, nil })
	if err != nil {
		t.Fatal(err)
	}
	f.token, f.session = f.login(t, f.user)
	return f
}
func (f *slidesReadFixture) login(t *testing.T, user string) (string, string) {
	t.Helper()
	session, err := sessions.NewDBStore(f.db).Create(t.Context(), user, "synthetic", "127.0.0.1", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	validator, err := auth.NewJWTValidator(slidesReadSecret)
	if err != nil {
		t.Fatal(err)
	}
	token, err := validator.IssueAccessToken(user, session.ID, "Synthetic", "slides-reader@example.test")
	if err != nil {
		t.Fatal(err)
	}
	return token, session.ID
}
func (f *slidesReadFixture) request(t *testing.T, token, target, workspace string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest("GET", "/api/v1/pages/"+url.PathEscape(target)+"/slides?workspace_id="+url.QueryEscape(workspace), nil)
	r.Header.Set("Authorization", "Bearer "+token)
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, r)
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("status%d missing no-store", w.Code)
	}
	return w
}
func slidesReadNoContent(t *testing.T, data []byte) {
	t.Helper()
	for _, marker := range []string{"Private Slides body", "Private Slides title", "Hidden image alt", `"deck"`, `"content_hash"`} {
		if bytes.Contains(data, []byte(marker)) {
			t.Fatalf("denied read disclosed %q", marker)
		}
	}
}
func (f *slidesReadFixture) unchangedRows(t *testing.T) {
	t.Helper()
	for _, table := range []string{"slides_heads", "slides_revisions", "slides_receipts"} {
		var count int
		if err := f.db.QueryRow("SELECT count(*) FROM "+table+" WHERE page_id=?", f.page).Scan(&count); err != nil || count != 1 {
			t.Fatalf("%s rows%d %v", table, count, err)
		}
	}
}

func TestSlidesHumanReadContractAndSchemaDenial(t *testing.T) {
	f := newSlidesReadFixture(t, true)
	for _, target := range []string{"deck", "@" + f.page} {
		w := f.request(t, f.token, target, f.workspace, nil)
		if w.Code != 200 {
			t.Fatalf("read%d %s", w.Code, w.Body)
		}
		var value map[string]json.RawMessage
		if err := json.Unmarshal(w.Body.Bytes(), &value); err != nil || len(value) != 6 {
			t.Fatalf("closed response %s %v", w.Body, err)
		}
		var state struct {
			PageID        string      `json:"page_id"`
			SchemaVersion int         `json:"schema_version"`
			Revision      int64       `json:"revision"`
			OrderVersion  int64       `json:"order_version"`
			ContentHash   string      `json:"content_hash"`
			Deck          slides.Deck `json:"deck"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &state); err != nil {
			t.Fatal(err)
		}
		want, _ := slides.Canonical(f.deck)
		got, _ := slides.Canonical(state.Deck)
		hash, _ := slides.DeckHash(f.deck)
		if state.PageID != f.page || state.SchemaVersion != 1 || state.Deck.SchemaVersion != 1 || state.Revision != 1 || state.OrderVersion != 1 || state.ContentHash != hash || !bytes.Equal(want, got) {
			t.Fatal("read lost exact saved state")
		}
	}
	for _, headers := range []map[string]string{{"X-Internal-Token": "synthetic-internal"}, {actingAgentSlugHeader: "agent"}} {
		w := f.request(t, f.token, "deck", f.workspace, headers)
		if w.Code != 403 {
			t.Fatalf("agent marker accepted %d", w.Code)
		}
		slidesReadNoContent(t, w.Body.Bytes())
	}
	original, _ := slides.Canonical(f.deck)
	originalHash, _ := slides.DeckHash(f.deck)
	for _, kind := range []string{"schema2", "future", "identity", "hash"} {
		t.Run(kind, func(t *testing.T) {
			deck := f.deck
			schema := 1
			hash := originalHash
			canonical := original
			switch kind {
			case "schema2":
				deck.SchemaVersion = 2
				deck.Slides = append([]slides.Slide{}, deck.Slides...)
				deck.Slides[0].Elements = append(append([]slides.Element{}, deck.Slides[0].Elements...), slides.Element{ID: "hidden_image", Type: "image", X: 1, Y: 1, Width: 10, Height: 10, Image: &slides.Image{AssetID: "fixture_asset", ExpectedSHA256: strings.Repeat("a", 64), Alt: "Hidden image alt"}})
				schema = 2
				canonical, _ = slides.Canonical(deck)
				hash, _ = slides.DeckHash(deck)
			case "future":
				schema = 99
			case "identity":
				deck.DeckID = "another_identity"
				canonical, _ = slides.Canonical(deck)
				hash, _ = slides.DeckHash(deck)
			case "hash":
				hash = strings.Repeat("a", 64)
			}
			execOrFatal(t, f.db, "UPDATE slides_heads SET schema_version=?,canonical_json=?,content_hash=? WHERE page_id=?", schema, canonical, hash, f.page)
			w := f.request(t, f.token, "@"+f.page, f.workspace, nil)
			if w.Code < 400 || ((kind == "schema2" || kind == "future") && w.Code != 409) {
				t.Fatalf("%s returned%d %s", kind, w.Code, w.Body)
			}
			slidesReadNoContent(t, w.Body.Bytes())
			var retained []byte
			if err := f.db.QueryRow("SELECT canonical_json FROM slides_heads WHERE page_id=?", f.page).Scan(&retained); err != nil || !bytes.Equal(retained, canonical) {
				t.Fatal("failed read changed data")
			}
			execOrFatal(t, f.db, "UPDATE slides_heads SET schema_version=1,canonical_json=?,content_hash=? WHERE page_id=?", original, originalHash, f.page)
		})
	}
	f.unchangedRows(t)
	// Existing native Document content must not be treated as a Slides Page.
	_, err := documents.New(f.db, documents.Options{}).Init(t.Context(), documents.Key{WorkspaceID: f.workspace, PageID: f.page}, 1, []byte("synthetic-mixed-type"), []byte(`[]`), func(context.Context, *sql.Tx) (string, error) { return "user:" + f.user, nil })
	if err != nil {
		t.Fatal(err)
	}
	w := f.request(t, f.token, "deck", f.workspace, nil)
	if w.Code != 404 {
		t.Fatalf("mixed type%d", w.Code)
	}
	slidesReadNoContent(t, w.Body.Bytes())
}

func TestSlidesHumanReadCurrentAuthorityAndDisabled(t *testing.T) {
	f := newSlidesReadFixture(t, true)
	for _, change := range []struct{ apply, undo string }{
		{"UPDATE users SET suspended_at='2026-10-19T00:00:00Z' WHERE id=?", "UPDATE users SET suspended_at=NULL WHERE id=?"},
		{"UPDATE workspace_members SET access_mode='restricted' WHERE user_id=?", "UPDATE workspace_members SET access_mode='trusted' WHERE user_id=?"},
	} {
		execOrFatal(t, f.db, change.apply, f.user)
		w := f.request(t, f.token, "deck", f.workspace, nil)
		if w.Code < 400 {
			t.Fatalf("changed principal accepted%d", w.Code)
		}
		slidesReadNoContent(t, w.Body.Bytes())
		execOrFatal(t, f.db, change.undo, f.user)
	}
	otherOwner := generateCUID()
	pagesSeedUser(t, f.router.Pages(), f.workspace, otherOwner, "new-owner@slides.example", "MEMBER")
	execOrFatal(t, f.db, "UPDATE pages SET owner_user_id=? WHERE id=?", otherOwner, f.page)
	changed := f.request(t, f.token, "deck", f.workspace, nil)
	if changed.Code != 404 {
		t.Fatalf("changed owner %d", changed.Code)
	}
	slidesReadNoContent(t, changed.Body.Bytes())
	execOrFatal(t, f.db, "UPDATE pages SET owner_user_id=? WHERE id=?", f.user, f.page)
	other := generateCUID()
	execOrFatal(t, f.db, "INSERT INTO workspaces(id,name,slug) VALUES(?,'Other','slides-other')", other)
	execOrFatal(t, f.db, "INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES(?,?,?,'MEMBER')", generateCUID(), other, f.user)
	w := f.request(t, f.token, "@"+f.page, other, nil)
	if w.Code != 404 {
		t.Fatalf("foreign workspace%d", w.Code)
	}
	slidesReadNoContent(t, w.Body.Bytes())
	execOrFatal(t, f.db, "UPDATE user_sessions SET revoked_at='2026-10-19T00:00:00Z' WHERE id=?", f.session)
	w = f.request(t, f.token, "deck", f.workspace, nil)
	if w.Code != 401 {
		t.Fatalf("revoked session%d", w.Code)
	}
	slidesReadNoContent(t, w.Body.Bytes())
	f.unchangedRows(t)
	disabled := newSlidesReadFixture(t, false)
	r := httptest.NewRequest("GET", "/api/v1/pages/deck/slides?workspace_id="+disabled.workspace, nil)
	r.Header.Set("Authorization", "Bearer "+disabled.token)
	record := httptest.NewRecorder()
	disabled.router.ServeHTTP(record, r)
	if record.Code != 404 {
		t.Fatalf("disabled route%d", record.Code)
	}
}

func TestSlidesHumanReadActualCLI(t *testing.T) {
	f := newSlidesReadFixture(t, true)
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
	invoke := func(token, target string) ([]byte, error) {
		t.Helper()
		config := filepath.Join(dir, "config.yaml")
		if err := os.WriteFile(config, []byte("server: "+server.URL+"\nworkspace: "+f.workspace+"\ntoken: "+token+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
		cmd := exec.CommandContext(t.Context(), binary, "slide", "read", target, "--format", "json")
		for _, entry := range os.Environ() {
			if !strings.HasPrefix(entry, "CREWSHIP_") {
				cmd.Env = append(cmd.Env, entry)
			}
		}
		cmd.Env = append(cmd.Env, "CREWSHIP_CONFIG="+config, "CREWSHIP_NO_SLUG_CACHE=1")
		return cmd.CombinedOutput()
	}
	out, err := invoke(f.token, "@"+f.page)
	if err != nil || !bytes.Contains(out, []byte(f.page)) || !bytes.Contains(out, []byte("Private Slides body")) {
		t.Fatalf("actual session CLI %v %s", err, out)
	}
	for _, role := range []string{"OWNER", "ADMIN", "MANAGER", "MEMBER", "VIEWER"} {
		user := generateCUID()
		pagesSeedUser(t, f.router.Pages(), f.workspace, user, strings.ToLower(role)+"@slides.example", role)
		token, _ := f.login(t, user)
		out, err := invoke(token, "@"+f.page)
		if role == "OWNER" {
			if err != nil {
				t.Fatalf("OWNER read %v %s", err, out)
			}
			var count int
			if e := f.db.QueryRow("SELECT count(*) FROM audit_logs WHERE action='artifact.private_owner_accessed' AND entity_id=? AND user_id=?", f.page, user).Scan(&count); e != nil || count < 1 {
				t.Fatal("OWNER disclosure lacked audit")
			}
			execOrFatal(t, f.db, "CREATE TRIGGER slides_read_audit_failure BEFORE INSERT ON audit_logs BEGIN SELECT RAISE(ABORT,'synthetic audit failure'); END")
			failed, e := invoke(token, "@"+f.page)
			if e == nil {
				t.Fatal("audit failure disclosed Slides")
			}
			slidesReadNoContent(t, failed)
			execOrFatal(t, f.db, "DROP TRIGGER slides_read_audit_failure")
		} else {
			if err == nil || !bytes.Contains(out, []byte("404")) {
				t.Fatalf("%s private read %v %s", role, err, out)
			}
			slidesReadNoContent(t, out)
		}
	}
	// This credential is an actual separately scoped CLI-token row, not a session.
	scoped := "crewship_cli_slides_read_test"
	execOrFatal(t, f.db, "INSERT INTO cli_tokens(id,user_id,name,token_hash,tier,scopes) VALUES('slides-read-token',?,'Slides test',?,'STANDARD','[\"pages:read\"]')", f.user, hashStandard(scoped))
	out, err = invoke(scoped, "deck")
	if err != nil {
		t.Fatalf("scoped CLI read %v %s", err, out)
	}
	execOrFatal(t, f.db, "UPDATE cli_tokens SET scopes='[\"pages:write\"]' WHERE id='slides-read-token'")
	out, err = invoke(scoped, "deck")
	if err == nil || !bytes.Contains(out, []byte("403")) {
		t.Fatalf("removed read scope %v %s", err, out)
	}
	slidesReadNoContent(t, out)
	execOrFatal(t, f.db, "UPDATE cli_tokens SET scopes='[\"pages:read\"]',revoked_at='2026-10-19T00:00:00Z' WHERE id='slides-read-token'")
	out, err = invoke(scoped, "deck")
	if err == nil || !bytes.Contains(out, []byte("401")) {
		t.Fatalf("revoked CLI token %v %s", err, out)
	}
	slidesReadNoContent(t, out)
	execOrFatal(t, f.db, "UPDATE pages SET slug='renamed' WHERE id=?", f.page)
	execOrFatal(t, f.db, "INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json) VALUES(?,?,'deck','Replacement marker',?,'{}')", generateCUID(), f.workspace, f.user)
	out, err = invoke(f.token, "@"+f.page)
	if err != nil || bytes.Contains(out, []byte("Replacement marker")) {
		t.Fatalf("immutable rename %v %s", err, out)
	}
	f.unchangedRows(t)
	execOrFatal(t, f.db, "UPDATE user_sessions SET revoked_at='2026-10-19T00:00:00Z' WHERE id=?", f.session)
	out, err = invoke(f.token, "@"+f.page)
	if err == nil || !bytes.Contains(out, []byte("401")) {
		t.Fatalf("same revoked session CLI %v %s", err, out)
	}
	slidesReadNoContent(t, out)
	fresh, _ := f.login(t, f.user)
	execOrFatal(t, f.db, "DELETE FROM pages WHERE id=?", f.page)
	out, err = invoke(fresh, "@"+f.page)
	if err == nil || !bytes.Contains(out, []byte("404")) {
		t.Fatalf("deleted immutable reference %v %s", err, out)
	}
	slidesReadNoContent(t, out)
}
