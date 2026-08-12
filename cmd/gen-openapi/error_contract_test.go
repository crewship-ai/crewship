package main

import (
	"encoding/json"
	"os"
	"testing"
)

// The generated document declared, for every non-2xx status on every
// operation, `application/problem+json` carrying RFC 7807 with type/title/
// status/detail all required. No code path in the repository produces that
// (#1919):
//
//	replyError               -> {"error": "…"}  Content-Type: application/json   2125 calls
//	writeProblem             -> RFC 7807 body   Content-Type: application/json    451 calls
//	writeProblemContentType  -> RFC 7807 body   Content-Type: problem+json          1 call
//
// Both mainstream helpers route through writeJSON, which hard-sets
// application/json, so the declared MEDIA TYPE was wrong even for the
// handlers that get the body right. Verified against a live instance: a 401
// from the auth middleware, a 403 from a role gate and a 400 from the
// workspace gate all answer `{"error": …}` with `Content-Type:
// application/json`.
//
// These tests read the committed artifact rather than re-running the
// generator, so they fail if the document drifts from the code that writes it
// as well as if the writer regresses.

func loadSpec(t *testing.T) map[string]any {
	t.Helper()
	raw, err := os.ReadFile("../../internal/api/openapi.gen.json")
	if err != nil {
		t.Fatalf("read openapi.gen.json: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse openapi.gen.json: %v", err)
	}
	return doc
}

// errorContentOf returns the content map an operation declares for one status.
func errorContentOf(t *testing.T, doc map[string]any, path, method, status string) map[string]any {
	t.Helper()
	paths, _ := doc["paths"].(map[string]any)
	item, ok := paths[path].(map[string]any)
	if !ok {
		t.Fatalf("path %s is not in the document", path)
	}
	op, ok := item[method].(map[string]any)
	if !ok {
		t.Fatalf("%s %s is not in the document", method, path)
	}
	resps, _ := op["responses"].(map[string]any)
	resp, ok := resps[status].(map[string]any)
	if !ok {
		t.Fatalf("%s %s declares no %s response", method, path, status)
	}
	content, ok := resp["content"].(map[string]any)
	if !ok {
		t.Fatalf("%s %s %s declares no content", method, path, status)
	}
	return content
}

// TestErrorResponses_UseTheMediaTypeTheServerSends is the unconditional half.
// writeJSON hard-sets application/json and both mainstream helpers route
// through it, so no error response on any operation carries problem+json.
func TestErrorResponses_UseTheMediaTypeTheServerSends(t *testing.T) {
	doc := loadSpec(t)
	paths, _ := doc["paths"].(map[string]any)

	offenders := 0
	var sample string
	for path, item := range paths {
		methods, _ := item.(map[string]any)
		for method, raw := range methods {
			op, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			resps, _ := op["responses"].(map[string]any)
			for status, r := range resps {
				if status[0] == '2' {
					continue
				}
				resp, _ := r.(map[string]any)
				content, _ := resp["content"].(map[string]any)
				if _, bad := content["application/problem+json"]; bad {
					offenders++
					if sample == "" {
						sample = method + " " + path + " " + status
					}
				}
			}
		}
	}
	if offenders > 0 {
		t.Errorf("%d error response(s) still declare application/problem+json; the server sends "+
			"application/json for every one of them (first: %s)", offenders, sample)
	}
}

// TestErrorResponses_DeclareTheShapeTheHandlerWrites covers a handler whose
// error path is replyError. Its 400 must document {"error": string}, not the
// RFC 7807 envelope it never writes.
func TestErrorResponses_DeclareTheShapeTheHandlerWrites(t *testing.T) {
	doc := loadSpec(t)
	content := errorContentOf(t, doc, "/api/v1/crews/{crewId}/files/delete", "delete", "400")

	media, ok := content["application/json"].(map[string]any)
	if !ok {
		t.Fatalf("400 does not declare application/json, got %v", keysOf(content))
	}
	schema, _ := media["schema"].(map[string]any)
	props, _ := schema["properties"].(map[string]any)
	if _, ok := props["error"]; !ok {
		t.Errorf("400 schema has no `error` member; ProxyHandler.CrewFileDelete replies "+
			"replyError(w, 400, \"path parameter required\"). Got properties %v", keysOf(props))
	}
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
