package sidecar

// #2052 — the crew-wide CredStore had no agent dimension.
//
// One sidecar serves a whole crew container, and both Select call sites in the
// proxy asked only "which provider?". For the three fixed-host providers a
// crossed credential meant the wrong key paid; for OPENAI_COMPAT the upstream
// comes from the credential itself, so it meant agent A's prompts were POSTed
// to agent B's gateway, authenticated with B's key. The #2051 allowlist union
// covers B's host, so there is no 403 and nothing in the run says so.
//
// These tests drive the real proxy through the real route (/llm/openai-compat)
// with the real route-token identity resolver, and assert on the host that was
// actually dialled.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
)

const (
	scopeAgentA   = "agt_alpha"
	scopeAgentB   = "agt_bravo"
	scopeRouteKey = "route-key-for-one-workspace-crew-0001"
	scopeConfigFP = "cfp0123456789abcdef0123"
)

// scopedProxy builds a proxy whose CredStore holds the crew's credentials and
// whose identity resolver is the SAME one the sidecar server installs
// (Server.llmRouteIdentity), so the acting agent is resolved from the route
// token embedded in the disposable provider key rather than from a fixture.
func scopedProxy(t *testing.T, creds []Credential, capture **http.Request) *Proxy {
	t.Helper()
	cs := NewCredStore()
	cs.Load(creds)
	s := &Server{routeAuth: &RouteAuth{Key: scopeRouteKey}}
	p := NewProxy(ProxyConfig{
		CredStore:          cs,
		Allowlist:          NewDomainAllowlist(nil),
		Logger:             covLogger(),
		FreeMode:           true,
		ResolveLLMIdentity: s.llmRouteIdentity,
		ConfigFingerprint:  scopeConfigFP,
	})
	p.transport = roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		*capture = r
		return jsonUpstreamResponse(http.StatusOK, "application/json", `{"model":"local","usage":{}}`, nil), nil
	})
	return p
}

// scopeRouteRequest is the request an agent's OpenAI-compatible driver makes:
// the dummy provider key carries this agent's route token and the fingerprint
// of the credential set its run was launched with (bindLLMRouteToken).
func scopeRouteRequest(agentID string) *http.Request {
	tok := internaltoken.DeriveLLMRouteToken(scopeRouteKey, agentID)
	key := routedProviderDummyKeyForTest + "." + tok + internaltoken.RouteFingerprintDelimiter + scopeConfigFP
	req := httptest.NewRequest(http.MethodPost,
		"http://127.0.0.1:9119/llm/openai-compat/chat/completions",
		strings.NewReader(`{"model":"local"}`))
	req.Host = "127.0.0.1:9119"
	req.RemoteAddr = "127.0.0.1:54321"
	req.Header.Set("Authorization", "Bearer "+key)
	return req
}

// routedProviderDummyKeyForTest mirrors the orchestrator's dummy provider key
// (internal/orchestrator/exec_env.go). Duplicated rather than imported: the
// sidecar must not depend on the orchestrator, and the byte shape is the point.
const routedProviderDummyKeyForTest = "dummy-crewship-sidecar"

// TestLLMRoute_EndpointCredentialDoesNotCrossAgents is the #2052 reproduction.
// Two crew members each hold their own endpoint-backed credential; each must
// reach its OWN gateway. B's credential is loaded FIRST so that a provider-only
// Select hands it to A on the very first call.
func TestLLMRoute_EndpointCredentialDoesNotCrossAgents(t *testing.T) {
	creds := []Credential{
		{ID: "compat-b", Provider: ProviderOpenAICompat, Token: "sk-bravo-key",
			BaseURL: "https://b.example/v1", AgentIDs: []string{scopeAgentB}},
		{ID: "compat-a", Provider: ProviderOpenAICompat, Token: "sk-alpha-key",
			BaseURL: "https://a.example/v1", AgentIDs: []string{scopeAgentA}},
	}

	tests := []struct {
		name     string
		agentID  string
		wantHost string
		wantAuth string
	}{
		{
			name:     "agent A reaches A's gateway with A's key",
			agentID:  scopeAgentA,
			wantHost: "a.example",
			wantAuth: "Bearer sk-alpha-key",
		},
		{
			name:     "agent B reaches B's gateway with B's key",
			agentID:  scopeAgentB,
			wantHost: "b.example",
			wantAuth: "Bearer sk-bravo-key",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var upstream *http.Request
			p := scopedProxy(t, creds, &upstream)

			w := httptest.NewRecorder()
			p.ServeHTTP(w, scopeRouteRequest(tt.agentID))

			if w.Code != http.StatusOK {
				t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
			}
			if upstream == nil {
				t.Fatal("upstream transport never called")
			}
			if upstream.URL.Host != tt.wantHost {
				t.Errorf("dialled %q, want %q: the acting agent's prompt went to another member's endpoint",
					upstream.URL.Host, tt.wantHost)
			}
			if got := upstream.Header.Get("Authorization"); got != tt.wantAuth {
				t.Errorf("Authorization = %q, want %q: another member's key paid for this call", got, tt.wantAuth)
			}
		})
	}
}

// A crew member that holds no credential for the provider must be REFUSED, not
// quietly handed a sibling's. A loud 503 is diagnosable; a silent crossover is
// not, which is the whole of #2052.
func TestLLMRoute_MemberWithoutAGrantIsRefused(t *testing.T) {
	var upstream *http.Request
	p := scopedProxy(t, []Credential{
		{ID: "compat-b", Provider: ProviderOpenAICompat, Token: "sk-bravo-key",
			BaseURL: "https://b.example/v1", AgentIDs: []string{scopeAgentB}},
	}, &upstream)

	w := httptest.NewRecorder()
	p.ServeHTTP(w, scopeRouteRequest(scopeAgentA))

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body %s", w.Code, w.Body.String())
	}
	if upstream != nil {
		t.Errorf("upstream %q was dialled for an agent holding no credential", upstream.URL.Host)
	}
}

// A crew-scoped credential (crew link, or a crew/workspace binding) carries no
// agent ids and stays available to every member — the behaviour of every
// credential before ownership existed, and the case that must not regress.
func TestLLMRoute_CrewScopedCredentialServesEveryMember(t *testing.T) {
	creds := []Credential{
		{ID: "compat-crew", Provider: ProviderOpenAICompat, Token: "sk-crew-key",
			BaseURL: "https://crew.example/v1"},
	}
	for _, agentID := range []string{scopeAgentA, scopeAgentB} {
		var upstream *http.Request
		p := scopedProxy(t, creds, &upstream)
		w := httptest.NewRecorder()
		p.ServeHTTP(w, scopeRouteRequest(agentID))
		if w.Code != http.StatusOK {
			t.Fatalf("%s: status = %d, body %s", agentID, w.Code, w.Body.String())
		}
		if upstream == nil || upstream.URL.Host != "crew.example" {
			t.Fatalf("%s: dialled %v, want crew.example", agentID, upstream)
		}
	}
}

// The refusal must not be silent for a provider that does NOT require a
// credential.
//
// Anthropic, OpenAI and Google are RequireCredential:false — the reverse-proxy
// route forwards a request with no credential rather than 503ing, which is the
// pass-through it has always had for an EMPTY store. Once Select refuses on
// ownership, that same nil starts meaning "the store has one, but not yours",
// and falling through sends the agent's disposable dummy provider key to the
// real vendor. The vendor answers 401 and the operator reads it as a bad key —
// #2052's silent crossover swapped for a silent misattribution, which is no
// better. The two nils are told apart, and only the refusal fails closed.
func TestLLMRoute_PeerOnlyCredentialRefusesRatherThanForwardingTheDummy(t *testing.T) {
	var upstream *http.Request
	p := scopedProxy(t, []Credential{
		{ID: "ant-b", Provider: ProviderAnthropic, Token: "sk-ant-bravo", AgentIDs: []string{scopeAgentB}},
	}, &upstream)

	req := scopeRouteRequest(scopeAgentA)
	req.URL.Path = "/v1/messages"
	w := httptest.NewRecorder()
	p.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body %s", w.Code, w.Body.String())
	}
	if upstream != nil {
		t.Errorf("forwarded to %q with the agent's dummy key: the peer's credential was "+
			"refused and the request went upstream unauthenticated anyway", upstream.URL.Host)
	}
}

// …and the pass-through it replaced is untouched. An EMPTY store for a
// non-RequireCredential provider still forwards, exactly as it did before
// ownership existed — that path predates credentials entirely and turning it
// into a 503 would break every crew running one of these providers on a key the
// agent carries itself.
func TestLLMRoute_EmptyStoreStillPassesThroughForOptionalCredentialProviders(t *testing.T) {
	var upstream *http.Request
	p := scopedProxy(t, nil, &upstream)

	req := scopeRouteRequest(scopeAgentA)
	req.URL.Path = "/v1/messages"
	w := httptest.NewRecorder()
	p.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", w.Code, w.Body.String())
	}
	if upstream == nil {
		t.Fatal("an empty store stopped forwarding: the pre-#2052 pass-through is gone")
	}
}
