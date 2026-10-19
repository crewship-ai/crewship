package api

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/crewship-ai/crewship/internal/pages"
)

// Metadata-only heads deliberately contain no readable deck. These tests prove
// Page projection/privacy, not Slides validation, runtime admission or CLI use.
func seedSlidesMetadataHead(t *testing.T, h *PageHandler, workspace, page string, schema int) {
	t.Helper()
	execOrFatal(t, h.db, `INSERT INTO slides_heads(workspace_id,page_id,schema_version,head_rev,order_version,canonical_json,content_hash) VALUES(?,?,?,1,1,'{}','metadata-fixture-hash')`, workspace, page, schema)
}

func assertSlidesPageMetadata(t *testing.T, row map[string]any, schema int) {
	t.Helper()
	if row["has_slides"] != (schema != 0) || row["slides_schema"] != float64(schema) {
		t.Errorf("Slides metadata = (%v,%v), want (%v,%d)", row["has_slides"], row["slides_schema"], schema != 0, schema)
	}
	want := pages.DeriveContentTypes(pages.ContentFacts{HasSlides: schema != 0, SlidesSchema: schema, HumanOwned: true}).ContentDescriptor
	encoded, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	var reference map[string]any
	if err := json.Unmarshal(encoded, &reference); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(row["content_descriptor"], reference) {
		t.Errorf("content_descriptor = %#v, want compiled reference %#v", row["content_descriptor"], reference)
	}
	if want.Supported != (schema != 99) {
		t.Fatalf("compiled fixture support changed for schema%d: %#v", schema, want)
	}
}

func TestPagesSlidesMetadataGetAndList(t *testing.T) {
	rig := pagesTransferSetup(t)
	h := NewPageHandler(rig.db, nil, newTestLogger())
	for _, schema := range []int{0, 1, 2, 99} {
		id := fmt.Sprintf("slides-metadata-%d", schema)
		rig.seedPage(t, id, id, id, rig.userID, "")
		if schema != 0 {
			seedSlidesMetadataHead(t, h, rig.wsID, id, schema)
		}
		t.Run(id, func(t *testing.T) {
			assertSlidesPageMetadata(t, pagesGet(t, h, rig.wsID, rig.userID, "MEMBER", "@"+id), schema)
		})
	}
	rows := pagesListRows(t, h, rig.wsID, rig.userID, "MEMBER")
	if len(rows) != 4 {
		t.Fatalf("listed%d Pages, want4", len(rows))
	}
	for _, schema := range []int{0, 1, 2, 99} {
		assertSlidesPageMetadata(t, rows[fmt.Sprintf("slides-metadata-%d", schema)], schema)
	}
}

func TestPagesSlidesMetadataPrivateAudience(t *testing.T) {
	rig := pagesTransferSetup(t)
	h := NewPageHandler(rig.db, nil, newTestLogger())
	// This user is truly ADMIN in the database, not the fixture's OWNER.
	pagesSeedUser(t, h, rig.wsID, "slides-other-admin", "slides-admin@example.invalid", "ADMIN")
	const id = "private-slides-metadata"
	rig.seedPage(t, id, id, "Private Slides metadata marker", rig.userID, "")
	execOrFatal(t, rig.db, `UPDATE pages SET private_audience=1 WHERE id=?`, id)
	seedSlidesMetadataHead(t, h, rig.wsID, id, 1)
	assertSlidesPageMetadata(t, pagesGet(t, h, rig.wsID, rig.userID, "MEMBER", "@"+id), 1)
	rows := pagesListRows(t, h, rig.wsID, rig.userID, "MEMBER")
	assertSlidesPageMetadata(t, rows[id], 1)
	req := pagesRequest(t, "GET", "/api/v1/pages/@"+id, rig.wsID, "slides-other-admin", "ADMIN", "")
	req.SetPathValue("slug", "@"+id)
	out := httptest.NewRecorder()
	h.Get(out, req)
	if out.Code != 404 {
		t.Fatalf("uninvited ADMIN Page GET returned%d, want404", out.Code)
	}
	if rows := pagesListRows(t, h, rig.wsID, "slides-other-admin", "ADMIN"); len(rows) != 0 {
		t.Fatalf("uninvited ADMIN catalog exposed private Page: %#v", rows)
	}
}

func TestPagesSlidesMetadataBulkQueryCount(t *testing.T) {
	count := func(size int) int {
		h, counter, workspace, owner := newCountingPagesFixture(t)
		for i := 0; i < size; i++ {
			id := fmt.Sprintf("metadata-bulk-%d", i)
			execOrFatal(t, h.db, `INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json) VALUES(?,?,?,?,?,'{}')`, id, workspace, id, id, owner)
			seedSlidesMetadataHead(t, h, workspace, id, 1)
		}
		counter.reset()
		rows := pagesListRows(t, h, workspace, owner, "OWNER")
		n := counter.count()
		if len(rows) != size {
			t.Fatalf("listed%d Pages, want%d", len(rows), size)
		}
		for _, row := range rows {
			assertSlidesPageMetadata(t, row, 1)
		}
		return n
	}
	small, large := count(3), count(40)
	if small == 0 || small != large {
		t.Errorf("catalog query count grows:3 Pages=%d,40 Pages=%d", small, large)
	}
}
