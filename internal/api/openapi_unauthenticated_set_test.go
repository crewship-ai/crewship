package api

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"
)

type unauthenticatedOperation struct {
	method string
	path   string
	reason string
}

var expectedUnauthenticatedOperations = []unauthenticatedOperation{
	{"GET", "/api/health", "liveness probe must work before login"},
	{"POST", "/api/v1/auth/forgot", "starts account recovery before login"},
	{"GET", "/api/v1/auth/google/status", "reports whether pre-login Google authentication is available"},
	{"POST", "/api/v1/auth/pair/redeem", "exchanges a one-time pairing code for authentication"},
	{"POST", "/api/v1/auth/reset", "completes account recovery with a reset token"},
	{"POST", "/api/v1/auth/signup", "creates the first authenticated session during signup"},
	{"POST", "/api/v1/bootstrap", "initializes an instance before any account can authenticate"},
	{"GET", "/api/v1/oauth/callback", "authenticates the provider callback with OAuth state instead of a session"},
	{"POST", "/api/v1/page-webhooks/{token}", "writes ONE panel on behalf of a producer that cannot run the CLI — a cron on someone else's box, a Zapier step, a PLC gateway — authenticated by the 256-bit token in the path, whose authority is its issuer's and is re-derived on every request (docs/prd/pages.md §10b.5c)"},
	{"GET", "/api/v1/public/pages/{token}", "serves a published page to a reader with no account, authenticated by the 256-bit token in the path (docs/prd/pages.md §7.3.1)"},
	{"POST", "/api/v1/public/pages/{token}/unlock", "verifies a public page's optional password, which §7.3.3 keeps out of the URL and therefore out of the GET"},
	{"GET", "/api/v1/system/setup-status", "lets the pre-login UI determine whether setup is required"},
	{"GET", "/api/v1/system/telemetry", "lets the pre-login UI display telemetry consent state"},
	{"POST", "/api/v1/waitpoint-tokens/{token}", "authenticates a waitpoint action with the credential in the path"},
	{"POST", "/api/v1/webhooks/{crewId}/{agentId}/trigger", "authenticates webhook delivery with an HMAC signature"},
	{"POST", "/api/v1/webhooks/{token}", "authenticates webhook delivery with the credential in the path"},
}

func TestOpenAPIUnauthenticatedOperationsAreExhaustive(t *testing.T) {
	raw, err := os.ReadFile("openapi.gen.json")
	if err != nil {
		t.Fatalf("read openapi.gen.json: %v", err)
	}
	var document struct {
		Paths map[string]map[string]struct {
			Security json.RawMessage `json:"security"`
		} `json:"paths"`
	}
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatalf("parse openapi.gen.json: %v", err)
	}

	want := make(map[string]string, len(expectedUnauthenticatedOperations))
	for _, operation := range expectedUnauthenticatedOperations {
		key := operation.method + " " + operation.path
		if strings.TrimSpace(operation.reason) == "" {
			t.Errorf("%s has no written reason", key)
		}
		if _, duplicate := want[key]; duplicate {
			t.Errorf("duplicate expected unauthenticated operation %s", key)
		}
		want[key] = operation.reason
	}

	got := make(map[string]struct{})
	for path, pathItem := range document.Paths {
		for method, operation := range pathItem {
			if string(operation.Security) == "[]" {
				got[strings.ToUpper(method)+" "+path] = struct{}{}
			}
		}
	}

	var differences []string
	for key := range want {
		if _, found := got[key]; !found {
			differences = append(differences, fmt.Sprintf("missing from generated spec: %s", key))
		}
	}
	for key := range got {
		if _, allowed := want[key]; !allowed {
			differences = append(differences, fmt.Sprintf("missing allow-list entry for generated unauthenticated operation: %s", key))
		}
	}
	sort.Strings(differences)
	if len(differences) > 0 {
		t.Fatalf("OpenAPI security: [] operation set differs:\n%s", strings.Join(differences, "\n"))
	}
}
