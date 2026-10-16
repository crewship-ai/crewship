package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/slides"
)

func seedSearchSlidePage(t *testing.T, f *instanceFixture, id, text string) {
	t.Helper()
	wmSeedPage(t, f.db, "ws-old", id, id, id, "boss", "")
	deck := slideTestDeck(t, id)
	deck.Slides[0].Elements = []slides.Element{{ID: "search-text", Type: "text", X: 0, Y: 0, Width: 400, Height: 100, Text: &slides.Text{Value: text, FontFamily: "Arial", FontSize: 24, Color: "#000000", Align: "left"}}}
	for i := range deck.Slides {
		deck.Slides[i].Notes = ""
	}
	wantCode(t, f.slideDo(f.boss, "POST", "/api/v1/pages/"+id+"/slides/initialize", slideJSON(t, slides.InitializeRequest{PageID: id, SchemaVersion: 1, MutationID: "search-init", Deck: deck})), 200, "native source initialize")
}
func decodeNativeSearch(t *testing.T, body []byte) NativePageSearchResponse {
	t.Helper()
	var response NativePageSearchResponse
	if err := json.Unmarshal(body, &response); err != nil {
		t.Fatal(err)
	}
	return response
}

func TestNativeSearchRouterVisiblePaginationAndRevocation(t *testing.T) {
	f := newInstanceFixture(t)
	seedInstanceUser(t, f.db, "search-reader", "search-reader@example.test", "ws-old", "MEMBER")
	reader := mintTokenFor(t, f.db, "search-reader", "search-reader-session")
	for _, id := range []string{"a-visible", "z-visible"} {
		seedSearchSlidePage(t, f, id, "needle first and SECOND term")
		mustExec(t, f.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES(?,'user','search-reader','read','boss')`, id)
	}
	for i := 0; i < 60; i++ {
		seedSearchSlidePage(t, f, fmt.Sprintf("m-hidden-%02d", i), "needle SECOND hidden text canary")
	}
	query := "/api/v1/pages/search/content?q=needle%20second&limit=1"
	firstWire := f.slideDo(reader, "GET", query, "")
	wantCode(t, firstWire, 200, "first visible search")
	first := decodeNativeSearch(t, firstWire.Body.Bytes())
	if first.Scope != "native-docs-slides" || len(first.Matches) != 1 || first.Matches[0].PageID != "a-visible" || !first.HasMore || first.Cursor == "" {
		t.Fatalf("bad first visible page: %s", firstWire.Body)
	}
	if firstWire.Header().Get("Cache-Control") != "no-store" || strings.Contains(firstWire.Body.String(), "hidden") {
		t.Fatal("hidden/cache metadata leaked")
	}
	// Populate matching private projections as another authorized viewer. They
	// still cannot affect the reader's ordering, has_more or cursor boundaries.
	ownerWire := f.slideDo(f.boss, "GET", query, "")
	wantCode(t, ownerWire, 200, "owner indexes own private sources")
	secondWire := f.slideDo(reader, "GET", query+"&cursor="+url.QueryEscape(first.Cursor), "")
	wantCode(t, secondWire, 200, "second visible search")
	second := decodeNativeSearch(t, secondWire.Body.Bytes())
	if len(second.Matches) != 1 || second.Matches[0].PageID != "z-visible" || second.HasMore || second.Cursor != "" || strings.Contains(secondWire.Body.String(), "hidden") {
		t.Fatalf("hidden rows affected visible continuation: %s", secondWire.Body)
	}
	wantCode(t, f.slideDo(f.boss, "GET", query+"&cursor="+first.Cursor, ""), 400, "cursor bound to viewer")
	otherSession := "crewship_cli_search_reader_other"
	mustExec(t, f.db, `INSERT INTO cli_tokens(id,user_id,name,token_hash,created_at) VALUES('search-reader-other','search-reader','synthetic distinct credential',?,datetime('now'))`, hashStandard(otherSession))
	wantCode(t, f.slideDo(otherSession, "GET", query+"&cursor="+first.Cursor, ""), 400, "cursor bound to exact credential")
	wantCode(t, f.slideDo(reader, "GET", "/api/v1/pages/search/content?q=other&cursor="+first.Cursor, ""), 400, "cursor bound to query")
	mustExec(t, f.db, `DELETE FROM page_grants WHERE page_id='a-visible' AND subject_id='search-reader'`)
	afterPreviousRevoke := f.slideDo(reader, "GET", query+"&cursor="+first.Cursor, "")
	wantCode(t, afterPreviousRevoke, 200, "previous page grant revoked")
	if got := decodeNativeSearch(t, afterPreviousRevoke.Body.Bytes()); len(got.Matches) != 1 || got.Matches[0].PageID != "z-visible" || got.HasMore {
		t.Fatalf("revoked previous source made an empty hidden page: %s", afterPreviousRevoke.Body)
	}
	mustExec(t, f.db, `DELETE FROM page_grants WHERE page_id='z-visible' AND subject_id='search-reader'`)
	denied := f.slideDo(reader, "GET", query+"&cursor="+first.Cursor, "")
	wantCode(t, denied, 200, "all Page grants revoked")
	if got := decodeNativeSearch(t, denied.Body.Bytes()); len(got.Matches) != 0 || got.HasMore || got.Cursor != "" {
		t.Fatalf("revoked source remained: %s", denied.Body)
	}
	for _, test := range []struct {
		name   string
		scopes any
		want   int
	}{{"read", `["pages:read"]`, 200}, {"write", `["pages:write"]`, 403}, {"wildcard", `["pages:*"]`, 200}, {"unrelated", `["agents:read"]`, 403}, {"legacy", nil, 200}} {
		t.Run(test.name, func(t *testing.T) {
			token := "crewship_cli_search_" + test.name
			mustExec(t, f.db, `INSERT INTO cli_tokens(id,user_id,name,token_hash,tier,scopes,created_at) VALUES(?,'search-reader','synthetic search',?,'STANDARD',?,'2026-10-16T00:00:00Z')`, "search-"+test.name, hashStandard(token), test.scopes)
			wantCode(t, f.slideDo(token, "GET", query, ""), test.want, "CLI search ceiling")
			mustExec(t, f.db, `UPDATE cli_tokens SET revoked_at='2026-10-16T00:00:00Z' WHERE id=?`, "search-"+test.name)
			wantCode(t, f.slideDo(token, "GET", query, ""), 401, "exact CLI revocation")
		})
	}
}

func TestNativeSearchRouterRefusesIncompleteCoverageAndInvalidQuery(t *testing.T) {
	f := newInstanceFixture(t)
	seedSearchSlidePage(t, f, "search-complete-slide", "needle")
	query := "/api/v1/pages/search/content?q=needle"
	wantCode(t, f.slideDo(f.boss, "GET", query, ""), 200, "ready native source")
	seedSearchHeaderDocument(t, f.r.pages, "ws-old", "search-no-doc-engine", "boss")
	unavailable := f.slideDo(f.boss, "GET", query, "")
	wantCode(t, unavailable, 503, "missing managed engine refuses incomplete coverage")
	if strings.Contains(unavailable.Body.String(), `"matches"`) {
		t.Fatal("incomplete index returned false empty results")
	}
	mustExec(t, f.db, `UPDATE documents SET schema_version=99 WHERE page_id='search-no-doc-engine'`)
	wantCode(t, f.slideDo(f.boss, "GET", query, ""), 422, "unknown native schema explicit unsupported")
	for _, path := range []string{"/api/v1/pages/search/content", query + "&q=duplicate", query + "&limit=", query + "&limit=0", query + "&limit=51", query + "&cursor="} {
		wantCode(t, f.slideDo(f.boss, "GET", path, ""), 400, "invalid search input")
	}
	wantCode(t, f.slideDo("", "GET", query, ""), 401, "real Router authentication")
}

func TestNativeSearchCanceledAuthorityIsUnavailableNotAuthentication(t *testing.T) {
	h, _, _ := pagesAccessFixture(t)
	tx, err := h.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	request := httptest.NewRequest("GET", "/api/v1/pages/search/content?q=needle", nil).WithContext(ctx)
	_, _, err = h.nativeSearchViewer(ctx, request, tx)
	if !errors.Is(err, context.Canceled) || errors.Is(err, errNativeSearchAuthentication) || errors.Is(err, errNativeSearchMembership) {
		t.Fatalf("canceled authority mislabeled: %v", err)
	}
	response := httptest.NewRecorder()
	h.replyNativeSearchError(response, err)
	wantCode(t, response, 503, "canceled source authority unavailable")
}
