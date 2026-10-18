package slides

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestPatchPreconditionsAtomicityAndStableIdentity(t *testing.T) {
	current, _, _ := modelFixture(t)
	before, _ := DeckHash(current)
	target := current.Slides[0].Elements[0]
	hash, _ := ElementHash(target)
	updated := target
	text := *target.Text
	text.Value = "Upraveno česky\nDruhý řádek"
	updated.Text = &text
	request := PatchRequest{PageID: "fixture_page", SchemaVersion: 1, BaseRev: 1, MutationID: "patch_one", Operations: []Operation{{Op: "set_element", SlideID: current.Slides[0].ID, ElementID: target.ID, IfHash: hash, Element: &updated}}}
	raw, _ := json.Marshal(request)
	decoded, err := DecodePatch(raw)
	if err != nil {
		t.Fatal(err)
	}
	after, err := ApplyPatch(current, decoded)
	if err != nil {
		t.Fatal(err)
	}
	var changed *Slide
	for i := range after.Slides {
		if after.Slides[i].ID == current.Slides[0].ID {
			changed = &after.Slides[i]
		}
	}
	if changed == nil || changed.Elements[0].ID != target.ID || changed.Elements[0].Text.Value != text.Value {
		t.Fatal("patch lost target or text")
	}
	original, _ := DeckHash(current)
	if original != before {
		t.Fatal("patch mutated original")
	}
	request.Operations = append(request.Operations, Operation{Op: "delete_element", SlideID: current.Slides[0].ID, ElementID: target.ID, IfHash: hash})
	if _, err := ApplyPatch(current, request); !errors.Is(err, ErrConflict) {
		t.Fatalf("want stale intermediate target conflict: %v", err)
	}
	original, _ = DeckHash(current)
	if original != before {
		t.Fatal("rejected batch mutated original")
	}
	// Preconditions are evaluated against the candidate after prior operations.
	nextHash, _ := ElementHash(changed.Elements[0])
	request.Operations[1].IfHash = nextHash
	result, err := ApplyPatch(current, request)
	if err != nil {
		t.Fatal("valid sequential hash rejected", err)
	}
	for _, slide := range result.Slides {
		for _, element := range slide.Elements {
			if element.ID == target.ID {
				t.Fatal("sequential deletion not applied")
			}
		}
	}

}
func TestSmallPatchCannotCrossAggregateDeckBudget(t *testing.T) {
	fixture, _, _ := modelFixture(t)
	current := Deck{SchemaVersion: 1, DeckID: "budget_page", Width: 960, Height: 540, Theme: fixture.Theme, SlideOrder: []string{"budget_slide"}, Slides: []Slide{{ID: "budget_slide", Elements: []Element{}}}}
	for i := 0; i < 63; i++ {
		current.Slides[0].Elements = append(current.Slides[0].Elements, Element{ID: fmt.Sprintf("text_%d", i), Type: "text", Width: 1, Height: 1, Text: &Text{Value: strings.Repeat("x", MaxTextBytes), FontFamily: "Arial", FontSize: 12, Color: "#112233", Align: "left"}})
	}
	if err := Validate(current); err != nil {
		t.Fatal("budget fixture should fit", err)
	}
	before, _ := DeckHash(current)
	hash, _ := SlideHash(current.Slides[0])
	element := current.Slides[0].Elements[0]
	element.ID = "one_more"
	request := PatchRequest{PageID: "budget_page", SchemaVersion: 1, BaseRev: 1, MutationID: "small_delta", Operations: []Operation{{Op: "insert_element", SlideID: "budget_slide", IfHash: hash, Element: &element}}}
	raw, _ := json.Marshal(request)
	if len(raw) >= MaxDeckBytes {
		t.Fatal("patch itself must be small")
	}
	decoded, err := DecodePatch(raw)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = ApplyPatch(current, decoded); err == nil {
		t.Fatal("aggregate budget bypass")
	}
	after, _ := DeckHash(current)
	if after != before {
		t.Fatal("rejected budget patch mutated input")
	}
}
func TestRequestStrictFieldsAndStructuralOrderPrecondition(t *testing.T) {
	deck, _, _ := modelFixture(t)
	hash, _ := SlideHash(deck.Slides[0])
	notes := "changed"
	request := PatchRequest{PageID: "fixture_page", SchemaVersion: 1, BaseRev: 1, MutationID: "notes", Operations: []Operation{{Op: "set_notes", SlideID: deck.Slides[0].ID, IfHash: hash, Notes: &notes}}}
	raw, _ := json.Marshal(request)
	source := string(raw)
	for name, input := range map[string]string{
		"case alias":        strings.Replace(source, `"base_rev":1`, `"base_rev":1,"BASE_REV":2`, 1),
		"operation alias":   strings.Replace(source, `"notes":"changed"`, `"notes":"changed","NOTES":"other"`, 1),
		"duplicate":         strings.Replace(source, `"base_rev":1`, `"base_rev":1,"base_rev":2`, 1),
		"trailing":          source + ` {}`,
		"unknown operation": strings.Replace(source, `"set_notes"`, `"run_script"`, 1),
		"null string":       strings.Replace(source, `"notes":"changed"`, `"notes":null`, 1),
		"lone surrogate":    strings.Replace(source, `"changed"`, `"\ud800"`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodePatch([]byte(input)); err == nil {
				t.Fatal("ambiguous request accepted")
			}
		})
	}
	request.Operations = []Operation{{Op: "delete_slide", SlideID: deck.Slides[0].ID, IfHash: hash}}
	raw, _ = json.Marshal(request)
	if _, err := DecodePatch(raw); err == nil {
		t.Fatal("structural operation missing order precondition accepted")
	}
	order := int64(1)
	request.IfOrderVersion = &order
	raw, _ = json.Marshal(request)
	if _, err := DecodePatch(raw); err != nil {
		t.Fatal(err)
	}
}
func TestRequestElementSentinelDoesNotRestrictPublicIDs(t *testing.T) {
	deck, _, _ := modelFixture(t)
	hash, _ := SlideHash(deck.Slides[0])
	element := deck.Slides[0].Elements[0]
	element.ID = "__request_slide"
	request := PatchRequest{PageID: "fixture_page", SchemaVersion: 1, BaseRev: 1, MutationID: "insert", Operations: []Operation{{Op: "insert_element", SlideID: deck.Slides[0].ID, IfHash: hash, Element: &element}}}
	raw, _ := json.Marshal(request)
	if _, err := DecodePatch(raw); err != nil {
		t.Fatal(err)
	}
}

func TestInitializeRetainsPageSchemaAndCanonicalIdentity(t *testing.T) {
	deck, canonical, hash := modelFixture(t)
	request := InitializeRequest{PageID: deck.DeckID, SchemaVersion: deck.SchemaVersion, BaseRev: 0, MutationID: "create_deck", Deck: deck}
	raw, _ := json.Marshal(request)
	decoded, err := DecodeInitialize(raw)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Canonical(decoded.Deck)
	if err != nil || string(got) != canonical {
		t.Fatalf("initialize changed canonical content: %v", err)
	}
	if gotHash, err := DeckHash(decoded.Deck); err != nil || gotHash != hash {
		t.Fatalf("initialize changed content hash: %s %v", gotHash, err)
	}
	for name, mutate := range map[string]func(*InitializeRequest){
		"different Page":   func(r *InitializeRequest) { r.PageID = "other_page" },
		"different schema": func(r *InitializeRequest) { r.SchemaVersion = ImageSchemaVersion },
		"future schema":    func(r *InitializeRequest) { r.SchemaVersion = 99 },
		"noninitial base":  func(r *InitializeRequest) { r.BaseRev = 1 },
		"missing mutation": func(r *InitializeRequest) { r.MutationID = "" },
	} {
		t.Run(name, func(t *testing.T) {
			invalid := request
			mutate(&invalid)
			raw, _ := json.Marshal(invalid)
			if _, err := DecodeInitialize(raw); err == nil {
				t.Fatal("invalid initialization accepted")
			}
		})
	}
}

func TestPureImageUpgradeRequiresOriginalContentFence(t *testing.T) {
	// Uses the existing schema1 -> schema2 upgrade contract; no asset publication.
	deck, _, contentHash := modelFixture(t)
	slideHash, _ := SlideHash(deck.Slides[0])
	version := ImageSchemaVersion
	element := Element{ID: "image_one", Type: "image", X: 4, Y: 5, Width: 20, Height: 30, Image: &Image{AssetID: "asset_one", ExpectedSHA256: strings.Repeat("a", 64), Alt: "Sample česky"}}
	request := PatchRequest{PageID: deck.DeckID, SchemaVersion: deck.SchemaVersion, BaseRev: 1, MutationID: "first_image", TargetSchemaVersion: &version, IfContentHash: contentHash, Operations: []Operation{{Op: "insert_element", SlideID: deck.Slides[0].ID, IfHash: slideHash, Element: &element}}}
	raw, _ := json.Marshal(request)
	decoded, err := DecodePatch(raw)
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := ApplyPatch(deck, decoded)
	if err != nil || candidate.SchemaVersion != ImageSchemaVersion {
		t.Fatalf("upgrade %#v %v", candidate, err)
	}
	if candidate.DeckID != deck.DeckID {
		t.Fatal("upgrade changed Page identity")
	}
	beforeIDs, afterIDs := ObjectIDs(deck), ObjectIDs(candidate)
	for id, kind := range beforeIDs {
		if afterIDs[id] != kind {
			t.Fatalf("upgrade changed retained identity %s", id)
		}
	}
	if len(afterIDs) != len(beforeIDs)+1 || afterIDs[element.ID] != "element" {
		t.Fatal("upgrade did not add exactly the new image identity")
	}
	stale := decoded
	stale.IfContentHash = strings.Repeat("b", 64)
	if _, err := ApplyPatch(deck, stale); !errors.Is(err, ErrConflict) {
		t.Fatalf("missing original content fence: %v", err)
	}
	implicit := request
	implicit.TargetSchemaVersion = nil
	implicit.IfContentHash = ""
	raw, _ = json.Marshal(implicit)
	if _, err := DecodePatch(raw); err == nil {
		t.Fatal("implicit schema upgrade accepted")
	}
	if after, _ := DeckHash(deck); after != contentHash {
		t.Fatal("upgrade mutated original schema1 deck")
	}
}
