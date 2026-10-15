package slides

import (
	"encoding/json"
	"fmt"
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

func TestAggregateTableRenderingBudget(t *testing.T) {
	fixture, _, _ := modelFixture(t)
	deck := Deck{SchemaVersion: 1, DeckID: "table_budget", Width: 960, Height: 540, Theme: fixture.Theme, SlideOrder: []string{"tables"}, Slides: []Slide{{ID: "tables", Elements: []Element{}}}}
	cells := make([][]string, 30)
	for i := range cells {
		cells[i] = make([]string, 20)
	}
	for i := 0; i < 900; i++ {
		deck.Slides[0].Elements = append(deck.Slides[0].Elements, Element{ID: fmt.Sprintf("table_%d", i), Type: "table", Width: 1, Height: 1, Table: &Table{Cells: cells, FontFamily: "Arial", FontSize: 12, Color: "#112233", Fill: "#FFFFFF", Border: "#445566"}})
	}
	raw, err := Canonical(deck)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("900 admitted-size tables: %d canonical bytes, %d cells", len(raw), 900*30*20)
	if len(raw) > MaxDeckBytes {
		t.Fatal("focused fixture must fit byte budget")
	}
	if err = Validate(deck); err == nil {
		t.Fatal("540000-cell rendering workload accepted below 2MiB")
	}
}

func TestTableCellBudgetsCountAllTablesAndSlides(t *testing.T) {
	fixture, _, _ := modelFixture(t)
	makeTable := func(id string, rows, columns int) Element {
		cells := make([][]string, rows)
		for i := range cells {
			cells[i] = make([]string, columns)
		}
		return Element{ID: id, Type: "table", Width: 1, Height: 1, Table: &Table{Cells: cells, FontFamily: "Arial", FontSize: 12, Color: "#112233", Fill: "#FFFFFF", Border: "#445566"}}
	}
	deck := Deck{SchemaVersion: 1, DeckID: "table_boundaries", Width: 960, Height: 540, Theme: fixture.Theme, SlideOrder: []string{}, Slides: []Slide{}}
	for i := 0; i < 5; i++ {
		id := fmt.Sprintf("slide_%d", i)
		slide := Slide{ID: id, Elements: []Element{makeTable(id+"_a", 30, 20), makeTable(id+"_b", 30, 20), makeTable(id+"_c", 30, 20), makeTable(id+"_d", 10, 20)}}
		deck.Slides = append(deck.Slides, slide)
		deck.SlideOrder = append(deck.SlideOrder, id)
	}
	if err := Validate(deck); err != nil {
		t.Fatal("exact10000/deck and2000/slide should fit", err)
	}
	deck.Slides = append(deck.Slides, Slide{ID: "one_more_slide", Elements: []Element{makeTable("one_more_cell", 1, 1)}})
	deck.SlideOrder = append(deck.SlideOrder, "one_more_slide")
	if err := Validate(deck); err == nil || !strings.Contains(err.Error(), "10000") {
		t.Fatalf("deck aggregate should reject10001 cells: %v", err)
	}
	deck.Slides = deck.Slides[:1]
	deck.SlideOrder = deck.SlideOrder[:1]
	deck.Slides[0].Elements = append(deck.Slides[0].Elements, makeTable("one_more_slide_cell", 1, 1))
	if err := Validate(deck); err == nil || !strings.Contains(err.Error(), "2000") {
		t.Fatalf("slide aggregate should reject2001 cells: %v", err)
	}
	deck.Slides[0].Elements = deck.Slides[0].Elements[:1]
	if err := Validate(deck); err != nil {
		t.Fatal("basic600-cell table must remain valid", err)
	}
}
