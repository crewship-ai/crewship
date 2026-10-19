package api

import (
	"encoding/json"
	"io"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/auth/sessions"
	"github.com/crewship-ai/crewship/internal/slides"
)

func humanSlidesDeck(t *testing.T, id string) slides.Deck {
	t.Helper()
	raw, err := os.ReadFile("../../testdata/slides-schema-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Deck json.RawMessage `json:"deck"`
	}
	if err = json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	deck, err := slides.Decode(fixture.Deck)
	if err != nil {
		t.Fatal(err)
	}
	deck.DeckID = id
	return deck
}
func humanSlidesJSON(t *testing.T, value any) string {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
func TestSlidesHumanReadInitialize(t *testing.T) {
	f := newInstanceFixture(t)
	wmSeedPage(t, f.db, "ws-old", "slides-original", "slides-test", "Slides", "boss", "")
	mustExec(t, f.db, `UPDATE pages SET private_audience=1 WHERE id='slides-original'`)
	base := "/api/v1/pages/slides-test/slides"
	wantCode(t, f.do(f.boss, "GET", base+"?workspace_id=ws-old", ""), 404, "disabled route")
	f.r.RegisterSlidesHuman(true)
	body := humanSlidesJSON(t, slides.InitializeRequest{PageID: "slides-original", SchemaVersion: 1, MutationID: "init", Deck: humanSlidesDeck(t, "slides-original")})
	call := func(token, method, path, body string) *httptest.ResponseRecorder {
		return f.do(token, method, path+"?workspace_id=ws-old", body)
	}
	first := call(f.boss, "POST", base+"/initialize", body)
	wantCode(t, first, 200, "initialize")
	retry := call(f.boss, "POST", base+"/initialize", body)
	wantCode(t, retry, 200, "retry")
	var receipt slides.Receipt
	if json.Unmarshal(retry.Body.Bytes(), &receipt) != nil || receipt.Revision != 1 || !receipt.Duplicate {
		t.Fatalf("receipt %s", retry.Body)
	}
	read := call(f.boss, "GET", base, "")
	wantCode(t, read, 200, "read")
	var state slides.State
	if json.Unmarshal(read.Body.Bytes(), &state) != nil || state.Revision != 1 || state.Deck.DeckID != "slides-original" {
		t.Fatalf("state %s", read.Body)
	}
	wantCode(t, call(f.boss, "GET", base+"/revisions/1", ""), 200, "exact revision")
	wantCode(t, call(f.wsAdmin, "GET", base, ""), 404, "admin private denial")
	wantCode(t, call(f.boss, "POST", base+"/initialize", strings.Replace(body, "init", "changed", 1)), 409, "changed mutation")
	for _, op := range []string{"patch", "restore", "reorder"} {
		if call(f.boss, "POST", base+"/"+op, body).Code == 200 {
			t.Fatal("unsupported write succeeded")
		}
	}
	var rows int
	if err := f.db.QueryRow(`SELECT count(*) FROM slides_revisions WHERE page_id='slides-original'`).Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("revisions %d %v", rows, err)
	}
	mustExec(t, f.db, `UPDATE cli_tokens SET revoked_at='2026-01-01T00:00:00Z' WHERE token_hash=?`, sha256Hex(f.boss))
	wantCode(t, call(f.boss, "POST", base+"/initialize", body), 401, "revoked replay")
}
func TestSlidesHumanSchemaAndAudit(t *testing.T) {
	f := newInstanceFixture(t)
	f.r.RegisterSlidesHuman(true)
	wmSeedPage(t, f.db, "ws-old", "slides-other", "slides-other", "Slides", "wsadmin", "")
	mustExec(t, f.db, `UPDATE pages SET private_audience=1 WHERE id='slides-other'`)
	deck := humanSlidesDeck(t, "slides-other")
	body := humanSlidesJSON(t, slides.InitializeRequest{PageID: deck.DeckID, SchemaVersion: 1, MutationID: "init", Deck: deck})
	base := "/api/v1/pages/slides-other/slides"
	wantCode(t, f.do(f.wsAdmin, "POST", base+"/initialize?workspace_id=ws-old", body), 200, "custodian initialize")
	wantCode(t, f.do(f.boss, "GET", base+"?workspace_id=ws-old", ""), 200, "owner read")
	var count int
	if err := f.db.QueryRow(`SELECT count(*) FROM audit_logs WHERE action='artifact.private_owner_accessed' AND entity_id='slides-other'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("audit %d %v", count, err)
	}
	mustExec(t, f.db, `CREATE TRIGGER refuse_slides_audit BEFORE INSERT ON audit_logs BEGIN SELECT RAISE(ABORT,'audit denied'); END`)
	failed := f.do(f.boss, "GET", base+"?workspace_id=ws-old", "")
	wantCode(t, failed, 500, "audit failure")
	if strings.Contains(failed.Body.String(), deck.Slides[0].ID) {
		t.Fatal("content leaked")
	}
	deck.SchemaVersion = 2
	canonical, err := slides.Canonical(deck)
	if err != nil {
		t.Fatal(err)
	}
	hash, err := slides.DeckHash(deck)
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, f.db, `UPDATE slides_heads SET schema_version=2,canonical_json=?,content_hash=? WHERE page_id='slides-other'`, canonical, hash)
	rejected := f.do(f.boss, "GET", base+"?workspace_id=ws-old", "")
	wantCode(t, rejected, 409, "schema2 unavailable before audit")
	if strings.Contains(rejected.Body.String(), hash) {
		t.Fatal("unsupported hash leaked")
	}
}

// Revocation during upload occurs after middleware admission but before the
// real writer fence; no receipt, identity reservation or content may survive.
type slidesRevokingBody struct {
	io.Reader
	once func()
}

func (b *slidesRevokingBody) Read(p []byte) (int, error) {
	if b.once != nil {
		f := b.once
		b.once = nil
		f()
	}
	return b.Reader.Read(p)
}
func (b *slidesRevokingBody) Close() error { return nil }
func TestSlidesHumanInitializeRevocationAndRollback(t *testing.T) {
	for _, change := range []string{"credential", "custody", "membership", "storage"} {
		t.Run(change, func(t *testing.T) {
			f := newInstanceFixture(t)
			f.r.RegisterSlidesHuman(true)
			wmSeedPage(t, f.db, "ws-old", "slides-fenced", "slides-fenced", "Slides", "boss", "")
			mustExec(t, f.db, `UPDATE pages SET private_audience=1 WHERE id='slides-fenced'`)
			body := humanSlidesJSON(t, slides.InitializeRequest{PageID: "slides-fenced", SchemaVersion: 1, MutationID: "fenced", Deck: humanSlidesDeck(t, "slides-fenced")})
			req := httptest.NewRequest("POST", "/api/v1/pages/slides-fenced/slides/initialize?workspace_id=ws-old", nil)
			req.Header.Set("Authorization", "Bearer "+f.boss)
			req.Body = &slidesRevokingBody{Reader: strings.NewReader(body), once: func() {
				switch change {
				case "credential":
					mustExec(t, f.db, `UPDATE cli_tokens SET revoked_at='2000-01-01T00:00:00Z' WHERE token_hash=?`, sha256Hex(f.boss))
				case "custody":
					mustExec(t, f.db, `UPDATE pages SET owner_user_id='wsadmin' WHERE id='slides-fenced'`)
				case "membership":
					mustExec(t, f.db, `DELETE FROM workspace_members WHERE workspace_id='ws-old' AND user_id='boss'`)
				case "storage":
					mustExec(t, f.db, `CREATE TRIGGER reject_slides_revision BEFORE INSERT ON slides_revisions BEGIN SELECT RAISE(ABORT,'synthetic persistence failure'); END`)
				}
			}}
			response := httptest.NewRecorder()
			f.r.ServeHTTP(response, req)
			if response.Code < 400 {
				t.Fatalf("accepted revoked/faulted mutation: %s", response.Body)
			}
			for _, table := range []string{"slides_heads", "slides_revisions", "slides_receipts", "slides_object_ids"} {
				var n int
				if err := f.db.QueryRow("SELECT count(*) FROM " + table + " WHERE page_id='slides-fenced'").Scan(&n); err != nil || n != 0 {
					t.Fatalf("partial %s: %d %v", table, n, err)
				}
			}
		})
	}
}
func TestSlidesHumanCurrentCustodianAudit(t *testing.T) {
	f := newInstanceFixture(t)
	f.r.RegisterSlidesHuman(true)
	wmSeedPage(t, f.db, "ws-old", "slides-custody", "slides-custody", "Slides", "boss", "")
	mustExec(t, f.db, `UPDATE pages SET private_audience=1 WHERE id='slides-custody'`)
	body := humanSlidesJSON(t, slides.InitializeRequest{PageID: "slides-custody", SchemaVersion: 1, MutationID: "init", Deck: humanSlidesDeck(t, "slides-custody")})
	wantCode(t, f.do(f.boss, "POST", "/api/v1/pages/slides-custody/slides/initialize?workspace_id=ws-old", body), 200, "init")
	mustExec(t, f.db, `CREATE TRIGGER slide_custody_at_fence AFTER UPDATE ON slides_writer_fence BEGIN UPDATE pages SET owner_user_id='wsadmin' WHERE id='slides-custody'; END`)
	wantCode(t, f.do(f.boss, "GET", "/api/v1/pages/slides-custody/slides?workspace_id=ws-old", ""), 200, "read after custody transfer")
	var n int
	if err := f.db.QueryRow(`SELECT count(*) FROM audit_logs WHERE action='artifact.private_owner_accessed' AND json_extract(metadata,'$.owner_user_id')='wsadmin' AND entity_id='slides-custody'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("current custody audit %d %v", n, err)
	}
}

func TestSlidesHumanScopedCredentials(t *testing.T) {
	f := newInstanceFixture(t)
	f.r.RegisterSlidesHuman(true)
	wmSeedPage(t, f.db, "ws-old", "slides-scoped", "slides-scoped", "Slides", "boss", "")
	mustExec(t, f.db, `UPDATE pages SET private_audience=1 WHERE id='slides-scoped'`)
	body := humanSlidesJSON(t, slides.InitializeRequest{PageID: "slides-scoped", SchemaVersion: 1, MutationID: "init", Deck: humanSlidesDeck(t, "slides-scoped")})
	base := "/api/v1/pages/slides-scoped/slides"
	// Separately minted credentials: do not inherit the staging token's scopes.
	readToken, writeToken := "crewship_cli_slides_read_only", "crewship_cli_slides_write_only"
	for _, v := range []struct{ id, token, scope string }{{"slides-read", readToken, `["pages:read"]`}, {"slides-write", writeToken, `["pages:write"]`}} {
		mustExec(t, f.db, `INSERT INTO cli_tokens(id,user_id,name,token_hash,tier,scopes) VALUES(?,'boss',?,?, 'STANDARD',?)`, v.id, v.id, sha256Hex(v.token), v.scope)
	}
	wantCode(t, f.do(readToken, "POST", base+"/initialize?workspace_id=ws-old", body), 403, "read-only cannot initialize")
	wantCode(t, f.do(writeToken, "POST", base+"/initialize?workspace_id=ws-old", body), 200, "pages:write initialize")
	wantCode(t, f.do(writeToken, "GET", base+"?workspace_id=ws-old", ""), 404, "write-only cannot read")
	wantCode(t, f.do(readToken, "GET", base+"?workspace_id=ws-old", ""), 200, "pages:read")
	for _, header := range []string{"X-Internal-Token", actingAgentSlugHeader} {
		req := httptest.NewRequest("GET", base+"?workspace_id=ws-old", nil)
		req.Header.Set("Authorization", "Bearer "+readToken)
		req.Header.Set(header, "synthetic-agent")
		response := httptest.NewRecorder()
		f.r.ServeHTTP(response, req)
		wantCode(t, response, 404, "agent header denied")
	}
}

func TestSlidesHumanSession(t *testing.T) {
	f := newInstanceFixture(t)
	f.r.RegisterSlidesHuman(true)
	wmSeedPage(t, f.db, "ws-old", "slides-session", "slides-session", "Slides", "boss", "")
	mustExec(t, f.db, `UPDATE pages SET private_audience=1 WHERE id='slides-session'`)
	session, err := sessions.NewDBStore(f.db).Create(t.Context(), "boss", "slides", "127.0.0.1", auth.RefreshTokenTTL)
	if err != nil {
		t.Fatal(err)
	}
	validator, err := auth.NewJWTValidator("this-is-a-32-char-test-secret-pad")
	if err != nil {
		t.Fatal(err)
	}
	token, err := validator.IssueAccessToken("boss", session.ID, "Boss", "boss@ex.com")
	if err != nil {
		t.Fatal(err)
	}
	body := humanSlidesJSON(t, slides.InitializeRequest{PageID: "slides-session", SchemaVersion: 1, MutationID: "init", Deck: humanSlidesDeck(t, "slides-session")})
	base := "/api/v1/pages/slides-session/slides"
	wantCode(t, f.do(token, "POST", base+"/initialize?workspace_id=ws-old", body), 200, "session initialize")
	wantCode(t, f.do(token, "GET", base+"?workspace_id=ws-old", ""), 200, "session read")
	mustExec(t, f.db, `UPDATE user_sessions SET revoked_at='2000-01-01T00:00:00Z' WHERE id=?`, session.ID)
	wantCode(t, f.do(token, "GET", base+"?workspace_id=ws-old", ""), 401, "revoked exact session")
}
