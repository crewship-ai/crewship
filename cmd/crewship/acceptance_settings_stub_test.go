package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// settingsStub is the shared recording stub for the Settings acceptance tests
// (members, crew links, journal verify): the web UI's Settings pages are built
// on these endpoints, and the CLI is the contract agents use for the same
// calls. It answers from a route table and records every request, so a test
// asserts what the BUILT BINARY sent (see acceptance_auth_profile_test.go for
// why the binary and not RunE in-process). Config and runner are
// profileStubConfig / runProfileCLI.
type settingsStub struct {
	mu     sync.Mutex
	routes map[string]func(body map[string]any) (int, string)
	calls  []settingsCall
}

type settingsCall struct {
	Method, Path, Query string
	Body                map[string]any
}

func (s *settingsStub) start(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		s.mu.Lock()
		s.calls = append(s.calls, settingsCall{r.Method, r.URL.Path, r.URL.RawQuery, body})
		h := s.routes[r.Method+" "+r.URL.Path]
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if h == nil {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"no stub for ` + r.Method + " " + r.URL.Path + `"}`))
			return
		}
		code, out := h(body)
		w.WriteHeader(code)
		_, _ = w.Write([]byte(out))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// called returns the recorded requests for a method and path.
func (s *settingsStub) called(method, path string) []settingsCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []settingsCall
	for _, c := range s.calls {
		if c.Method == method && c.Path == path {
			out = append(out, c)
		}
	}
	return out
}
