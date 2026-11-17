package sidecar

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
	"github.com/crewship-ai/crewship/internal/bedrock"
	"github.com/crewship-ai/crewship/internal/scrubber"
)

func TestBedrockPeerUsesExactHostAccountWithoutInterruptingParent(t *testing.T) {
	var revoked atomic.Bool
	var authorityCalls atomic.Int64
	account := bedrock.Account{AuthMethod: "bedrock_api_key", APIKey: "peer-synthetic-secret", Region: "eu-central-1", Models: []string{"eu.anthropic.claude-opus-5-5"}, ExpiresAt: time.Now().Add(time.Hour).UTC().Format(time.RFC3339), EUOnly: true}
	authority := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorityCalls.Add(1)
		var request bedrock.ResolveRequest
		if r.URL.Path != bedrock.ResolvePath || r.Header.Get("X-Internal-Token") != "crew-ipc" || json.NewDecoder(r.Body).Decode(&request) != nil || request.RunID != "peer-run" || request.AgentID != "peer" || request.AccountID != "peer-account" || request.Model != account.Models[0] || revoked.Load() {
			http.Error(w, "not authorized", 403)
			return
		}
		_ = json.NewEncoder(w).Encode(bedrock.ResolveResponse{AccountID: "peer-account", Account: account})
	}))
	defer authority.Close()
	cs := NewCredStore()
	cs.Load([]Credential{{ID: "parent-account", Provider: ProviderAnthropic, Token: "parent-synthetic-key", AgentIDs: []string{"parent"}}})
	s := &Server{ipc: &IPCConfig{BaseURL: authority.URL, Token: "crew-ipc", WorkspaceID: "ws", CrewID: "crew"}, routeAuth: &RouteAuth{Key: "route-key"}, runs: newRunRegistry(), logger: covLogger(), scrubber: scrubber.New()}
	s.runs.start("parent-run", runState{AgentID: "parent"})
	s.runs.start("peer-run", runState{AgentID: "peer"})
	started, release := make(chan struct{}), make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	p := NewProxy(ProxyConfig{CredStore: cs, Allowlist: NewDomainAllowlist(nil), Logger: covLogger(), ConfigFingerprint: "parent-fp", ResolveLLMIdentity: s.llmRouteIdentity, ResolveBedrock: s.resolveBedrock})
	p.transport = roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host == "api.anthropic.com" {
			if r.Header.Get("x-api-key") != "parent-synthetic-key" {
				t.Error("parent account changed")
			}
			close(started)
			<-release
		} else if r.URL.Host != "bedrock-runtime.eu-central-1.amazonaws.com" || r.Header.Get("Authorization") != "Bearer peer-synthetic-secret" {
			t.Error("wrong peer account or destination")
		}
		return jsonUpstreamResponse(200, "application/json", `{"usage":{"inputTokens":1,"outputTokens":1}}`, nil), nil
	})
	request := func(agent, run, route, account string) *http.Request {
		r := httptest.NewRequest("POST", "http://localhost"+route, strings.NewReader(`{}`))
		r.RemoteAddr = "127.0.0.1:4444"
		token := internaltoken.DeriveLLMRunRouteToken("route-key", agent, run) + internaltoken.RouteFingerprintDelimiter + "parent-fp"
		r.Header.Set("Authorization", "Bearer dummy."+token)
		r.Header.Set("x-api-key", "dummy."+token)
		r.Header.Set(bedrock.AccountHeader, account)
		return r
	}
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		w := httptest.NewRecorder()
		p.ServeHTTP(w, request("parent", "parent-run", "/v1/messages", ""))
		done <- w
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("parent did not start")
	}
	route := "/llm/amazon-bedrock/model/" + account.Models[0] + "/converse"
	w := httptest.NewRecorder()
	p.ServeHTTP(w, request("peer", "peer-run", route, "peer-account"))
	if w.Code != 200 || strings.Contains(w.Body.String(), account.APIKey) {
		t.Errorf("peer failed: %d %s", w.Code, w.Body.String())
	}
	if p.configFingerprint != "parent-fp" || cs.CountsByProvider()[bedrock.Provider] != 0 {
		t.Error("peer mutated parent credentials/fingerprint")
	}
	revoked.Store(true)
	w = httptest.NewRecorder()
	p.ServeHTTP(w, request("peer", "peer-run", route, "peer-account"))
	if w.Code == 200 {
		t.Error("revoked authority fell back to cached account")
	}
	before := authorityCalls.Load()
	s.runs.end("peer-run")
	w = httptest.NewRecorder()
	p.ServeHTTP(w, request("peer", "peer-run", route, "peer-account"))
	if w.Code != 403 || authorityCalls.Load() != before {
		t.Error("ended peer reached credential authority")
	}
	close(release)
	select {
	case w := <-done:
		if w.Code != 200 {
			t.Errorf("parent interrupted: %d %s", w.Code, w.Body.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("parent did not finish")
	}
}

func TestBedrockSecretResolutionCannotBeForwardedToAgent(t *testing.T) {
	s := &Server{}
	for _, route := range []string{bedrock.ResolvePath, bedrock.ResolvePath + "?x=y", "/api/v1/internal/bedrock/../bedrock/resolve", "/api/v1/internal/bedrock/%72esolve"} {
		for _, headers := range []bool{false, true} {
			w := httptest.NewRecorder()
			r := httptest.NewRequest("POST", "/agent", nil)
			if headers {
				s.proxyIPCJSONHeaders(w, r, "POST", route, "test", nil, nil)
			} else {
				s.proxyToAPI(w, r, "POST", route)
			}
			if w.Code != 403 {
				t.Fatalf("agent forwarder accepted %s: %d", route, w.Code)
			}
		}
	}
	r := httptest.NewRequest("POST", "/agent", nil)
	if _, err := s.resolveBedrock(r, "peer", "account", "model"); err == nil {
		t.Fatal("caller without authenticated run reached resolver")
	}
	transport, ok := bedrockAuthorityClient.Transport.(*http.Transport)
	if !ok || transport.Proxy != nil || bedrockAuthorityClient.CheckRedirect(nil, nil) != http.ErrUseLastResponse {
		t.Fatal("secret IPC allows ambient proxy or redirects")
	}
}

func TestBedrockAuthorityAdmissionIsBounded(t *testing.T) {
	for range cap(bedrockAuthoritySlots) {
		bedrockAuthoritySlots <- struct{}{}
	}
	defer func() {
		for range cap(bedrockAuthoritySlots) {
			<-bedrockAuthoritySlots
		}
	}()
	s := &Server{ipc: &IPCConfig{BaseURL: "http://must-not-be-contacted.invalid", Token: "crew-token", CrewID: "crew"}}
	r := httptest.NewRequest("POST", "/llm/amazon-bedrock", nil)
	r = r.WithContext(context.WithValue(r.Context(), authenticatedLLMRunKey{}, "run"))
	if _, err := s.resolveBedrock(r, "agent", "account", "model"); err == nil || !strings.Contains(err.Error(), "busy") {
		t.Fatal("unbounded authority admission", err)
	}
}
