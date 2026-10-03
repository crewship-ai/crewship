package devcontainer

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/moby/moby/client"
)

func TestResolverAuditFailureRemovesOnlyCreatedBuilder(t *testing.T) {
	for _, cleanupFails := range []bool{false, true} {
		t.Run(map[bool]string{false: "cleanup succeeds", true: "cleanup fails"}[cleanupFails], func(t *testing.T) {
			image := "sha256:" + strings.Repeat("a", 64)
			removed := false
			created := false
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch {
				case r.Method == "GET" && strings.Contains(r.URL.Path, "/images/"):
					_ = json.NewEncoder(w).Encode(map[string]any{"Id": image, "Os": "linux", "Architecture": "amd64", "Config": map[string]any{}})
				case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/containers/create"):
					created = true
					_ = json.NewEncoder(w).Encode(map[string]any{"Id": "owned-builder"})
				case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/containers/owned-builder/json"):
					// The daemon returned weaker isolation than requested: never start it.
					_ = json.NewEncoder(w).Encode(map[string]any{"Image": image, "Config": map[string]any{"User": "0"}, "HostConfig": map[string]any{"Privileged": true}})
				case r.Method == "DELETE" && strings.HasSuffix(r.URL.Path, "/containers/owned-builder"):
					removed = true
					if cleanupFails {
						w.WriteHeader(500)
						_, _ = w.Write([]byte(`{"message":"fixture removal failed"}`))
					} else {
						w.WriteHeader(204)
					}
				default:
					t.Errorf("unexpected daemon operation %s %s", r.Method, r.URL.Path)
					w.WriteHeader(500)
				}
			}))
			defer server.Close()
			docker, err := client.New(client.WithHost(server.URL), client.WithAPIVersion("1.43"))
			if err != nil {
				t.Fatal(err)
			}
			defer docker.Close()
			result, err := ResolveMiseLock(context.Background(), docker, image, &MiseConfig{Tools: map[string]string{"node": "22"}}, false)
			if result != nil || err == nil || !created || !removed {
				t.Fatalf("cleanup lost: result=%+v err=%v created=%v removed=%v", result, err, created, removed)
			}
			if cleanupFails && !strings.Contains(err.Error(), "remove builder owned-builder") {
				t.Fatalf("cleanup failure hidden: %v", err)
			}
		})
	}
}
