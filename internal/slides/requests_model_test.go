package slides

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"unicode/utf16"
)

func selectedFixture(t *testing.T) (State, SelectedRequestInput) {
	t.Helper()
	deck, _, _ := modelFixture(t)
	for i := range deck.Slides {
		for j := range deck.Slides[i].Elements {
			element := &deck.Slides[i].Elements[j]
			if element.Type == "text" {
				element.Text.Value = "Před 🐎 český\ntext za"
				hash, _ := ElementHash(*element)
				content, _ := DeckHash(deck)
				from := len(utf16.Encode([]rune("Před ")))
				to := from + len(utf16.Encode([]rune("🐎 český\ntext")))
				return State{SchemaVersion: deck.SchemaVersion, Revision: 7, OrderVersion: 1, ContentHash: content, Deck: deck}, SelectedRequestInput{PageID: deck.DeckID, TypeID: "slides", SchemaVersion: 1, BaseRev: 7, SubmissionID: "submission", ChatID: "chat", Instruction: "Improve selected wording", Selection: TextSelection{SelectionSchemaVersion: 1, Kind: "text_range", SlideID: deck.Slides[i].ID, ElementID: element.ID, ElementHash: hash, From: from, To: to, Quote: "🐎 český\ntext"}}
			}
		}
	}
	t.Fatal("text fixture missing")
	return State{}, SelectedRequestInput{}
}
func TestSelectedRequestStrictProtocolAndUTF16(t *testing.T) {
	state, request := selectedFixture(t)
	raw, _ := json.Marshal(request)
	decoded, err := DecodeSelectedRequest(raw)
	if err != nil || !reflect.DeepEqual(request, decoded) {
		t.Fatal(decoded, err)
	}
	verified, err := VerifyTextSelection(state, request)
	if err != nil {
		t.Fatal(err)
	}
	corrupted := verified
	corrupted.ContentHash = strings.Repeat("f", 64)
	if _, err := SelectedReplacementPatch(state, request.PageID, "request", request.BaseRev, corrupted, "replacement"); err == nil {
		t.Fatal("corrupted persisted original hash accepted")
	}
	patch, err := SelectedReplacementPatch(state, request.PageID, "request", request.BaseRev, verified, "Nový text 🐎")
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := ApplyPatch(state.Deck, patch)
	if err != nil {
		t.Fatal(err)
	}
	expected := cloneDeckForRequestTest(t, state.Deck)
	for i := range expected.Slides {
		for j := range expected.Slides[i].Elements {
			e := &expected.Slides[i].Elements[j]
			if e.ID == request.Selection.ElementID {
				e.Text.Value = "Před Nový text 🐎 za"
			}
		}
	}
	expectedBytes, _ := Canonical(expected)
	candidateBytes, _ := Canonical(candidate)
	if string(expectedBytes) != string(candidateBytes) {
		t.Fatal("replacement changed nonselected content/style/geometry")
	}
	for _, change := range []func(*SelectedRequestInput){
		func(r *SelectedRequestInput) { r.BaseRev++ }, func(r *SelectedRequestInput) { r.PageID = "other_page" }, func(r *SelectedRequestInput) { r.Selection.From++ },
		func(r *SelectedRequestInput) { r.Selection.To = 1 << 30 }, func(r *SelectedRequestInput) { r.Selection.Quote = "similar quote" }, func(r *SelectedRequestInput) { r.Selection.ElementHash = strings.Repeat("f", 64) },
		func(r *SelectedRequestInput) { r.Selection.SlideID = "other_slide" }, func(r *SelectedRequestInput) { r.Selection.SelectionSchemaVersion = 2 },
	} {
		changed := request
		change(&changed)
		if _, err := VerifyTextSelection(state, changed); err == nil {
			t.Fatal("widened/stale immutable selection accepted", changed)
		}
	}
	for _, bad := range []string{
		strings.Replace(string(raw), `"schema_version":1`, `"schema_version":2`, 1), strings.Replace(string(raw), `"from":5`, `"from":5,"FROM":6`, 1),
		strings.Replace(string(raw), `"quote":`, `"quote":null,"unknown":`, 1), string(raw) + `{}`, strings.Replace(string(raw), `"instruction":`, `"instruction":"other","instruction":`, 1),
	} {
		if _, err := DecodeSelectedRequest([]byte(bad)); err == nil {
			t.Fatal("strict selected protocol accepted", bad)
		}
	}
	if _, err := SelectedReplacementPatch(state, request.PageID, "request", 7, verified, strings.Repeat("a", MaxTextBytes)); err == nil {
		t.Fatal("aggregate final text overflow accepted")
	}
}
func cloneDeckForRequestTest(t *testing.T, deck Deck) Deck {
	t.Helper()
	raw, err := json.Marshal(deck)
	if err != nil {
		t.Fatal(err)
	}
	result, err := Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	return result
}
func TestSelectedRequestRejectsNontextTargets(t *testing.T) {
	state, request := selectedFixture(t)
	for _, slide := range state.Deck.Slides {
		for _, element := range slide.Elements {
			if element.Type != "text" {
				changed := request
				changed.Selection.SlideID = slide.ID
				changed.Selection.ElementID = element.ID
				changed.Selection.ElementHash, _ = ElementHash(element)
				if _, err := VerifyTextSelection(state, changed); err == nil || errors.Is(err, ErrConflict) {
					t.Fatal("nontext target was not explicitly refused", element.Type, err)
				}
			}
		}
	}
}

func TestSelectedProtocolOneOverDeckTwoPreservesImage(t *testing.T) {
	state, request := selectedFixture(t)
	state.SchemaVersion = ImageSchemaVersion
	state.Deck.SchemaVersion = ImageSchemaVersion
	image := Element{ID: "private_raster", Type: "image", X: 0, Y: 0, Width: 20, Height: 20, Image: &Image{AssetID: "asset_private", ExpectedSHA256: strings.Repeat("a", 64), Alt: "Private raster"}}
	state.Deck.Slides[0].Elements = append(state.Deck.Slides[0].Elements, image)
	state.ContentHash, _ = DeckHash(state.Deck)
	verified, err := VerifyTextSelection(state, request)
	if err != nil {
		t.Fatal(err)
	}
	patch, err := SelectedReplacementPatch(state, request.PageID, "mixed_version_request", 7, verified, "replacement")
	if err != nil || patch.SchemaVersion != 2 {
		t.Fatal(patch, err)
	}
	candidate, err := ApplyPatch(state.Deck, patch)
	if err != nil {
		t.Fatal(err)
	}
	var got Element
	for _, slide := range candidate.Slides {
		for _, element := range slide.Elements {
			if element.ID == image.ID {
				got = element
			}
		}
	}
	if !reflect.DeepEqual(got, image) {
		t.Fatal("selected text replacement changed private image")
	}
	request.Selection.SlideID = state.Deck.Slides[0].ID
	request.Selection.ElementID = image.ID
	request.Selection.ElementHash, _ = ElementHash(image)
	if _, err = VerifyTextSelection(state, request); err == nil {
		t.Fatal("selected adapter admitted image")
	}
}
