package llmroute

import (
	"fmt"
	"strings"
)

// registry, order and the three lookup indexes are written ONLY by the func
// init() in this file, and read from the sidecar's per-request path. There is
// no mutex, and that is deliberate — the same trade internal/llm's registry
// makes: making registration concurrent-safe would put an RLock on the hot
// read for the benefit of a write that only happens at package init, before
// main runs.
//
// The rule this buys: register is init-only. The way to add a provider is a
// line in the init below.
var (
	registry = map[string]Spec{}
	order    []string
	byPrefix = map[string]string{} // PathPrefix -> ID
	byHost   = map[string]string{} // lowercased host -> ID
	byEnvVar = map[string]string{} // env var name -> ID
)

// grandfatheredPrefixes are the three loopback prefixes that existed before
// this table did. They are exempt from the reserved-segment rule below because
// they are the reason those three segments are reserved in the first place.
var grandfatheredPrefixes = map[string]bool{
	"/v1":     true,
	"/openai": true,
	"/gemini": true,
}

// reservedSegments lists every top-level path segment the sidecar's own
// control plane claims in internal/sidecar/server.go, plus the three
// grandfathered proxy prefixes.
//
// The sidecar's loopback path namespace is shared between ~30 control-plane
// routes and the LLM proxy, and the control plane is matched FIRST. A provider
// prefix that collided with one would be a route-shadowing primitive — which
// is also why a new provider may only claim a path under "/llm/", where a
// single lookup decides the route and no arm-ordering hazard exists.
//
// WS3 owns the test that keeps this list honest against the sidecar's real
// route set; it cannot live here, because a leaf package cannot import the
// package it is describing.
var reservedSegments = []string{
	"agent",
	"assign",
	"connections",
	"credentials",
	"crew",
	"crew-connections",
	"crews",
	"escalate",
	"expose-port",
	"gemini",
	"health",
	"healthz",
	"issue",
	"issues",
	"keeper",
	"manifest",
	"mcp",
	"memory",
	"mission",
	"openai",
	"pages",
	"pipelines",
	"query",
	"report-confidence",
	"results",
	"routines",
	"skills",
	"spawn",
	"standup",
	"v1",
}

// bodyCodecs is the closed set of response-body shapes parseLLMUsage can read.
// "" means the provider's responses are not parsed for usage at all.
var bodyCodecs = map[string]bool{
	"":          true,
	"anthropic": true,
	"openai":    true,
	"google":    true,
}

// ReservedPathSegments returns the top-level loopback path segments a provider
// prefix may not claim, sorted, as a copy.
func ReservedPathSegments() []string {
	out := make([]string, len(reservedSegments))
	copy(out, reservedSegments)
	return out
}

// register adds spec to the table. Init-only — see the note on registry.
//
// It panics rather than returning an error for the same reason
// llm.RegisterProvider does: every failure it can see is a programming mistake
// in a package-level init, and a provider that silently failed to register
// would surface much later as a 404 on a loopback path the code plainly
// declares — or, worse, as a credential quietly dropped between the vault and
// the sidecar.
//
// Every check runs BEFORE the first write, so a panicking register leaves the
// table exactly as it found it. That is what lets the validator's own tests
// call register with bad specs without polluting the real table.
func register(s Spec) {
	validateIdentity(s)
	validateAuthRules(s)
	validateUpstream(s)
	validatePathPrefix(s)
	validateIndexes(s)

	registry[s.ID] = s
	order = append(order, s.ID)
	byPrefix[s.PathPrefix] = s.ID
	for _, h := range s.Hosts {
		byHost[strings.ToLower(h)] = s.ID
	}
	for _, e := range s.KeyEnvVars {
		byEnvVar[e] = s.ID
	}
}

// validateIdentity checks the three identity fields whose casing is a wire
// contract with another system: ID is the CredStore key and the boot payload's
// `provider`, LedgerProvider is the paymaster rate-card key, and BodyCodec is
// parseLLMUsage's switch value.
func validateIdentity(s Spec) {
	switch {
	case s.ID == "":
		panic("llmroute: register: empty ID")
	case s.ID != strings.ToUpper(s.ID):
		panic(fmt.Sprintf("llmroute: register(%q): ID must be UPPERCASE", s.ID))
	case s.LedgerProvider == "":
		panic(fmt.Sprintf("llmroute: register(%q): empty LedgerProvider", s.ID))
	case s.LedgerProvider != strings.ToLower(s.LedgerProvider):
		panic(fmt.Sprintf("llmroute: register(%q): LedgerProvider %q must be lowercase", s.ID, s.LedgerProvider))
	case !bodyCodecs[s.BodyCodec]:
		panic(fmt.Sprintf("llmroute: register(%q): unknown BodyCodec %q", s.ID, s.BodyCodec))
	}
	if _, dup := registry[s.ID]; dup {
		panic(fmt.Sprintf("llmroute: register(%q): duplicate provider id", s.ID))
	}
}

// validateIndexes refuses a spec that would make an existing host or env var
// resolve to two providers. Both lookups are single-valued, so the second
// claimant would otherwise silently take over the first one's traffic.
func validateIndexes(s Spec) {
	for _, h := range s.Hosts {
		if other, dup := byHost[strings.ToLower(h)]; dup {
			panic(fmt.Sprintf("llmroute: register(%q): host %q already claimed by %q", s.ID, h, other))
		}
	}
	for _, e := range s.KeyEnvVars {
		if other, dup := byEnvVar[e]; dup {
			panic(fmt.Sprintf("llmroute: register(%q): env var %q already claimed by %q", s.ID, e, other))
		}
	}
}

// validateAuthRules enforces the one property ApplyAuth relies on: every token
// matches exactly one rule. Without the trailing default rule a token could
// fall through the whole set and be forwarded upstream carrying whatever dummy
// the agent env supplied.
func validateAuthRules(s Spec) {
	if len(s.AuthRules) == 0 {
		panic(fmt.Sprintf("llmroute: register(%q): no AuthRules", s.ID))
	}
	defaults := 0
	for i, r := range s.AuthRules {
		if len(r.Slots) == 0 {
			panic(fmt.Sprintf("llmroute: register(%q): AuthRule %d has no Slots", s.ID, i))
		}
		for _, slot := range r.Slots {
			if slot.Name == "" {
				panic(fmt.Sprintf("llmroute: register(%q): AuthRule %d has an unnamed slot", s.ID, i))
			}
			if slot.Placement != PlaceHeader && slot.Placement != PlaceQuery {
				panic(fmt.Sprintf("llmroute: register(%q): AuthRule %d slot %q has unknown Placement %q", s.ID, i, slot.Name, slot.Placement))
			}
		}
		if r.TokenPrefix != "" {
			continue
		}
		defaults++
		if i != len(s.AuthRules)-1 {
			panic(fmt.Sprintf("llmroute: register(%q): the default AuthRule must be last", s.ID))
		}
	}
	if defaults != 1 {
		panic(fmt.Sprintf("llmroute: register(%q): want exactly 1 default AuthRule, have %d", s.ID, defaults))
	}
}

// validateUpstream enforces that a spec names exactly one source of truth for
// where the request goes, and that a credential-supplied one is never optional.
func validateUpstream(s Spec) {
	if s.UpstreamFromCredential == (s.UpstreamHost != "") {
		panic(fmt.Sprintf("llmroute: register(%q): set exactly one of UpstreamHost or UpstreamFromCredential", s.ID))
	}
	if s.UpstreamFromCredential && !s.RequireCredential {
		// Without the credential there is no upstream at all, so "forward it
		// anyway" is not a behaviour this spec can have.
		panic(fmt.Sprintf("llmroute: register(%q): UpstreamFromCredential requires RequireCredential", s.ID))
	}
}

// validatePathPrefix enforces the loopback namespace rules (see
// reservedSegments).
func validatePathPrefix(s Spec) {
	p := s.PathPrefix
	if !strings.HasPrefix(p, "/") {
		panic(fmt.Sprintf("llmroute: register(%q): PathPrefix %q must start with /", s.ID, p))
	}
	if strings.HasSuffix(p, "/") {
		panic(fmt.Sprintf("llmroute: register(%q): PathPrefix %q must not end with /", s.ID, p))
	}
	if other, dup := byPrefix[p]; dup {
		panic(fmt.Sprintf("llmroute: register(%q): PathPrefix %q already claimed by %q", s.ID, p, other))
	}
	if grandfatheredPrefixes[p] {
		return
	}
	// Reserved is checked before the shape rule so both rules stay reachable:
	// "/credentials" trips the reserved segment, "/anything-else" trips the
	// shape rule.
	first := strings.SplitN(strings.TrimPrefix(p, "/"), "/", 2)[0]
	for _, seg := range reservedSegments {
		if first == seg {
			panic(fmt.Sprintf("llmroute: register(%q): PathPrefix %q claims reserved segment %q", s.ID, p, seg))
		}
	}
	if !strings.HasPrefix(p, "/llm/") {
		panic(fmt.Sprintf("llmroute: register(%q): PathPrefix %q must be under /llm/", s.ID, p))
	}
}

// Specs returns every spec in DECLARATION order, deep-copied.
//
// Declaration order because this list is what `crewship provider route list`
// renders and what WS3 generates its per-descriptor isolation tests from;
// sorting would reorder both for no reason anyone could point at. Deep copies
// because this is the inspect-the-table entry point — unlike the lookups,
// which sit on a per-request path and hand back read-only shallow copies.
func Specs() []Spec {
	out := make([]Spec, 0, len(order))
	for _, id := range order {
		out = append(out, cloneSpec(registry[id]))
	}
	return out
}

func cloneSpec(s Spec) Spec {
	c := s
	if s.Hosts != nil {
		c.Hosts = append([]string(nil), s.Hosts...)
	}
	if s.KeyEnvVars != nil {
		c.KeyEnvVars = append([]string(nil), s.KeyEnvVars...)
	}
	if s.StaticHeaders != nil {
		c.StaticHeaders = make(map[string]string, len(s.StaticHeaders))
		for k, v := range s.StaticHeaders {
			c.StaticHeaders[k] = v
		}
	}
	if s.AuthRules != nil {
		c.AuthRules = make([]AuthRule, len(s.AuthRules))
		for i, r := range s.AuthRules {
			r.Slots = append([]AuthSlot(nil), r.Slots...)
			c.AuthRules[i] = r
		}
	}
	return c
}

// Lookup resolves an ID to its spec. Exact and case-sensitive: the ID is a
// wire value carried in the boot payload and stored in the credentials table,
// and quietly accepting "OpenRouter" for "OPENROUTER" would make two spellings
// of the same credential route differently depending on which side read it.
func Lookup(id string) (Spec, bool) {
	s, ok := registry[id]
	return s, ok
}

// LookupProvider resolves a credential's `provider` COLUMN to its spec. Unlike
// Lookup it folds case and trims, because that column is not a wire value: it
// is free text an operator types into the dashboard, a REST client or the CLI,
// and the server has never rejected an unrecognised one.
//
// The two functions are deliberately different. A spec ID that arrives
// mis-cased on the wire means two components disagree and should fail loudly.
// A provider column that arrives mis-cased means a human typed "openai_compat",
// and the alternative to accepting it is what actually happened: the credential
// stored successfully, skipped the endpoint-shape gate on the way in, skipped
// the value split on the way out, and was then dropped from the CredStore
// without an error anywhere. It validated nothing and routed nowhere, and the
// only signal was a server-side log line. Only the CLI normalised; the web UI
// and every API client did not.
func LookupProvider(provider string) (Spec, bool) {
	return Lookup(strings.ToUpper(strings.TrimSpace(provider)))
}

// ProviderCarriesUpstream reports whether a credential for this provider stores
// {baseURL, apiKey, headers} as one object rather than a bare token — i.e.
// whether its value must be split before any of it goes near an auth header.
//
// This lives here, on the table that owns the UpstreamFromCredential field,
// because the consumers are in three packages that cannot import each other:
// the API tier's storage gate, the orchestrator's routing decision, and the
// pipeline runner's http-step resolver. The third of those was written when
// `type = 'API_KEY'` still implied "bare token" and had no reason to ask.
func ProviderCarriesUpstream(provider string) bool {
	s, ok := LookupProvider(provider)
	return ok && s.UpstreamFromCredential
}

// MatchPath resolves a loopback request path to the provider that claims it,
// longest prefix wins.
//
// A path matches a prefix only at a segment boundary — "/v1/messages" matches
// "/v1", "/v1beta/models" does not. That reproduces the trailing-slash
// HasPrefix checks handleLocal used ("/v1/", "/openai/", "/gemini/") exactly,
// including their 404 for a request to the bare prefix.
//
// Longest-prefix rather than declaration order is what stops a new provider
// from being swallowed by Anthropic's "/v1" catch-all: the route a request
// takes is a property of the table, not of the order its rows were written in.
func MatchPath(path string) (Spec, bool) {
	best := ""
	for prefix := range byPrefix {
		if len(prefix) <= len(best) {
			continue
		}
		if strings.HasPrefix(path, prefix+"/") {
			best = prefix
		}
	}
	if best == "" {
		return Spec{}, false
	}
	return registry[byPrefix[best]], true
}

// MatchHost resolves an upstream hostname to the provider that owns it, for
// the forward-proxy (HTTP_PROXY) path. The caller lowercases and strips the
// port; this is an exact map lookup on the hot egress path.
//
// Only the three grandfathered providers populate Hosts — see Spec.Hosts.
func MatchHost(host string) (Spec, bool) {
	id, ok := byHost[host]
	if !ok {
		return Spec{}, false
	}
	return registry[id], true
}

// LookupByEnvVar resolves an agent-facing API-key env var name to its
// provider. This is the back-compat identification path: it is how a
// credential whose `provider` column predates this table still reaches the
// right CredStore slot.
func LookupByEnvVar(env string) (Spec, bool) {
	id, ok := byEnvVar[env]
	if !ok {
		return Spec{}, false
	}
	return registry[id], true
}

// The built-in providers.
//
// The first three rows are a re-description of what handleLocal,
// injectCredential, providerForHost and /health already did, field for field —
// they are not a behaviour change, and RequireCredential is false on all three
// because that is what proxy.go did (a nil credential fell through to a
// pass-through forward, which is the Anthropic OAuth path).
//
// The last two are new, and both set RequireCredential: for OpenRouter because
// there is no env-carried token to fall back on, and for the generic endpoint
// because without the credential there is no upstream to forward to.
func init() {
	register(Spec{
		ID:             "ANTHROPIC",
		DisplayName:    "Anthropic",
		LedgerProvider: "anthropic",
		BodyCodec:      "anthropic",
		// Claude Code points ANTHROPIC_BASE_URL at http://127.0.0.1:9119 and
		// sends /v1/messages, so the prefix IS the upstream path and must not
		// be stripped.
		PathPrefix:   "/v1",
		StripPrefix:  false,
		Hosts:        []string{"api.anthropic.com"},
		UpstreamHost: "api.anthropic.com",
		StaticHeaders: map[string]string{
			"anthropic-version": "2023-06-01",
		},
		AuthRules: []AuthRule{
			// An OAuth token authenticates as a Bearer; an API key uses
			// x-api-key. The token says which, so the router doesn't have to.
			{TokenPrefix: "sk-ant-oat", Slots: []AuthSlot{
				{Placement: PlaceHeader, Name: "Authorization", Prefix: "Bearer "},
			}},
			{Slots: []AuthSlot{
				{Placement: PlaceHeader, Name: "x-api-key"},
			}},
		},
		KeyEnvVars:      []string{"ANTHROPIC_API_KEY"},
		LegacyHealthKey: "anthropic_creds",
	})
	register(Spec{
		ID:             "OPENAI",
		DisplayName:    "OpenAI",
		LedgerProvider: "openai",
		BodyCodec:      "openai",
		// Codex reaches this via OPENAI_BASE_URL=http://127.0.0.1:9119/openai/v1,
		// so requests arrive as /openai/v1/… and the routing prefix — which
		// exists only to disambiguate from Anthropic's /v1 on the shared
		// port — is stripped before forwarding.
		PathPrefix:      "/openai",
		StripPrefix:     true,
		Hosts:           []string{"api.openai.com"},
		UpstreamHost:    "api.openai.com",
		AuthRules:       []AuthRule{{Slots: []AuthSlot{{Placement: PlaceHeader, Name: "Authorization", Prefix: "Bearer "}}}},
		KeyEnvVars:      []string{"OPENAI_API_KEY"},
		LegacyHealthKey: "openai_creds",
	})
	register(Spec{
		ID:             "GOOGLE",
		DisplayName:    "Google Gemini",
		LedgerProvider: "google",
		BodyCodec:      "google",
		// The Gemini CLI reaches this via
		// GOOGLE_GEMINI_BASE_URL=http://127.0.0.1:9119/gemini; the @google/genai
		// SDK appends /v1beta/… to the base URL.
		PathPrefix:   "/gemini",
		StripPrefix:  true,
		Hosts:        []string{"generativelanguage.googleapis.com"},
		UpstreamHost: "generativelanguage.googleapis.com",
		AuthRules: []AuthRule{{Slots: []AuthSlot{
			// Both slots, same value: the SDK sends the header and other
			// clients send ?key=, and whichever we left alone would reach the
			// upstream still carrying the agent's dummy.
			{Placement: PlaceHeader, Name: "x-goog-api-key"},
			{Placement: PlaceQuery, Name: "key"},
		}}},
		KeyEnvVars:      []string{"GOOGLE_API_KEY", "GEMINI_API_KEY"},
		LegacyHealthKey: "google_creds",
	})
	register(Spec{
		ID:             "OPENROUTER",
		DisplayName:    "OpenRouter",
		LedgerProvider: "openrouter",
		// OpenRouter speaks the OpenAI wire format but bills under its own
		// rate card (internal/paymaster/pricing.go has an "openrouter" row).
		BodyCodec:   "openai",
		PathPrefix:  "/llm/openrouter",
		StripPrefix: true,
		// Hosts is deliberately nil: openrouter.ai is already on
		// egressallow.DefaultAllowedDomains, and claiming it here would turn
		// today's pass-through into a 503 for every BYOK crew that dials it
		// with its own key.
		UpstreamHost:      "openrouter.ai",
		UpstreamBasePath:  "/api/v1",
		RequireCredential: true,
		AuthRules:         []AuthRule{{Slots: []AuthSlot{{Placement: PlaceHeader, Name: "Authorization", Prefix: "Bearer "}}}},
		KeyEnvVars:        []string{"OPENROUTER_API_KEY"},
	})
	register(Spec{
		ID:          "OPENAI_COMPAT",
		DisplayName: "OpenAI-compatible endpoint",
		// No rate row exists — and none should be invented, because the price
		// of a bring-your-own endpoint's tokens is not something this project
		// can know. Calls bill at $0 and the docs say so.
		LedgerProvider:         "openai-compat",
		BodyCodec:              "openai",
		PathPrefix:             "/llm/openai-compat",
		StripPrefix:            true,
		UpstreamFromCredential: true,
		RequireCredential:      true,
		AuthRules:              []AuthRule{{Slots: []AuthSlot{{Placement: PlaceHeader, Name: "Authorization", Prefix: "Bearer "}}}},
		// No KeyEnvVars: this is an endpoint, not a key an agent CLI reads
		// from a conventional variable, so there is no env-var name that
		// identifies it. It is reached through the credential's provider
		// column only.
	})
}
