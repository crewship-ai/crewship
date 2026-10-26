package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDocumentAvailabilityKeepsLegacyAndWordCraftIndependent(t *testing.T) {
	prefix := "/mooring-sdk/generated/" + strings.Repeat("a", 64)
	for _, mode := range []string{"disabled", "startup-failed", "no-editor", "empty-editor", "closed", "native"} {
		t.Run(mode, func(t *testing.T) {
			r := &Router{pages: &PageHandler{}}
			if mode != "startup-failed" {
				m := &collaborationHTTPManager{config: MooringHTTPConfig{Enabled: mode != "disabled", SDKPrefix: prefix, EditorBundle: func(*http.Request) (string, string) {
					return strings.Repeat("b", 64), "https://editor.example/index.html"
				}}}
				if mode == "no-editor" {
					m.config.EditorBundle = nil
				}
				if mode == "empty-editor" {
					m.config.EditorBundle = func(*http.Request) (string, string) { return "", "" }
				}
				m.closed = mode == "closed"
				r.mooringHTTP = m
			}
			status := r.documentAvailability(httptest.NewRequest("GET", "/api/v1/documents/status", nil))
			if status.Enabled {
				t.Fatal("native capability enabled the unrelated legacy engine")
			}
			want := ""
			if mode == "native" {
				want = prefix
			}
			if status.WordCraftSDKPrefix != want {
				t.Fatalf("prefix=%q want%q", status.WordCraftSDKPrefix, want)
			}
			raw, err := json.Marshal(status)
			if err != nil {
				t.Fatal(err)
			}
			if want == "" && strings.Contains(string(raw), "wordcraft_sdk_prefix") {
				t.Fatal("disabled capability was advertised")
			}
		})
	}
}
