package main

import (
	"reflect"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/api"
	"github.com/crewship-ai/crewship/internal/slides"
)

func TestSlidesContractsMatchActualWire(t *testing.T) {
	components := slideSchemaComponents()
	for name, dto := range map[string]any{"SlideDeck": slides.Deck{}, "Slide": slides.Slide{}, "SlideState": slides.State{}, "SlideReceipt": slides.Receipt{}, "SlideRevision": slides.Revision{}, "SlideOutline": api.SlideOutline{}} {
		properties := components[name].(map[string]any)["properties"].(map[string]any)
		typ := reflect.TypeOf(dto)
		if len(properties) != typ.NumField() {
			t.Fatalf("%s contract fields%d actual%d", name, len(properties), typ.NumField())
		}
		for i := range typ.NumField() {
			field := strings.Split(typ.Field(i).Tag.Get("json"), ",")[0]
			if _, ok := properties[field]; !ok {
				t.Errorf("%s missing field%s", name, field)
			}
		}
	}
}
func TestSlidesMutationContractRequiresImmutableBinding(t *testing.T) {
	catalog := slideSchemaCatalog()
	for _, operation := range []string{"initialize", "patch", "reorder", "restore"} {
		body := catalog["POST /api/v1/pages/{slug}/slides/"+operation].Request
		required := body["required"].([]string)
		for _, field := range []string{"page_id", "schema_version", "base_rev", "mutation_id"} {
			found := false
			for _, value := range required {
				found = found || value == field
			}
			if !found {
				t.Errorf("%s missing immutable field%s", operation, field)
			}
		}
		if body["additionalProperties"] != false {
			t.Fatal("mutation not closed")
		}
	}
	variants := slideSchemaComponents()["SlideOperation"].(map[string]any)["oneOf"].([]any)
	if len(variants) != 6 {
		t.Fatal("wrong operation union")
	}
}
