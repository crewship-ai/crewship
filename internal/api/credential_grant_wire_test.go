package api

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestCredentialGrantDTOJSON(t *testing.T) {
	for _, grants := range []map[string]string{nil, {}, {"agent": ""}} {
		raw, err := json.Marshal(mcpCredEntry{AgentGrants: grants})
		if err != nil {
			t.Fatal(err)
		}
		var decoded mcpCredEntry
		if err = json.Unmarshal(raw, &decoded); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(grants, decoded.AgentGrants) {
			t.Fatalf("grant semantics changed: %s", raw)
		}
	}
}
