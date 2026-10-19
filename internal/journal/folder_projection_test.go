package journal

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestFolderMembershipPublicProjection(t *testing.T) {
	for _, tc := range []struct {
		name     string
		markers  map[string]any
		slug, id bool
	}{
		{"new-ordinary", map[string]any{"private_audience": false, "private_output_metadata": false}, true, true},
		{"private-chat", map[string]any{"private_audience": true, "private_output_metadata": true}, false, false},
		{"opaque-output", map[string]any{"private_audience": false, "private_output_metadata": true}, false, true},
		{"legacy-ordinary", nil, false, false}, {"legacy-private", nil, false, false},
		{"missing-metadata", map[string]any{"private_audience": false}, false, false},
		{"string-marker", map[string]any{"private_audience": "false", "private_output_metadata": false}, false, false},
		{"integer-marker", map[string]any{"private_audience": 0, "private_output_metadata": false}, false, false},
		{"null-marker", map[string]any{"private_audience": nil, "private_output_metadata": false}, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload := map[string]any{"page": "secret-page-slug", "page_id": "opaque-page-id", "op": "moved", "from": "first", "to": "second", "pages_version": int64(3), "actor_user_id": "actor", "title": "secret-page-title"}
			for k, v := range tc.markers {
				payload[k] = v
			}
			entry := Entry{Type: "page.folder_membership_changed", Summary: "Moved secret-page-slug secret-page-title", Payload: payload, Refs: map[string]any{"page_id": "opaque-page-id", "page": "secret-page-slug"}}
			canonical, _ := json.Marshal(entry)
			projected := PublicFolderMembership(entry)
			wire, _ := json.Marshal(SerializeEntry(entry))
			if strings.Contains(string(wire), "secret-page-slug") != tc.slug || strings.Contains(string(wire), "secret-page-title") != tc.slug {
				t.Errorf("descriptive metadata projection: %s", wire)
			}
			if strings.Contains(string(wire), "opaque-page-id") != tc.id {
				t.Errorf("identity projection: %s", wire)
			}
			if strings.Contains(string(wire), "private_audience") || strings.Contains(string(wire), "private_output_metadata") {
				t.Error("internal classification on wire")
			}
			if projected.Payload["op"] != "moved" || projected.Payload["from"] != "first" || projected.Payload["to"] != "second" {
				t.Error("safe movement metadata lost")
			}
			projected.Payload["op"] = "changed-projection"
			if projected.Refs != nil {
				projected.Refs = nil
			}
			after, _ := json.Marshal(entry)
			if string(canonical) != string(after) {
				t.Error("canonical entry mutated")
			}
		})
	}
	t.Run("other-event-unchanged", func(t *testing.T) {
		entry := Entry{Type: EntryPagePanelUpdated, Summary: "Existing panel", Payload: map[string]any{"page": "ordinary", "private_audience": "not-this-contract"}}
		if got := PublicFolderMembership(entry); !reflect.DeepEqual(entry, got) {
			t.Fatal("unrelated event changed")
		}
	})
}
