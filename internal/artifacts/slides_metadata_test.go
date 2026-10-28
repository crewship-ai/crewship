package artifacts

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
)

func TestSlidesMetadataOnlyAdmitsClosedReferencedIndex(t *testing.T) {
	index := []byte(`{"schema_version":1,"order":["intro"]}`)
	sum := sha256.Sum256(index)
	manifest := storedManifest{EntryPoint: "slides/intro.html", Files: map[string]storedFile{"deck.json": {Hash: hex.EncodeToString(sum[:]), MediaType: "application/json"}, "slides/intro.html": {Hash: strings.Repeat("a", 64), MediaType: "text/html"}}}
	check := func(want bool) {
		t.Helper()
		raw, _ := json.Marshal(manifest)
		if got := IsSlidesArtifactIndex(raw, index); got != want {
			t.Fatalf("classification %v want %v", got, want)
		}
	}
	check(true)
	manifest.Files["dashboard.json"] = storedFile{Hash: strings.Repeat("b", 64), MediaType: "application/json"}
	check(false)
	delete(manifest.Files, "dashboard.json")
	manifest.Files["slides/intro.html"] = storedFile{Hash: strings.Repeat("a", 64), MediaType: "text/javascript"}
	check(false)
	manifest.Files["slides/intro.html"] = storedFile{Hash: strings.Repeat("a", 64), MediaType: "text/html"}
	index = []byte(`{"schema_version":1,"order":["intro","intro"]}`)
	check(false)
	index = []byte(`{"schema_version":1,"order":["intro"]}` + strings.Repeat(" ", 16384))
	check(false)
}

func TestSlidesStoredManifestRejectsSQLGoSelectionAmbiguity(t *testing.T) {
	hash := strings.Repeat("a", 64)
	valid := `{"entry_point":"slides/intro.html","files":{"deck.json":{"hash":"` + hash + `","media_type":"application/json"},"slides/intro.html":{"hash":"` + hash + `","media_type":"text/html"}}}`
	if !ValidateSlidesArtifactManifest([]byte(valid)) {
		t.Fatal("valid metadata rejected")
	}
	for _, raw := range []string{
		strings.Replace(valid, `"files":`, `"files":{},"files":`, 1),
		strings.Replace(valid, `"files":`, `"Files":{},"files":`, 1),
		strings.Replace(valid, `"hash":"`, `"Hash":"`+hash+`","hash":"`, 1),
		strings.Replace(valid, `"media_type":"`, `"Media_Type":"application/json","media_type":"`, 1),
	} {
		if ValidateSlidesArtifactManifest([]byte(raw)) {
			t.Error("ambiguous manifest admitted")
		}
	}
}
