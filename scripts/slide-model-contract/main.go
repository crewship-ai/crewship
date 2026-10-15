// Emits actual Go schema-1 canonical/hash output for the TS contract check.
package main

import (
	"encoding/json"
	"github.com/crewship-ai/crewship/internal/slides"
	"os"
)

func main() {
	raw, err := os.ReadFile("testdata/slides-schema-v1.json")
	if err != nil {
		panic(err)
	}
	var fixture struct {
		Deck json.RawMessage `json:"deck"`
	}
	if err = json.Unmarshal(raw, &fixture); err != nil {
		panic(err)
	}
	deck, err := slides.Decode(fixture.Deck)
	if err != nil {
		panic(err)
	}
	canonical, err := slides.Canonical(deck)
	if err != nil {
		panic(err)
	}
	hash, err := slides.DeckHash(deck)
	if err != nil {
		panic(err)
	}
	elements := map[string]string{}
	for _, slide := range deck.Slides {
		for _, element := range slide.Elements {
			h, err := slides.ElementHash(element)
			if err != nil {
				panic(err)
			}
			elements[element.ID] = h
		}
	}
	if err = json.NewEncoder(os.Stdout).Encode(map[string]any{"canonical_json": string(canonical), "hash": hash, "elements": elements, "table_cell_limits": map[string]int{"deck": slides.MaxTableCellsPerDeck, "slide": slides.MaxTableCellsPerSlide}}); err != nil {
		panic(err)
	}
}
