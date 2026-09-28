package sidecar

import (
	"encoding/json"
	"testing"
)

func TestCredentialGrantWireRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		name    string
		grants  map[string]string
		allowed bool
	}{
		{"legacy", nil, true}, {"deny all", map[string]string{}, false}, {"granted", map[string]string{"a": ""}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := json.Marshal(Credential{ID: "key", Provider: ProviderAnthropic, Token: "synthetic", AgentGrants: tc.grants})
			if err != nil {
				t.Fatal(err)
			}
			var c Credential
			if err = json.Unmarshal(raw, &c); err != nil {
				t.Fatal(err)
			}
			store := NewCredStore()
			store.Load([]Credential{c})
			if got := store.Select(ProviderAnthropic, "a") != nil; got != tc.allowed {
				t.Fatalf("wire changed permission: got=%v want=%v", got, tc.allowed)
			}
		})
	}
}
