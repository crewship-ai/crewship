// Emits actual compiled Go registry and derivations for the TypeScript parity check.
package main

import (
	"encoding/json"
	"github.com/crewship-ai/crewship/internal/pages"
	"os"
)

func main() {
	var fixtures []struct {
		Facts pages.ContentFacts `json:"facts"`
	}
	data, err := os.ReadFile("testdata/page-content-derivation.json")
	if err != nil {
		panic(err)
	}
	if err = json.Unmarshal(data, &fixtures); err != nil {
		panic(err)
	}
	results := []pages.ContentDerivation{}
	for _, fixture := range fixtures {
		results = append(results, pages.DeriveContentTypes(fixture.Facts))
	}
	if err = json.NewEncoder(os.Stdout).Encode(struct {
		Registry []pages.ContentTypeDescriptor `json:"registry"`
		Results  []pages.ContentDerivation     `json:"results"`
	}{pages.ContentTypes(), results}); err != nil {
		panic(err)
	}
}
