package main

import (
	"reflect"
	"testing"
)

func TestScheduleSchemaSkipReasonIsOptionalHistoricalEnum(t *testing.T) {
	schema := executionSchemaComponents()["Schedule"].(map[string]any)
	properties := schema["properties"].(map[string]any)
	raw, ok := properties["last_skip_reason"]
	if !ok {
		t.Fatal("Schedule response does not document last_skip_reason")
	}
	field := raw.(map[string]any)
	if field["type"] != "string" || field["readOnly"] != true || !reflect.DeepEqual(field["enum"], []string{"capacity", "catchup", "inactive", "busy"}) {
		t.Fatalf("reason schema: %+v", field)
	}
	if rawRequired, present := schema["required"]; present {
		required, ok := rawRequired.([]string)
		if !ok {
			t.Fatalf("unexpected required representation: %T", rawRequired)
		}
		for _, name := range required {
			if name == "last_skip_reason" {
				t.Fatal("legacy missing reason must stay valid")
			}
		}
	}
}
