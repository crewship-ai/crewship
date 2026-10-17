package backup

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/crewship-ai/crewship/internal/slides"
)

func slidesBackupFixture(t *testing.T) (slides.Deck, string, string) {
	t.Helper()
	raw, err := os.ReadFile("../../testdata/slides-schema-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Deck      json.RawMessage `json:"deck"`
		Canonical string          `json:"canonical_json"`
		Hash      string          `json:"hash"`
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	deck, err := slides.Decode(fixture.Deck)
	if err != nil {
		t.Fatal(err)
	}
	return deck, fixture.Canonical, fixture.Hash
}

func TestSlidesBackupForkPreservesCanonicalIdentity(t *testing.T) {
	deck, canonical, hash := slidesBackupFixture(t)
	row := func() map[string]any {
		return map[string]any{"workspace_id": "old-workspace", "page_id": deck.DeckID, "schema_version": 1, "canonical_json": canonical, "content_hash": hash, "mutation_id": "original-mutation"}
	}
	dump := &DBDump{Tables: map[string][]map[string]any{
		"slides_heads": {row()}, "slides_revisions": {row()},
		"slides_receipts":   {{"workspace_id": "old-workspace", "page_id": deck.DeckID}},
		"slides_object_ids": {{"workspace_id": "old-workspace", "page_id": deck.DeckID, "object_id": "stable-object"}},
	}}
	if err := remapSlides(dump, map[string]map[string]string{"workspaces": {"old-workspace": "fork-workspace"}, "pages": {deck.DeckID: "fork-page"}}); err != nil {
		t.Fatal(err)
	}
	deck.DeckID = "fork-page"
	want, err := slides.Canonical(deck)
	if err != nil {
		t.Fatal(err)
	}
	wantHash, err := slides.DeckHash(deck)
	if err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"slides_heads", "slides_revisions"} {
		r := dump.Tables[table][0]
		if r["page_id"] != "fork-page" || r["workspace_id"] != "fork-workspace" || r["canonical_json"] != string(want) || r["content_hash"] != wantHash || r["mutation_id"] != "original-mutation" {
			t.Fatalf("%s lost canonical fork identity or historical mutation", table)
		}
		if err := validateSlidesRestoreRow(table, r); err != nil {
			t.Fatal(err)
		}
	}
	if len(dump.Tables["slides_receipts"]) != 0 {
		t.Fatal("fork retained original replay receipts")
	}
	object := dump.Tables["slides_object_ids"][0]
	if object["page_id"] != "fork-page" || object["workspace_id"] != "fork-workspace" || object["object_id"] != "stable-object" {
		t.Fatal("object scope remapped incorrectly")
	}
}

func TestSlidesBackupRestoreRejectsInvalidCanonicalState(t *testing.T) {
	deck, canonical, hash := slidesBackupFixture(t)
	for _, tc := range []struct {
		name   string
		change func(map[string]any)
		valid  bool
	}{
		{"valid", func(map[string]any) {}, true},
		{"hash mismatch", func(r map[string]any) { r["content_hash"] = "invalid" }, false},
		{"page mismatch", func(r map[string]any) { r["page_id"] = "other-page" }, false},
		{"schema mismatch", func(r map[string]any) { r["schema_version"] = 2 }, false},
		{"fractional schema", func(r map[string]any) { r["schema_version"] = 1.5 }, false},
		{"invalid canonical", func(r map[string]any) { r["canonical_json"] = "{}" }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := map[string]any{"page_id": deck.DeckID, "schema_version": 1, "canonical_json": canonical, "content_hash": hash}
			tc.change(r)
			if err := validateSlidesRestoreRow("slides_heads", r); (err == nil) != tc.valid {
				t.Fatalf("validation result=%v valid=%v", err, tc.valid)
			}
		})
	}
}

func TestSlidesBackupLegacyDumpNoop(t *testing.T) {
	for _, tables := range []map[string][]map[string]any{nil, {}} {
		dump := &DBDump{Tables: tables}
		if err := remapSlides(dump, nil); err != nil {
			t.Fatal(err)
		}
		if len(dump.Tables) != 0 {
			t.Fatal("legacy dump gained absent Slides tables")
		}
	}
}
