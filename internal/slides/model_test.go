package slides

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func modelFixture(t *testing.T) (Deck, string, string) {
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
	if err = json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	deck, err := Decode(fixture.Deck)
	if err != nil {
		t.Fatal(err)
	}
	return deck, fixture.Canonical, fixture.Hash
}
func TestCanonicalModelFixture(t *testing.T) {
	deck, canonical, wantHash := modelFixture(t)
	encoded, err := Canonical(deck)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != canonical {
		t.Fatalf("canonical differs:\n%s\n%s", encoded, canonical)
	}
	hash, err := DeckHash(deck)
	if err != nil || hash != wantHash {
		t.Fatalf("hash %s want %s: %v", hash, wantHash, err)
	}
	if _, err := Decode(encoded); err != nil {
		t.Fatal(err)
	}
	deck.Slides[0], deck.Slides[1] = deck.Slides[1], deck.Slides[0]
	again, _ := DeckHash(deck)
	if again != hash {
		t.Fatal("storage array order changed hash")
	}
}
func TestModelRejectsUnsupportedAndAmbiguousInput(t *testing.T) {
	deck, _, _ := modelFixture(t)
	raw, _ := json.Marshal(deck)
	source := string(raw)
	tests := map[string]string{
		"element case alias": strings.Replace(source, `"x":24`, `"x":24,"X":33`, 1),
		"nested case alias":  strings.Replace(source, `"font_size":28`, `"font_size":28,"FONT_SIZE":30`, 1),
		"capitalized alias":  strings.Replace(source, `"deck_id":"fixture_page"`, `"deck_id":"fixture_page","DECK_ID":"changed_page"`, 1),
		"null table cell":    strings.Replace(source, `"Položka"`, `null`, 1),
		"unknown":            strings.Replace(source, `"schema_version":1`, `"schema_version":1,"script":"alert(1)"`, 1),
		"duplicate key":      strings.Replace(source, `"schema_version":1`, `"schema_version":1,"schema_version":1`, 1),
		"schema":             strings.Replace(source, `"schema_version":1`, `"schema_version":2`, 1),
		"missing coordinate": strings.Replace(source, `"x":24,`, ``, 1),
		"fraction":           strings.Replace(source, `"x":24`, `"x":24.0`, 1),
		"trailing":           source + ` {}`,
		"image":              strings.Replace(source, `"type":"text"`, `"type":"image"`, 1),
		"surrogate":          strings.Replace(source, `"notes":""`, `"notes":"\ud800"`, 1),
		"lone low surrogate": strings.Replace(source, `"notes":""`, `"notes":"\udc00"`, 1),
		"null notes":         strings.Replace(source, `"notes":""`, `"notes":null`, 1),
	}
	for name, input := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := Decode([]byte(input)); err == nil {
				t.Fatal("invalid input accepted")
			}
		})
	}
	for name, mutate := range map[string]func(*Deck){
		"duplicate ID":    func(d *Deck) { d.Slides[1].Elements[0].ID = d.Slides[0].Elements[0].ID },
		"duplicate order": func(d *Deck) { d.SlideOrder[1] = d.SlideOrder[0] },
		"outside":         func(d *Deck) { d.Slides[0].Elements[0].X = d.Width },
		"text budget":     func(d *Deck) { d.Slides[0].Elements[0].Text.Value = strings.Repeat("x", MaxTextBytes+1) },
		"ragged table":    func(d *Deck) { d.Slides[1].Elements[0].Table.Cells[1] = []string{} },
		"wrong payload":   func(d *Deck) { d.Slides[0].Elements[0].Shape = &Shape{} },
	} {
		t.Run(name, func(t *testing.T) {
			d, _, _ := modelFixture(t)
			mutate(&d)
			data, _ := json.Marshal(d)
			if _, err := Decode(data); err == nil {
				t.Fatal("invalid semantic input accepted")
			}
		})
	}
}
