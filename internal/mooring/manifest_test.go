package mooring

import (
	"encoding/json"
	"testing"
)

func TestManifestDispatchWithoutApplicationBranches(t *testing.T) {
	m := Manifest{ID: "unrelated-editor", Commands: []Command{{Name: "replace", Effect: "replace-state", InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["count"],"properties":{"count":{"type":"integer","minimum":0}}}`)}}}
	raw, e := m.Apply("replace", json.RawMessage(`{"count":4}`))
	if e != nil || string(raw) != `{"count":4}` {
		t.Fatalf("%s %v", raw, e)
	}
	if _, e = m.Apply("replace", json.RawMessage(`{"count":-1}`)); e == nil {
		t.Fatal("invalid schema accepted")
	}
	if _, e = m.Apply("unregistered", json.RawMessage(`{}`)); e == nil {
		t.Fatal("unknown command accepted")
	}
}
