package main

import "testing"

func TestInventoryEndpointEvidenceParsesHeadingsCodeAndTables(t *testing.T) {
	docs := []docFile{{Path: "docs/api-reference/widgets.mdx", Text: `
| Method | Endpoint | Purpose |
| POST | [/api/v1/widgets](#create) | Create a widget |

### GET /api/v1/widgets/{id}
**Auth:** bearer token
**Request:** path parameter id
**Response:** widget
| Status | Condition |
| 200 | Found |

### Create
POST /api/v1/widgets
**Auth:** bearer token
**Request body:** JSON
**Response:** widget
| Status | Condition |
| 201 | Created |
`}}

	evidence := inventoryEndpointEvidence(docs)
	if len(evidence["POST /api/v1/widgets"]) != 2 {
		t.Fatalf("POST evidence = %#v, want table and code/section evidence", evidence["POST /api/v1/widgets"])
	}
	if len(evidence["GET /api/v1/widgets/{id}"]) != 1 {
		t.Fatalf("GET evidence = %#v", evidence["GET /api/v1/widgets/{id}"])
	}

	checks := contractFor("GET", "/api/v1/widgets/{id}", evidence, "internal/api/router_widgets.go", []string{"internal/api/widgets_test.go"})
	if len(checks.Structural.Missing) != 0 {
		t.Fatalf("complete contract missing = %v", checks.Structural.Missing)
	}
	if !checks.SemanticRuntime.OpenAPIOperation || len(checks.SemanticRuntime.TestSignals) != 1 {
		t.Fatalf("semantic/runtime checks = %#v", checks.SemanticRuntime)
	}
}

func TestContractForFlagsMissingStructuralFields(t *testing.T) {
	evidence := inventoryEndpointEvidence([]docFile{{Path: "docs/api-reference/widgets.mdx", Text: "### GET /api/v1/widgets\nA widget listing."}})
	checks := contractFor("GET", "/api/v1/widgets", evidence, "", nil)
	want := []string{"auth", "request", "response", "statuses"}
	if len(checks.Structural.Missing) != len(want) {
		t.Fatalf("missing = %v, want %v", checks.Structural.Missing, want)
	}
	for i := range want {
		if checks.Structural.Missing[i] != want[i] {
			t.Fatalf("missing[%d] = %q, want %q", i, checks.Structural.Missing[i], want[i])
		}
	}
	if len(checks.SemanticRuntime.TestSignals) != 0 || checks.SemanticRuntime.OpenAPIOperation != true {
		t.Fatalf("semantic/runtime checks should remain separate: %#v", checks.SemanticRuntime)
	}
}

func TestCanonicalDocPathRemovesQueryAndPunctuation(t *testing.T) {
	if got := canonicalDocPath("/api/v1/widgets/{id}?expand=owner)."); got != "/api/v1/widgets/{id}" {
		t.Fatalf("canonical path = %q", got)
	}
}
