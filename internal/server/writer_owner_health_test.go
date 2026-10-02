package server

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHealthAndReadinessReportLostWriterOwnership(t *testing.T) {
	s := newTestServer()
	s.writerOwnerCheck = func() error { return errors.New("private database path must not leak") }
	for _, path := range []string{"/healthz", "/readyz"} {
		recorder := httptest.NewRecorder()
		s.mux.ServeHTTP(recorder, httptest.NewRequest("GET", path, nil))
		if recorder.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s = %d", path, recorder.Code)
		}
		if !strings.Contains(recorder.Body.String(), `"writer_ownership":"lost"`) || strings.Contains(recorder.Body.String(), "private database") {
			t.Fatalf("unsafe health body: %s", recorder.Body.String())
		}
	}
	s.writerOwnerCheck = func() error { return nil }
	recorder := httptest.NewRecorder()
	s.mux.ServeHTTP(recorder, httptest.NewRequest("GET", "/healthz", nil))
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"writer_ownership":"held"`) {
		t.Fatal(recorder.Body.String())
	}
}
