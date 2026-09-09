package pagebuild

import (
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestRuntimeRequiresSeparateSite(t *testing.T) {
	for _, runtime := range []string{"https://studio.example.com:9443", "https://pages.example.com", "https://a.b.example.com", "javascript:alert(1)", "https://user:pass@example.net", "https://example.net/path", "http://example.net"} {
		if ValidateRuntimeOrigin(runtime, "https://studio.example.com") == nil {
			t.Fatalf("unsafe runtime %s", runtime)
		}
	}
	if err := ValidateRuntimeOrigin("https://pages.example.net", "https://studio.example.com"); err != nil {
		t.Fatal(err)
	}
	if err := ValidateRuntimeOrigin("http://127.0.0.2:1234", "http://127.0.0.1:1234"); err != nil {
		t.Fatal(err)
	}
}
func TestRuntimeHeadersAndConstantBootstrap(t *testing.T) {
	w := httptest.NewRecorder()
	w.Header().Set("X-Frame-Options", "DENY")
	ServeRuntime(w, "https://studio.example.com")
	if w.Header().Get("X-Frame-Options") != "" {
		t.Fatal("bootstrap remains unframeable")
	}
	for _, part := range []string{"connect-src 'none'", "frame-src 'none'", "frame-ancestors https://studio.example.com", "script-src 'nonce-"} {
		if !strings.Contains(w.Header().Get("Content-Security-Policy"), part) {
			t.Fatal(part)
		}
	}
	if strings.Contains(w.Body.String(), "<iframe") || !strings.Contains(w.Body.String(), "event.origin !== expectedParent") {
		t.Fatal("invalid trusted bootstrap")
	}
}

// Used only by the Playwright script; the child process closes itself on timeout.
func TestServeRuntimeBrowserHarness(t *testing.T) {
	file := os.Getenv("CREWSHIP_TEST_RUNTIME_FILE")
	if file == "" {
		// SKIP-WAIVER(#2472): this blocking server is started by the mandatory Chromium CI harness.
		t.Skip("browser harness only")
	}
	studio := os.Getenv("CREWSHIP_TEST_STUDIO_ORIGIN")
	listener, err := net.Listen("tcp", "127.0.0.2:0")
	if err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{}, 1)
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/__stop" {
			select {
			case stop <- struct{}{}:
			default:
			}
			w.WriteHeader(204)
			return
		}
		ServeRuntime(w, studio)
	}), ReadHeaderTimeout: 5 * time.Second}
	defer server.Close()
	go server.Serve(listener)
	if err := os.WriteFile(file, []byte("http://"+listener.Addr().String()+RuntimePath), 0600); err != nil {
		t.Fatal(err)
	}
	select {
	case <-stop:
	case <-time.After(45 * time.Second):
		t.Fatal("browser harness timeout")
	}
}

func TestDevelopmentRuntimeRequiresExplicitSameOriginAndValidTLS(t *testing.T) {
	const studio = "https://studio.example.com"
	for _, tc := range []struct {
		runtime         string
		enabled, wantOK bool
	}{
		{studio, false, false}, {studio, true, true},
		{"https://pages.example.com", true, false},
		{"https://studio.example.com:9443", true, false},
		{"http://studio.example.com", true, false},
		{"https://studio.example.com/path", true, false},
		{"https://user:pass@studio.example.com", true, false},
		{"https://pages.example.net", false, true},
	} {
		if err := ValidateRuntimeOriginForDevelopment(tc.runtime, studio, tc.enabled); (err == nil) != tc.wantOK {
			t.Errorf("runtime=%q enabled=%v err=%v", tc.runtime, tc.enabled, err)
		}
	}
}

func TestRuntimeRejectsCleartextRemoteEvenWithHTTPStudio(t *testing.T) {
	for _, runtime := range []string{"http://pages.example.net", "http://192.0.2.1", "http://localhost"} {
		t.Run(runtime, func(t *testing.T) {
			if err := ValidateRuntimeOrigin(runtime, "http://studio.example.com"); err == nil {
				t.Fatal("remote HTTP runtime accepted")
			}
		})
	}
}
