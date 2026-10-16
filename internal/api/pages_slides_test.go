package api

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/slides"
)

func slideTestDeck(t *testing.T, page string) slides.Deck {
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
	deck.DeckID = page
	return deck
}
func slideJSON(t *testing.T, value any) string {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
func TestSlidesHTTPDurableCASLiveACLAndSlugIdentity(t *testing.T) {
	f := newInstanceFixture(t)
	wmSeedPage(t, f.db, "ws-old", "slide-page-original", "slide-page", "Slides", "boss", "")
	deck := slideTestDeck(t, "slide-page-original")
	base := "/api/v1/pages/slide-page/slides"
	init := slides.InitializeRequest{PageID: deck.DeckID, SchemaVersion: 1, MutationID: "init", Deck: deck}
	wantCode(t, f.slideDo(f.boss, "POST", base+"/initialize", slideJSON(t, init)), 200, "initialize")
	for _, path := range []string{"/api/v1/pages/slide-page/artifact", "/api/v1/pages/slide-page/project"} {
		wantCode(t, f.slideDo(f.boss, "GET", path, ""), 409, "legacy content cannot open Slides")
	}
	wantCode(t, f.slideDo(f.boss, "PATCH", "/api/v1/pages/slide-page", `{"panels":[]}`), 409, "legacy panels cannot replace Slides")
	metadata := f.slideDo(f.boss, "GET", "/api/v1/pages/slide-page", "")
	wantCode(t, metadata, 200, "Page metadata")
	var wire map[string]any
	if json.Unmarshal(metadata.Body.Bytes(), &wire) != nil || wire["has_slides"] != true {
		t.Fatalf("Slides metadata missing %s", metadata.Body)
	}
	descriptor := wire["content_descriptor"].(map[string]any)
	if descriptor["supported"] != true || descriptor["type_id"] != "slides" || wire["can_edit_slides"] != true {
		t.Fatalf("native viewer or live edit capability missing %v", wire)
	}
	for _, query := range []string{`INSERT INTO page_artifacts(page_id,head_rev) VALUES('slide-page-original',0)`, `INSERT INTO documents(workspace_id,page_id,schema_version,snapshot,projection) VALUES('ws-old','slide-page-original',1,X'00','{}')`, `INSERT INTO page_boards(page_id,seq,format) VALUES('slide-page-original',0,'crewship-board-excalidraw-0.18.1-v1')`, `INSERT INTO page_project_drafts(page_id) VALUES('slide-page-original')`, `INSERT INTO page_project_live(page_id) VALUES('slide-page-original')`, `INSERT INTO page_panels(id,page_id) VALUES('forbidden-panel','slide-page-original')`} {
		if _, err := f.db.Exec(query); err == nil || !strings.Contains(err.Error(), "Page already contains Slides") {
			t.Fatalf("storage exclusion failed: %s %v", query, err)
		}
	}
	var state slides.State
	read := f.slideDo(f.boss, "GET", base, "")
	wantCode(t, read, 200, "read")
	if err := json.Unmarshal(read.Body.Bytes(), &state); err != nil || state.Revision != 1 {
		t.Fatalf("state %s %v", read.Body, err)
	}
	start := time.Now()
	for range 5 {
		wantCode(t, f.slideDo(f.boss, "GET", base, ""), 200, "observed read")
	}
	t.Logf("five actual authenticated SQLite API reads: %s", time.Since(start))
	slide := state.Deck.Slides[0]
	hash, _ := slides.SlideHash(slide)
	notes := "český text\nsecond paragraph"
	patch := slides.PatchRequest{PageID: deck.DeckID, SchemaVersion: 1, BaseRev: 1, MutationID: "notes", Operations: []slides.Operation{{Op: "set_notes", SlideID: slide.ID, IfHash: hash, Notes: &notes}}}
	body := slideJSON(t, patch)
	wantCode(t, f.slideDo(f.boss, "POST", base+"/patch", body), 200, "patch")
	wantCode(t, f.slideDo(f.boss, "POST", base+"/patch", body), 200, "receipt replay")
	patch.MutationID = "stale"
	wantCode(t, f.slideDo(f.boss, "POST", base+"/patch", slideJSON(t, patch)), 409, "whole revision conflict")
	wantCode(t, f.slideDo(f.boss, "GET", base+"/outline", ""), 200, "outline")
	wantCode(t, f.slideDo(f.boss, "GET", base+"/history", ""), 200, "history")
	wantCode(t, f.slideDo(f.boss, "GET", base+"/revisions/1", ""), 200, "pinned revision")
	restore := slides.RestoreRequest{PageID: deck.DeckID, SchemaVersion: 1, BaseRev: 2, MutationID: "restore", IfOrderVersion: 1, Revision: 1}
	wantCode(t, f.slideDo(f.boss, "POST", base+"/restore", slideJSON(t, restore)), 200, "restore")
	// A reader sees confirmed content but cannot write; a grant revocation also
	// blocks a byte-identical receipt replay, not only new mutations.
	seedInstanceUser(t, f.db, "slide-reader", "sr@example.com", "ws-old", "MEMBER")
	token := mintTokenFor(t, f.db, "slide-reader", "slide-reader-session")
	mustExec(t, f.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES('slide-page-original','user','slide-reader','read','boss')`)
	wantCode(t, f.slideDo(token, "GET", base, ""), 200, "reader")
	wantCode(t, f.slideDo(token, "POST", base+"/patch", body), 403, "reader cannot write")
	mustExec(t, f.db, `DELETE FROM page_grants WHERE page_id='slide-page-original'`)
	wantCode(t, f.slideDo(token, "GET", base, ""), 403, "revoked reader")
	// Recreating a slug with IDENTICAL content/object IDs does not retarget an
	// already prepared mutation. Required page_id remains the original binding.
	mustExec(t, f.db, `DELETE FROM pages WHERE id='slide-page-original'`)
	wmSeedPage(t, f.db, "ws-old", "slide-page-recreated", "slide-page", "Slides", "boss", "")
	replacement := slideTestDeck(t, "slide-page-recreated")
	init.PageID = replacement.DeckID
	init.Deck = replacement
	wantCode(t, f.slideDo(f.boss, "POST", base+"/initialize", slideJSON(t, init)), 200, "reinitialize new Page")
	wantCode(t, f.slideDo(f.boss, "POST", base+"/patch", body), 409, "immutable Page mismatch")
	read = f.slideDo(f.boss, "GET", base, "")
	wantCode(t, read, 200, "recreated read")
	if err := json.Unmarshal(read.Body.Bytes(), &state); err != nil || state.Revision != 1 {
		t.Fatal("stale mutation changed recreated Page")
	}
	// Revoking the exact CLI credential rejects existing acknowledged mutation IDs too.
	mustExec(t, f.db, `UPDATE cli_tokens SET revoked_at='2026-01-01T00:00:00Z' WHERE token_hash=?`, sha256Hex(f.boss))
	wantCode(t, f.slideDo(f.boss, "GET", base, ""), 401, "revoked exact CLI token")
}
func TestSlidesCustodyBlocksDepartureUntilHumanSuccessor(t *testing.T) {
	f := custodyRouterFixture(t, "artifact")
	mustExec(t, f.db, `DELETE FROM page_artifacts WHERE page_id='private-output'`)
	mustExec(t, f.db, `DELETE FROM pages WHERE id='regular-page'`)
	owner := mintTokenFor(t, f.db, "departing", "slides-custody-session")
	deck := slideTestDeck(t, "private-output")
	wantCode(t, f.slideDo(owner, "POST", "/api/v1/pages/private-output/slides/initialize", slideJSON(t, slides.InitializeRequest{PageID: deck.DeckID, SchemaVersion: 1, MutationID: "init", Deck: deck})), 200, "custody initialize")
	var member string
	if err := f.db.QueryRow(`SELECT id FROM workspace_members WHERE workspace_id='ws-old' AND user_id='departing'`).Scan(&member); err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/workspaces/ws-old/members/" + member
	tables := []string{"slides_heads", "slides_revisions", "slides_receipts", "slides_object_ids", "pages", "workspace_members", "page_grants"}
	before := custodySnapshot(t, f.db, tables...)
	response := f.slideDo(f.wsAdmin, "DELETE", path, "")
	wantCode(t, response, 409, "no implicit crew fallback")
	wantCode(t, f.slideDo(f.wsAdmin, "DELETE", "/api/v1/admin/users/departing/data", `{"reason":"Slides custody test"}`), 409, "erasure requires human successor")
	var conflict struct {
		Pages []pageOwnershipSummary `json:"pages"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &conflict); err != nil || len(conflict.Pages) != 1 || conflict.Pages[0].Type != "slides" {
		t.Fatalf("Slides custody metadata %s", response.Body)
	}
	if custodySnapshot(t, f.db, tables...) != before {
		t.Fatal("blocked departure changed content/authority")
	}
	wantCode(t, f.slideDo(f.wsAdmin, "POST", "/api/v1/workspaces/ws-old/page-ownership/private-output", `{"expected_owner_user_id":"departing","successor_user_id":"reader"}`), 200, "explicit successor")
	wantCode(t, f.slideDo(f.wsAdmin, "DELETE", path, ""), 200, "departure after handoff")
	reader := mintTokenFor(t, f.db, "reader", "slides-successor-session")
	wantCode(t, f.slideDo(reader, "GET", "/api/v1/pages/private-output/slides", ""), 200, "confirmed Slides survive")
}

func (f *instanceFixture) slideDo(token, method, path, body string) *httptest.ResponseRecorder {
	return f.do(token, method, path+"?workspace_id=ws-old", body)
}

func TestSlidesCustodyAccountErasureAfterExplicitSuccessor(t *testing.T) {
	f := custodyRouterFixture(t, "artifact")
	mustExec(t, f.db, `DELETE FROM page_artifacts WHERE page_id='private-output'`)
	mustExec(t, f.db, `DELETE FROM pages WHERE id='regular-page'`)
	owner := mintTokenFor(t, f.db, "departing", "slide-erasure-owner")
	deck := slideTestDeck(t, "private-output")
	wantCode(t, f.slideDo(owner, "POST", "/api/v1/pages/private-output/slides/initialize", slideJSON(t, slides.InitializeRequest{PageID: deck.DeckID, SchemaVersion: 1, MutationID: "init", Deck: deck})), 200, "initialize")
	wantCode(t, f.slideDo(f.wsAdmin, "DELETE", "/api/v1/admin/users/departing/data", `{"reason":"Slides erasure test"}`), 409, "erasure blocked before handoff")
	wantCode(t, f.slideDo(f.wsAdmin, "POST", "/api/v1/workspaces/ws-old/page-ownership/private-output", `{"expected_owner_user_id":"departing","successor_user_id":"reader"}`), 200, "explicit successor")
	wantCode(t, f.slideDo(f.wsAdmin, "DELETE", "/api/v1/admin/users/departing/data", `{"reason":"Slides erasure after handoff"}`), 202, "erasure after handoff")
	reader := mintTokenFor(t, f.db, "reader", "slide-erasure-reader")
	wantCode(t, f.slideDo(reader, "GET", "/api/v1/pages/private-output/slides", ""), 200, "Slides survive account erasure")
}
