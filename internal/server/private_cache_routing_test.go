package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"

	goapi "github.com/crewship-ai/crewship/internal/api"
)

func TestPrivateCacheWriterCannotReceiveAPIBodies(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		if _, wrapped := w.(*privateCacheWriter); wrapped {
			t.Fatal("API unexpectedly uses UI cache writer")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"text":"<script>alert(1)</script>"}`))
	})
	static := goapi.StaticFileHandler(fstest.MapFS{"index.html": {Data: []byte("<!doctype html><title>Static</title>")}})
	s := &Server{mux: mux, spaHandler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, wrapped := w.(*privateCacheWriter); !wrapped {
			t.Fatal("UI document lost cache boundary")
		}
		static.ServeHTTP(w, r)
	})}
	h := securityHeadersMiddlewareWith(s.combinedHandler(), securityHeaderOptions{editorPerRequest: true})
	for _, p := range []string{"/api/v1/admin/instance-backup-plans", "/api/v1/approvals", "/chat?q=%3Cscript%3Ealert(1)%3C/script%3E"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", p, nil))
		if w.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Fatal("missing nosniff")
		}
		if isMuxRoutedPath(httptest.NewRequest("GET", p, nil).URL.Path) {
			if w.Header().Get("Content-Type") != "application/json" {
				t.Fatal("API MIME changed")
			}
		} else if w.Body.String() != "<!doctype html><title>Static</title>" {
			t.Fatal("request reflected in UI", w.Body.String())
		}
	}
}
