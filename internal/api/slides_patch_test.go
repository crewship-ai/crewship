package api

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/slides"
)

func TestSlidesHumanPatchContract(t *testing.T) {
	f := newInstanceFixture(t)
	wmSeedPage(t, f.db, "ws-old", "slides-patch", "slides-patch", "Slides", "boss", "")
	mustExec(t, f.db, `UPDATE pages SET private_audience=1 WHERE id='slides-patch'`)
	const base = "/api/v1/pages/@slides-patch/slides?workspace_id=ws-old"
	wantCode(t, f.do(f.boss, "PATCH", base, "{}"), 404, "disabled")
	f.r.RegisterSlidesHuman(true)
	deck := humanSlidesDeck(t, "slides-patch")
	init := slides.InitializeRequest{PageID: deck.DeckID, SchemaVersion: 1, MutationID: "initialize", Deck: deck}
	wantCode(t, f.do(f.boss, "POST", "/api/v1/pages/@slides-patch/slides/initialize?workspace_id=ws-old", humanSlidesJSON(t, init)), 200, "initialize")
	hash, _ := slides.SlideHash(deck.Slides[0])
	note := "Saved by human"
	request := slides.PatchRequest{PageID: deck.DeckID, SchemaVersion: 1, BaseRev: 1, MutationID: "save-one", Operations: []slides.Operation{{Op: "set_notes", SlideID: deck.Slides[0].ID, IfHash: hash, Notes: &note}}}
	raw := humanSlidesJSON(t, request)
	first := f.do(f.boss, "PATCH", base, raw)
	wantCode(t, first, 200, "patch")
	var result struct {
		Deck     slides.Deck `json:"deck"`
		Rev      int64       `json:"rev"`
		Order    int64       `json:"order_version"`
		Mutation string      `json:"mutation_id"`
	}
	if err := json.Unmarshal(first.Body.Bytes(), &result); err != nil || result.Rev != 2 || result.Order != 1 || result.Mutation != "save-one" || savedSlideNotes(result.Deck, deck.Slides[0].ID) != note {
		t.Fatalf("patch envelope %s %v", first.Body, err)
	}
	if first.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("cacheable patch")
	}
	retry := f.do(f.boss, "PATCH", base, raw)
	wantCode(t, retry, 200, "retry")
	if retry.Body.String() != first.Body.String() {
		t.Fatalf("retry changed response %s", retry.Body)
	}
	read := f.do(f.boss, "GET", base, "")
	wantCode(t, read, 200, "read")
	var readBody map[string]json.RawMessage
	if json.Unmarshal(read.Body.Bytes(), &readBody) != nil || string(readBody["rev"]) != "2" || string(readBody["revision"]) != "2" {
		t.Fatalf("compatible GET rev %s", read.Body)
	}
	request.MutationID = "stale"
	conflict := f.do(f.boss, "PATCH", base, humanSlidesJSON(t, request))
	wantCode(t, conflict, 409, "stale")
	var current map[string]json.RawMessage
	if json.Unmarshal(conflict.Body.Bytes(), &current) != nil || len(current) != 3 || string(current["rev"]) != "2" || current["deck"] == nil || current["order_version"] == nil {
		t.Fatalf("conflict lacks current authorized snapshot %s", conflict.Body)
	}
	wantCode(t, f.do(f.boss, "PATCH", base, `{"unknown":true}`), 400, "strict request")
	for _, body := range []string{raw, "{"} {
		private := f.do(f.wsAdmin, "PATCH", base, body)
		missing := f.do(f.wsAdmin, "PATCH", "/api/v1/pages/@missing/slides?workspace_id=ws-old", body)
		if private.Code != 404 || missing.Code != 404 || private.Body.String() != missing.Body.String() || !strings.HasPrefix(private.Header().Get("Content-Type"), "application/json") {
			t.Fatalf("private oracle %d %s / %d %s", private.Code, private.Body, missing.Code, missing.Body)
		}
	}
	var n int
	if err := f.db.QueryRow(`SELECT count(*) FROM slides_revisions WHERE page_id='slides-patch'`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("revision count %d %v", n, err)
	}
}

func TestSlidesHumanPatchRevokedBeforeFenceHidesConflict(t *testing.T) {
	f := newInstanceFixture(t)
	f.r.RegisterSlidesHuman(true)
	wmSeedPage(t, f.db, "ws-old", "slides-patch-revoked", "slides-patch-revoked", "Slides", "boss", "")
	mustExec(t, f.db, `UPDATE pages SET private_audience=1 WHERE id='slides-patch-revoked'`)
	deck := humanSlidesDeck(t, "slides-patch-revoked")
	init := slides.InitializeRequest{PageID: deck.DeckID, SchemaVersion: 1, MutationID: "initialize", Deck: deck}
	wantCode(t, f.do(f.boss, "POST", "/api/v1/pages/@slides-patch-revoked/slides/initialize?workspace_id=ws-old", humanSlidesJSON(t, init)), 200, "initialize")
	mustExec(t, f.db, `CREATE TRIGGER slides_revoke_before_patch AFTER UPDATE ON slides_writer_fence BEGIN UPDATE pages SET owner_user_id='wsadmin' WHERE id='slides-patch-revoked'; END`)
	hash, _ := slides.SlideHash(deck.Slides[0])
	note := "Never saved"
	request := slides.PatchRequest{PageID: deck.DeckID, SchemaVersion: 1, BaseRev: 0, MutationID: "stale-and-revoked", Operations: []slides.Operation{{Op: "set_notes", SlideID: deck.Slides[0].ID, IfHash: hash, Notes: &note}}}
	response := f.do(f.boss, "PATCH", "/api/v1/pages/@slides-patch-revoked/slides?workspace_id=ws-old", humanSlidesJSON(t, request))
	wantCode(t, response, 404, "revoked at fence")
	if strings.Contains(response.Body.String(), `"deck"`) || strings.Contains(response.Body.String(), deck.DeckID) {
		t.Fatal("conflict disclosed revoked content")
	}
}

func savedSlideNotes(deck slides.Deck, id string) string {
	for _, slide := range deck.Slides {
		if slide.ID == id {
			return slide.Notes
		}
	}
	return ""
}

func TestSlidesHumanPatchUploadRevocationAndStorageRollback(t *testing.T) {
	for _, change := range []string{"credential", "membership", "custody", "storage"} {
		t.Run(change, func(t *testing.T) {
			f := newInstanceFixture(t)
			f.r.RegisterSlidesHuman(true)
			wmSeedPage(t, f.db, "ws-old", "slides-upload", "slides-upload", "Slides", "boss", "")
			mustExec(t, f.db, `UPDATE pages SET private_audience=1 WHERE id='slides-upload'`)
			deck := humanSlidesDeck(t, "slides-upload")
			init := slides.InitializeRequest{PageID: deck.DeckID, SchemaVersion: 1, MutationID: "initialize", Deck: deck}
			wantCode(t, f.do(f.boss, "POST", "/api/v1/pages/@slides-upload/slides/initialize?workspace_id=ws-old", humanSlidesJSON(t, init)), 200, "initialize")
			hash, _ := slides.SlideHash(deck.Slides[0])
			note := "Must roll back"
			patch := slides.PatchRequest{PageID: deck.DeckID, SchemaVersion: 1, BaseRev: 1, MutationID: "not-committed", Operations: []slides.Operation{{Op: "set_notes", SlideID: deck.Slides[0].ID, IfHash: hash, Notes: &note}}}
			req := httptest.NewRequest("PATCH", "/api/v1/pages/@slides-upload/slides?workspace_id=ws-old", nil)
			req.Header.Set("Authorization", "Bearer "+f.boss)
			req.Body = &slidesRevokingBody{Reader: strings.NewReader(humanSlidesJSON(t, patch)), once: func() {
				switch change {
				case "credential":
					mustExec(t, f.db, `UPDATE cli_tokens SET revoked_at='2000-01-01T00:00:00Z' WHERE token_hash=?`, sha256Hex(f.boss))
				case "membership":
					mustExec(t, f.db, `DELETE FROM workspace_members WHERE workspace_id='ws-old' AND user_id='boss'`)
				case "custody":
					mustExec(t, f.db, `UPDATE pages SET owner_user_id='wsadmin' WHERE id='slides-upload'`)
				case "storage":
					mustExec(t, f.db, `CREATE TRIGGER slides_patch_storage_failure BEFORE INSERT ON slides_revisions BEGIN SELECT RAISE(ABORT,'synthetic persistence failure'); END`)
				}
			}}
			response := httptest.NewRecorder()
			f.r.ServeHTTP(response, req)
			want := 404
			if change == "storage" {
				want = 500
			}
			wantCode(t, response, want, "live authority/storage")
			if strings.Contains(response.Body.String(), `"deck"`) {
				t.Fatal("refused write returned content")
			}
			for _, table := range []string{"slides_revisions", "slides_receipts"} {
				var n int
				if err := f.db.QueryRow("SELECT count(*) FROM " + table + " WHERE page_id='slides-upload'").Scan(&n); err != nil || n != 1 {
					t.Fatalf("partial %s %d %v", table, n, err)
				}
			}
			var rev int64
			if err := f.db.QueryRow(`SELECT head_rev FROM slides_heads WHERE page_id='slides-upload'`).Scan(&rev); err != nil || rev != 1 {
				t.Fatalf("head changed %d %v", rev, err)
			}
		})
	}
}
