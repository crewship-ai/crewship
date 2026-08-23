package sidecar

// The generic OpenAI-compatible provider — the only one whose upstream host
// comes from a credential rather than a compile-time literal.
//
// That single change is what turns "paste a base URL" into an egress
// primitive, so the tests here are mostly about the two gates that stop it:
//
//	layer 2  the crew allowlist, checked ONLY on this path (the three fixed-host
//	         providers make zero allowlist calls, which is how their behaviour
//	         stays byte-identical)
//	layer 3  p.dialSSRF — resolve-then-pin, link-local / cloud-metadata /
//	         reserved refused unconditionally, RFC1918 / loopback refused unless
//	         the crew opted in
//
// Create-time URL validation is layer 1 and lives in crewshipd. It cannot be
// the control: DNS can rebind between validate and dial, and the crew-scoped
// decision is not knowable then.

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// compatProxy builds a proxy holding one OPENAI_COMPAT credential.
func compatProxy(t *testing.T, cred *Credential, allowed []string, freeMode bool, capture **http.Request) (*Proxy, *[]egressEvent) {
	t.Helper()
	cs := NewCredStore()
	if cred != nil {
		cs.Load([]Credential{*cred})
	}
	events := &[]egressEvent{}
	p := NewProxy(ProxyConfig{
		CredStore: cs,
		Allowlist: NewDomainAllowlist(allowed),
		Logger:    covLogger(),
		FreeMode:  freeMode,
		OnEgress: func(host, method, provider string, statusCode int, denied bool) {
			*events = append(*events, egressEvent{host, method, provider, statusCode, denied})
		},
	})
	p.transport = roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		*capture = r
		return jsonUpstreamResponse(http.StatusOK, "application/json", `{"model":"local","usage":{}}`, nil), nil
	})
	return p, events
}

type egressEvent struct {
	host     string
	method   string
	provider string
	status   int
	denied   bool
}

func compatCred(baseURL string, headers map[string]string) *Credential {
	return &Credential{
		ID:       "compat-1",
		Provider: ProviderOpenAICompat,
		Token:    "sk-my_gateway-proxy-key-0001",
		BaseURL:  baseURL,
		Headers:  headers,
	}
}

func TestCompatProxy_UpstreamComesFromTheCredential(t *testing.T) {
	var upstream *http.Request
	proxy, _ := compatProxy(t,
		compatCred("https://llm.internal.example/v1", map[string]string{"X-Org": "acme"}),
		[]string{"llm.internal.example"}, false, &upstream)

	req := httptest.NewRequest("POST", "http://127.0.0.1:9119/llm/openai-compat/chat/completions",
		strings.NewReader(`{"model":"local"}`))
	req.Host = "127.0.0.1:9119"
	req.RemoteAddr = "127.0.0.1:54321"
	req.Header.Set("Authorization", "Bearer dummy-crewship-sidecar")
	w := httptest.NewRecorder()
	proxy.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
	}
	if upstream == nil {
		t.Fatal("upstream transport never called")
	}
	if upstream.URL.Scheme != "https" || upstream.URL.Host != "llm.internal.example" {
		t.Errorf("upstream = %s://%s, want https://llm.internal.example", upstream.URL.Scheme, upstream.URL.Host)
	}
	// The credential's own path is the endpoint base and is joined ahead of
	// the (prefix-stripped) request path.
	if upstream.URL.Path != "/v1/chat/completions" {
		t.Errorf("upstream path = %q, want /v1/chat/completions", upstream.URL.Path)
	}
	if got := upstream.Header.Get("Authorization"); got != "Bearer sk-my_gateway-proxy-key-0001" {
		t.Errorf("Authorization = %q: the agent's dummy must be overwritten by the CredStore token", got)
	}
	if got := upstream.Header.Get("X-Org"); got != "acme" {
		t.Errorf("X-Org = %q, want acme: the credential's custom headers travel with it", got)
	}
}

// A credential-supplied header must never be able to shadow the token the
// sidecar injects — otherwise "paste a credential" would include "and here is
// the Authorization header the upstream should see instead".
func TestCompatProxy_CustomHeaderCannotShadowTheInjectedToken(t *testing.T) {
	var upstream *http.Request
	proxy, _ := compatProxy(t,
		compatCred("https://llm.internal.example/v1", map[string]string{"Authorization": "Bearer attacker-chosen"}),
		[]string{"llm.internal.example"}, false, &upstream)

	req := httptest.NewRequest("POST", "http://127.0.0.1:9119/llm/openai-compat/chat/completions", strings.NewReader(`{}`))
	req.Host = "127.0.0.1:9119"
	req.RemoteAddr = "127.0.0.1:54321"
	proxy.ServeHTTP(httptest.NewRecorder(), req)

	if upstream == nil {
		t.Fatal("upstream transport never called")
	}
	if got := upstream.Header.Get("Authorization"); got != "Bearer sk-my_gateway-proxy-key-0001" {
		t.Errorf("Authorization = %q, want the injected token to be written LAST", got)
	}
}

// Layer 2. A credential-supplied host that is not on the crew's allowlist is
// refused BEFORE any dial, and the denial is emitted so it surfaces in Crow's
// Nest rather than only in the sidecar log.
func TestCompatProxy_DeniedWhenUpstreamNotAllowlisted(t *testing.T) {
	var upstream *http.Request
	proxy, events := compatProxy(t,
		compatCred("https://exfil.example.com/v1", nil),
		[]string{"llm.internal.example"}, false, &upstream)

	req := httptest.NewRequest("POST", "http://127.0.0.1:9119/llm/openai-compat/chat/completions", strings.NewReader(`{}`))
	req.Host = "127.0.0.1:9119"
	req.RemoteAddr = "127.0.0.1:54321"
	w := httptest.NewRecorder()
	proxy.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", w.Code)
	}
	if upstream != nil {
		t.Fatalf("request reached %s despite not being allowlisted — the credential is an unmetered egress primitive", upstream.URL.Host)
	}
	if len(*events) != 1 {
		t.Fatalf("egress events = %d, want exactly one denial", len(*events))
	}
	e := (*events)[0]
	if e.host != "exfil.example.com" || e.status != http.StatusForbidden || !e.denied || e.provider != string(ProviderOpenAICompat) {
		t.Errorf("egress event = %+v, want {exfil.example.com, 403, denied, OPENAI_COMPAT}", e)
	}
}

// The other half of layer 2: the three fixed-host providers must make ZERO
// allowlist calls, so an operator who has locked a crew down to one internal
// endpoint has not accidentally cut Anthropic off. This is the property that
// keeps their behaviour byte-identical while the new provider is fenced.
func TestFixedHostProviders_MakeNoAllowlistCall(t *testing.T) {
	cases := []struct {
		path string
		cred Credential
		host string
	}{
		{"/v1/messages", Credential{ID: "a1", Provider: ProviderAnthropic, Token: "sk-ant-api03-REAL"}, "api.anthropic.com"},
		{"/openai/v1/responses", Credential{ID: "o1", Provider: ProviderOpenAI, Token: "sk-proj-REAL"}, "api.openai.com"},
		{"/gemini/v1beta/models/g:generateContent", Credential{ID: "g1", Provider: ProviderGoogle, Token: "AIzaSyREAL"}, "generativelanguage.googleapis.com"},
	}
	for _, tc := range cases {
		t.Run(tc.host, func(t *testing.T) {
			cs := NewCredStore()
			cs.Load([]Credential{tc.cred})
			var upstream *http.Request
			// An allowlist that permits NOTHING. Restricted mode, no domains.
			proxy := NewProxy(ProxyConfig{
				CredStore: cs,
				Allowlist: NewDomainAllowlist(nil),
				Logger:    covLogger(),
				FreeMode:  false,
			})
			proxy.transport = roundTripperFunc(func(r *http.Request) (*http.Response, error) {
				upstream = r
				return jsonUpstreamResponse(http.StatusOK, "application/json", `{}`, nil), nil
			})
			req := httptest.NewRequest("POST", "http://127.0.0.1:9119"+tc.path, strings.NewReader(`{}`))
			req.Host = "127.0.0.1:9119"
			req.RemoteAddr = "127.0.0.1:54321"
			w := httptest.NewRecorder()
			proxy.ServeHTTP(w, req)

			if w.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200: the reverse-proxy path must not consult the allowlist for a fixed-host provider", w.Code)
			}
			if upstream == nil || upstream.URL.Host != tc.host {
				t.Fatalf("did not reach %s", tc.host)
			}
		})
	}
}

// Free mode is the operator's explicit opt-out of egress limits, so the
// allowlist check is skipped there — matching what handleHTTP and
// handleConnect already do. The SSRF dialer still applies (see below).
func TestCompatProxy_FreeModeSkipsAllowlist(t *testing.T) {
	var upstream *http.Request
	proxy, _ := compatProxy(t, compatCred("https://anything.example.com/v1", nil), nil, true, &upstream)

	req := httptest.NewRequest("POST", "http://127.0.0.1:9119/llm/openai-compat/chat/completions", strings.NewReader(`{}`))
	req.Host = "127.0.0.1:9119"
	req.RemoteAddr = "127.0.0.1:54321"
	w := httptest.NewRecorder()
	proxy.ServeHTTP(w, req)

	if w.Code != http.StatusOK || upstream == nil {
		t.Fatalf("free mode should forward: status %d", w.Code)
	}
}

// Layer 3, and the reason create-time validation cannot be the control: even
// with AllowPrivate on (the crew opted into RFC1918/loopback for a LAN
// endpoint) and the host on the allowlist, a name that RESOLVES to link-local
// — the cloud metadata service — is refused at dial time.
func TestCompatProxy_LinkLocalRefusedEvenWithAllowPrivate(t *testing.T) {
	cs := NewCredStore()
	cs.Load([]Credential{*compatCred("http://llm.internal.example/v1", nil)})

	proxy := NewProxy(ProxyConfig{
		CredStore:    cs,
		Allowlist:    NewDomainAllowlist([]string{"llm.internal.example"}),
		Logger:       covLogger(),
		AllowPrivate: true,
	})
	// The host passed create-time validation and the allowlist; DNS answers
	// with the metadata address. This is the rebinding shape the resolve-then-
	// pin dialer exists for.
	proxy.dnsResolve = func(ctx context.Context, host string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("169.254.169.254")}}, nil
	}

	req := httptest.NewRequest("POST", "http://127.0.0.1:9119/llm/openai-compat/chat/completions", strings.NewReader(`{}`))
	req.Host = "127.0.0.1:9119"
	req.RemoteAddr = "127.0.0.1:54321"
	w := httptest.NewRecorder()
	proxy.ServeHTTP(w, req)

	if w.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want 502 (dial refused)", w.Code)
	}
	if body := w.Body.String(); strings.Contains(body, "169.254") {
		t.Errorf("error body leaks the resolved address: %q", body)
	}
}

// A malformed or missing base URL is a 502 and the request is never dialled.
// It must not fall back to some default host, and it must not 200.
func TestCompatProxy_UnresolvableUpstreamIs502(t *testing.T) {
	cases := []struct {
		name    string
		baseURL string
	}{
		{"empty", ""},
		{"not_http", "file:///etc/passwd"},
		{"no_host", "https:///v1"},
		{"userinfo", "https://user:pass@llm.internal.example/v1"},
		{"oversized", "https://llm.internal.example/" + strings.Repeat("a", 2100)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var upstream *http.Request
			proxy, _ := compatProxy(t, compatCred(tc.baseURL, nil), []string{"llm.internal.example"}, false, &upstream)

			req := httptest.NewRequest("POST", "http://127.0.0.1:9119/llm/openai-compat/chat/completions", strings.NewReader(`{}`))
			req.Host = "127.0.0.1:9119"
			req.RemoteAddr = "127.0.0.1:54321"
			w := httptest.NewRecorder()
			proxy.ServeHTTP(w, req)

			if w.Code != http.StatusBadGateway {
				t.Errorf("status = %d, want 502", w.Code)
			}
			if upstream != nil {
				t.Errorf("request was dialled at %s despite an unusable base URL", upstream.URL.Host)
			}
		})
	}
}

func TestCompatProxy_NilCredIs503NotPassThrough(t *testing.T) {
	var upstream *http.Request
	proxy, _ := compatProxy(t, nil, []string{"llm.internal.example"}, false, &upstream)

	req := httptest.NewRequest("POST", "http://127.0.0.1:9119/llm/openai-compat/chat/completions", strings.NewReader(`{}`))
	req.Host = "127.0.0.1:9119"
	req.RemoteAddr = "127.0.0.1:54321"
	w := httptest.NewRecorder()
	proxy.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", w.Code)
	}
	if upstream != nil {
		t.Error("a credential-supplied upstream with no credential has no upstream; nothing may be forwarded")
	}
}
