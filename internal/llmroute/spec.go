package llmroute

// AuthPlacement names where in the outbound request a token is written.
type AuthPlacement string

const (
	// PlaceHeader writes the token as a request header.
	PlaceHeader AuthPlacement = "header"
	// PlaceQuery writes the token as a URL query parameter.
	PlaceQuery AuthPlacement = "query"
)

// AuthSlot is one place a token is written.
//
// Semantics are ALWAYS overwrite, never conditionally-set: the agent env
// carries a dummy in most of these slots (exec_env.go sets
// GEMINI_API_KEY=dummy-crewship-sidecar, OPENAI_API_KEY=sk-dummy-…), so an
// unwritten slot is a dummy LEAK, not an absence. Google needs two slots for
// exactly this reason — the @google/genai SDK sends x-goog-api-key while other
// clients send ?key=, and whichever one we left alone would still be carrying
// the dummy when the request reached the upstream.
type AuthSlot struct {
	// Placement selects header or query. REQUIRED.
	Placement AuthPlacement
	// Name is the header name or the query-parameter name. REQUIRED.
	Name string
	// Prefix is written ahead of the token — "Bearer " or "".
	Prefix string
}

// AuthRule selects a slot set by the SHAPE OF THE TOKEN ITSELF. This is how
// Anthropic's OAuth-vs-api-key branch stops being a special case in the
// router: "sk-ant-oat…" is not an Anthropic fact, it is "this provider accepts
// two auth shapes and the secret tells you which".
//
// TokenPrefix == "" is the default rule and MUST be last. Registration
// enforces that there is exactly one of them, so a token can never fall
// through a provider's rule set unauthenticated — the fail-open default that
// injectCredential's three-arm switch had for CURSOR and FACTORY.
type AuthRule struct {
	// TokenPrefix selects this rule when the token starts with it. Empty
	// marks the default rule.
	TokenPrefix string
	// Slots are every place this rule writes the token. REQUIRED, non-empty.
	Slots []AuthSlot
}

// Spec is one row of the route table: everything the sidecar needs to take a
// request off the loopback listener, authenticate it, and put it on the wire.
//
// A Spec returned by Lookup / MatchPath / MatchHost / LookupByEnvVar is a
// shallow copy: its Slots, Hosts, KeyEnvVars and StaticHeaders share backing
// storage with the registry, so they are READ-ONLY. (Those lookups sit on the
// sidecar's per-request path, and deep-copying four collections per proxied
// request to defend against a mutation nobody performs is the wrong trade.)
// Specs(), which exists for inspection rather than routing, deep-copies.
type Spec struct {
	// ── identity ──

	// ID is the CredStore key AND the boot-payload `provider` value.
	// UPPERCASE. REQUIRED.
	ID string
	// DisplayName is the human casing used in CLI output ("OpenRouter").
	DisplayName string

	// LedgerProvider is the lowercase key sent to /internal/cost/record and
	// thence to paymaster.Estimate. REQUIRED.
	//
	// A spec whose LedgerProvider has no rate row in
	// internal/paymaster/pricing.go bills every call at $0 — which is the
	// honest outcome for a bring-your-own endpoint whose prices we cannot
	// know, and a bug for anything else.
	LedgerProvider string

	// BodyCodec names which response-body shape parseLLMUsage should read:
	// "anthropic" | "openai" | "google" | "" (no usage parsing).
	//
	// DISTINCT from LedgerProvider, and that distinction is the whole reason
	// the two fields exist rather than one: OpenRouter speaks the OpenAI body
	// shape but bills under its own rate card.
	BodyCodec string

	// ── routing: agent → sidecar (reverse-proxy path) ──

	// PathPrefix is the exact loopback path prefix this provider claims, with
	// a leading slash and no trailing slash. REQUIRED. Either one of the three
	// grandfathered literals ("/v1", "/openai", "/gemini") or under "/llm/".
	PathPrefix string
	// StripPrefix removes PathPrefix from the outbound path. False forwards
	// the path verbatim — Anthropic's "/v1/…" IS the upstream path.
	StripPrefix bool

	// ── routing: host → provider (forward-proxy path) ──

	// Hosts are the upstream hostnames that identify this provider when the
	// agent dials it through HTTP_PROXY rather than the reverse-proxy path.
	//
	// Populated ONLY for the three grandfathered providers. A new provider
	// leaves this nil ON PURPOSE: mapping a host flips handleHTTP from
	// pass-through to a hard 503 when no credential is present, which would
	// break every existing BYOK crew that dials that host with its own key in
	// the agent env. See TestSpecs_NewProvidersClaimNoHosts.
	Hosts []string

	// ── upstream ──

	// UpstreamHost is the fixed host for a built-in provider. Empty exactly
	// when UpstreamFromCredential is set.
	UpstreamHost string
	// UpstreamBasePath is joined ahead of the (possibly stripped) request
	// path — e.g. "/api/v1" for OpenRouter.
	UpstreamBasePath string
	// UpstreamFromCredential takes the upstream from the credential's base
	// URL instead of this table. Implies RequireCredential: for a
	// credential-supplied upstream, no credential means there is no upstream.
	UpstreamFromCredential bool

	// RequireCredential makes a nil credential a 503 and the request is NEVER
	// forwarded. False preserves today's silent pass-through, which is the
	// Anthropic OAuth path (the env carries CLAUDE_CODE_OAUTH_TOKEN and the
	// request already has Authorization: Bearer).
	RequireCredential bool

	// ── auth ──

	// StaticHeaders are set on every outbound request that carries a
	// credential — {"anthropic-version": "2023-06-01"}.
	StaticHeaders map[string]string
	// AuthRules select where the token is written. REQUIRED; exactly one rule
	// has an empty TokenPrefix and it is last.
	AuthRules []AuthRule

	// KeyEnvVars are the agent-facing env-var names that identify this
	// provider when the credential's `provider` column does not — the
	// credTypeToProvider back-compat path.
	KeyEnvVars []string

	// LegacyHealthKey is the pre-phase-2 /health field name
	// ("anthropic_creds"). Empty for a provider that had no such field, which
	// is every provider added from here on: /health reports those under the
	// provider_creds map instead of growing a fourth top-level count.
	LegacyHealthKey string
}
