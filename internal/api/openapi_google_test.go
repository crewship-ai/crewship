package api

import (
	"encoding/json"
	"strings"
	"testing"
)

// GET /openapi.json is unauthenticated and is what client generators and
// schemathesis read. cmd/gen-openapi builds it by regex-scanning
// router_*.go and does NOT strip comments, so a registration that was
// "removed" by commenting it out still ships in the spec as a live route.
//
// That is how the Google redirect and callback survived being switched off:
// unreachable in the router, still advertised in the published contract.
// The redirect then came back a second time from a comment that quoted the
// call shape while explaining the very problem.
//
// This pins the published surface rather than the source, because the spec
// is the artifact consumers trust.
func TestOpenAPISpec_DoesNotAdvertiseTheGoogleFlow(t *testing.T) {
	var doc struct {
		Paths map[string]map[string]any `json:"paths"`
	}
	if err := json.Unmarshal(openAPISpec, &doc); err != nil {
		t.Fatalf("embedded openapi.gen.json is not valid JSON: %v", err)
	}
	if len(doc.Paths) == 0 {
		t.Fatal("embedded spec has no paths — the generator or the embed is broken")
	}

	// The status route is deliberately kept: it answers a flat false so an
	// older frontend build gets a definite "off" rather than a hanging or
	// erroring probe. Everything else Google must be absent.
	const allowed = "/api/v1/auth/google/status"

	for path := range doc.Paths {
		if !strings.Contains(path, "google") || path == allowed {
			continue
		}
		t.Errorf("spec advertises %q, but the Google flow is switched off and the route is not registered.\n"+
			"A caller generating a client from this spec gets a 404. Check router_auth.go for a "+
			"commented-out registration — gen-openapi scans source text and cannot tell one from live code.", path)
	}
}

// The companion property: the route that IS kept must stay in the spec, so
// this test can't be satisfied by deleting the Google surface wholesale.
func TestOpenAPISpec_KeepsTheGoogleStatusProbe(t *testing.T) {
	var doc struct {
		Paths map[string]map[string]any `json:"paths"`
	}
	if err := json.Unmarshal(openAPISpec, &doc); err != nil {
		t.Fatalf("embedded openapi.gen.json is not valid JSON: %v", err)
	}
	if _, ok := doc.Paths["/api/v1/auth/google/status"]; !ok {
		t.Error("the status probe is registered in router_auth.go but missing from the spec")
	}
}
