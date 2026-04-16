package api

import (
	"context"
	"testing"
)

// TestAllowAllPolicy_AlwaysAllows is a regression guard for the MVP policy.
// A future policy file will add its own tests; this test exists so if someone
// ever accidentally swaps the default wiring to something that doesn't
// trivially admit, CI fails loudly.
func TestAllowAllPolicy_AlwaysAllows(t *testing.T) {
	p := AllowAllPolicy{}
	cases := []PortExposeRequest{
		{Port: 80, AgentSlug: "viktor"},
		{Port: 22, AgentSlug: "root"}, // privileged port in the container: still allowed — isolation is container-level
		{Port: 65535, Description: "edge"},
		{Port: 3000, TTLSeconds: 86400},
	}
	for _, c := range cases {
		got, reason, err := p.Check(context.Background(), &c)
		if err != nil {
			t.Errorf("err = %v for %+v", err, c)
		}
		if got != ExposeAllow {
			t.Errorf("decision = %q for %+v, want %q", got, c, ExposeAllow)
		}
		if reason == "" {
			t.Errorf("reason empty for %+v", c)
		}
	}
}
