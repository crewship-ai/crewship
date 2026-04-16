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
	cases := []struct {
		name string
		req  PortExposeRequest
	}{
		{"http-as-viktor", PortExposeRequest{Port: 80, AgentSlug: "viktor"}},
		// Privileged port in the container: still allowed — isolation is container-level.
		{"ssh-as-root", PortExposeRequest{Port: 22, AgentSlug: "root"}},
		{"max-port", PortExposeRequest{Port: 65535, Description: "edge"}},
		{"max-ttl", PortExposeRequest{Port: 3000, TTLSeconds: 86400}},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			got, reason, err := p.Check(context.Background(), &c.req)
			if err != nil {
				t.Errorf("err = %v for %+v", err, c.req)
			}
			if got != ExposeAllow {
				t.Errorf("decision = %q for %+v, want %q", got, c.req, ExposeAllow)
			}
			if reason == "" {
				t.Errorf("reason empty for %+v", c.req)
			}
		})
	}
}
