package pages

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

func TestContentTypeDerivationContract(t *testing.T) {
	data, err := os.ReadFile("content_types_cases.json")
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

func TestContentTypeBoardFormatDescriptor(t *testing.T) {
	descriptor, ok := LookupContentType("board", 1)
	if !ok || descriptor.Viewer != "page-board" || descriptor.Editor != "page-board" || descriptor.AuthoringSkill != "crewship-artifacts" {
		t.Fatalf("Board descriptor missing: %#v", descriptor)
	}
	if !reflect.DeepEqual(descriptor.AnchorKinds, []string{"board", "board-element"}) || !reflect.DeepEqual(descriptor.ExportFormats, []string{"board-draft.json"}) {
		t.Fatalf("Board capabilities lost: %#v", descriptor)
	}
}

// Every combination must preserve specialized-storage exclusivity. These facts
// can arrive independently from separate storage probes.
func TestContentTypeDerivationRejectsStorageCombinations(t *testing.T) {
	for mask := 0; mask < 64; mask++ {
		facts := ContentFacts{
			HasDocument: mask&1 != 0, DocumentSchema: 3,
			HasBoard: mask&2 != 0, BoardFormat: "crewship-board-excalidraw-0.18.1-v1",
			HasArtifact: mask&4 != 0, ArtifactProfile: "static-html-v1", ArtifactVersion: 1,
			HasSlides: mask&8 != 0, SlidesSchema: 2,
			HasPanels: mask&16 != 0, HasProject: mask&32 != 0, HumanOwned: true,
		}
		specialized := 0
		for _, present := range []bool{facts.HasDocument, facts.HasBoard, facts.HasArtifact, facts.HasSlides} {
			if present {
				specialized++
			}
		}
		conflict := specialized > 1 || (specialized > 0 && (facts.HasPanels || facts.HasProject))
		actual := DeriveContentTypes(facts)
		if conflict {
			if actual.ContentDescriptor.Reason != "conflicting-storage" || actual.ContentDescriptor.Supported || actual.CanInitializeContent || actual.ApplicationDescriptor != nil {
				t.Fatalf("mask %d accepts conflicting storage: %#v", mask, actual)
			}
		} else if !actual.ContentDescriptor.Supported {
			t.Fatalf("mask %d rejects known format: %#v", mask, actual)
		}
	}
}

func TestContentTypeLookupReturnsOwnedValues(t *testing.T) {
	original, ok := LookupContentType("panels", 1)
	if !ok {
		t.Fatal("missing panels descriptor")
	}
	mutated, _ := LookupContentType("panels", 1)
	mutated.AnchorKinds[0] = "mutated-anchor"
	mutated.ExportFormats[0] = "mutated-export"
	mutated.Viewer = "mutated-viewer"
	next, _ := LookupContentType("panels", 1)
	if !reflect.DeepEqual(next, original) {
		t.Fatalf("lookup aliases registry: %#v", next)
	}
	for _, id := range []string{"panels", "application", "html", "doc", "board", "slides", "future"} {
		for _, version := range []int{-1, 0, 99} {
			if _, ok := LookupContentType(id, version); ok {
				t.Fatalf("accepted %s version %d", id, version)
			}
		}
	}
}
