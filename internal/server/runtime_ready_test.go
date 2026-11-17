package server

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"
)

type readyContainer struct {
	mockContainer
	err error
}

func (m readyContainer) RuntimeReady(context.Context) error { return m.err }
func TestRuntimeReadinessPreservesLiveness(t *testing.T) {
	for _, tc := range []struct {
		name                      string
		runtimeError, errorAtInit error
		noDocker                  bool
		want                      int
	}{
		{name: "ready runtime", want: 200}, {name: "mandatory bind failed", runtimeError: errors.New("bind unavailable"), want: 503},
		{name: "explicit Docker initialization failed", errorAtInit: errors.New("proxy refused"), noDocker: true, want: 503},
		{name: "intentional no docker", noDocker: true, want: 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &Server{runtimeInitError: tc.errorAtInit}
			if !tc.noDocker {
				s.container = &readyContainer{err: tc.runtimeError}
			}
			ready := httptest.NewRecorder()
			s.handleReadyz(ready, httptest.NewRequest("GET", "/readyz", nil))
			if ready.Code != tc.want {
				t.Fatalf("readyz=%d: %s", ready.Code, ready.Body.String())
			}
			live := httptest.NewRecorder()
			s.handleHealthz(live, httptest.NewRequest("GET", "/healthz", nil))
			if live.Code != 200 {
				t.Fatalf("liveness=%d", live.Code)
			}
		})
	}
}
