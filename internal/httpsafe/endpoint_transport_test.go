package httpsafe

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestEndpointTransportRequiresPrivateOptIn(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	defer server.Close()
	for _, allow := range []bool{false, true} {
		transport := SafeTransportForEndpoint(allow)
		defer transport.CloseIdleConnections()
		client := &http.Client{Transport: transport, Timeout: time.Second}
		response, err := client.Get(server.URL)
		if !allow {
			if !errors.Is(err, ErrBlocked) {
				t.Fatalf("private endpoint without opt-in = %v", err)
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusNoContent {
			t.Fatalf("private endpoint status = %d", response.StatusCode)
		}
	}
}

func TestEndpointDialersRefuseMalformedAndUnavailableAddresses(t *testing.T) {
	for _, allow := range []bool{false, true} {
		dial := SafeDialContextForEndpoint(time.Second, allow)
		if _, err := dial(t.Context(), "tcp", "missing-port"); err == nil || !strings.Contains(err.Error(), "invalid address") {
			t.Fatalf("bad address = %v", err)
		}
		if _, err := dial(t.Context(), "tcp", "169.254.169.254:80"); !errors.Is(err, ErrBlocked) {
			t.Fatalf("metadata address = %v", err)
		}
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		if _, err := dial(ctx, "tcp", "not-resolved.invalid:443"); err == nil || !strings.Contains(err.Error(), "DNS resolution failed") {
			t.Fatalf("cancelled DNS lookup = %v", err)
		}
		if _, err := dial(ctx, "tcp", "8.8.8.8:443"); !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled public dial = %v", err)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := SafeDialContext(time.Second)(ctx, "tcp", "not-resolved.invalid:443"); err == nil {
		t.Fatal("cancelled strict lookup succeeded")
	}
	if _, err := SafeDialContext(time.Second)(ctx, "tcp", "8.8.8.8:443"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled strict dial = %v", err)
	}
}

func TestTrustedEndpointClientAllowsLocalServiceButNeverFollowsRedirects(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redirect" {
			w.Header().Set("Location", "http://169.254.169.254/latest/meta-data/")
			w.WriteHeader(http.StatusFound)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	for _, timeout := range []time.Duration{time.Second, 20 * time.Second} {
		client := TrustedEndpointClient(timeout)
		defer client.CloseIdleConnections()
		for _, tc := range []struct {
			path   string
			status int
		}{{"/", http.StatusNoContent}, {"/redirect", http.StatusFound}} {
			response, err := client.Get(server.URL + tc.path)
			if err != nil {
				t.Fatal(err)
			}
			response.Body.Close()
			if response.StatusCode != tc.status {
				t.Fatalf("%s = %d, want %d", tc.path, response.StatusCode, tc.status)
			}
		}
		if response, err := client.Get("http://169.254.169.254/latest/meta-data/"); err == nil {
			response.Body.Close()
			t.Fatal("operator endpoint reached cloud metadata")
		} else if !strings.Contains(err.Error(), "blocked address") {
			t.Fatalf("metadata blocked for the wrong reason: %v", err)
		}
	}
}

func TestEndpointURLRejectsZonedMetadataBeforeDial(t *testing.T) {
	for _, allow := range []bool{false, true} {
		if _, err := ValidateURLForEndpoint("https://[fe80::1%25eth0]/metadata", allow); !errors.Is(err, ErrInvalidURL) {
			t.Fatalf("zoned link-local URL passed the initial validation: %v", err)
		}
	}
}
