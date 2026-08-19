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
