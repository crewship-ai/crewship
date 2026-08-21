package sidecar

// The sidecar's loopback listener has ONE path namespace, shared between ~30
// control-plane routes (matched first, in server.go's buildHandler) and the
// provider reverse-proxy prefixes (matched after, in handleLocal). A provider
// prefix that collided with a control route would be a route-shadowing
// primitive, and the collision would be silent — the control route would
// simply stop being reachable, or a provider's traffic would be answered by
// /credentials.
//
// internal/llmroute enforces "no provider may claim a reserved segment" at
// registration time, but a leaf package cannot import the package it is
// describing, so its reserved list is a hand-maintained copy. This file is
// what keeps that copy honest: it derives the real route set from the sidecar
// source and fails when the two drift — which is what happens when someone
// adds a control route and nobody thinks about the descriptor table.

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/llmroute"
)

// routeLiteralRe matches the three shapes the sidecar uses to select a handler
// by path. Restricted to those contexts on purpose: a bare "/…" string literal
// elsewhere in the package is a filesystem path or an IPC URL, not a route this
// listener claims.
var routeLiteralRe = regexp.MustCompile(`(?:r\.URL\.Path == |strings\.(?:HasPrefix|TrimPrefix)\(r\.URL\.Path, |path == |strings\.(?:HasPrefix|TrimPrefix)\(path, )"(/[a-zA-Z0-9._/-]*)"`)

// controlPlaneRouteSegments derives the set of TOP-LEVEL path segments the
// sidecar's own handlers claim, by scanning the package's non-test sources.
func controlPlaneRouteSegments(t *testing.T) map[string][]string {
	t.Helper()

	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob package sources: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("no .go files found in the package directory; this guard would pass vacuously")
	}

	segments := map[string][]string{}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		for _, m := range routeLiteralRe.FindAllStringSubmatch(string(src), -1) {
			seg := strings.SplitN(strings.TrimPrefix(m[1], "/"), "/", 2)[0]
			if seg == "" {
				continue // "/" — the root, claimed by nobody
			}
			segments[seg] = append(segments[seg], f+": "+m[1])
		}
	}
	if len(segments) == 0 {
		t.Fatalf("derived zero control-plane routes from %d source files; the regex has drifted from the code and this guard is no longer guarding anything", len(files))
	}
	return segments
}

// TestDescriptorPrefixesDisjointFromControlPlane: no provider prefix may share
// a first segment with a control route. /v1, /openai and /gemini are safe
// today; the assertion exists for the next row added to the table.
func TestDescriptorPrefixesDisjointFromControlPlane(t *testing.T) {
	control := controlPlaneRouteSegments(t)

	specs := llmroute.Specs()
	if len(specs) == 0 {
		t.Fatal("llmroute.Specs() is empty; this guard would pass vacuously")
	}
	for _, s := range specs {
		seg := strings.SplitN(strings.TrimPrefix(s.PathPrefix, "/"), "/", 2)[0]
		if where, clash := control[seg]; clash {
			t.Errorf("provider %s claims %q, whose first segment %q is already a sidecar control route (%s). "+
				"The control plane matches FIRST, so this provider is unreachable — and if the order ever changes, the control route is",
				s.ID, s.PathPrefix, seg, strings.Join(where, ", "))
		}
	}
}

// TestReservedPathSegments_CoversEveryControlRoute is the tripwire on
// llmroute's hand-maintained reserved list. Registration refuses a provider
// prefix whose first segment is reserved; a control route MISSING from that
// list is a segment a future provider could claim, and the failure would only
// show up as a control-plane route that quietly stopped answering.
func TestReservedPathSegments_CoversEveryControlRoute(t *testing.T) {
	reserved := map[string]bool{}
	for _, s := range llmroute.ReservedPathSegments() {
		reserved[s] = true
	}
	if len(reserved) == 0 {
		t.Fatal("llmroute.ReservedPathSegments() is empty; this guard would pass vacuously")
	}

	var missing []string
	for seg, where := range controlPlaneRouteSegments(t) {
		if !reserved[seg] {
			missing = append(missing, seg+" ("+strings.Join(where, ", ")+")")
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("these sidecar control-plane segments are not in llmroute.ReservedPathSegments(), so a provider could register a prefix that shadows them:\n  %s",
			strings.Join(missing, "\n  "))
	}
}

// TestHandleLocal_RoutingIsolation_AllDescriptors is generated from Specs(),
// so a sixth provider gets isolation coverage for free rather than needing
// someone to remember to add a case.
//
// For every registered provider it drives a request on that provider's prefix
// with a credential for EVERY provider in the store, and asserts the request
// reached that provider's upstream and no other. A descriptor whose prefix is
// swallowed by a longer/earlier one shows up here as a wrong host.
func TestHandleLocal_RoutingIsolation_AllDescriptors(t *testing.T) {
	specs := llmroute.Specs()
	if len(specs) == 0 {
		t.Fatal("llmroute.Specs() is empty; this guard would pass vacuously")
	}

	// One credential per provider, all loaded at once, so a router that picked
	// the wrong spec would still find a usable credential and silently
	// misroute rather than 503.
	var creds []Credential
	for i, s := range specs {
		c := Credential{
			ID:       "cred-" + s.ID,
			Provider: ProviderType(s.ID),
			Token:    "tok-" + strings.ToLower(s.ID) + "-000000000000",
			Priority: i,
		}
		if s.UpstreamFromCredential {
			c.BaseURL = "https://endpoint-" + strings.ToLower(s.ID) + ".example/v1"
		}
		creds = append(creds, c)
	}

	for _, s := range specs {
		t.Run(s.ID, func(t *testing.T) {
			cs := NewCredStore()
			cs.Load(creds)
			var upstream *http.Request
			proxy := NewProxy(ProxyConfig{
				CredStore: cs,
				Allowlist: NewDomainAllowlist(nil),
				Logger:    covLogger(),
				FreeMode:  true, // isolation is the subject here, not egress
			})
			proxy.transport = roundTripperFunc(func(r *http.Request) (*http.Response, error) {
				upstream = r
				return jsonUpstreamResponse(http.StatusOK, "application/json", `{}`, nil), nil
			})

			req := httptest.NewRequest("POST", "http://127.0.0.1:9119"+s.PathPrefix+"/probe/endpoint", strings.NewReader(`{}`))
			req.Host = "127.0.0.1:9119"
			req.RemoteAddr = "127.0.0.1:54321"
			w := httptest.NewRecorder()
			proxy.ServeHTTP(w, req)

			if upstream == nil {
				t.Fatalf("%s: %s%s reached no upstream (status %d)", s.ID, s.PathPrefix, "/probe/endpoint", w.Code)
			}

			wantHost := s.UpstreamHost
			if s.UpstreamFromCredential {
				wantHost = "endpoint-" + strings.ToLower(s.ID) + ".example"
			}
			if upstream.URL.Host != wantHost {
				t.Errorf("%s%s routed to %q, want %q — another descriptor swallowed this prefix",
					s.PathPrefix, "/probe/endpoint", upstream.URL.Host, wantHost)
			}
			// The token that reached the wire must be THIS provider's.
			wantTok := "tok-" + strings.ToLower(s.ID) + "-000000000000"
			if !requestCarriesToken(upstream, wantTok) {
				t.Errorf("%s: no auth slot carries this provider's token; headers=%v query=%v", s.ID, upstream.Header, upstream.URL.RawQuery)
			}
			for _, other := range specs {
				if other.ID == s.ID {
					continue
				}
				otherTok := "tok-" + strings.ToLower(other.ID) + "-000000000000"
				if requestCarriesToken(upstream, otherTok) {
					t.Errorf("%s request carries %s's token — credential cross-contamination", s.ID, other.ID)
				}
			}
		})
	}
}

// requestCarriesToken reports whether tok appears in any header value or in
// the query string — the two placements an AuthSlot can use.
func requestCarriesToken(r *http.Request, tok string) bool {
	for _, vv := range r.Header {
		for _, v := range vv {
			if strings.Contains(v, tok) {
				return true
			}
		}
	}
	return strings.Contains(r.URL.RawQuery, tok)
}

// TestHandleLocal_PrefixPrecedence_CompatNotSwallowedByV1 is the sharpest
// collision the old switch-arm ordering was vulnerable to, stated on its own:
// Anthropic's /v1 is a catch-all, and an OpenAI-compatible endpoint's natural
// path shape is /v1/chat/completions. The descriptor's prefixes keep them
// apart because matching is longest-prefix over the table, not first-arm-wins.
func TestHandleLocal_PrefixPrecedence_CompatNotSwallowedByV1(t *testing.T) {
	cs := NewCredStore()
	cs.Load([]Credential{
		{ID: "a1", Provider: ProviderAnthropic, Token: "sk-ant-api03-ANTHROPIC-TOKEN"},
		{ID: "c1", Provider: ProviderOpenAICompat, Token: "sk-compat-TOKEN-0000", BaseURL: "https://llm.internal.example/v1"},
	})
	var upstream *http.Request
	proxy := NewProxy(ProxyConfig{
		CredStore: cs,
		Allowlist: NewDomainAllowlist(nil),
		Logger:    covLogger(),
		FreeMode:  true,
	})
	proxy.transport = roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		upstream = r
		return jsonUpstreamResponse(http.StatusOK, "application/json", `{}`, nil), nil
	})

	req := httptest.NewRequest("POST", "http://127.0.0.1:9119/llm/openai-compat/v1/chat/completions", strings.NewReader(`{}`))
	req.Host = "127.0.0.1:9119"
	req.RemoteAddr = "127.0.0.1:54321"
	proxy.ServeHTTP(httptest.NewRecorder(), req)

	if upstream == nil {
		t.Fatal("no upstream request")
	}
	if upstream.URL.Host != "llm.internal.example" {
		t.Errorf("routed to %q, want llm.internal.example: Anthropic's /v1 catch-all swallowed the compat prefix", upstream.URL.Host)
	}
	if strings.Contains(upstream.Header.Get("x-api-key"), "ANTHROPIC-TOKEN") {
		t.Error("the Anthropic credential was injected into a compat-endpoint request")
	}
}
