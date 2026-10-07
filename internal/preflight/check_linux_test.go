//go:build linux

package preflight

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCheckUsesLiveConfiguredRuntime(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("API-Version", "1.48")
		if strings.HasSuffix(r.URL.Path, "/_ping") {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"Version":"fixture-runtime","ApiVersion":"1.48","MinAPIVersion":"1.24"}`))
	}))
	defer server.Close()
	t.Setenv("DOCKER_HOST", strings.Replace(server.URL, "http://", "tcp://", 1))
	t.Setenv("DOCKER_TLS_VERIFY", "")
	t.Setenv("DOCKER_CERT_PATH", "")
	t.Setenv("DOCKER_API_VERSION", "")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if got := Check(ctx); got.Status != RuntimeRunning || len(got.Installed) != 0 {
		t.Fatalf("live runtime result=%+v", got)
	}
}

func TestCheckDistinguishesInstalledFromMissing(t *testing.T) {
	for _, installed := range []bool{false, true} {
		t.Run(map[bool]string{false: "missing", true: "installed"}[installed], func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("PATH", dir)
			t.Setenv("DOCKER_HOST", "unix://"+filepath.Join(dir, "absent.sock"))
			t.Setenv("DOCKER_TLS_VERIFY", "")
			t.Setenv("DOCKER_CERT_PATH", "")
			if installed {
				if err := os.WriteFile(filepath.Join(dir, "docker"), []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			got := Check(ctx)
			if installed {
				if got.Status != RuntimeInstalledNotRunning || len(got.Installed) != 1 || got.Installed[0].Name != "Docker Engine" {
					t.Fatalf("installed runtime=%+v", got)
				}
			} else if got.Status != RuntimeMissing || len(got.Installed) != 0 {
				t.Fatalf("missing runtime=%+v", got)
			}
		})
	}
}
