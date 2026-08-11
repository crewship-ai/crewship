package main

import "testing"

// requiredQueryParams infers "required" from a very specific shape: the
// handler reads the parameter, then a top-level guard returns 4xx when it is
// EMPTY. That is a stronger statement than OpenAPI's `required`, which only
// means the parameter must be present — an empty string satisfies it.
//
// The gap is not theoretical. It is 10 of the 41 remaining findings behind
// #1815, all the same shape:
//
//	GET /api/v1/admin/backups/inspect?path=
//	[400] {"error":"path query param required"}
//
// Schemathesis sends the empty string because the document says it may, and
// the server rejects it because the handler says it may not. Eight of those
// ten only became visible once #1850 started marking parameters required at
// all — the document got more accurate and uncovered the next layer.
//
// So a parameter this inference marks required also carries minLength: 1. The
// inference already proves it; nothing new is detected here.

func TestRequiredQueryParam_AlsoDeclaresNonEmpty(t *testing.T) {
	doc := loadSpec(t)
	paths, _ := doc["paths"].(map[string]any)

	item, ok := paths["/api/v1/crews/{crewId}/files/delete"].(map[string]any)
	if !ok {
		t.Fatal("the file-delete route is not in the document")
	}
	op, _ := item["delete"].(map[string]any)
	params, _ := op["parameters"].([]any)

	var found bool
	for _, raw := range params {
		p, _ := raw.(map[string]any)
		if p["name"] != "path" || p["in"] != "query" {
			continue
		}
		found = true
		if p["required"] != true {
			t.Fatalf("`path` is not marked required; ProxyHandler.CrewFileDelete 400s without it")
		}
		schema, _ := p["schema"].(map[string]any)
		if schema["minLength"] == nil {
			t.Errorf("`path` is required but the schema permits the empty string. The handler "+
				"rejects it: replyError(w, 400, \"path parameter required\"). Got schema %v", schema)
		}
	}
	if !found {
		t.Fatal("no `path` query parameter on the file-delete route")
	}
}

// TestOptionalQueryParam_StaysUnconstrained is the other half. minLength must
// follow the same proof `required` follows — a parameter the handler does not
// reject when empty keeps accepting the empty string, or this becomes a
// blanket rule dressed as an inference.
func TestOptionalQueryParam_StaysUnconstrained(t *testing.T) {
	doc := loadSpec(t)
	paths, _ := doc["paths"].(map[string]any)

	checked := 0
	for _, item := range paths {
		methods, _ := item.(map[string]any)
		for _, raw := range methods {
			op, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			params, _ := op["parameters"].([]any)
			for _, praw := range params {
				p, _ := praw.(map[string]any)
				if p["in"] != "query" || p["required"] == true {
					continue
				}
				schema, _ := p["schema"].(map[string]any)
				if schema["minLength"] != nil {
					t.Errorf("optional query parameter %v carries minLength; only a parameter the "+
						"handler rejects when empty may", p["name"])
				}
				checked++
			}
		}
	}
	if checked == 0 {
		t.Fatal("no optional query parameters found — the assertion proved nothing")
	}
	t.Logf("checked %d optional query parameters", checked)
}
