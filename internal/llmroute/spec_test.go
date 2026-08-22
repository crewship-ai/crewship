package llmroute

import (
	"reflect"
	"strings"
	"testing"
)

// TestSpecs_EveryRowIsComplete is the derived guard: every invariant the
// registration validator enforces is re-asserted here against the real table,
// so a row that somehow bypassed register (a future direct append to the map,
// a merge that dropped a validator call) still fails.
//
// It t.Fatals rather than skipping when the table is empty. A guard derived
// from a collection reports a cheerful "ok" over an empty one, which is the
// exact failure mode this project's skip budget exists to prevent.
func TestSpecs_EveryRowIsComplete(t *testing.T) {
	specs := Specs()
	if len(specs) == 0 {
		t.Fatal("Specs() is empty; every assertion below would vacuously pass")
	}

	for _, s := range specs {
		t.Run(s.ID, func(t *testing.T) {
			if s.ID != strings.ToUpper(s.ID) {
				t.Errorf("ID %q is not UPPERCASE; it is the CredStore key and the boot payload's provider value", s.ID)
			}
			if s.DisplayName == "" {
				t.Error("empty DisplayName")
			}
			if s.LedgerProvider == "" || s.LedgerProvider != strings.ToLower(s.LedgerProvider) {
				t.Errorf("LedgerProvider %q must be non-empty and lowercase; paymaster rate-card keys are lowercase", s.LedgerProvider)
			}
			if !bodyCodecs[s.BodyCodec] {
				t.Errorf("BodyCodec %q is outside the closed set parseLLMUsage can read", s.BodyCodec)
			}
			if s.PathPrefix == "" {
				t.Error("empty PathPrefix")
			}

			if s.UpstreamFromCredential == (s.UpstreamHost != "") {
				t.Errorf("UpstreamFromCredential=%v with UpstreamHost=%q: set exactly one", s.UpstreamFromCredential, s.UpstreamHost)
			}
			if s.UpstreamFromCredential && !s.RequireCredential {
				t.Error("UpstreamFromCredential without RequireCredential: a nil credential would mean no upstream at all")
			}

			if len(s.AuthRules) == 0 {
				t.Fatal("no AuthRules; a token would have nowhere to be written")
			}
			defaults := 0
			for i, r := range s.AuthRules {
				if len(r.Slots) == 0 {
					t.Errorf("AuthRule %d has no slots", i)
				}
				for _, slot := range r.Slots {
					if slot.Name == "" {
						t.Errorf("AuthRule %d has an unnamed slot", i)
					}
					if slot.Placement != PlaceHeader && slot.Placement != PlaceQuery {
						t.Errorf("AuthRule %d slot %q has Placement %q", i, slot.Name, slot.Placement)
					}
				}
				if r.TokenPrefix == "" {
					defaults++
					if i != len(s.AuthRules)-1 {
						t.Errorf("the default AuthRule is at index %d of %d, not last", i, len(s.AuthRules))
					}
				}
			}
			if defaults != 1 {
				t.Errorf("want exactly 1 default AuthRule, have %d; a token could fall through unauthenticated", defaults)
			}
		})
	}
}

// TestSpecs_Table pins the routing-relevant fields of every row.
//
// It is a deliberate ledger, not a restatement: the first three rows are what
// handleLocal / injectCredential / providerForHost did before the table
// existed, and a change to any field here changes the bytes an existing
// provider puts on the wire. Adding a provider means adding a row below — on
// purpose, so a new row cannot arrive without someone writing down what it
// routes.
func TestSpecs_Table(t *testing.T) {
	type row struct {
		id            string
		pathPrefix    string
		strip         bool
		hosts         []string
		upstreamHost  string
		basePath      string
		fromCred      bool
		requireCred   bool
		ledger        string
		codec         string
		legacyHealth  string
		keyEnvVars    []string
		staticHeaders map[string]string
	}
	want := []row{
		{
			id: "ANTHROPIC", pathPrefix: "/v1", strip: false,
			hosts: []string{"api.anthropic.com"}, upstreamHost: "api.anthropic.com",
			requireCred: false, ledger: "anthropic", codec: "anthropic",
			legacyHealth: "anthropic_creds", keyEnvVars: []string{"ANTHROPIC_API_KEY"},
			staticHeaders: map[string]string{"anthropic-version": "2023-06-01"},
		},
		{
			id: "OPENAI", pathPrefix: "/openai", strip: true,
			hosts: []string{"api.openai.com"}, upstreamHost: "api.openai.com",
			requireCred: false, ledger: "openai", codec: "openai",
			legacyHealth: "openai_creds", keyEnvVars: []string{"OPENAI_API_KEY"},
		},
		{
			id: "GOOGLE", pathPrefix: "/gemini", strip: true,
			hosts:        []string{"generativelanguage.googleapis.com"},
			upstreamHost: "generativelanguage.googleapis.com",
			requireCred:  false, ledger: "google", codec: "google",
			legacyHealth: "google_creds", keyEnvVars: []string{"GOOGLE_API_KEY", "GEMINI_API_KEY"},
		},
		{
			id: "OPENROUTER", pathPrefix: "/llm/openrouter", strip: true,
			hosts: nil, upstreamHost: "openrouter.ai", basePath: "/api/v1",
			requireCred: true, ledger: "openrouter", codec: "openai",
			legacyHealth: "", keyEnvVars: []string{"OPENROUTER_API_KEY"},
		},
		{
			id: "OPENAI_COMPAT", pathPrefix: "/llm/openai-compat", strip: true,
			hosts: nil, fromCred: true,
			requireCred: true, ledger: "openai-compat", codec: "openai",
			legacyHealth: "", keyEnvVars: nil,
		},
	}

	got := Specs()
	if len(got) != len(want) {
		t.Fatalf("Specs() has %d rows, want %d: %v", len(got), len(want), ids(got))
	}
	for i, w := range want {
		g := got[i]
		if g.ID != w.id {
			t.Fatalf("row %d is %q, want %q (Specs() is declaration order)", i, g.ID, w.id)
		}
		t.Run(w.id, func(t *testing.T) {
			if g.PathPrefix != w.pathPrefix || g.StripPrefix != w.strip {
				t.Errorf("PathPrefix/StripPrefix = %q/%v, want %q/%v", g.PathPrefix, g.StripPrefix, w.pathPrefix, w.strip)
			}
			if !reflect.DeepEqual(g.Hosts, w.hosts) {
				t.Errorf("Hosts = %v, want %v", g.Hosts, w.hosts)
			}
			if g.UpstreamHost != w.upstreamHost || g.UpstreamBasePath != w.basePath || g.UpstreamFromCredential != w.fromCred {
				t.Errorf("upstream = %q+%q fromCred=%v, want %q+%q fromCred=%v",
					g.UpstreamHost, g.UpstreamBasePath, g.UpstreamFromCredential, w.upstreamHost, w.basePath, w.fromCred)
			}
			if g.RequireCredential != w.requireCred {
				t.Errorf("RequireCredential = %v, want %v", g.RequireCredential, w.requireCred)
			}
			if g.LedgerProvider != w.ledger || g.BodyCodec != w.codec {
				t.Errorf("ledger/codec = %q/%q, want %q/%q", g.LedgerProvider, g.BodyCodec, w.ledger, w.codec)
			}
			if g.LegacyHealthKey != w.legacyHealth {
				t.Errorf("LegacyHealthKey = %q, want %q", g.LegacyHealthKey, w.legacyHealth)
			}
			if !reflect.DeepEqual(g.KeyEnvVars, w.keyEnvVars) {
				t.Errorf("KeyEnvVars = %v, want %v", g.KeyEnvVars, w.keyEnvVars)
			}
			if !reflect.DeepEqual(g.StaticHeaders, w.staticHeaders) {
				t.Errorf("StaticHeaders = %v, want %v", g.StaticHeaders, w.staticHeaders)
			}
		})
	}
}

// TestSpecs_NewProvidersClaimNoHosts pins the deliberate asymmetry of §1.4 as a
// named property rather than leaving it to look like an oversight.
//
// openrouter.ai is already on egressallow.DefaultAllowedDomains, so an
// existing BYOK crew reaches it today through handleHTTP's pass-through with
// its own key in the agent env. The moment a Hosts entry mapped that host to a
// provider, handleHTTP would demand a CredStore credential and 503 without
// one — a regression for every such crew. Only the three providers that
// already had a host mapping keep one.
func TestSpecs_NewProvidersClaimNoHosts(t *testing.T) {
	grandfathered := map[string]bool{"ANTHROPIC": true, "OPENAI": true, "GOOGLE": true}
	specs := Specs()
	if len(specs) == 0 {
		t.Fatal("Specs() is empty")
	}
	for _, s := range specs {
		if grandfathered[s.ID] {
			if len(s.Hosts) == 0 {
				t.Errorf("%s lost its Hosts entry; handleHTTP would stop injecting its credential", s.ID)
			}
			continue
		}
		if len(s.Hosts) != 0 {
			t.Errorf("%s claims Hosts %v; a new provider must not, or handleHTTP turns pass-through into a 503 for BYOK crews", s.ID, s.Hosts)
		}
	}
}

// TestSpecs_ReturnsDeepCopies proves the inspect-the-table entry point cannot
// be used to edit the table. Lookup/MatchPath deliberately do NOT copy (they
// are per-request), which is why the read-only contract is documented on Spec
// and the deep copy lives here.
func TestSpecs_ReturnsDeepCopies(t *testing.T) {
	first := Specs()
	for i := range first {
		first[i].AuthRules[0].Slots[0].Name = "mutated"
		for k := range first[i].StaticHeaders {
			first[i].StaticHeaders[k] = "mutated"
		}
		if len(first[i].Hosts) > 0 {
			first[i].Hosts[0] = "mutated"
		}
		if len(first[i].KeyEnvVars) > 0 {
			first[i].KeyEnvVars[0] = "mutated"
		}
	}

	for _, s := range Specs() {
		if s.AuthRules[0].Slots[0].Name == "mutated" {
			t.Errorf("%s: AuthRules share backing storage with the registry", s.ID)
		}
		for k, v := range s.StaticHeaders {
			if v == "mutated" {
				t.Errorf("%s: StaticHeaders[%q] shares backing storage with the registry", s.ID, k)
			}
		}
		for _, h := range s.Hosts {
			if h == "mutated" {
				t.Errorf("%s: Hosts shares backing storage with the registry", s.ID)
			}
		}
		for _, e := range s.KeyEnvVars {
			if e == "mutated" {
				t.Errorf("%s: KeyEnvVars shares backing storage with the registry", s.ID)
			}
		}
	}
}

func ids(specs []Spec) []string {
	out := make([]string, 0, len(specs))
	for _, s := range specs {
		out = append(out, s.ID)
	}
	return out
}
