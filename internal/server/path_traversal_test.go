package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPathTraversalRejectMiddleware(t *testing.T) {
	t.Parallel()
	// inner records whether the wrapped handler was reached. The
	// middleware MUST short-circuit before forwarding on a traversal
	// hit and pass through otherwise.
	reached := false
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	})
	wrapped := pathTraversalRejectMiddleware(inner)

	cases := []struct {
		name   string
		path   string
		want   int
		passed bool
	}{
		// Reject: literal ".." path segments are the audit's M11
		// concern -- the stdlib mux normalises and 301-redirects.
		{"parent traversal", "/api/v1/admin/../something", http.StatusBadRequest, false},
		{"leading traversal", "/../etc/passwd", http.StatusBadRequest, false},
		{"trailing traversal", "/api/v1/..", http.StatusBadRequest, false},
		{"double traversal", "/api/../v1/../healthz", http.StatusBadRequest, false},

		// Pass: benign paths must not trip the filter, including
		// filenames or slugs that *contain* ".." but not as a full
		// segment.
		{"root", "/", http.StatusOK, true},
		{"healthz", "/healthz", http.StatusOK, true},
		{"slug with two dots", "/files/two..dots.txt", http.StatusOK, true},
		{"single dot segment", "/api/v1/./healthz", http.StatusOK, true},
		{"three dots in middle", "/foo/...bar/baz", http.StatusOK, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reached = false
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			rec := httptest.NewRecorder()
			wrapped.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Errorf("path=%q got code=%d, want %d", tc.path, rec.Code, tc.want)
			}
			if reached != tc.passed {
				t.Errorf("path=%q reached=%v, want passed=%v", tc.path, reached, tc.passed)
			}
		})
	}
}
