package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCommandProductDiscoveryRequiresLivePinnedRenderer(t *testing.T) {
	for _, scenario := range []string{"missing", "off", "closed", "missing-bundle", "live"} {
		t.Run(scenario, func(t *testing.T) {
			r := &Router{pages: &PageHandler{}, mooringHTTP: &collaborationHTTPManager{}}
			if scenario != "missing" {
				f := &commandProductFactory{enabled: scenario != "off", closed: scenario == "closed", editorBundle: func(*http.Request) (string, string) {
					return strings.Repeat("a", 64), "https://editor.example.test/index.html"
				}}
				if scenario == "missing-bundle" {
					f.editorBundle = func(*http.Request) (string, string) { return "", "" }
				}
				r.mooringHTTP.commandFactory = f
			}
			status := r.documentAvailability(httptest.NewRequest("GET", "/api/v1/documents/status", nil))
			if status.WordCraftCommands != (scenario == "live") {
				t.Fatal("native command capability advertised without pinned live product", scenario, status)
			}
			if status.Enabled || status.WordCraftSDKPrefix != "" {
				t.Fatal("command capability enabled unrelated editors")
			}
		})
	}
}

func TestCommandProductDisabledHandlerRetainsData(t *testing.T) {
	m := &collaborationHTTPManager{}
	w := httptest.NewRecorder()
	m.commands(w, httptest.NewRequest("POST", "/", nil))
	if w.Code != 503 {
		t.Fatal("disabled command handler enabled editing", w.Code)
	}
}
