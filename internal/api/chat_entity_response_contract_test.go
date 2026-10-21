package api

import (
	"bytes"
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

// The named DTOs used by handlers must preserve the frontend's actual fixture
// fields. Unknown fields fail decoding instead of disappearing in a round trip.
func TestChatEntityResponseDTOsPreserveWireFixtures(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value any
	}{{"page.json", &EntityRefPage{}}, {"operation.json", &EntityOperationResponse{}}} {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := os.ReadFile("testdata/chat-entity-refs/" + tc.name)
			if err != nil {
				t.Fatal(err)
			}
			dec := json.NewDecoder(bytes.NewReader(raw))
			dec.DisallowUnknownFields()
			if err = dec.Decode(tc.value); err != nil {
				t.Fatal(err)
			}
			encoded, err := json.Marshal(tc.value)
			if err != nil {
				t.Fatal(err)
			}
			var before, after any
			if err = json.Unmarshal(raw, &before); err != nil {
				t.Fatal(err)
			}
			if err = json.Unmarshal(encoded, &after); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before, after) {
				t.Fatalf("DTO changed fixture wire shape: %s", encoded)
			}
		})
	}
}
