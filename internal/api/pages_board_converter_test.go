package api

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBoardConverterHostBoundary(t *testing.T) {
	h := &PageHandler{pageRuntimeOrigin: "https://runtime.example.net", pageStudioOrigin: "https://studio.example.com"}
	for _, host := range []string{"runtime.example.net", "studio.example.com", "untrusted.example.net"} {
		w := httptest.NewRecorder()
		h.BoardConverter(w, httptest.NewRequest("GET", "https://"+host+"/api/v1/pages/runtime/board-converter", nil))
		if host == "runtime.example.net" {
			if w.Code != 200 || !strings.Contains(w.Body.String(), "/board-converter/runner.js") {
				t.Fatal(w.Code, w.Body.String())
			}
		} else if w.Code != 404 {
			t.Fatalf("wrong host %s accepted", host)
		}
	}
	h.pageRuntimeOrigin = h.pageStudioOrigin
	h.pageRuntimeDevelopmentSameOrigin = true
	if h.boardConverterURL() != "" {
		t.Fatal("development same-origin exception enabled unisolated conversion")
	}
}
