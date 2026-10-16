// Emits actual Go versioned canonical/hash output for the TS contract check.
package main

import (
	"encoding/json"
	"os"

	"github.com/crewship-ai/crewship/internal/pageassets"
	"github.com/crewship-ai/crewship/internal/slides"
)

func main() {
	path := "testdata/slides-schema-v1.json"
	if len(os.Args) == 2 {
		path = os.Args[1]
	}
	raw, err := os.ReadFile(path)
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
	if err = json.NewEncoder(os.Stdout).Encode(map[string]any{"canonical_json": string(canonical), "hash": hash, "elements": elements, "raster_limits": map[string]int{"dimension": pageassets.MaxDimension, "pixels": pageassets.MaxPixels, "upload_bytes": pageassets.MaxInput, "stored_bytes": pageassets.MaxOutput}, "table_cell_limits": map[string]int{"deck": slides.MaxTableCellsPerDeck, "slide": slides.MaxTableCellsPerSlide}}); err != nil {
		panic(err)
	}
}
