package orchestrator

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestSidecarBootPreservesEmptyGrantDenial(t *testing.T) {
	for _, tc := range []struct {
		name   string
		grants map[string]string
	}{
		{"legacy", nil}, {"deny all", map[string]string{}}, {"granted", map[string]string{"a": ""}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input, err := json.Marshal([]Credential{{ID: "key", Provider: "ANTHROPIC", EnvVarName: "ANTHROPIC_API_KEY", Type: "API_KEY", PlainValue: "synthetic", AgentGrants: tc.grants}})
			if err != nil {
				t.Fatal(err)
			}
			var creds []Credential
			if err = json.Unmarshal(input, &creds); err != nil {
				t.Fatal(err)
			}
			payload, err := json.Marshal(buildSidecarCreds(creds, nil))
			if err != nil {
				t.Fatal(err)
			}
			var decoded []sidecarCred
			if err = json.Unmarshal(payload, &decoded); err != nil {
				t.Fatal(err)
			}
			if len(decoded) != 1 || !reflect.DeepEqual(decoded[0].AgentGrants, tc.grants) {
				t.Fatalf("grant authority changed in JSON round trip: %s", payload)
			}
		})
	}
}
