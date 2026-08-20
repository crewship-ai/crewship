package modelcatalog

import (
	"bytes"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
)

// tinyCatalog loads testdata/tiny.json, the hand-written fixture that carries
// one instance of every normalization edge the Parse contract names. Using a
// fixture rather than an inline literal keeps the edges visible as JSON — the
// shape the bug would actually arrive in.
func tinyCatalog(t *testing.T) Catalog {
	t.Helper()
	b, err := os.ReadFile("testdata/tiny.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	c, err := Parse(b)
	if err != nil {
		t.Fatalf("Parse(tiny.json): %v", err)
	}
	return c
}

func ptr(f float64) *float64 { return &f }

// TestParse_Normalization pins the decode-time contract: keys are canonical,
// the key beats the embedded id, and unaddressable entries are dropped. Every
// later method assumes all three, so this is the table the rest stands on.
func TestParse_Normalization(t *testing.T) {
	c := tinyCatalog(t)

	t.Run("providers", func(t *testing.T) {
		// "" is dropped (unaddressable); " ACME " and "Hollow" canonicalize.
		want := []string{"acme", "hollow"}
		if got := c.Providers(); !reflect.DeepEqual(got, want) {
			t.Fatalf("Providers() = %v, want %v", got, want)
		}
	})

	t.Run("provider ID comes from the key, not the payload", func(t *testing.T) {
		// tiny.json says "id": "acme-corp-legacy-id" under key " ACME ".
		if got := c["acme"].ID; got != "acme" {
			t.Fatalf("acme.ID = %q, want %q (map key must win)", got, "acme")
		}
	})

	t.Run("empty provider key is dropped", func(t *testing.T) {
		if _, ok := c[""]; ok {
			t.Fatal(`catalog[""] exists; an unaddressable provider must be dropped`)
		}
		if _, ok := c.Lookup("", "never-reachable"); ok {
			t.Fatal(`Lookup("", ...) succeeded; the blank-key provider leaked`)
		}
	})

	cases := []struct {
		name    string
		model   string
		wantOK  bool
		wantID  string
		wantDsp string
	}{
		{
			name:    "model ID comes from the key, not the payload",
			model:   "rocket-1", // fixture key is "Rocket-1", id is "rocket-one-legacy-id"
			wantOK:  true,
			wantID:  "rocket-1",
			wantDsp: "Rocket 1",
		},
		{
			name:   "the payload id does not resolve",
			model:  "rocket-one-legacy-id",
			wantOK: false,
		},
		{
			name:    "missing name falls back to the id",
			model:   "anvil-2",
			wantOK:  true,
			wantID:  "anvil-2",
			wantDsp: "anvil-2",
		},
		{
			name:    "unknown upstream fields decode away",
			model:   "undated",
			wantOK:  true,
			wantID:  "undated",
			wantDsp: "Undated",
		},
		{
			name:   "blank model key is dropped",
			model:  "  ",
			wantOK: false,
		},
		{
			name:   "the dropped model's payload id does not resolve either",
			model:  "blank-key",
			wantOK: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, ok := c.Lookup("acme", tc.model)
			if ok != tc.wantOK {
				t.Fatalf("Lookup(acme, %q) ok = %v, want %v", tc.model, ok, tc.wantOK)
			}
			if !ok {
				return
			}
			if m.ID != tc.wantID {
				t.Errorf("ID = %q, want %q", m.ID, tc.wantID)
			}
			if got := m.DisplayName(); got != tc.wantDsp {
				t.Errorf("DisplayName() = %q, want %q", got, tc.wantDsp)
			}
		})
	}
}

// TestParse_CollisionIsDeterministic guards the one place Go's randomized map
// iteration could leak into the output: two raw keys that canonicalize to the
// same string. Whichever wins, it must be the same one on every decode, or the
// catalog is nondeterministic across process restarts.
func TestParse_CollisionIsDeterministic(t *testing.T) {
	const raw = `{
	  "OpenAI": {"name": "upper", "models": {"M": {"name": "upper-m"}, "m": {"name": "lower-m"}}},
	  "openai": {"name": "lower", "models": {"m": {"name": "lower-m"}}}
	}`

	first, err := Parse([]byte(raw))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	want := first["openai"].Name
	for i := 0; i < 50; i++ {
		got, err := Parse([]byte(raw))
		if err != nil {
			t.Fatalf("Parse: %v", err)
		}
		if got["openai"].Name != want {
			t.Fatalf("run %d: openai.Name = %q, want %q (collision resolution must be stable)",
				i, got["openai"].Name, want)
		}
	}
	// Sorted raw-key order is "OpenAI" < "openai" (uppercase sorts first), so
	// the lowercase spelling is the documented winner.
	if want != "lower" {
		t.Fatalf("openai.Name = %q, want %q (last in sorted raw-key order wins)", want, "lower")
	}
}

// TestParse_Errors covers the failure path. A malformed snapshot must surface
// as an error, never as a half-decoded catalog.
func TestParse_Errors(t *testing.T) {
	cases := []struct {
		name string
		in   string
	}{
		{"not json", "this is not json"},
		{"truncated", `{"anthropic": {"models": {`},
		{"top level is an array", `[{"id": "anthropic"}]`},
		{"top level is a string", `"anthropic"`},
		{"models is not an object", `{"anthropic": {"models": ["a","b"]}}`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if c, err := Parse([]byte(tc.in)); err == nil {
				t.Fatalf("Parse(%q) = %v, want error", tc.in, c)
			} else if !strings.HasPrefix(err.Error(), "modelcatalog: parse:") {
				t.Fatalf("error %q lacks the package prefix", err)
			}
			if c, err := Decode(strings.NewReader(tc.in)); err == nil {
				t.Fatalf("Decode(%q) = %v, want error", tc.in, c)
			} else if !strings.HasPrefix(err.Error(), "modelcatalog: decode:") {
				t.Fatalf("error %q lacks the package prefix", err)
			}
		})
	}
}

// TestDecode_MatchesParse proves the two entry points cannot drift: they share
// normalize(), and this is what fails if someone inlines it into one of them.
func TestDecode_MatchesParse(t *testing.T) {
	b, err := os.ReadFile("testdata/tiny.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	parsed, err := Parse(b)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	decoded, err := Decode(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if !reflect.DeepEqual(parsed, decoded) {
		t.Fatal("Decode and Parse produced different catalogs from the same bytes")
	}
}

// TestDecode_ReaderError checks that a failing reader surfaces as a wrapped
// error rather than an empty catalog with a nil error.
func TestDecode_ReaderError(t *testing.T) {
	sentinel := errors.New("boom")
	if _, err := Decode(errReader{sentinel}); !errors.Is(err, sentinel) {
		t.Fatalf("Decode(errReader) = %v, want it to wrap %v", err, sentinel)
	}
}

type errReader struct{ err error }

func (r errReader) Read([]byte) (int, error) { return 0, r.err }

// TestLookup_CaseInsensitive mirrors the case-insensitivity that
// llm.CuratedModels and paymaster.lookupPrice already offer, because the two
// spellings genuinely both arrive: the API enum form is uppercase and the
// Provider.Name() form is lowercase.
func TestLookup_CaseInsensitive(t *testing.T) {
	c := tinyCatalog(t)

	cases := []struct {
		provider, model string
		want            bool
	}{
		{"acme", "rocket-1", true},
		{"ACME", "ROCKET-1", true},
		{" acme ", " rocket-1 ", true},
		{"\tAcMe\n", "Rocket-1", true},
		{"acme", "rocket-2", false},
		{"nosuch", "rocket-1", false},
		{"", "rocket-1", false},
		{"acme", "", false},
	}

	for _, tc := range cases {
		t.Run(tc.provider+"/"+tc.model, func(t *testing.T) {
			m, ok := c.Lookup(tc.provider, tc.model)
			if ok != tc.want {
				t.Fatalf("Lookup(%q, %q) ok = %v, want %v", tc.provider, tc.model, ok, tc.want)
			}
			if !ok {
				if !reflect.DeepEqual(m, Model{}) {
					t.Fatalf("miss returned a non-zero Model: %+v", m)
				}
				return
			}
			if m.ID != "rocket-1" {
				t.Fatalf("ID = %q, want %q", m.ID, "rocket-1")
			}
		})
	}
}

// TestLookup_DecodesEveryField checks the struct tags against the fixture, so
// a typo in a json tag shows up here rather than as a silently zero field in
// the console three releases later.
func TestLookup_DecodesEveryField(t *testing.T) {
	c := tinyCatalog(t)
	m, ok := c.Lookup("acme", "rocket-1")
	if !ok {
		t.Fatal("rocket-1 missing from the fixture")
	}

	want := Model{
		ID:               "rocket-1",
		Name:             "Rocket 1",
		Family:           "rocket",
		Attachment:       true,
		Reasoning:        true,
		ToolCall:         true,
		StructuredOutput: true,
		Temperature:      false,
		Knowledge:        "2026-01-31",
		ReleaseDate:      "2026-04-14",
		Modalities:       Modalities{Input: []string{"text", "image"}, Output: []string{"text"}},
		Limit:            Limit{Context: 1000000, Output: 128000},
		Cost:             &Cost{Input: 5, Output: 25, CacheRead: ptr(0.5), CacheWrite: ptr(6.25)},
	}
	if !reflect.DeepEqual(m, want) {
		t.Fatalf("Lookup(acme, rocket-1) =\n%+v\ncost %+v\nwant\n%+v\ncost %+v",
			m, m.Cost, want, want.Cost)
	}
}

// TestModels_Ordering pins newest-first ordering with an ID tiebreak. Undated
// entries sort last because "" is less than any date string — a UI rendering
// the list top-to-bottom should not lead with a model nobody dated.
func TestModels_Ordering(t *testing.T) {
	cases := []struct {
		name     string
		provider string
		want     []string
	}{
		{
			name:     "release date desc, then id asc",
			provider: "acme",
			// 2026-06-02: anvil-2, freefall (id tiebreak); 2026-04-14: rocket-1;
			// undated last.
			want: []string{"anvil-2", "freefall", "rocket-1", "undated"},
		},
		{
			name:     "known provider with no models is empty, not nil",
			provider: "hollow",
			want:     []string{},
		},
		{
			name:     "unknown provider is nil",
			provider: "nosuch",
			want:     nil,
		},
		{
			name:     "provider lookup is case-insensitive here too",
			provider: " ACME ",
			want:     []string{"anvil-2", "freefall", "rocket-1", "undated"},
		},
	}

	c := tinyCatalog(t)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := c.Models(tc.provider)
			if tc.want == nil {
				if got != nil {
					t.Fatalf("Models(%q) = %v, want nil", tc.provider, got)
				}
				return
			}
			if got == nil {
				t.Fatalf("Models(%q) = nil, want %v (non-nil)", tc.provider, tc.want)
			}
			ids := make([]string, len(got))
			for i, m := range got {
				ids[i] = m.ID
			}
			if !reflect.DeepEqual(ids, tc.want) {
				t.Fatalf("Models(%q) ids = %v, want %v", tc.provider, ids, tc.want)
			}
		})
	}
}

// TestModels_CopyIndependence is the reason Models and Lookup clone. Default()
// hands the same Catalog to every caller in the process; one caller sorting or
// editing its result must not be visible to the next.
func TestModels_CopyIndependence(t *testing.T) {
	c := tinyCatalog(t)

	first := c.Models("acme")
	if len(first) == 0 {
		t.Fatal("fixture has no acme models")
	}
	for i := range first {
		first[i].Name = "MUTATED"
		first[i].ID = "mutated"
		if len(first[i].Modalities.Input) > 0 {
			first[i].Modalities.Input[0] = "MUTATED"
		}
		if first[i].Cost != nil {
			first[i].Cost.Input = 99999
		}
	}

	second := c.Models("acme")
	for _, m := range second {
		if m.Name == "MUTATED" || m.ID == "mutated" {
			t.Fatalf("mutation leaked into the catalog: %+v", m)
		}
		for _, mod := range m.Modalities.Input {
			if mod == "MUTATED" {
				t.Fatalf("modality slice is shared with the catalog: %+v", m)
			}
		}
		if m.Cost != nil && m.Cost.Input == 99999 {
			t.Fatalf("Cost pointer is shared with the catalog: %+v", m.Cost)
		}
	}

	// Same guarantee through Lookup, which returns a value but carries the
	// same reference-typed fields.
	got, ok := c.Lookup("acme", "rocket-1")
	if !ok {
		t.Fatal("rocket-1 missing")
	}
	got.Modalities.Input[0] = "MUTATED"
	got.Cost.Input = 99999
	again, _ := c.Lookup("acme", "rocket-1")
	if again.Modalities.Input[0] == "MUTATED" || again.Cost.Input == 99999 {
		t.Fatalf("Lookup returned an aliasing view: %+v cost %+v", again, again.Cost)
	}
}

// TestProviders_SortedCopy checks both halves of the contract: sorted output,
// and a fresh slice per call.
func TestProviders_SortedCopy(t *testing.T) {
	c := tinyCatalog(t)

	got := c.Providers()
	for i := 1; i < len(got); i++ {
		if got[i-1] > got[i] {
			t.Fatalf("Providers() not sorted: %v", got)
		}
	}
	got[0] = "zzz-mutated"
	if again := c.Providers(); again[0] == "zzz-mutated" {
		t.Fatal("Providers() returned a slice aliased across calls")
	}

	if empty := (Catalog{}).Providers(); len(empty) != 0 {
		t.Fatalf("empty catalog Providers() = %v, want empty", empty)
	}
}

// TestDisplayName covers the fallback rule on its own, including the
// whitespace-only name that a hand-edited snapshot could introduce.
func TestDisplayName(t *testing.T) {
	cases := []struct {
		name string
		in   Model
		want string
	}{
		{"name wins", Model{ID: "gpt-5.5", Name: "GPT-5.5"}, "GPT-5.5"},
		{"empty name falls back to id", Model{ID: "gpt-5.5"}, "gpt-5.5"},
		{"blank name falls back to id", Model{ID: "gpt-5.5", Name: "   "}, "gpt-5.5"},
		{"both empty", Model{}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.in.DisplayName(); got != tc.want {
				t.Fatalf("DisplayName() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestRates_NilMirror is the billing-correctness table. The distinction under
// test — absent cache rate mirrors input, explicit zero stays zero — is the
// difference between charging the input rate and charging nothing, on the
// channel that carries most of the tokens in a long agent run.
func TestRates_NilMirror(t *testing.T) {
	cases := []struct {
		name                                    string
		in                                      Model
		wantIn, wantOut, wantCacheR, wantCacheW float64
		wantOK                                  bool
	}{
		{
			name:   "no cost block means not priced",
			in:     Model{ID: "unpriced"},
			wantOK: false,
		},
		{
			name:       "all four channels published",
			in:         Model{Cost: &Cost{Input: 5, Output: 25, CacheRead: ptr(0.5), CacheWrite: ptr(6.25)}},
			wantIn:     5,
			wantOut:    25,
			wantCacheR: 0.5,
			wantCacheW: 6.25,
			wantOK:     true,
		},
		{
			name:       "absent cache_read mirrors input",
			in:         Model{Cost: &Cost{Input: 2, Output: 6, CacheWrite: ptr(2.5)}},
			wantIn:     2,
			wantOut:    6,
			wantCacheR: 2,
			wantCacheW: 2.5,
			wantOK:     true,
		},
		{
			name:       "absent cache_write mirrors input",
			in:         Model{Cost: &Cost{Input: 4, Output: 24, CacheRead: ptr(0.4)}},
			wantIn:     4,
			wantOut:    24,
			wantCacheR: 0.4,
			wantCacheW: 4,
			wantOK:     true,
		},
		{
			name:       "both absent mirror input",
			in:         Model{Cost: &Cost{Input: 0.3, Output: 0.9}},
			wantIn:     0.3,
			wantOut:    0.9,
			wantCacheR: 0.3,
			wantCacheW: 0.3,
			wantOK:     true,
		},
		{
			name:       "explicit zero stays zero",
			in:         Model{Cost: &Cost{Input: 1, Output: 3, CacheRead: ptr(0), CacheWrite: ptr(0)}},
			wantIn:     1,
			wantOut:    3,
			wantCacheR: 0,
			wantCacheW: 0,
			wantOK:     true,
		},
		{
			name:   "a free model is still priced",
			in:     Model{Cost: &Cost{}},
			wantOK: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in, out, cr, cw, ok := tc.in.Rates()
			if ok != tc.wantOK {
				t.Fatalf("Rates() ok = %v, want %v", ok, tc.wantOK)
			}
			if !ok {
				if in != 0 || out != 0 || cr != 0 || cw != 0 {
					t.Fatalf("Rates() returned %v/%v/%v/%v on a miss, want all zero", in, out, cr, cw)
				}
				return
			}
			if in != tc.wantIn || out != tc.wantOut || cr != tc.wantCacheR || cw != tc.wantCacheW {
				t.Fatalf("Rates() = in %v out %v cacheRead %v cacheWrite %v, want %v/%v/%v/%v",
					in, out, cr, cw, tc.wantIn, tc.wantOut, tc.wantCacheR, tc.wantCacheW)
			}
		})
	}
}

// TestRates_FromFixture runs the same rule through a real decode, so a JSON
// tag change on Cost cannot pass TestRates_NilMirror (which builds structs by
// hand) while breaking the wire shape.
func TestRates_FromFixture(t *testing.T) {
	c := tinyCatalog(t)

	cases := []struct {
		model                                   string
		wantIn, wantOut, wantCacheR, wantCacheW float64
		wantOK                                  bool
	}{
		{model: "rocket-1", wantIn: 5, wantOut: 25, wantCacheR: 0.5, wantCacheW: 6.25, wantOK: true},
		{model: "anvil-2", wantIn: 2, wantOut: 8, wantCacheR: 2, wantCacheW: 2, wantOK: true},
		{model: "freefall", wantIn: 1, wantOut: 3, wantCacheR: 0, wantCacheW: 0, wantOK: true},
		{model: "undated", wantOK: false},
	}

	for _, tc := range cases {
		t.Run(tc.model, func(t *testing.T) {
			m, ok := c.Lookup("acme", tc.model)
			if !ok {
				t.Fatalf("%s missing from the fixture", tc.model)
			}
			in, out, cr, cw, ok := m.Rates()
			if ok != tc.wantOK {
				t.Fatalf("Rates() ok = %v, want %v", ok, tc.wantOK)
			}
			if !ok {
				return
			}
			if in != tc.wantIn || out != tc.wantOut || cr != tc.wantCacheR || cw != tc.wantCacheW {
				t.Fatalf("Rates() = %v/%v/%v/%v, want %v/%v/%v/%v",
					in, out, cr, cw, tc.wantIn, tc.wantOut, tc.wantCacheR, tc.wantCacheW)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Tiered pricing.
//
// The tier fixtures are inline rather than in testdata/tiny.json on purpose:
// tiny.json's model set is asserted item-by-item by TestModels_Ordering here
// and by the counting tests in embed_test.go, so adding a model to it means
// editing tables that have nothing to do with tiers. The shapes below are
// copied from real snapshot rows — google/gemini-2.5-pro for the one-tier
// case (including the context_over_200k duplicate) and
// openrouter/qwen/qwen3-coder-flash for the two-tier case — so the wire form
// under test is the form the bug would actually arrive in.
// ---------------------------------------------------------------------------

const tieredFixture = `{
  "TierVendor": {
    "models": {
      "one-tier": {
        "cost": {
          "input": 1.25, "output": 10, "cache_read": 0.125,
          "context_over_200k": {"input": 99, "output": 99, "cache_read": 99},
          "tiers": [
            {"input": 2.5, "output": 15, "cache_read": 0.25,
             "tier": {"size": 200000, "type": "context"}}
          ]
        }
      },
      "two-tiers": {
        "cost": {
          "input": 0.195, "output": 0.975, "cache_read": 0.039, "cache_write": 0.24375,
          "tiers": [
            {"input": 0.52, "output": 2.6, "cache_read": 0.104, "cache_write": 0.65,
             "tier": {"size": 128000, "type": "context"}},
            {"input": 0.325, "output": 1.625, "cache_read": 0.065, "cache_write": 0.40625,
             "tier": {"size": 32000, "type": "context"}}
          ]
        }
      },
      "odd-axis": {
        "cost": {
          "input": 1, "output": 2,
          "tiers": [
            {"input": 9, "output": 9, "tier": {"size": 1000, "type": "quantity"}},
            {"input": 4, "output": 8, "tier": {"size": 5000, "type": "CONTEXT"}}
          ]
        }
      },
      "zero-bound": {
        "cost": {
          "input": 1, "output": 2,
          "tiers": [{"input": 9, "output": 9, "tier": {"size": 0, "type": "context"}}]
        }
      },
      "mirror-tier": {
        "cost": {
          "input": 1, "output": 2, "cache_read": 0.1,
          "tiers": [{"input": 4, "output": 8, "tier": {"size": 100, "type": "context"}}]
        }
      },
      "flat": {"cost": {"input": 3, "output": 9}}
    }
  }
}`

func tieredCatalog(t *testing.T) Catalog {
	t.Helper()
	c, err := Parse([]byte(tieredFixture))
	if err != nil {
		t.Fatalf("Parse(tieredFixture): %v", err)
	}
	return c
}

func tieredModel(t *testing.T, id string) Model {
	t.Helper()
	m, ok := tieredCatalog(t).Lookup("tiervendor", id)
	if !ok {
		t.Fatalf("%s missing from the tier fixture", id)
	}
	return m
}

// TestParse_TierNormalization pins the decode-time half of tier support.
// RatesAt trusts none of it — it selects by threshold, not slice position —
// but paymaster's ceiling walk and any future consumer do, and an unknown tier
// axis that survived decode would be a rate rule nobody wrote.
func TestParse_TierNormalization(t *testing.T) {
	c := tieredCatalog(t)

	cases := []struct {
		name  string
		model string
		want  []CostTier
	}{
		{
			name:  "a single tier decodes with its bound",
			model: "one-tier",
			want: []CostTier{
				{Input: 2.5, Output: 15, CacheRead: ptr(0.25),
					Tier: TierBound{Type: "context", Size: 200000}},
			},
		},
		{
			name:  "tiers published out of order are sorted ascending",
			model: "two-tiers",
			want: []CostTier{
				{Input: 0.325, Output: 1.625, CacheRead: ptr(0.065), CacheWrite: ptr(0.40625),
					Tier: TierBound{Type: "context", Size: 32000}},
				{Input: 0.52, Output: 2.6, CacheRead: ptr(0.104), CacheWrite: ptr(0.65),
					Tier: TierBound{Type: "context", Size: 128000}},
			},
		},
		{
			name:  "a non-context axis is dropped, and the axis is matched case-insensitively",
			model: "odd-axis",
			want: []CostTier{
				{Input: 4, Output: 8, Tier: TierBound{Type: "CONTEXT", Size: 5000}},
			},
		},
		{
			name:  "a non-positive threshold is not a boundary and is dropped",
			model: "zero-bound",
			want:  nil,
		},
		{
			name:  "a model with no tiers decodes to nil, not an empty slice",
			model: "flat",
			want:  nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, ok := c.Lookup("tiervendor", tc.model)
			if !ok {
				t.Fatalf("%s missing from the fixture", tc.model)
			}
			if m.Cost == nil {
				t.Fatalf("%s decoded with no cost block", tc.model)
			}
			if !reflect.DeepEqual(m.Cost.Tiers, tc.want) {
				t.Fatalf("Tiers = %+v, want %+v", m.Cost.Tiers, tc.want)
			}
		})
	}
}

// TestRatesAt is the tier-selection table. The threshold is exclusive — the
// "exactly at size" row is the one that pins it, and it is the row a
// >=-instead-of-> typo lands on.
func TestRatesAt(t *testing.T) {
	cases := []struct {
		name                                    string
		model                                   string
		ctx                                     int64
		wantIn, wantOut, wantCacheR, wantCacheW float64
	}{
		{
			name: "below the only threshold is base", model: "one-tier", ctx: 100_000,
			wantIn: 1.25, wantOut: 10, wantCacheR: 0.125, wantCacheW: 1.25,
		},
		{
			name: "exactly at the threshold is still base", model: "one-tier", ctx: 200_000,
			wantIn: 1.25, wantOut: 10, wantCacheR: 0.125, wantCacheW: 1.25,
		},
		{
			name: "one token over crosses", model: "one-tier", ctx: 200_001,
			wantIn: 2.5, wantOut: 15, wantCacheR: 0.25, wantCacheW: 2.5,
		},
		{
			name: "zero context is base", model: "one-tier", ctx: 0,
			wantIn: 1.25, wantOut: 10, wantCacheR: 0.125, wantCacheW: 1.25,
		},
		{
			name: "a negative count cannot select a tier", model: "one-tier", ctx: -1,
			wantIn: 1.25, wantOut: 10, wantCacheR: 0.125, wantCacheW: 1.25,
		},
		{
			name: "below both thresholds is base", model: "two-tiers", ctx: 20_000,
			wantIn: 0.195, wantOut: 0.975, wantCacheR: 0.039, wantCacheW: 0.24375,
		},
		{
			name: "between two thresholds takes the lower tier", model: "two-tiers", ctx: 50_000,
			wantIn: 0.325, wantOut: 1.625, wantCacheR: 0.065, wantCacheW: 0.40625,
		},
		{
			name: "above both takes the upper tier", model: "two-tiers", ctx: 200_000,
			wantIn: 0.52, wantOut: 2.6, wantCacheR: 0.104, wantCacheW: 0.65,
		},
		{
			// The tier publishes no cache_read; mirroring the BASE input (1)
			// instead of the selected tier's (4) under-bills every cache read
			// on the tier that just quadrupled the rate.
			name: "an absent tier cache rate mirrors the tier's own input", model: "mirror-tier", ctx: 1_000,
			wantIn: 4, wantOut: 8, wantCacheR: 4, wantCacheW: 4,
		},
		{
			name: "the same model below its threshold still mirrors the base", model: "mirror-tier", ctx: 10,
			wantIn: 1, wantOut: 2, wantCacheR: 0.1, wantCacheW: 1,
		},
		{
			// context_over_200k says 99/99/99. It is never decoded: its name
			// disagrees with the real threshold on 34 of the 65 snapshot models
			// that carry it, so tiers is the only authority.
			name: "context_over_200k is ignored in favour of tiers", model: "one-tier", ctx: 250_000,
			wantIn: 2.5, wantOut: 15, wantCacheR: 0.25, wantCacheW: 2.5,
		},
		{
			name: "an untiered model ignores the context entirely", model: "flat", ctx: 10_000_000,
			wantIn: 3, wantOut: 9, wantCacheR: 3, wantCacheW: 3,
		},
		{
			// The dropped "quantity" tier prices input at 9. If it were applied
			// this row would read 9/9 — a call repriced on an axis nothing
			// upstream measures.
			name: "a dropped axis never reprices", model: "odd-axis", ctx: 2_000,
			wantIn: 1, wantOut: 2, wantCacheR: 1, wantCacheW: 1,
		},
		{
			name: "the surviving axis on the same model still applies", model: "odd-axis", ctx: 6_000,
			wantIn: 4, wantOut: 8, wantCacheR: 4, wantCacheW: 4,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := tieredModel(t, tc.model)
			in, out, cr, cw, ok := m.RatesAt(tc.ctx)
			if !ok {
				t.Fatalf("RatesAt(%d) ok = false, want true", tc.ctx)
			}
			if in != tc.wantIn || out != tc.wantOut || cr != tc.wantCacheR || cw != tc.wantCacheW {
				t.Fatalf("RatesAt(%d) = %v/%v/%v/%v, want %v/%v/%v/%v",
					tc.ctx, in, out, cr, cw, tc.wantIn, tc.wantOut, tc.wantCacheR, tc.wantCacheW)
			}
		})
	}

	t.Run("an unpriced model is still unpriced at any context", func(t *testing.T) {
		in, out, cr, cw, ok := Model{ID: "unpriced"}.RatesAt(1_000_000)
		if ok || in != 0 || out != 0 || cr != 0 || cw != 0 {
			t.Fatalf("RatesAt on a costless model = %v/%v/%v/%v ok=%v, want all zero and false", in, out, cr, cw, ok)
		}
	})

	// Parse sorts, but RatesAt must not depend on it: a Model built by hand in
	// a test or by a future consumer never passes through normalize.
	t.Run("selection does not depend on slice order", func(t *testing.T) {
		m := Model{Cost: &Cost{
			Input: 1, Output: 2,
			Tiers: []CostTier{
				{Input: 8, Output: 16, Tier: TierBound{Type: "context", Size: 200}},
				{Input: 4, Output: 8, Tier: TierBound{Type: "context", Size: 100}},
			},
		}}
		if in, _, _, _, _ := m.RatesAt(150); in != 4 {
			t.Fatalf("RatesAt(150) input = %v, want 4 (the lower tier, listed second)", in)
		}
		if in, _, _, _, _ := m.RatesAt(250); in != 8 {
			t.Fatalf("RatesAt(250) input = %v, want 8", in)
		}
	})
}

// TestRates_IsTheBaseBlock keeps the old accessor's meaning nailed down. Six
// call sites still use Rates(); if it ever started answering with a tier, they
// would all silently change what they report.
func TestRates_IsTheBaseBlock(t *testing.T) {
	for _, id := range []string{"one-tier", "two-tiers", "mirror-tier", "flat", "odd-axis"} {
		t.Run(id, func(t *testing.T) {
			m := tieredModel(t, id)
			in, out, cr, cw, ok := m.Rates()
			bIn, bOut, bCR, bCW, bOK := m.RatesAt(0)
			if ok != bOK || in != bIn || out != bOut || cr != bCR || cw != bCW {
				t.Fatalf("Rates() = %v/%v/%v/%v ok=%v, RatesAt(0) = %v/%v/%v/%v ok=%v; they must be the same call",
					in, out, cr, cw, ok, bIn, bOut, bCR, bCW, bOK)
			}
		})
	}
}

// TestCeilingRates covers the accessor paymaster bills with. The rule it pins
// is not "the last tier" but "the most expensive one", and that the row comes
// out of a single published tier rather than being assembled per channel.
func TestCeilingRates(t *testing.T) {
	cases := []struct {
		name                                    string
		in                                      Model
		wantIn, wantOut, wantCacheR, wantCacheW float64
		wantOK                                  bool
	}{
		{
			name:   "no cost block is still unpriced",
			in:     Model{ID: "unpriced"},
			wantOK: false,
		},
		{
			name:   "an untiered model's ceiling is its base",
			in:     Model{Cost: &Cost{Input: 3, Output: 9, CacheRead: ptr(0.3)}},
			wantIn: 3, wantOut: 9, wantCacheR: 0.3, wantCacheW: 3, wantOK: true,
		},
		{
			name: "one tier above base wins",
			in: Model{Cost: &Cost{Input: 1.25, Output: 10, CacheRead: ptr(0.125),
				Tiers: []CostTier{{Input: 2.5, Output: 15, CacheRead: ptr(0.25),
					Tier: TierBound{Type: "context", Size: 200000}}}}},
			wantIn: 2.5, wantOut: 15, wantCacheR: 0.25, wantCacheW: 2.5, wantOK: true,
		},
		{
			name: "the most expensive of several tiers wins, not the last",
			in: Model{Cost: &Cost{Input: 1, Output: 2,
				Tiers: []CostTier{
					{Input: 9, Output: 18, Tier: TierBound{Type: "context", Size: 100}},
					{Input: 4, Output: 8, Tier: TierBound{Type: "context", Size: 200}},
				}}},
			wantIn: 9, wantOut: 18, wantCacheR: 9, wantCacheW: 9, wantOK: true,
		},
		{
			// Not 5/9: a row welded together from the dearest cell of each
			// column describes a price no provider charges.
			name: "the row is one published tier, not a per-channel maximum",
			in: Model{Cost: &Cost{Input: 1, Output: 9,
				Tiers: []CostTier{{Input: 5, Output: 6, Tier: TierBound{Type: "context", Size: 100}}}}},
			wantIn: 5, wantOut: 6, wantCacheR: 5, wantCacheW: 5, wantOK: true,
		},
		{
			name: "a base dearer than its tier keeps the base",
			in: Model{Cost: &Cost{Input: 10, Output: 20, CacheRead: ptr(1),
				Tiers: []CostTier{{Input: 2, Output: 4, Tier: TierBound{Type: "context", Size: 100}}}}},
			wantIn: 10, wantOut: 20, wantCacheR: 1, wantCacheW: 10, wantOK: true,
		},
		{
			name: "an input tie breaks toward the higher output",
			in: Model{Cost: &Cost{Input: 2, Output: 4,
				Tiers: []CostTier{{Input: 2, Output: 12, Tier: TierBound{Type: "context", Size: 100}}}}},
			wantIn: 2, wantOut: 12, wantCacheR: 2, wantCacheW: 2, wantOK: true,
		},
		{
			name: "the selected tier's absent cache rates mirror its own input",
			in: Model{Cost: &Cost{Input: 1, Output: 2, CacheRead: ptr(0.1), CacheWrite: ptr(0.2),
				Tiers: []CostTier{{Input: 4, Output: 8, Tier: TierBound{Type: "context", Size: 100}}}}},
			wantIn: 4, wantOut: 8, wantCacheR: 4, wantCacheW: 4, wantOK: true,
		},
		{
			name: "an explicit zero on the selected tier stays zero",
			in: Model{Cost: &Cost{Input: 1, Output: 2,
				Tiers: []CostTier{{Input: 4, Output: 8, CacheRead: ptr(0),
					Tier: TierBound{Type: "context", Size: 100}}}}},
			wantIn: 4, wantOut: 8, wantCacheR: 0, wantCacheW: 4, wantOK: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in, out, cr, cw, ok := tc.in.CeilingRates()
			if ok != tc.wantOK {
				t.Fatalf("CeilingRates() ok = %v, want %v", ok, tc.wantOK)
			}
			if !ok {
				if in != 0 || out != 0 || cr != 0 || cw != 0 {
					t.Fatalf("CeilingRates() returned %v/%v/%v/%v on a miss, want all zero", in, out, cr, cw)
				}
				return
			}
			if in != tc.wantIn || out != tc.wantOut || cr != tc.wantCacheR || cw != tc.wantCacheW {
				t.Fatalf("CeilingRates() = %v/%v/%v/%v, want %v/%v/%v/%v",
					in, out, cr, cw, tc.wantIn, tc.wantOut, tc.wantCacheR, tc.wantCacheW)
			}
		})
	}
}

// TestTiers_CopyIndependence is the reason clone deep-copies the tier slice.
// Adding a slice field to Cost silently breaks the one invariant clone exists
// to hold: Default() hands the same Catalog to every caller in the process, and
// a returned Model must not be a window onto it.
func TestTiers_CopyIndependence(t *testing.T) {
	c := tieredCatalog(t)

	got, ok := c.Lookup("tiervendor", "one-tier")
	if !ok {
		t.Fatal("one-tier missing")
	}
	if len(got.Cost.Tiers) == 0 {
		t.Fatal("one-tier decoded with no tiers")
	}
	got.Cost.Tiers[0].Input = 99999
	got.Cost.Tiers[0].Tier.Size = 1
	*got.Cost.Tiers[0].CacheRead = 99999

	again, _ := c.Lookup("tiervendor", "one-tier")
	if again.Cost.Tiers[0].Input == 99999 || again.Cost.Tiers[0].Tier.Size == 1 {
		t.Fatalf("Lookup returned an aliasing view of the tier slice: %+v", again.Cost.Tiers)
	}
	if *again.Cost.Tiers[0].CacheRead == 99999 {
		t.Fatalf("tier cache pointer is shared with the catalog: %+v", again.Cost.Tiers[0])
	}

	// Same guarantee through Models, which is the path paymaster walks.
	first := c.Models("tiervendor")
	for i := range first {
		for j := range first[i].Cost.Tiers {
			first[i].Cost.Tiers[j].Input = 99999
		}
	}
	for _, m := range c.Models("tiervendor") {
		for _, tier := range m.Cost.Tiers {
			if tier.Input == 99999 {
				t.Fatalf("mutation through Models leaked into the catalog: %s %+v", m.ID, tier)
			}
		}
	}
}

// TestDefault_TieredModelsAreRealAndDearer runs the accessors over the
// committed snapshot rather than a fixture. It is the guard that catches a
// refresh which renamed the tier axis, moved the thresholds, or dropped tiers
// entirely — after which every one of these methods would keep working and
// quietly answer with base rates.
func TestDefault_TieredModelsAreRealAndDearer(t *testing.T) {
	c := Default()
	tiered, dearer := 0, 0
	for _, prov := range c.Providers() {
		for _, m := range c.Models(prov) {
			if m.Cost == nil || len(m.Cost.Tiers) == 0 {
				continue
			}
			tiered++
			key := prov + "/" + m.ID

			var prev int64
			for _, tier := range m.Cost.Tiers {
				if tier.Tier.Type != tierTypeContext {
					t.Errorf("%s: tier survived normalization on axis %q", key, tier.Tier.Type)
				}
				if tier.Tier.Size <= prev {
					t.Errorf("%s: tiers are not ascending: %d after %d", key, tier.Tier.Size, prev)
				}
				prev = tier.Tier.Size
				if tier.Input < 0 || tier.Output < 0 {
					t.Errorf("%s: negative tier rate %+v", key, tier)
				}
			}

			baseIn, baseOut, _, _, ok := m.Rates()
			if !ok {
				t.Errorf("%s: has tiers but no base rates", key)
				continue
			}
			ceilIn, ceilOut, _, _, _ := m.CeilingRates()
			if ceilIn < baseIn || ceilOut < baseOut {
				t.Errorf("%s: ceiling %v/%v is below base %v/%v — CeilingRates is not selecting the dearest row",
					key, ceilIn, ceilOut, baseIn, baseOut)
			}
			if ceilIn > baseIn {
				dearer++
			}

			// The ceiling must be reachable: pricing at a rate no context can
			// select would be an over-estimate nobody can explain.
			top := m.Cost.Tiers[len(m.Cost.Tiers)-1].Tier.Size
			atIn, atOut, _, _, _ := m.RatesAt(top + 1)
			if atIn != ceilIn || atOut != ceilOut {
				t.Errorf("%s: RatesAt(%d) = %v/%v but CeilingRates = %v/%v; the dearest tier is not the highest one",
					key, top+1, atIn, atOut, ceilIn, ceilOut)
			}
		}
	}
	if tiered == 0 {
		t.Fatal("no tiered model in the snapshot — either the refresh dropped cost.tiers or the field stopped decoding; this whole feature is dead if so")
	}
	if dearer == 0 {
		t.Error("no tiered model in the snapshot prices above its base rate; the ceiling rule is untested by data")
	}
	t.Logf("%d tiered models, %d of them dearer above the threshold", tiered, dearer)
}

// TestDefault_GeminiProTiers pins two real rows end to end. gemini-2.5-pro is
// the witness because paymaster's hand-written table carries the same model at
// its UPPER tier (2.50/15.00) — so this assertion is what catches the snapshot
// and the hand table drifting apart, and the cache_write figure is what catches
// a mirror taken against the base block instead of the selected tier.
func TestDefault_GeminiProTiers(t *testing.T) {
	m, ok := Default().Lookup("google", "gemini-2.5-pro")
	if !ok {
		t.Skip("google/gemini-2.5-pro is not in this snapshot")
	}

	cases := []struct {
		ctx                                     int64
		wantIn, wantOut, wantCacheR, wantCacheW float64
	}{
		// Below 200k: base, with cache_write absent so it mirrors base input.
		{ctx: 100_000, wantIn: 1.25, wantOut: 10, wantCacheR: 0.125, wantCacheW: 1.25},
		// Above 200k: the upper tier, with cache_write mirroring the TIER's
		// input (2.5). Mirroring the base would read 1.25 here.
		{ctx: 300_000, wantIn: 2.5, wantOut: 15, wantCacheR: 0.25, wantCacheW: 2.5},
	}
	for _, tc := range cases {
		in, out, cr, cw, ok := m.RatesAt(tc.ctx)
		if !ok {
			t.Fatal("gemini-2.5-pro carries no cost block")
		}
		if in != tc.wantIn || out != tc.wantOut || cr != tc.wantCacheR || cw != tc.wantCacheW {
			t.Errorf("RatesAt(%d) = %v/%v/%v/%v, want %v/%v/%v/%v (provider may have repriced)",
				tc.ctx, in, out, cr, cw, tc.wantIn, tc.wantOut, tc.wantCacheR, tc.wantCacheW)
		}
	}

	if in, out, _, _, _ := m.CeilingRates(); in != 2.5 || out != 15 {
		t.Errorf("CeilingRates() = %v/%v, want 2.5/15 — the same numbers paymaster's priceTable carries by hand", in, out)
	}
}

// TestZeroCatalog_IsUsable guards the degraded path: when the embedded
// snapshot fails to decode, Default returns an empty catalog and every method
// must still answer rather than panic.
func TestZeroCatalog_IsUsable(t *testing.T) {
	var c Catalog // nil map — reads are legal on a nil map, writes are not

	if _, ok := c.Lookup("anthropic", "claude-haiku-4-5"); ok {
		t.Fatal("nil catalog returned a hit")
	}
	if got := c.Models("anthropic"); got != nil {
		t.Fatalf("nil catalog Models() = %v, want nil", got)
	}
	if got := c.Providers(); len(got) != 0 {
		t.Fatalf("nil catalog Providers() = %v, want empty", got)
	}
}
