package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestProbeLocalModelEndpoint_OpenAICompat(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"data":[{"id":"qwen2.5-coder:7b"},{"id":"llama3"}]}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	res := probeLocalModelEndpoint(context.Background(), srv.URL+"/v1")
	if !res.Valid {
		t.Fatalf("expected reachable, got error=%q", res.Error)
	}
	if want := "2 model"; !strings.Contains(res.Error, want) {
		t.Fatalf("expected model count note containing %q, got %q", want, res.Error)
	}
}

func TestProbeLocalModelEndpoint_OllamaNativeFallback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			http.Error(w, "not found", http.StatusNotFound)
		case "/api/tags":
			w.Write([]byte(`{"models":[{"name":"qwen2.5-coder:7b"}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	res := probeLocalModelEndpoint(context.Background(), srv.URL+"/v1")
	if !res.Valid {
		t.Fatalf("expected reachable via /api/tags fallback, got error=%q", res.Error)
	}
}

func TestProbeLocalModelEndpoint_Unreachable(t *testing.T) {
	// A port that nothing listens on → connection refused, reported not-valid.
	res := probeLocalModelEndpoint(context.Background(), "http://127.0.0.1:1/v1")
	if res.Valid {
		t.Fatal("expected unreachable endpoint to report not valid")
	}
	if res.Error == "" {
		t.Fatal("expected an error message for an unreachable endpoint")
	}
}

func TestEndpointDialControl_BlocksMetadata(t *testing.T) {
	blocked := []string{"169.254.169.254:80", "169.254.0.1:443", "[fe80::1]:80", "224.0.0.1:80", "0.0.0.0:80"}
	for _, addr := range blocked {
		if err := endpointDialControl("tcp", addr, nil); err == nil {
			t.Errorf("expected %s to be refused by endpointDialControl", addr)
		}
	}
	allowed := []string{"192.168.1.10:11434", "10.0.0.5:11434", "127.0.0.1:11434", "8.8.8.8:443"}
	for _, addr := range allowed {
		if err := endpointDialControl("tcp", addr, nil); err != nil {
			t.Errorf("expected %s to be allowed (private/loopback/public are fine here), got %v", addr, err)
		}
	}
}
