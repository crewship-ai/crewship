package server

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/crewship-ai/crewship/internal/config"
)

func TestCredentialSyncWorksWithoutDevelopmentFrontend(t *testing.T) {
	t.Parallel()
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/internal/credentials" || r.URL.Query().Get("include_values") != "true" || r.Header.Get("X-Internal-Token") != "test-internal-token" {
			t.Errorf("unexpected internal credential request: %s", r.URL.Path)
			w.WriteHeader(http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
	}))
	defer backend.Close()
	cfg := config.Default()
	cfg.Server.Port = backend.Listener.Addr().(*net.TCPAddr).Port
	cfg.Auth.NextjsURL = "http://127.0.0.1:1"
	cfg.Auth.InternalToken = "test-internal-token"
	cfg.LLMProxy.Enabled = true
	_, syncer, _ := newLLMProxy(cfg, newSilentLogger(), nil)
	if err := syncer.SyncNow(context.Background()); err != nil {
		t.Fatalf("embedded deployment must sync through its backend without Next.js: %v", err)
	}
}
