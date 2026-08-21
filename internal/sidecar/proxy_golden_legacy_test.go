package sidecar

// Byte-identity proof for the three grandfathered providers.
//
// Phase 2 replaced three hardcoded things at once — the `/gemini/ /openai/
// /v1/` prefix switch in handleLocal, the three-arm `injectCredential`, and the
// per-provider (host, stripPrefix) arguments to reverseProxyToProvider — with
// one descriptor lookup. Each of those had a failure mode that is SILENT: a
// dropped auth slot means the request goes upstream carrying the dummy the
// agent env still holds, and the only symptom is a 401 the agent reports as a
// model error. Assertions like "the header is set" do not catch a slot that
// moved, a prefix that survived the strip, or a query string that got
// re-encoded differently.
//
// So the assertion here is the COMPLETE outbound request — method, scheme,
// host, Host header, path, RawPath, RawQuery, request-target and every header
// with its value, sorted — compared against golden files captured by running
// this same table through the pre-phase-2 proxy. The golden files in
// testdata/legacy_outbound/ are the pre-change behaviour, full stop; nothing
// in this file can regenerate them.
//
// Two exceptions to byte-identity are declared in the phase-2 plan and are NOT
// visible here, because neither touches the outbound request: cost-ledger rows
// now carry real token counts under a lowercase provider key (the E1 fix, see
// TestReverseProxy_RecordsRealTokenCounts), and /health gained a trailing
// provider_creds object (see TestHealth_LegacyPrefixByteIdentical, which pins
// the eight keys that came before it).

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// goldenDir holds one file per row of the legacy table below.
const goldenDir = "testdata/legacy_outbound"

// dumpOutbound renders every part of a forwarded request that an upstream can
// observe, in a stable order. Header values are printed verbatim — these are
// test tokens, and a redacted dump would defeat the whole point of the file.
func dumpOutbound(r *http.Request) string {
	var b strings.Builder
	fmt.Fprintf(&b, "method %s\n", r.Method)
	fmt.Fprintf(&b, "scheme %s\n", r.URL.Scheme)
	fmt.Fprintf(&b, "url.host %s\n", r.URL.Host)
	fmt.Fprintf(&b, "req.host %s\n", r.Host)
	fmt.Fprintf(&b, "path %s\n", r.URL.Path)
	fmt.Fprintf(&b, "rawpath %s\n", r.URL.RawPath)
	fmt.Fprintf(&b, "rawquery %s\n", r.URL.RawQuery)
	fmt.Fprintf(&b, "target %s\n", r.URL.RequestURI())
	fmt.Fprintf(&b, "requesturi %q\n", r.RequestURI)

	names := make([]string, 0, len(r.Header))
	for k := range r.Header {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		for _, v := range r.Header[k] {
			fmt.Fprintf(&b, "header %s: %s\n", k, v)
		}
	}
	return b.String()
}

// newGoldenProxy builds a proxy whose transport captures the outbound request
// instead of dialling. FreeMode is on so the forward-proxy rows reach the
// injection path without an allowlist detour — the allowlist is exercised
// separately (TestCompatProxy_DeniedWhenUpstreamNotAllowlisted).
func newGoldenProxy(t *testing.T, creds []Credential, capture **http.Request) *Proxy {
	t.Helper()
	cs := NewCredStore()
	if len(creds) > 0 {
		cs.Load(creds)
	}
	p := NewProxy(ProxyConfig{
		CredStore:   cs,
		Allowlist:   NewDomainAllowlist(nil),
		Logger:      covLogger(),
		FreeMode:    true,
		BillingMode: "metered",
	})
	p.transport = roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		*capture = r
		return jsonUpstreamResponse(http.StatusOK, "application/json", `{}`, nil), nil
	})
	return p
}

// legacyGoldenCase is one (request, credential) pair whose forwarded shape is
// frozen. `agentHeaders` are what the CLI adapter's env leaves on the request —
// crucially including the dummy keys, so a golden that still contains
// "dummy-crewship-sidecar" in an auth slot would be a visible leak.
type legacyGoldenCase struct {
	name         string
	golden       string
	method       string
	url          string
	host         string
	agentHeaders map[string]string
	creds        []Credential
}

func legacyGoldenCases() []legacyGoldenCase {
	return []legacyGoldenCase{
		// ---- handleLocal: ANTHROPIC_BASE_URL=http://127.0.0.1:9119 ----
		{
			name: "anthropic_local_apikey", golden: "anthropic_local_apikey.golden",
			method: "POST", url: "http://127.0.0.1:9119/v1/messages", host: "127.0.0.1:9119",
			agentHeaders: map[string]string{
				"Content-Type": "application/json",
				"x-api-key":    "sk-ant-dummy-crewship-sidecar",
			},
			creds: []Credential{{ID: "a1", Provider: ProviderAnthropic, Token: "sk-ant-api03-REALKEY"}},
		},
		{
			// The OAuth branch: a sk-ant-oat* token must land in Authorization
			// and must NOT set x-api-key. Losing this branch is invisible at
			// runtime — Anthropic just 401s.
			name: "anthropic_local_oauth", golden: "anthropic_local_oauth.golden",
			method: "POST", url: "http://127.0.0.1:9119/v1/messages", host: "127.0.0.1:9119",
			agentHeaders: map[string]string{"Content-Type": "application/json"},
			creds:        []Credential{{ID: "a1", Provider: ProviderAnthropic, Token: "sk-ant-oat01-REALTOKEN"}},
		},
		{
			// Empty CredStore: the request is forwarded untouched. This is the
			// live CLAUDE_CODE_OAUTH_TOKEN path, where the agent env holds the
			// token and the sidecar has nothing to inject.
			name: "anthropic_local_nocred", golden: "anthropic_local_nocred.golden",
			method: "POST", url: "http://127.0.0.1:9119/v1/messages", host: "127.0.0.1:9119",
			agentHeaders: map[string]string{"Authorization": "Bearer sk-ant-oat01-FROM-ENV"},
		},
		{
			// Percent-encoded segment plus a query string. Anthropic's spec is
			// StripPrefix=false, so the path is forwarded verbatim and RawPath
			// must survive — clearing it here would re-derive the target from
			// Path and silently change the escaping.
			name: "anthropic_local_escaped_path", golden: "anthropic_local_escaped_path.golden",
			method: "GET", url: "http://127.0.0.1:9119/v1/models/claude%2Fsonnet?beta=true", host: "127.0.0.1:9119",
			creds: []Credential{{ID: "a1", Provider: ProviderAnthropic, Token: "sk-ant-api03-REALKEY"}},
		},
		// ---- handleLocal: OPENAI_BASE_URL=http://127.0.0.1:9119/openai/v1 ----
		{
			name: "openai_local_responses", golden: "openai_local_responses.golden",
			method: "POST", url: "http://127.0.0.1:9119/openai/v1/responses?stream=true", host: "127.0.0.1:9119",
			agentHeaders: map[string]string{
				"Content-Type":  "application/json",
				"Authorization": "Bearer sk-dummy-crewship-sidecar",
			},
			creds: []Credential{{ID: "o1", Provider: ProviderOpenAI, Token: "sk-proj-REALOPENAIKEY"}},
		},
		{
			name: "openai_local_nocred", golden: "openai_local_nocred.golden",
			method: "POST", url: "http://127.0.0.1:9119/openai/v1/chat/completions", host: "127.0.0.1:9119",
			agentHeaders: map[string]string{"Authorization": "Bearer caller-supplied"},
		},
		// ---- handleLocal: GOOGLE_GEMINI_BASE_URL=http://127.0.0.1:9119/gemini ----
		{
			// Google is the two-slot case: x-goog-api-key AND ?key=. Setting
			// only one leaves the other holding the agent's dummy. The golden
			// also pins the sort-and-re-encode side effect of writing the query
			// param, which reorders alt/key deterministically.
			name: "google_local_stream", golden: "google_local_stream.golden",
			method: "POST", url: "http://127.0.0.1:9119/gemini/v1beta/models/gemini-2.5-flash:streamGenerateContent?alt=sse", host: "127.0.0.1:9119",
			agentHeaders: map[string]string{
				"Content-Type":   "application/json",
				"x-goog-api-key": "dummy-crewship-sidecar",
			},
			creds: []Credential{{ID: "g1", Provider: ProviderGoogle, Token: "AIzaSyREALGOOGLEKEY"}},
		},
		{
			name: "google_local_nocred", golden: "google_local_nocred.golden",
			method: "POST", url: "http://127.0.0.1:9119/gemini/v1beta/models/g:generateContent", host: "127.0.0.1:9119",
			agentHeaders: map[string]string{"x-goog-api-key": "caller-supplied"},
		},
		// ---- handleHTTP: HTTP_PROXY forward-proxy path ----
		{
			// Proxy-Authorization must be stripped (RFC 2616 13.5.1, and it is
			// an exfiltration vector); the real key must overwrite the dummy.
			name: "anthropic_forward", golden: "anthropic_forward.golden",
			method: "POST", url: "http://api.anthropic.com/v1/messages", host: "api.anthropic.com",
			agentHeaders: map[string]string{
				"Content-Type":        "application/json",
				"x-api-key":           "sk-ant-dummy-crewship-sidecar",
				"Proxy-Authorization": "Basic attacker-creds",
			},
			creds: []Credential{{ID: "a1", Provider: ProviderAnthropic, Token: "sk-ant-api03-REALKEY"}},
		},
		{
			name: "openai_forward", golden: "openai_forward.golden",
			method: "POST", url: "http://api.openai.com/v1/chat/completions", host: "api.openai.com",
			agentHeaders: map[string]string{"Authorization": "Bearer sk-dummy-crewship-sidecar"},
			creds:        []Credential{{ID: "o1", Provider: ProviderOpenAI, Token: "sk-proj-REALOPENAIKEY"}},
		},
		{
			name: "google_forward", golden: "google_forward.golden",
			method: "POST", url: "http://generativelanguage.googleapis.com/v1/models/gemini-pro:generateContent?key=agent-dummy", host: "generativelanguage.googleapis.com",
			creds: []Credential{{ID: "g1", Provider: ProviderGoogle, Token: "AIzaSyREALGOOGLEKEY"}},
		},
	}
}

func TestLegacyProviders_OutboundRequestIsByteIdentical(t *testing.T) {
	cases := legacyGoldenCases()
	if len(cases) == 0 {
		t.Fatal("legacyGoldenCases() is empty: the byte-identity guard would pass vacuously")
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(goldenDir, tc.golden)
			want, err := os.ReadFile(path)
			if err != nil {
				// t.Fatal, never t.Skip: a missing fixture must fail the run.
				// A skip reports the same "ok" as a pass and would retire the
				// only proof that the three legacy providers still work.
				t.Fatalf("golden fixture %s is missing or unreadable (%v); it was captured from the pre-phase-2 proxy and must be restored, not regenerated", path, err)
			}

			var upstream *http.Request
			proxy := newGoldenProxy(t, tc.creds, &upstream)

			req := httptest.NewRequest(tc.method, tc.url, strings.NewReader(`{"model":"m"}`))
			req.Host = tc.host
			// handleLocal is now gated on a loopback PEER as well as a
			// loopback Host header (see handleHTTP). httptest.NewRequest
			// defaults RemoteAddr to 192.0.2.1, which production never sees:
			// the sidecar binds 127.0.0.1:9119 inside the agent's own netns.
			req.RemoteAddr = "127.0.0.1:54321"
			for k, v := range tc.agentHeaders {
				req.Header.Set(k, v)
			}
			w := httptest.NewRecorder()
			proxy.ServeHTTP(w, req)

			if w.Code != http.StatusOK {
				t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
			}
			if upstream == nil {
				t.Fatal("upstream transport never called: the request never reached a provider")
			}
			if got := dumpOutbound(upstream); got != string(want) {
				t.Errorf("outbound request changed.\n--- got ---\n%s\n--- want (%s) ---\n%s", got, path, want)
			}
			// Belt and braces on the thing that actually hurts: no dummy may
			// survive in any header the upstream sees.
			for k, vv := range upstream.Header {
				for _, v := range vv {
					if strings.Contains(v, "dummy-crewship-sidecar") && len(tc.creds) > 0 {
						t.Errorf("header %s still carries the agent dummy (%q) despite a credential being present", k, v)
					}
				}
			}
		})
	}
}

// The /v1 prefix is Anthropic's and it is a catch-all, which is exactly why
// the old switch had to list /openai/ and /gemini/ ABOVE it. Matching must be
// segment-bounded: /v1beta/... is a Gemini-shaped path and has always 404ed
// here rather than being forwarded to api.anthropic.com. A naive
// strings.HasPrefix(path, "/v1") in the descriptor matcher would route it to
// Anthropic and the failure would look like a Google outage.
func TestHandleLocal_PrefixMatchIsSegmentBounded(t *testing.T) {
	notRouted := []string{
		"/v1beta/models/g:generateContent",
		"/openaix/v1/responses",
		"/geminis/v1beta/models",
		"/llm/openrouterx/chat/completions",
		"/",
		"/nope",
	}
	for _, path := range notRouted {
		t.Run(path, func(t *testing.T) {
			var upstream *http.Request
			proxy := newGoldenProxy(t, nil, &upstream)
			req := httptest.NewRequest("POST", "http://127.0.0.1:9119"+path, strings.NewReader(`{}`))
			req.Host = "127.0.0.1:9119"
			req.RemoteAddr = "127.0.0.1:54321" // reach handleLocal, so the 404 is the router's verdict
			w := httptest.NewRecorder()
			proxy.ServeHTTP(w, req)

			if upstream != nil {
				t.Fatalf("%s was forwarded to %s%s; it must not match any provider prefix", path, upstream.URL.Host, upstream.URL.Path)
			}
			if w.Code != http.StatusNotFound {
				t.Errorf("status = %d, want 404", w.Code)
			}
		})
	}
}

// The eight pre-phase-2 /health keys must serialise byte-identically, in the
// same order, with the same values. The payload moved from fmt.Fprintf to a
// json.Marshal of a struct, which is exactly the kind of change that silently
// reorders keys or starts HTML-escaping a value — and the orchestrator's
// restart-skip (domains_hash), stale-sidecar (sidecar_hash) and orphan-token
// (token_fp) checks all read this body.
//
// provider_creds is appended AFTER token_fp; this test pins the prefix, so it
// fails if anything is inserted before or between the original eight.
func TestHealth_LegacyPrefixByteIdentical(t *testing.T) {
	cs := NewCredStore()
	cs.Load([]Credential{
		{ID: "a1", Provider: ProviderAnthropic, Token: "sk-ant-1"},
		{ID: "a2", Provider: ProviderAnthropic, Token: "sk-ant-2"},
		{ID: "o1", Provider: ProviderOpenAI, Token: "sk-oai-1"},
	})
	proxy := NewProxy(ProxyConfig{
		CredStore:         cs,
		Allowlist:         NewDomainAllowlist(nil),
		Logger:            covLogger(),
		FreeMode:          false,
		BuildHash:         "abc123def456",
		PolicyDomainsHash: "0f0f0f0f",
		TokenFP:           "deadbeef",
	})

	req := httptest.NewRequest("GET", "http://127.0.0.1:9119/health", nil)
	req.Host = "127.0.0.1:9119"
	req.RemoteAddr = "127.0.0.1:54321"
	w := httptest.NewRecorder()
	proxy.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	const wantPrefix = `{"status":"ok","anthropic_creds":2,"openai_creds":1,"google_creds":0,` +
		`"network_mode":"restricted","sidecar_hash":"abc123def456","domains_hash":"0f0f0f0f","token_fp":"deadbeef"`
	if got := w.Body.String(); !strings.HasPrefix(got, wantPrefix) {
		t.Errorf("/health prefix changed.\n got: %s\nwant: %s...", got, wantPrefix)
	}
}
