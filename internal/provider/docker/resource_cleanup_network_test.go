package docker

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/moby/moby/client"

	"github.com/crewship-ai/crewship/internal/resourcelifecycle"
)

// Docker answers 403 for a network with active endpoints, and so does a
// socket proxy without NetworkRemove in its allowlist. Only the first is
// "in use" (pending); the second must surface (review of #2767).
func TestCleanupRuntimeRemoveNetworkClassifiesRefusals(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		want   error
	}{
		{"active endpoints", 403, `{"message":"error while removing network: network n id 1 has active endpoints"}`, resourcelifecycle.ErrNetworkInUse},
		{"conflict", 409, `{"message":"conflict"}`, resourcelifecycle.ErrNetworkInUse},
		{"socket proxy forbids", 403, `<html>403 Forbidden</html>`, resourcelifecycle.ErrNetworkForbidden},
		{"gone", 404, `{"message":"network n not found"}`, resourcelifecycle.ErrNotFound},
		{"removed", 204, ``, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodDelete || !strings.Contains(r.URL.Path, "/networks/") {
					http.Error(w, "unexpected", 500)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			cli, err := client.New(client.WithHost("tcp://"+strings.TrimPrefix(srv.URL, "http://")), client.WithAPIVersion("1.47"))
			if err != nil {
				t.Fatal(err)
			}
			defer cli.Close()
			err = (&cleanupRuntime{client: cli}).RemoveNetwork(context.Background(), "n")
			if tc.want == nil {
				if err != nil {
					t.Fatalf("got %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
		})
	}
}
