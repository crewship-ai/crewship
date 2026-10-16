package main

import "testing"

func TestGuestIssueSearchOpenAPIContract(t *testing.T) {
	const collection = "/api/v1/workspaces/{workspaceId}/restricted-issues"
	const detail = collection + "/{issueId}"
	doc := buildDocument([]route{{method: "GET", path: collection}, {method: "GET", path: detail}})
	paths := doc["paths"].(map[string]any)
	for _, path := range []string{collection, detail} {
		op := paths[path].(map[string]any)["get"].(map[string]any)
		params := map[string]map[string]any{}
		for _, p := range op["parameters"].([]map[string]any) {
			if p["in"] == "query" {
				params[p["name"].(string)] = p
			}
		}
		if path == detail {
			if params["q"] != nil {
				t.Fatal("detail incorrectly offers the collection search")
			}
			continue
		}
		for _, name := range []string{"q", "project_id", "limit", "offset"} {
			if params[name] == nil || params[name]["required"] != false {
				t.Fatalf("missing optional collection parameter %s", name)
			}
		}
		search := params["q"]["schema"].(map[string]any)
		if search["type"] != "string" || search["maxLength"] != 256 {
			t.Fatalf("unbounded search schema: %#v", search)
		}
		limit := params["limit"]["schema"].(map[string]any)
		if limit["minimum"] != 1 || limit["maximum"] != 500 || limit["default"] != 100 {
			t.Fatalf("wrong list pagination: %#v", limit)
		}
		offset := params["offset"]["schema"].(map[string]any)
		if offset["minimum"] != 0 || offset["maximum"] != 1000000 {
			t.Fatalf("wrong offset bounds: %#v", offset)
		}
	}
}
