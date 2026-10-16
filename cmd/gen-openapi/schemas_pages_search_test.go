package main

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/crewship-ai/crewship/internal/api"
)

func TestNativeSearchResponseRequiresActualSerializedFields(t *testing.T) {
	response := pagesSchemaCatalog()["GET /api/v1/pages/search/content"].Response
	match := response["properties"].(map[string]any)["matches"].(map[string]any)["items"].(map[string]any)
	for name, tc := range map[string]struct {
		schema map[string]any
		dto    any
	}{
		"response": {response, api.NativePageSearchResponse{}},
		"match":    {match, api.NativePageSearchMatch{}},
	} {
		t.Run(name, func(t *testing.T) {
			raw, err := json.Marshal(tc.dto)
			if err != nil {
				t.Fatal(err)
			}
			var wire map[string]any
			if err = json.Unmarshal(raw, &wire); err != nil {
				t.Fatal(err)
			}
			required, ok := tc.schema["required"].([]string)
			if !ok {
				t.Fatal("actual serialized fields are not required")
			}
			got := map[string]bool{}
			want := map[string]bool{}
			props := tc.schema["properties"].(map[string]any)
			for _, key := range required {
				if _, exists := props[key]; !exists {
					t.Fatalf("required field %s lacks schema", key)
				}
				got[key] = true
			}
			for key := range wire {
				want[key] = true
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("required=%v actual mandatory JSON=%v", got, want)
			}
		})
	}
	if _, ok := response["properties"].(map[string]any)["cursor"]; !ok {
		t.Fatal("optional cursor omitted from contract")
	}
}
