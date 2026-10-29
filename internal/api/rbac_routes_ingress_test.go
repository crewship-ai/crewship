package api

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAuthedMutIngressFencePrecedesAuthentication(t *testing.T) {
	for _, mode := range []string{"ordinary", "open-fence", "suspended-fence"} {
		t.Run(mode, func(t *testing.T) {
			r := &Router{mux: newRouteMux(), authMw: NewAuthMiddleware(nil, nil, nil, slog.Default())}
			called := false
			handler := func(http.ResponseWriter, *http.Request) { called = true }
			const path = "/api/v1/pages/{slug}/mooring/session"
			if mode == "ordinary" {
				r.authedMut("POST", path, roleSelf, handler)
			} else {
				fence, err := newCollaborationIngressFence()
				if err != nil {
					t.Fatal(err)
				}
				if mode == "suspended-fence" {
					fence.SuspendBeforeRestore()
				}
				r.authedMut("POST", path, roleSelf, handler, fence.Wrap)
			}
			response := httptest.NewRecorder()
			r.mux.ServeHTTP(response, httptest.NewRequest("POST", "/api/v1/pages/original/mooring/session", nil))
			want := http.StatusUnauthorized
			if mode == "suspended-fence" {
				// If the fence moved after authentication, this would be 401:
				// the original request must encounter the restore fence first.
				want = http.StatusServiceUnavailable
			}
			if response.Code != want || called {
				t.Fatalf("status=%d want=%d handler_called=%v", response.Code, want, called)
			}
			if len(r.mutationRoutes) != 1 || r.mutationRoutes[0].Role != roleSelf || r.mutationRoutes[0].Scope != scopeSelf {
				t.Fatal("ingress middleware changed the declared route authority")
			}
		})
	}
}
