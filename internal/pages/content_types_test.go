package pages

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

func TestContentTypeDerivationContract(t *testing.T) {
	data, err := os.ReadFile("../../testdata/page-content-derivation.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures []struct {
		Name     string
		Facts    ContentFacts
		Expected ContentDerivation
	}
	if err = json.Unmarshal(data, &fixtures); err != nil {
		t.Fatal(err)
	}
	for _, fixture := range fixtures {
		t.Run(fixture.Name, func(t *testing.T) {
			actual := DeriveContentTypes(fixture.Facts)
			if !reflect.DeepEqual(actual, fixture.Expected) {
				t.Fatalf("actual %#v want %#v", actual, fixture.Expected)
			}
		})
	}
}
func TestContentTypeRegistryReturnsOwnedValues(t *testing.T) {
	first := ContentTypes()
	first[0].AnchorKinds[0] = "changed"
	if ContentTypes()[0].AnchorKinds[0] == "changed" {
		t.Fatal("registry mutated")
	}
	if _, ok := LookupContentType("doc", 99); ok {
		t.Fatal("unknown version accepted")
	}
}

func TestIntegratedBoardContentAdapter(t *testing.T) {
	descriptor, ok := LookupContentType("board", 1)
	if !ok || descriptor.Viewer != "page-board" || descriptor.Editor != "page-board" || descriptor.AuthoringSkill != "crewship-artifacts" {
		t.Fatalf("Board adapter unavailable: %#v", descriptor)
	}
	if !reflect.DeepEqual(descriptor.AnchorKinds, []string{"board", "board-element"}) || !reflect.DeepEqual(descriptor.ExportFormats, []string{"board-draft.json"}) {
		t.Fatalf("Board capabilities lost: %#v", descriptor)
	}
}
