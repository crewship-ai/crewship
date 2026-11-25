package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
)

type readyContainer struct {
	mockContainer
	err error
}

func (m readyContainer) RuntimeReady(context.Context) error { return m.err }

func TestReadyzReportsRuntimeWithoutBreakingLiveness(t *testing.T) {
	for _, tc := range []struct {
		name        string
		container   func() *Server
		wantStatus  int
		wantRuntime string // "" = the key must be absent
	}{
		{"runtime ready", func() *Server { return &Server{container: &readyContainer{}} }, 200, "ok"},
		{"daemon cannot see the runtime files", func() *Server {
			return &Server{container: &readyContainer{err: errors.New("/secret/path must not appear")}}
		}, 503, "unavailable"},
		{"provider without a readiness opinion", func() *Server { return &Server{container: &mockContainer{}} }, 200, ""},
		{"no container provider (--no-docker)", func() *Server { return &Server{} }, 200, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := tc.container()
			ready := httptest.NewRecorder()
			s.handleReadyz(ready, httptest.NewRequest("GET", "/readyz", nil))
			if ready.Code != tc.wantStatus {
				t.Fatalf("readyz=%d: %s", ready.Code, ready.Body.String())
			}
			if strings.Contains(ready.Body.String(), "/secret/path") {
				t.Fatalf("readyz leaks the reason: %s", ready.Body.String())
			}
			var body struct {
				Checks map[string]string `json:"checks"`
			}
			if err := json.Unmarshal(ready.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if got := body.Checks["runtime"]; got != tc.wantRuntime {
				t.Fatalf("checks.runtime=%q, want %q", got, tc.wantRuntime)
			}
			live := httptest.NewRecorder()
			s.handleHealthz(live, httptest.NewRequest("GET", "/healthz", nil))
			if live.Code != 200 {
				t.Fatalf("an unready runtime failed liveness: %d", live.Code)
			}
		})
	}
}
