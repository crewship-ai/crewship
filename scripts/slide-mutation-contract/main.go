// Exercises the actual Go semantic interpreter against TS-planned requests.
package main

import (
	"encoding/json"
	"io"
	"os"

	"github.com/crewship-ai/crewship/internal/slides"
)

func main() {
	var cases []struct {
		Deck    json.RawMessage `json:"deck"`
		Request json.RawMessage `json:"request"`
	}
	if err := json.NewDecoder(io.LimitReader(os.Stdin, 8<<20)).Decode(&cases); err != nil {
		panic(err)
	}
	results := []string{}
	for _, fixture := range cases {
		deck, err := slides.Decode(fixture.Deck)
		if err != nil {
			panic(err)
		}
		request, err := slides.DecodePatch(fixture.Request)
		if err != nil {
			panic(err)
		}
		candidate, err := slides.ApplyPatch(deck, request)
		if err != nil {
			panic(err)
		}
		encoded, err := slides.Canonical(candidate)
		if err != nil {
			panic(err)
		}
		results = append(results, string(encoded))
	}
	if err := json.NewEncoder(os.Stdout).Encode(results); err != nil {
		panic(err)
	}
}
