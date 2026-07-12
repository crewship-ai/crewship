package scrubber

import (
	"strings"
	"testing"
)

// #974: the local-model endpoint auth token rides inside OPENCODE_CONFIG_CONTENT
// as a camelCase "apiKey" field, and a reverse-proxy/LiteLLM virtual key is a
// generic (non-JWT) bearer. Both were missed by the previous case-sensitive
// generic pattern and JWT-only bearer pattern.
func TestScrub_EndpointTokenForms(t *testing.T) {
	s := New()

	redacts := []struct {
		name string
		in   string
	}{
		{"camelCase apiKey in JSON", `{"provider":{"ollama":{"options":{"apiKey":"sk-proxy-Ab12Cd34Ef56"}}}}`},
		{"generic bearer header", `Authorization: Bearer proxy_Ab12Cd34Ef56Gh78`},
		{"lowercase bearer scheme", `authorization: bearer proxy_Ab12Cd34Ef56Gh78`},
		{"APIKEY env-style still covered", `APIKEY=proxy_Ab12Cd34Ef56Gh78`},
	}
	for _, tc := range redacts {
		out := s.Scrub(tc.in)
		if strings.Contains(out, "proxy_Ab12Cd34Ef56") || strings.Contains(out, "sk-proxy-Ab12Cd34Ef56") {
			t.Errorf("%s: secret survived: %q -> %q", tc.name, tc.in, out)
		}
		if !strings.Contains(out, "REDACTED") {
			t.Errorf("%s: expected a REDACTED marker: %q -> %q", tc.name, tc.in, out)
		}
	}

	// The camelCase-key path must preserve the key name (so the redaction is
	// legible) — same contract as the existing snake_case generic scrub.
	out := s.Scrub(`"apiKey":"sk-proxy-Ab12Cd34Ef56"`)
	if !strings.Contains(out, `"apiKey":"[REDACTED]"`) {
		t.Errorf("camelCase key name not preserved: %q", out)
	}
}

// Guard against over-redaction: the generic bearer floor (12 chars) must not
// eat ordinary prose that merely contains the word "bearer".
func TestScrub_BearerNoOverReach(t *testing.T) {
	s := New()
	prose := "Please bear with me while the bearer of news arrives."
	if got := s.Scrub(prose); got != prose {
		t.Errorf("prose was over-redacted: %q -> %q", prose, got)
	}
}
