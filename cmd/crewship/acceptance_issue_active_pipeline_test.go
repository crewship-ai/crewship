package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestAcceptanceGuestActivePipelineConflictCLI(t *testing.T) {
	for _, operation := range []string{"move", "delete"} {
		t.Run(operation, func(t *testing.T) {
			var mutations atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path == "/api/v1/workspaces" {
					_, _ = io.WriteString(w, `[{"id":"ws_test","slug":"ws_test"}]`)
					return
				}
				method := "PATCH"
				if operation == "delete" {
					method = "DELETE"
				}
				if r.Method != method || r.URL.Path != "/api/v1/workspaces/ws_test/restricted-issues/CLIENT-1" {
					t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
					w.WriteHeader(404)
					return
				}
				mutations.Add(1)
				w.WriteHeader(http.StatusConflict)
				_, _ = io.WriteString(w, `{"error":"This issue has active routine work"}`)
			}))
			defer server.Close()
			args := []string{"issue", "shared", "move", "CLIENT-1", "--status", "TODO"}
			if operation == "delete" {
				args = []string{"issue", "shared-delete", "CLIENT-1", "--project", "p1", "--version", strings.Repeat("a", 64), "--confirm"}
			}
			out, err := runGuestCLI(t, server.URL, "", args...)
			if err == nil || !strings.Contains(out, "active routine work") || mutations.Load() != 1 {
				t.Fatalf("active work refusal: err=%v requests=%d output=%s", err, mutations.Load(), out)
			}
		})
	}
}
