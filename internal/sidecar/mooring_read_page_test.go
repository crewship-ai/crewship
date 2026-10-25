package sidecar

import (
	"encoding/json"
	"net/url"
	"strings"
	"testing"
)

func TestMooringReadPageRetainsOpaqueCursorAndWideRevisionWithoutSelectors(t *testing.T) {
	cursor := `["snapshot&?=1","stable/Č😀"]`
	raw, _ := json.Marshal(map[string]any{"page": map[string]any{"cursor": cursor, "revision": "18446744073709551615", "max_bytes": 4096}})
	path, paged, err := mooringReadToolPath(raw)
	if err != nil || !paged {
		t.Fatalf("page refused: %v", err)
	}
	u, err := url.Parse(path)
	if err != nil || u.Path != "/api/v1/internal/mooring/task" || u.Query().Get("cursor") != cursor || u.Query().Get("revision") != "18446744073709551615" || u.Query().Get("page") != "1" || u.Query().Get("max_bytes") != "4096" {
		t.Fatalf("fixed path or lossless continuation changed: %s", path)
	}
	path, paged, err = mooringReadToolPath(json.RawMessage(`{}`))
	if err != nil || paged || strings.Contains(path, "?") {
		t.Fatal("original whole-D1 call changed")
	}
	for _, raw := range []string{
		`null`, `[]`, `{"page":null}`, `{"page":[]}`, `{"page":{},"page_id":"other"}`,
		`{"page":{"workspace_id":"other"}}`, `{"page":{"cursor":"opaque"}}`,
		`{"page":{"revision":1}}`, `{"page":{"revision":null}}`, `{"page":{"revision":"01"}}`,
		`{"page":{"revision":"18446744073709551616"}}`, `{"page":{"max_bytes":null}}`,
		`{"page":{"max_bytes":1023}}`, `{"page":{"max_bytes":262145}}`, `{"page":{"max_bytes":4096.0}}`,
		`{"page":{"cursor":""}}`, `{"page":{"revision":"0","revision":"1"}}`,
	} {
		if _, _, err := mooringReadToolPath(json.RawMessage(raw)); err == nil {
			t.Errorf("invalid/selector request accepted: %s", raw)
		}
	}
}

func TestMooringReadPageBudgetIncludesMCPJSONEscapingAndID(t *testing.T) {
	req := memoryMCPRequest{ID: json.RawMessage(`"bounded-id"`)}
	if !boundedMooringPageResult(req, 200, []byte(`{"native_json":"{\"opaque\":18446744073709551615}"}`)) {
		t.Fatal("bounded wide native JSON refused")
	}
	// A raw body below 2 MiB can exceed the envelope after JSON escaping.
	if boundedMooringPageResult(req, 200, []byte(strings.Repeat("\x00", 400_000))) {
		t.Fatal("escaped MCP envelope escaped its own bound")
	}
}
