package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/quiesce"
)

func TestQuiesceGate(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) })
	gate := quiesceGate(ok)
	cases := []struct {
		name     string
		window   bool
		holds    []string
		method   string
		path     string
		wantCode int
	}{
		{name: "no window: writes pass", method: "POST", path: "/api/v1/missions", wantCode: http.StatusTeapot},
		{name: "window: reads pass", window: true, method: "GET", path: "/api/v1/missions", wantCode: http.StatusTeapot},
		{name: "window: writes held", window: true, method: "PATCH", path: "/api/v1/missions/m1", wantCode: http.StatusServiceUnavailable},
		{name: "window: uploads held", window: true, method: "POST", path: "/api/v1/workspaces/w/attachments", wantCode: http.StatusServiceUnavailable},
		{name: "window: sidecar writes held", window: true, method: "POST", path: "/api/v1/internal/journal/emit", wantCode: http.StatusServiceUnavailable},
		{name: "window: backup endpoints open", window: true, method: "POST", path: "/api/v1/admin/instance/backups/bundles/pin", wantCode: http.StatusTeapot},
		{name: "window: resume open", window: true, method: "POST", path: "/api/v1/admin/instance/holds/resume", wantCode: http.StatusTeapot},
		{name: "window: sign-in open", window: true, method: "POST", path: "/api/auth/callback/credentials", wantCode: http.StatusTeapot},
		{name: "window: traversal past the allowlist is held", window: true, method: "POST", path: "/api/v1/admin/instance/backups/../../../credentials", wantCode: http.StatusServiceUnavailable},
		{name: "window: webhooks held", window: true, method: "POST", path: "/api/v1/webhooks/tok", wantCode: http.StatusServiceUnavailable},
		{name: "webhooks hold: webhook refused", holds: []string{quiesce.HoldWebhooks}, method: "POST", path: "/api/v1/page-webhooks/tok", wantCode: http.StatusServiceUnavailable},
		{name: "webhooks hold: other writes pass", holds: []string{quiesce.HoldWebhooks}, method: "POST", path: "/api/v1/missions", wantCode: http.StatusTeapot},
		{name: "routines hold: webhooks pass", holds: []string{quiesce.HoldRoutines}, method: "POST", path: "/api/v1/webhooks/tok", wantCode: http.StatusTeapot},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var hs []quiesce.Hold
			for _, k := range tc.holds {
				hs = append(hs, quiesce.Hold{Key: k})
			}
			quiesce.DefaultHolds().Replace(hs)
			defer quiesce.DefaultHolds().Replace(nil)
			if tc.window {
				w, err := quiesce.Default().Begin(context.Background(), quiesce.Options{HoldCap: time.Minute})
				if err != nil {
					t.Fatal(err)
				}
				defer w.Release()
			}
			rr := httptest.NewRecorder()
			gate.ServeHTTP(rr, httptest.NewRequest(tc.method, tc.path, nil))
			if rr.Code != tc.wantCode {
				t.Fatalf("code = %d, want %d (%s)", rr.Code, tc.wantCode, rr.Body.String())
			}
			if rr.Code == http.StatusServiceUnavailable && rr.Header().Get("Retry-After") == "" {
				t.Fatal("503 without Retry-After")
			}
		})
	}
}
