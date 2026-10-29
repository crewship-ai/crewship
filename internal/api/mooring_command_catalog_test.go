package api

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestCommandCatalogPinMatchesGeneratedQualified72(t *testing.T) {
	raw, e := os.ReadFile("../../schemas/wordcraft/capabilities.json")
	if e != nil {
		t.Fatal(e)
	}
	var catalog struct {
		Version  string `json:"version"`
		Commands []struct {
			ID                  string                         `json:"id"`
			Qualification       string                         `json:"qualification"`
			EngineQualification struct{ Status, Scope string } `json:"engineQualification"`
		} `json:"commands"`
	}
	if json.Unmarshal(raw, &catalog) != nil || catalog.Version != "wordcraft:"+commandEngineRevision+":5" || len(catalog.Commands) != 645 {
		t.Fatal("invalid pinned generated catalog")
	}
	if commandCatalogIdentity != "wordcraft-c1-qualified72-v5:"+strings.TrimPrefix(commandDigest(raw), "sha256:") {
		t.Fatal("Go/native catalog pin drift")
	}
	count := 0
	seen := map[string]bool{}
	for _, c := range catalog.Commands {
		if seen[c.ID] || c.Qualification != "unqualified" {
			t.Fatal("duplicate or product promotion", c.ID)
		}
		seen[c.ID] = true
		if c.EngineQualification.Status == "qualified" {
			if c.EngineQualification.Scope != "isolated-c1-fixture" {
				t.Fatal("wrong fixture scope", c.ID)
			}
			count++
		}
	}
	if count != 72 {
		t.Fatal("qualified fixture ID count", count)
	}
}
