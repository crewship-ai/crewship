package pagebuild

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBoardConverterHasNoPlatformOrNetworkCapability(t *testing.T) {
	w := httptest.NewRecorder()
	w.Header().Set("X-Frame-Options", "DENY")
	ServeBoardConverter(w, "https://runtime.example.net", "https://studio.example.com:443/")
	if w.Code != 200 || w.Header().Get("X-Frame-Options") != "" || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("unexpected headers: %d %v", w.Code, w.Header())
	}
	policy := w.Header().Get("Content-Security-Policy")
	for _, required := range []string{"sandbox allow-scripts;", "default-src 'none'", "connect-src 'none'", "worker-src 'none'", "frame-src 'none'", "base-uri 'none'", "form-action 'none'", "frame-ancestors https://studio.example.com"} {
		if !strings.Contains(policy, required) {
			t.Errorf("missing %s in %s", required, policy)
		}
	}
	for _, forbidden := range []string{"allow-same-origin", "allow-popups", "unsafe-eval", "script-src 'unsafe-inline'", "script-src *"} {
		if strings.Contains(policy, forbidden) {
			t.Errorf("unexpected capability %s", forbidden)
		}
	}
	body := w.Body.String()
	if !strings.Contains(body, `window.__CREWSHIP_BOARD_CONVERTER_PARENT__="https://studio.example.com"`) || !strings.Contains(body, `src="/board-converter/runner.js"`) {
		t.Fatalf("unexpected public bootstrap: %s", body)
	}
	other := httptest.NewRecorder()
	ServeBoardConverter(other, "https://runtime.example.net", "https://studio.example.com")
	if other.Header().Get("Content-Security-Policy") == policy {
		t.Fatal("nonce reused across responses")
	}
}

func TestBoardConverterRefusesSameSiteAndMalformedOrigin(t *testing.T) {
	for _, runtime := range []string{"https://runtime.example.com", "https://studio.example.com", "http://public.example.net", "https://runtime.example.net/path", "https://user:password@runtime.example.net"} {
		w := httptest.NewRecorder()
		ServeBoardConverter(w, runtime, "https://studio.example.com")
		if w.Code != 503 || strings.Contains(w.Body.String(), "<script") {
			t.Fatalf("unsafe runtime accepted %s", runtime)
		}
	}
}
