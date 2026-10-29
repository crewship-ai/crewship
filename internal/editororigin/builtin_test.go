package editororigin

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCompiledPackagesUseTheQualifiedEditorBoundary(t *testing.T) {
	h, e := New(Options{Origin: "https://editor.example.test", StudioOrigin: "https://studio.other.test"})
	if e != nil {
		t.Fatal(e)
	}
	entries, _ := compiledPackages.ReadDir("packages")
	if len(entries) == 0 {
		t.Fatal("no compiled package")
	}
	for _, entry := range entries {
		req := httptest.NewRequest("GET", "https://editor.example.test/b/"+entry.Name()+"/index.html", nil)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != 200 || !strings.Contains(w.Header().Get("Content-Security-Policy"), "sandbox allow-scripts") || w.Header().Get("Set-Cookie") != "" {
			t.Fatalf("compiled package escaped isolation: %d %v", w.Code, w.Header())
		}
	}
}
