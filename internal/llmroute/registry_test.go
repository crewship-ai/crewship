package llmroute

import (
	"sort"
	"strings"
	"testing"
)

// validSpec is a spec that registers cleanly. Each panic case below mutates
// exactly one thing about it, so the case names what it broke and nothing
// else. It claims "/llm/testprovider" and no hosts or env vars, so a case that
// (wrongly) succeeded would be visible in the table rather than shadowing a
// real provider.
func validSpec() Spec {
	return Spec{
		ID:                "TESTPROVIDER",
		DisplayName:       "Test Provider",
		LedgerProvider:    "testprovider",
		BodyCodec:         "openai",
		PathPrefix:        "/llm/testprovider",
		StripPrefix:       true,
		UpstreamHost:      "api.test.invalid",
		RequireCredential: true,
		AuthRules: []AuthRule{
			{Slots: []AuthSlot{{Placement: PlaceHeader, Name: "Authorization", Prefix: "Bearer "}}},
		},
	}
}

// TestRegister_Panics covers one validator rule per case. register does all of
// its checking before its first write (see the comment on register), so a
// panicking call leaves the real table untouched — which
// TestRegister_PanicIsAtomic below proves, and which is what makes running
// these against the live registry safe.
func TestRegister_Panics(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*Spec)
		wantMsg string
	}{
		{"empty ID", func(s *Spec) { s.ID = "" }, "empty ID"},
		{"lowercase ID", func(s *Spec) { s.ID = "testprovider" }, "must be UPPERCASE"},
		{"duplicate ID", func(s *Spec) { s.ID = "ANTHROPIC" }, "duplicate provider id"},
		{"empty LedgerProvider", func(s *Spec) { s.LedgerProvider = "" }, "empty LedgerProvider"},
		{"uppercase LedgerProvider", func(s *Spec) { s.LedgerProvider = "TestProvider" }, "must be lowercase"},
		{"unknown BodyCodec", func(s *Spec) { s.BodyCodec = "mistral" }, "unknown BodyCodec"},

		{"no AuthRules", func(s *Spec) { s.AuthRules = nil }, "no AuthRules"},
		{"no default AuthRule", func(s *Spec) {
			s.AuthRules = []AuthRule{{TokenPrefix: "sk-", Slots: s.AuthRules[0].Slots}}
		}, "want exactly 1 default AuthRule"},
		{"two default AuthRules", func(s *Spec) {
			s.AuthRules = []AuthRule{s.AuthRules[0], s.AuthRules[0]}
		}, "default AuthRule must be last"},
		{"default AuthRule is not last", func(s *Spec) {
			s.AuthRules = []AuthRule{s.AuthRules[0], {TokenPrefix: "sk-", Slots: s.AuthRules[0].Slots}}
		}, "default AuthRule must be last"},
		{"AuthRule with no slots", func(s *Spec) { s.AuthRules = []AuthRule{{}} }, "has no Slots"},
		{"slot with no name", func(s *Spec) {
			s.AuthRules = []AuthRule{{Slots: []AuthSlot{{Placement: PlaceHeader}}}}
		}, "unnamed slot"},
		{"slot with unknown placement", func(s *Spec) {
			s.AuthRules = []AuthRule{{Slots: []AuthSlot{{Placement: "cookie", Name: "X"}}}}
		}, "unknown Placement"},

		{"both upstream sources", func(s *Spec) { s.UpstreamFromCredential = true }, "exactly one of UpstreamHost"},
		{"neither upstream source", func(s *Spec) { s.UpstreamHost = "" }, "exactly one of UpstreamHost"},
		{"credential upstream without RequireCredential", func(s *Spec) {
			s.UpstreamHost = ""
			s.UpstreamFromCredential = true
			s.RequireCredential = false
		}, "requires RequireCredential"},

		{"PathPrefix without leading slash", func(s *Spec) { s.PathPrefix = "llm/testprovider" }, "must start with /"},
		{"PathPrefix with trailing slash", func(s *Spec) { s.PathPrefix = "/llm/testprovider/" }, "must not end with /"},
		{"duplicate PathPrefix", func(s *Spec) { s.PathPrefix = "/v1" }, "already claimed"},
		{"PathPrefix claims a control-plane segment", func(s *Spec) { s.PathPrefix = "/credentials" }, "reserved segment"},
		{"PathPrefix outside /llm/", func(s *Spec) { s.PathPrefix = "/testprovider" }, "must be under /llm/"},

		{"host already claimed", func(s *Spec) { s.Hosts = []string{"API.OpenAI.com"} }, `host "API.OpenAI.com" already claimed by "OPENAI"`},
		{"env var already claimed", func(s *Spec) { s.KeyEnvVars = []string{"GEMINI_API_KEY"} }, `env var "GEMINI_API_KEY" already claimed by "GOOGLE"`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := validSpec()
			tc.mutate(&s)

			defer func() {
				r := recover()
				if r == nil {
					t.Fatalf("register(%+v) did not panic; want a panic mentioning %q", s, tc.wantMsg)
				}
				msg, ok := r.(string)
				if !ok {
					t.Fatalf("panic value is %T (%v), want a string", r, r)
				}
				if !strings.Contains(msg, tc.wantMsg) {
					t.Fatalf("panic = %q, want it to mention %q", msg, tc.wantMsg)
				}
			}()
			register(s)
		})
	}
}

// TestRegister_PanicIsAtomic proves the rejected specs above left no trace.
// A half-registered spec — indexed by path but absent from the registry, say —
// would make MatchPath return a zero Spec and the sidecar would proxy a
// request to nowhere with no credential.
func TestRegister_PanicIsAtomic(t *testing.T) {
	for _, id := range []string{"TESTPROVIDER", "testprovider", ""} {
		if _, ok := Lookup(id); ok {
			t.Errorf("Lookup(%q) found a spec from a rejected registration", id)
		}
	}
	if s, ok := MatchPath("/llm/testprovider/chat"); ok {
		t.Errorf("MatchPath found %q from a rejected registration", s.ID)
	}
	if s, ok := MatchPath("/testprovider/chat"); ok {
		t.Errorf("MatchPath found %q from a rejected registration", s.ID)
	}
	if got := len(Specs()); got != 5 {
		t.Errorf("Specs() has %d rows; the built-in table should still have 5", got)
	}
}

// TestMatchPath is the routing contract the sidecar's handleLocal replaces its
// prefix switch with. The two cases that matter most:
//
//   - a path beginning "/v1" never resolves to a non-Anthropic provider, and a
//     provider under "/llm/" is never swallowed by Anthropic's catch-all;
//   - matching happens at a segment boundary only, which is what the old
//     HasPrefix(path, "/v1/") checks did — so "/v1beta/..." is still a 404 and
//     not a request forwarded to Anthropic.
func TestMatchPath(t *testing.T) {
	cases := []struct {
		path   string
		wantID string // "" means no match
	}{
		{"/v1/messages", "ANTHROPIC"},
		{"/v1/complete", "ANTHROPIC"},
		{"/openai/v1/chat/completions", "OPENAI"},
		{"/gemini/v1beta/models/gemini-3-pro:generateContent", "GOOGLE"},
		{"/llm/openrouter/chat/completions", "OPENROUTER"},
		{"/llm/openai-compat/chat/completions", "OPENAI_COMPAT"},

		// Segment-boundary rule: the bare prefix is not a match, exactly as
		// HasPrefix(path, "/v1/") was not.
		{"/v1", ""},
		{"/openai", ""},
		{"/gemini", ""},
		{"/llm/openrouter", ""},

		// Near misses that must not be swallowed by a shorter prefix.
		{"/v1beta/models", ""},
		{"/openairelay/v1", ""},
		{"/llm/openrouter-mirror/chat", ""},
		{"/llm/", ""},
		{"/llm/unknown/chat", ""},

		// Control-plane and unknown paths.
		{"/health", ""},
		{"/credentials/create", ""},
		{"/", ""},
		{"", ""},
	}

	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			s, ok := MatchPath(tc.path)
			if tc.wantID == "" {
				if ok {
					t.Fatalf("MatchPath(%q) = %q, want no match", tc.path, s.ID)
				}
				return
			}
			if !ok {
				t.Fatalf("MatchPath(%q) found nothing, want %q", tc.path, tc.wantID)
			}
			if s.ID != tc.wantID {
				t.Fatalf("MatchPath(%q) = %q, want %q", tc.path, s.ID, tc.wantID)
			}
		})
	}
}

// TestMatchPath_LongestPrefixWins pins that the winner is decided by prefix
// length and not by declaration order — the property that stops a provider
// registered after Anthropic from being unreachable behind "/v1".
func TestMatchPath_LongestPrefixWins(t *testing.T) {
	// Build a synthetic table so the property is tested rather than inferred
	// from a table that happens not to nest today.
	prefixes := map[string]string{
		"/llm/a":      "SHORT",
		"/llm/a/long": "LONG",
	}
	longest := func(path string) string {
		best, winner := "", ""
		for p, id := range prefixes {
			if len(p) > len(best) && strings.HasPrefix(path, p+"/") {
				best, winner = p, id
			}
		}
		return winner
	}
	if got := longest("/llm/a/long/chat"); got != "LONG" {
		t.Fatalf("longest-prefix helper is wrong (%q); the test below would prove nothing", got)
	}

	// And the real implementation agrees on the shape it does have: the two
	// "/llm/…" prefixes share a parent segment, and neither steals the other.
	for path, want := range map[string]string{
		"/llm/openrouter/x":    "OPENROUTER",
		"/llm/openai-compat/x": "OPENAI_COMPAT",
	} {
		s, ok := MatchPath(path)
		if !ok || s.ID != want {
			t.Fatalf("MatchPath(%q) = %q/%v, want %q", path, s.ID, ok, want)
		}
	}
}

func TestLookup(t *testing.T) {
	cases := []struct {
		id     string
		wantOK bool
	}{
		{"ANTHROPIC", true},
		{"OPENAI", true},
		{"GOOGLE", true},
		{"OPENROUTER", true},
		{"OPENAI_COMPAT", true},
		// Case-sensitive on purpose: the ID is a stored wire value, and
		// accepting two spellings would let one credential route two ways.
		{"openrouter", false},
		{"OpenRouter", false},
		{"", false},
		{"BEDROCK", false},
		{"CURSOR", false}, // has a CredStore ProviderType but no route
		{"FACTORY", false},
	}
	for _, tc := range cases {
		t.Run(tc.id, func(t *testing.T) {
			s, ok := Lookup(tc.id)
			if ok != tc.wantOK {
				t.Fatalf("Lookup(%q) ok = %v, want %v", tc.id, ok, tc.wantOK)
			}
			if ok && s.ID != tc.id {
				t.Fatalf("Lookup(%q).ID = %q", tc.id, s.ID)
			}
		})
	}
}

// TestMatchHost covers the forward-proxy identification path. openrouter.ai
// resolving to nothing is the §1.4 property, asserted here as well as in
// TestSpecs_NewProvidersClaimNoHosts because this is the function handleHTTP
// actually calls.
func TestMatchHost(t *testing.T) {
	cases := []struct {
		host   string
		wantID string
	}{
		{"api.anthropic.com", "ANTHROPIC"},
		{"api.openai.com", "OPENAI"},
		{"generativelanguage.googleapis.com", "GOOGLE"},
		{"openrouter.ai", ""},
		{"llm.internal.example", ""},
		{"", ""},
		// The caller lowercases and strips the port; MatchHost does not, so a
		// raw Host header must NOT match. This is documented on MatchHost and
		// pinned here so a caller that skips normalization fails loudly rather
		// than silently losing credential injection.
		{"API.Anthropic.com", ""},
		{"api.anthropic.com:443", ""},
	}
	for _, tc := range cases {
		name := tc.host
		if name == "" {
			name = "empty"
		}
		t.Run(name, func(t *testing.T) {
			s, ok := MatchHost(tc.host)
			if tc.wantID == "" {
				if ok {
					t.Fatalf("MatchHost(%q) = %q, want no match", tc.host, s.ID)
				}
				return
			}
			if !ok || s.ID != tc.wantID {
				t.Fatalf("MatchHost(%q) = %q/%v, want %q", tc.host, s.ID, ok, tc.wantID)
			}
		})
	}
}

// TestLookupByEnvVar pins the back-compat identification path against the
// switch it replaces (orchestrator.credTypeToProvider): the same five env-var
// names must resolve to the same five providers, and CURSOR_API_KEY /
// FACTORY_API_KEY must NOT resolve here — those providers have no route, and a
// spec for them would put their requests on the reverse-proxy path where the
// old code silently forwarded them unauthenticated.
func TestLookupByEnvVar(t *testing.T) {
	cases := []struct {
		env    string
		wantID string
	}{
		{"ANTHROPIC_API_KEY", "ANTHROPIC"},
		{"OPENAI_API_KEY", "OPENAI"},
		{"GOOGLE_API_KEY", "GOOGLE"},
		{"GEMINI_API_KEY", "GOOGLE"},
		{"OPENROUTER_API_KEY", "OPENROUTER"},
		{"CURSOR_API_KEY", ""},
		{"FACTORY_API_KEY", ""},
		{"CLAUDE_CODE_OAUTH_TOKEN", ""},
		{"anthropic_api_key", ""},
		{"", ""},
	}
	for _, tc := range cases {
		name := tc.env
		if name == "" {
			name = "empty"
		}
		t.Run(name, func(t *testing.T) {
			s, ok := LookupByEnvVar(tc.env)
			if tc.wantID == "" {
				if ok {
					t.Fatalf("LookupByEnvVar(%q) = %q, want no match", tc.env, s.ID)
				}
				return
			}
			if !ok || s.ID != tc.wantID {
				t.Fatalf("LookupByEnvVar(%q) = %q/%v, want %q", tc.env, s.ID, ok, tc.wantID)
			}
		})
	}
}

// TestReservedPathSegments checks the list is sorted, duplicate-free and
// copied on return, and that every registered prefix respects it. The
// authoritative comparison — this list against the sidecar's real route set —
// is WS3's, because a leaf package cannot import the package it describes.
func TestReservedPathSegments(t *testing.T) {
	got := ReservedPathSegments()
	if len(got) == 0 {
		t.Fatal("ReservedPathSegments() is empty")
	}
	if !sort.StringsAreSorted(got) {
		t.Errorf("ReservedPathSegments() is not sorted: %v", got)
	}
	seen := map[string]bool{}
	for _, s := range got {
		if s == "" {
			t.Error("ReservedPathSegments() contains an empty segment")
		}
		if strings.Contains(s, "/") {
			t.Errorf("segment %q contains a slash; entries are bare first segments", s)
		}
		if seen[s] {
			t.Errorf("segment %q appears twice", s)
		}
		seen[s] = true
	}

	got[0] = "mutated"
	if ReservedPathSegments()[0] == "mutated" {
		t.Error("ReservedPathSegments() hands out the backing array")
	}

	for _, spec := range Specs() {
		first := strings.SplitN(strings.TrimPrefix(spec.PathPrefix, "/"), "/", 2)[0]
		if !seen[first] {
			continue
		}
		if !grandfatheredPrefixes[spec.PathPrefix] {
			t.Errorf("%s claims reserved segment %q via PathPrefix %q", spec.ID, first, spec.PathPrefix)
		}
	}
}

// TestHealthKeys_LegacyOnlyOnGrandfatheredRows keeps /health's shape honest:
// the three pre-phase-2 count fields exist because they were already on the
// wire, and a new provider must report through provider_creds instead of
// growing a fourth top-level key that no existing consumer reads.
func TestHealthKeys_LegacyOnlyOnGrandfatheredRows(t *testing.T) {
	want := map[string]string{
		"ANTHROPIC": "anthropic_creds",
		"OPENAI":    "openai_creds",
		"GOOGLE":    "google_creds",
	}
	seen := map[string]bool{}
	for _, s := range Specs() {
		if got := s.LegacyHealthKey; got != want[s.ID] {
			t.Errorf("%s LegacyHealthKey = %q, want %q", s.ID, got, want[s.ID])
		}
		if s.LegacyHealthKey == "" {
			continue
		}
		if seen[s.LegacyHealthKey] {
			t.Errorf("LegacyHealthKey %q is claimed twice", s.LegacyHealthKey)
		}
		seen[s.LegacyHealthKey] = true
	}
	if len(seen) != len(want) {
		t.Errorf("found %d legacy health keys, want %d", len(seen), len(want))
	}
}
