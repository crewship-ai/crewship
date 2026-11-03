package api

import (
	"errors"
	"fmt"
	"net/http/httptest"
	"testing"

	"github.com/crewship-ai/crewship/internal/artifacts"
	"github.com/crewship-ai/crewship/internal/slides"
	"github.com/crewship-ai/crewship/internal/storageheadroom"
)

// B9 (#4826): a human Slides save below the free-disk reserve gets the exact
// storage-full response native Craft writes return, nothing is written, and
// the same request saves once space returns.
func TestSlidesHumanInitializeBelowFreeDiskReserveUsesStorageRefusal(t *testing.T) {
	f := newInstanceFixture(t)
	wmSeedPage(t, f.db, "ws-old", "slides-headroom", "slides-headroom", "Slides", "boss", "")
	mustExec(t, f.db, `UPDATE pages SET private_audience=1 WHERE id='slides-headroom'`)
	f.r.RegisterSlidesHuman(true)
	path := "/api/v1/pages/slides-headroom/slides/initialize?workspace_id=ws-old"
	body := humanSlidesJSON(t, slides.InitializeRequest{PageID: "slides-headroom", SchemaVersion: 1, MutationID: "init", Deck: humanSlidesDeck(t, "slides-headroom")})
	mustExec(t, f.db, `UPDATE artifact_storage_global SET min_free_bytes=? WHERE id=1`, int64(1)<<62)

	got := f.do(f.boss, "POST", path, body)
	want := httptest.NewRecorder()
	artifactStorageRefusal(want, artifacts.ErrStorageQuota)
	if got.Code != 413 || got.Body.String() != want.Body.String() {
		t.Fatalf("Slides save below reserve = %d %s, want native refusal %d %s", got.Code, got.Body, want.Code, want.Body)
	}
	var rows int
	if err := f.db.QueryRow(`SELECT (SELECT count(*) FROM slides_heads WHERE page_id='slides-headroom')+(SELECT count(*) FROM slides_revisions WHERE page_id='slides-headroom')+(SELECT count(*) FROM slides_receipts WHERE page_id='slides-headroom')`).Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("refused save wrote %d rows: %v", rows, err)
	}
	mustExec(t, f.db, `UPDATE artifact_storage_global SET min_free_bytes=0 WHERE id=1`)
	wantCode(t, f.do(f.boss, "POST", path, body), 200, "same request after space returned")
}

// Every legacy document surface (private Docs authority and agent document
// creation) answers through documentAuthorityError. The free-disk refusal must
// map to the same storage-full response, not 429 document_quota or 500.
func TestDocumentAuthorityMapsFreeDiskRefusalToStorageRefusal(t *testing.T) {
	want := httptest.NewRecorder()
	artifactStorageRefusal(want, artifacts.ErrStorageQuota)
	for _, err := range []error{
		storageheadroom.ErrInsufficient,
		fmt.Errorf("commit: %w", storageheadroom.ErrInsufficient),
		errors.Join(storageheadroom.ErrInsufficient, errors.New("statfs failed")),
	} {
		got := httptest.NewRecorder()
		documentAuthorityError(got, err)
		if got.Code != 413 || got.Body.String() != want.Body.String() {
			t.Fatalf("%v mapped to %d %s, want %d %s", err, got.Code, got.Body, want.Code, want.Body)
		}
	}
}
