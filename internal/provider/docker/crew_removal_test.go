package docker

import (
	"context"
	"net/http"
	"strings"
	"testing"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/client"
)

func TestCrewRemovalPreservesPolicy(t *testing.T) {
	for _, tc := range []struct {
		name                     string
		force, volumes, conflict bool
	}{
		{name: "explicit restart", force: true},
		{name: "inactive reconcile", volumes: true},
		{name: "retention"},
		{name: "running conflict", conflict: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			p, close := newFakeDockerProvider(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != http.MethodDelete || !strings.HasSuffix(r.URL.Path, "/containers/exact-owned-id") {
					t.Errorf("unexpected removal %s %s", r.Method, r.URL.Path)
				}
				truth := func(key string) bool { v := r.URL.Query().Get(key); return v == "1" || v == "true" }
				if truth("force") != tc.force || truth("v") != tc.volumes {
					t.Errorf("policy changed: %s", r.URL.RawQuery)
				}
				if tc.conflict {
					w.WriteHeader(http.StatusConflict)
					w.Write([]byte(`{"message":"container started"}`))
				} else {
					w.WriteHeader(http.StatusNoContent)
				}
			})
			defer close()
			err := p.removeCrewContainer(context.Background(), "exact-owned-id", client.ContainerRemoveOptions{Force: tc.force, RemoveVolumes: tc.volumes})
			if tc.conflict {
				if !cerrdefs.IsConflict(err) {
					t.Fatalf("lost Docker conflict: %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if calls != 1 {
				t.Fatalf("calls=%d, want one removal", calls)
			}
		})
	}
}
