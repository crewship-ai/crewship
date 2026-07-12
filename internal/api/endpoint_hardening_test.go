package api

import (
	"strings"
	"testing"
)

// #974 S3/S7: validateEndpointURL hardening — HTTPS required when auth is
// attached (except private-IP literals), embedded userinfo rejected, length cap.
func TestValidateEndpointURL_Hardening(t *testing.T) {
	cases := []struct {
		name    string
		value   string
		wantErr string // substring; "" means must be accepted
	}{
		// S3 — token over http
		{"http+token public host rejected", `{"baseURL":"http://llm.example.com/v1","apiKey":"sk-x"}`, "cleartext"},
		{"https+token accepted", `{"baseURL":"https://llm.example.com/v1","apiKey":"sk-x"}`, ""},
		{"http+token private literal accepted", `{"baseURL":"http://192.168.1.10:11434/v1","apiKey":"sk-x"}`, ""},
		{"http+token loopback accepted", `{"baseURL":"http://127.0.0.1:11434/v1","apiKey":"sk-x"}`, ""},
		{"http+token hostname rejected", `{"baseURL":"http://ollama.lan:11434/v1","apiKey":"sk-x"}`, "cleartext"},
		{"http no-token still fine", `http://ollama.lan:11434/v1`, ""},
		{"https no-token fine", `https://llm.example.com/v1`, ""},
		{"http+header public rejected", `{"baseURL":"http://llm.example.com/v1","headers":{"X-Key":"v"}}`, "cleartext"},

		// S7 — userinfo + length
		{"userinfo rejected", `http://user:pass@host:11434/v1`, "must not embed credentials"},
		{"userinfo in JSON rejected", `{"baseURL":"http://user:pass@host:11434/v1"}`, "must not embed credentials"},
		{"oversize rejected", `{"baseURL":"https://h/v1","apiKey":"` + strings.Repeat("a", 9000) + `"}`, "too large"},

		// hard-blocked still rejected regardless of auth
		{"metadata IP rejected", `http://169.254.169.254/v1`, "metadata"},

		// baseline still valid
		{"plain private literal ok", `http://192.168.1.222:11434/v1`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := validateEndpointURL(tc.value)
			if tc.wantErr == "" {
				if got != "" {
					t.Errorf("expected accept, got error: %q", got)
				}
				return
			}
			if !strings.Contains(strings.ToLower(got), strings.ToLower(tc.wantErr)) {
				t.Errorf("expected error containing %q, got %q", tc.wantErr, got)
			}
		})
	}
}
