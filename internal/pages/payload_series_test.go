package pages

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/schemas"
)

// series.v1 carries three of §3's four chart rules in its shape, and §14 names
// two of them as required tests: *"spec validation (… two units in one
// series.v1, >5 series …)"*. Both are below. The fourth rule — colour belongs
// to the entity, not the ordinal — has no representation in Go at all, and
// that absence IS its enforcement: there is no colour field for a producer to
// set, so the renderer derives it from the name and a filter cannot recolour
// anything.

func f(v float64) *float64 { return &v }

// lbl builds an axis. An empty string means an UNNAMED tick — `null` on the
// wire — because a Go composite literal has no shorter way to write one and
// "" is not a legal label, so the shorthand cannot be mistaken for data.
func lbl(names ...string) []*string {
	out := make([]*string, len(names))
	for i, n := range names {
		if n == "" {
			continue
		}
		s := n
		out[i] = &s
	}
	return out
}

func TestValidateSeries_Accepts(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		raw  string
		want func(*testing.T, *SeriesPayload)
	}{
		{
			name: "one unit, three labels, two series",
			raw: `{"unit":"ms","labels":["mon","tue","wed"],
			       "series":[{"name":"api","values":[120,130,118]},
			                 {"name":"worker","values":[80,0,95]}]}`,
			want: func(t *testing.T, p *SeriesPayload) {
				if p.Unit != "ms" || len(p.Series) != 2 {
					t.Errorf("payload = %+v", p)
				}
				// §9b.4 per data point: a measured zero survives as a zero.
				if v := p.Series[1].Values[1]; v == nil || *v != 0 {
					t.Errorf("a measured 0 became %v; it is a zero-height bar, not a gap", v)
				}
			},
		},
		{
			name: "a null point is a gap, not a zero",
			raw: `{"unit":"%","labels":["a","b"],
			       "series":[{"name":"uptime","values":[99.5,null]}]}`,
			want: func(t *testing.T, p *SeriesPayload) {
				if p.Series[0].Values[1] != nil {
					t.Error("a null point decoded to a number; null is no basis to compute (§9b.4)")
				}
			},
		},
		{
			// A compound unit is still one unit, and it is what this panel is
			// for. The rule is one unit, not one word.
			name: "a compound unit",
			raw:  `{"unit":"req/s","labels":["a"],"series":[{"name":"api","values":[5]}]}`,
			want: func(t *testing.T, p *SeriesPayload) {
				if p.Unit != "req/s" {
					t.Errorf("unit = %q", p.Unit)
				}
			},
		},
		{
			name: "every point null — the producer looked and had no basis anywhere",
			raw:  `{"unit":"ms","labels":["a","b"],"series":[{"name":"api","values":[null,null]}]}`,
			want: func(t *testing.T, p *SeriesPayload) {
				for i, v := range p.Series[0].Values {
					if v != nil {
						t.Errorf("point %d = %v, want nil", i, *v)
					}
				}
			},
		},
		{
			name: "negative values",
			raw:  `{"unit":"Kč","labels":["a","b"],"series":[{"name":"cash","values":[-40,60]}]}`,
			want: func(t *testing.T, p *SeriesPayload) {
				if v := p.Series[0].Values[0]; v == nil || *v != -40 {
					t.Errorf("negative point = %v", v)
				}
			},
		},
		{
			name: "exactly five series, the palette's whole width",
			raw: `{"unit":"ks","labels":["a"],"series":[
			        {"name":"one","values":[1]},{"name":"two","values":[2]},
			        {"name":"three","values":[3]},{"name":"four","values":[4]},
			        {"name":"five","values":[5]}]}`,
			want: func(t *testing.T, p *SeriesPayload) {
				if len(p.MergeOverflow().Series) != 5 {
					t.Error("five series were merged; the merge starts at the sixth")
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, err := ValidateSeries([]byte(tc.raw))
			if err != nil {
				t.Fatalf("rejected a legal payload: %v", err)
			}
			if p.Schema() != SchemaSeries {
				t.Errorf("Schema() = %q", p.Schema())
			}
			tc.want(t, p)
		})
	}
}

// §3: *"One unit per panel. Two units is two panels. Dual axes are the most
// common chart error and cannot be defended at render time."* §14 lists "two
// units in one series.v1" as a required test.
//
// Enforcement is in two places and the first is the shape: a series object has
// no unit field, so `additionalProperties: false` refuses the ordinary way of
// doing it. This test covers both that and the one door left — a `unit` string
// that names two.
func TestValidateSeries_OneUnitPerPanel(t *testing.T) {
	t.Parallel()

	t.Run("a series cannot declare a unit of its own", func(t *testing.T) {
		for _, raw := range []string{
			`{"unit":"ms","labels":["a"],"series":[{"name":"api","values":[1],"unit":"%"}]}`,
			`{"unit":"ms","labels":["a"],"series":[{"name":"api","values":[1],"axis":"right"}]}`,
			`{"unit":"ms","labels":["a"],"series":[{"name":"api","values":[1],"yAxisId":"right"}]}`,
			`{"unit":"ms","labels":["a"],"units":["ms","%"],"series":[{"name":"api","values":[1]}]}`,
			`{"unit":"ms","unit2":"%","labels":["a"],"series":[{"name":"api","values":[1]}]}`,
		} {
			if _, err := ValidateSeries([]byte(raw)); err == nil {
				t.Errorf("a second unit was accepted: %s", raw)
			}
		}
	})

	t.Run("a unit string that names two units is refused", func(t *testing.T) {
		for _, unit := range []string{"ms and %", "Kč, ks", "count | rate", "ms + %", "req / s a %", "ms;%", "ms & %"} {
			raw, _ := json.Marshal(map[string]any{
				"unit": unit, "labels": []string{"a"},
				"series": []any{map[string]any{"name": "api", "values": []any{1}}},
			})
			err := errFrom(ValidateSeries(raw))
			if err == nil {
				t.Errorf("unit %q was accepted; §3 says two units is two panels", unit)
				continue
			}
			var ve *ValidationError
			if !errors.As(err, &ve) || ve.Code != CodeInconsistentPayload {
				t.Errorf("unit %q: code = %v, want %s", unit, err, CodeInconsistentPayload)
			}
		}
	})

	t.Run("a genuine single compound unit stays legal", func(t *testing.T) {
		for _, unit := range []string{"req/s", "MB/day", "ms", "Kč", "%", "errors/1k req"} {
			raw, _ := json.Marshal(map[string]any{
				"unit": unit, "labels": []string{"a"},
				"series": []any{map[string]any{"name": "api", "values": []any{1}}},
			})
			if _, err := ValidateSeries(raw); err != nil {
				t.Errorf("a single unit %q was refused: %v", unit, err)
			}
		}
	})

	t.Run("a unit is required", func(t *testing.T) {
		for _, raw := range []string{
			`{"labels":["a"],"series":[{"name":"api","values":[1]}]}`,
			`{"unit":"","labels":["a"],"series":[{"name":"api","values":[1]}]}`,
			`{"unit":"   ","labels":["a"],"series":[{"name":"api","values":[1]}]}`,
		} {
			if _, err := ValidateSeries([]byte(raw)); err == nil {
				t.Errorf("a unitless payload was accepted: %s", raw)
			}
		}
	})
}

// §3: *"Max 5 series, sixth merges into 'other'."* §14 names it as a required
// test. A MERGE, not a rejection — a producer whose dimension grew from four
// values to nine keeps pushing and keeps getting a readable chart, and the
// stored payload keeps every series it sent.
func TestSeriesPayload_MergeOverflow(t *testing.T) {
	t.Parallel()

	t.Run("five or fewer are untouched", func(t *testing.T) {
		for n := 1; n <= MaxRenderableSeries; n++ {
			p := buildSeries(t, n, 2)
			got := p.MergeOverflow()
			if len(got.Series) != n {
				t.Errorf("%d series merged to %d; the merge starts at the sixth", n, len(got.Series))
			}
			if got != p {
				t.Errorf("%d series was copied; nothing had to change", n)
			}
		}
	})

	t.Run("the sixth and everything after it become one 'other'", func(t *testing.T) {
		p := &SeriesPayload{
			Unit:   "ks",
			Labels: lbl("mon", "tue"),
			Series: []Series{
				{Name: "a", Values: []*float64{f(1), f(1)}},
				{Name: "b", Values: []*float64{f(2), f(2)}},
				{Name: "c", Values: []*float64{f(3), f(3)}},
				{Name: "d", Values: []*float64{f(4), f(4)}},
				{Name: "e", Values: []*float64{f(5), f(5)}},
				{Name: "f", Values: []*float64{f(6), f(6)}},
				{Name: "g", Values: []*float64{f(7), f(7)}},
			},
		}
		got := p.MergeOverflow()

		// Five bars per category and five colours, not six: the bound comes
		// from the palette, so "other" occupies one of the five slots.
		if len(got.Series) != MaxRenderableSeries {
			t.Fatalf("merged to %d series, want %d", len(got.Series), MaxRenderableSeries)
		}
		names := seriesNames(got)
		want := []string{"a", "b", "c", "d", OverflowSeriesName}
		if strings.Join(names, ",") != strings.Join(want, ",") {
			t.Errorf("names = %v, want %v", names, want)
		}
		// e + f + g = 5 + 6 + 7 = 18 at each label.
		for i, v := range got.Series[4].Values {
			if v == nil || *v != 18 {
				t.Errorf("other[%d] = %v, want 18", i, v)
			}
		}

		// The stored payload is untouched: merging on the way in would destroy
		// data to satisfy a palette, and a wider palette could never get it back.
		if len(p.Series) != 7 {
			t.Errorf("the source payload was mutated to %d series", len(p.Series))
		}
	})

	t.Run("a point where every merged series is null stays null", func(t *testing.T) {
		p := buildSeries(t, 7, 2)
		for i := MaxRenderableSeries - 1; i < len(p.Series); i++ {
			p.Series[i].Values[0] = nil
		}
		got := p.MergeOverflow()
		other := got.Series[len(got.Series)-1]
		if other.Values[0] != nil {
			t.Errorf("other[0] = %v; summing nothing into a measured zero invents a measurement (§9b.4)", *other.Values[0])
		}
		if other.Values[1] == nil {
			t.Error("other[1] is nil; the points that WERE measured must survive the merge")
		}
	})

	t.Run("a point where some are measured is the sum of those", func(t *testing.T) {
		p := &SeriesPayload{
			Unit: "ks", Labels: lbl("mon"),
			Series: []Series{
				{Name: "a", Values: []*float64{f(1)}}, {Name: "b", Values: []*float64{f(1)}},
				{Name: "c", Values: []*float64{f(1)}}, {Name: "d", Values: []*float64{f(1)}},
				{Name: "e", Values: []*float64{f(10)}}, {Name: "f", Values: []*float64{nil}},
				{Name: "g", Values: []*float64{f(5)}},
			},
		}
		other := p.MergeOverflow().Series[4]
		if other.Values[0] == nil || *other.Values[0] != 15 {
			t.Errorf("other[0] = %v, want 15 — reporting nothing would discard what we did measure", other.Values[0])
		}
	})

	t.Run("a pre-existing 'other' absorbs the overflow rather than doubling", func(t *testing.T) {
		p := &SeriesPayload{
			Unit: "ks", Labels: lbl("mon"),
			Series: []Series{
				{Name: OverflowSeriesName, Values: []*float64{f(2)}},
				{Name: "b", Values: []*float64{f(1)}}, {Name: "c", Values: []*float64{f(1)}},
				{Name: "d", Values: []*float64{f(1)}}, {Name: "e", Values: []*float64{f(3)}},
				{Name: "f", Values: []*float64{f(4)}},
			},
		}
		got := p.MergeOverflow()
		if n := countNamed(got, OverflowSeriesName); n != 1 {
			t.Fatalf("%d series named %q; the legend and the colour are keyed by name", n, OverflowSeriesName)
		}
		// 2 (its own) + 3 + 4 (the overflow) = 9.
		if v := got.Series[0].Values[0]; v == nil || *v != 9 {
			t.Errorf("other = %v, want 9", v)
		}
	})
}

// The agreements JSON Schema cannot express, because one array's length is the
// other array's contract.
func TestValidateSeries_Rejects(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		raw  string
		code ErrorCode
	}{
		{
			// Not cosmetic: a short array shifts every bar after the gap onto
			// the wrong category, and the chart goes on looking correct.
			name: "fewer values than labels",
			raw:  `{"unit":"ms","labels":["a","b","c"],"series":[{"name":"api","values":[1,2]}]}`,
			code: CodeInconsistentPayload,
		},
		{
			name: "more values than labels",
			raw:  `{"unit":"ms","labels":["a"],"series":[{"name":"api","values":[1,2]}]}`,
			code: CodeInconsistentPayload,
		},
		{
			name: "two series with the same name",
			raw:  `{"unit":"ms","labels":["a"],"series":[{"name":"api","values":[1]},{"name":"api","values":[2]}]}`,
			code: CodeInconsistentPayload,
		},
		{"no labels", `{"unit":"ms","labels":[],"series":[{"name":"api","values":[]}]}`, CodeSchemaViolation},
		{"no series", `{"unit":"ms","labels":["a"],"series":[]}`, CodeSchemaViolation},
		{"no series key at all", `{"unit":"ms","labels":["a"]}`, CodeSchemaViolation},
		{"a series with no name", `{"unit":"ms","labels":["a"],"series":[{"values":[1]}]}`, CodeSchemaViolation},
		{"an empty series name", `{"unit":"ms","labels":["a"],"series":[{"name":"","values":[1]}]}`, CodeSchemaViolation},
		{"a string value", `{"unit":"ms","labels":["a"],"series":[{"name":"api","values":["1"]}]}`, CodeSchemaViolation},
		{"a nested object value", `{"unit":"ms","labels":["a"],"series":[{"name":"api","values":[{"v":1}]}]}`, CodeSchemaViolation},
		{"an empty label", `{"unit":"ms","labels":[""],"series":[{"name":"api","values":[1]}]}`, CodeSchemaViolation},
		{"not JSON at all", `{"unit":`, CodeInvalidJSON},
		// §8 rules 2 and 3 apply to every schema in the vocabulary, not only
		// to the one an agent writes: there is no image and no URL anywhere.
		{"a colour a producer chose", `{"unit":"ms","labels":["a"],"series":[{"name":"api","values":[1],"color":"#ff0000"}]}`, CodeSchemaViolation},
		{"an icon", `{"unit":"ms","labels":["a"],"series":[{"name":"api","values":[1],"icon":"https://x"}]}`, CodeSchemaViolation},
		{"a link", `{"unit":"ms","labels":["a"],"series":[{"name":"api","values":[1],"url":"https://x"}]}`, CodeSchemaViolation},
		// §4 rule 2: freshness is computed server-side from the stored
		// timestamp, so there is no field for a producer to supply one.
		{"a producer-supplied timestamp", `{"unit":"ms","labels":["a"],"series":[{"name":"api","values":[1]}],"produced_at":"2020-01-01T00:00:00Z"}`, CodeSchemaViolation},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ValidateSeries([]byte(tc.raw))
			if err == nil {
				t.Fatal("accepted")
			}
			var ve *ValidationError
			if !errors.As(err, &ve) {
				t.Fatalf("want *ValidationError, got %T", err)
			}
			if ve.Code != tc.code {
				t.Errorf("code = %q, want %q (%v)", ve.Code, tc.code, err)
			}
			if ve.Schema != SchemaSeries {
				t.Errorf("schema = %q, want %q", ve.Schema, SchemaSeries)
			}
		})
	}
}

// The bounds: 50 categories, 24 series on the wire. Past 50 the bars are
// thinner than their gaps and the honest rendering is a table; past 24 the
// producer is aggregating in the wrong place.
func TestValidateSeries_Caps(t *testing.T) {
	t.Parallel()

	if _, err := ValidateSeries(rawSeries(t, maxSeriesLabels, 1)); err != nil {
		t.Errorf("%d labels was refused: %v", maxSeriesLabels, err)
	}
	if _, err := ValidateSeries(rawSeries(t, maxSeriesLabels+1, 1)); err == nil {
		t.Errorf("%d labels was accepted; the cap is %d", maxSeriesLabels+1, maxSeriesLabels)
	}
	if _, err := ValidateSeries(rawSeries(t, 1, maxSeriesPerPayload)); err != nil {
		t.Errorf("%d series was refused: %v", maxSeriesPerPayload, err)
	}
	if _, err := ValidateSeries(rawSeries(t, 1, maxSeriesPerPayload+1)); err == nil {
		t.Errorf("%d series was accepted; the cap is %d", maxSeriesPerPayload+1, maxSeriesPerPayload)
	}
}

// ── the sparse axis ───────────────────────────────────────────────────────
//
// `labels[]` used to demand a non-empty string for every tick, which made a
// 24-point window impossible to render: the producer had to send 24 names, and
// 24 names across a half-width panel truncate to "-1…" apiece. The producer
// cannot fix that — it does not know the panel's width — so the schema was
// forcing a rendering decision onto the one participant who cannot make it.
//
// `null` is now a tick the producer declines to name. The value at that index
// is untouched: a null LABEL removes a name, never a data point.

func TestValidateSeries_SparseAxis(t *testing.T) {
	t.Parallel()

	// Four names over a twelve-point window — the shape ping.py pushes.
	raw := `{"unit":"ms","labels":["-55s",null,null,"-40s",null,null,"-25s",null,null,"-10s",null,"now"],
	         "series":[{"name":"api","values":[1,2,3,4,5,6,7,8,9,10,11,12]}]}`
	p, err := ValidateSeries([]byte(raw))
	if err != nil {
		t.Fatalf("a sparse axis was refused: %v", err)
	}
	if len(p.Labels) != 12 {
		t.Fatalf("%d labels survived, want 12 — a null label is still a category", len(p.Labels))
	}
	if len(p.Series[0].Values) != len(p.Labels) {
		t.Fatalf("%d values for %d labels; the two arrays are still one contract",
			len(p.Series[0].Values), len(p.Labels))
	}
	if p.Labels[1] != nil {
		t.Errorf("labels[1] = %q; an unnamed tick must decode as absent, not as an empty name", *p.Labels[1])
	}
	if p.Labels[0] == nil || *p.Labels[0] != "-55s" {
		t.Errorf("labels[0] = %v, want -55s", p.Labels[0])
	}
	if n := p.NamedLabels(); n != 5 {
		t.Errorf("NamedLabels() = %d, want 5", n)
	}
}

// The half of the old rule that was right: a label must be a label. An empty
// string is what a broken format expression produces, and accepting it would
// make a deliberate blank indistinguishable from a bug — the same reason §9b.4
// keeps `0` and `null` apart one column to the right.
func TestValidateSeries_RefusesAnEmptyLabelString(t *testing.T) {
	t.Parallel()

	for _, raw := range []string{
		`{"unit":"ms","labels":[""],"series":[{"name":"api","values":[1]}]}`,
		`{"unit":"ms","labels":["a","","c"],"series":[{"name":"api","values":[1,2,3]}]}`,
		`{"unit":"ms","labels":["a","   "],"series":[{"name":"api","values":[1,2]}]}`,
	} {
		if _, err := ValidateSeries([]byte(raw)); err == nil {
			t.Errorf("accepted an empty label: %s", raw)
		}
	}
}

// An axis where NOTHING is named is not a sparse axis, it is a chart whose x
// meaning nobody stated. The renderer may thin names down to one; it may never
// be handed zero.
func TestValidateSeries_RefusesAnAxisWithNoNameAnywhere(t *testing.T) {
	t.Parallel()

	raw := `{"unit":"ms","labels":[null,null,null],"series":[{"name":"api","values":[1,2,3]}]}`
	_, err := ValidateSeries([]byte(raw))
	if err == nil {
		t.Fatal("accepted an axis with no name on it")
	}
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("want *ValidationError, got %T", err)
	}
	if ve.Code != CodeInconsistentPayload {
		t.Errorf("code = %q, want %q", ve.Code, CodeInconsistentPayload)
	}
}

// The wire contract only widened. Every payload a live producer is already
// pushing — all labels named, which was the only legal shape — validates
// unchanged, which is what makes this deployable under running producers.
func TestValidateSeries_SparseAxisIsBackwardCompatible(t *testing.T) {
	t.Parallel()

	for _, raw := range []string{
		`{"unit":"ms","labels":["mon","tue","wed"],"series":[{"name":"api","values":[1,2,3]}]}`,
		`{"unit":"ms","labels":["-45s","-40s","-35s"],"series":[{"name":"api","values":[1,null,3]}]}`,
	} {
		p, err := ValidateSeries([]byte(raw))
		if err != nil {
			t.Fatalf("a payload that was legal before is refused now: %v (%s)", err, raw)
		}
		if p.NamedLabels() != len(p.Labels) {
			t.Errorf("NamedLabels() = %d for %d fully named labels", p.NamedLabels(), len(p.Labels))
		}
	}
}

// The merge copies the axis, and an unnamed tick has to survive the copy — it
// is a position in every series' values array.
func TestSeriesPayload_MergeOverflow_KeepsUnnamedTicks(t *testing.T) {
	t.Parallel()

	p := &SeriesPayload{Unit: "ks", Labels: lbl("mon", "")}
	for i := 0; i < 6; i++ {
		p.Series = append(p.Series, Series{Name: fmt.Sprintf("s%d", i), Values: []*float64{f(1), f(1)}})
	}
	got := p.MergeOverflow()
	if len(got.Labels) != 2 {
		t.Fatalf("%d labels survived the merge, want 2", len(got.Labels))
	}
	if got.Labels[1] != nil {
		t.Errorf("the unnamed tick came back named %q", *got.Labels[1])
	}
	if v := got.Series[len(got.Series)-1].Values[1]; v == nil || *v != 2 {
		t.Errorf("other at the unnamed tick = %v, want 2 — a nameless category still carries data", v)
	}
}

// ValidatePayload is the single entry point every write path uses.
func TestValidatePayload_DispatchesSeries(t *testing.T) {
	t.Parallel()

	p, err := ValidatePayload(SchemaSeries, []byte(`{"unit":"ms","labels":["a"],"series":[{"name":"api","values":[1]}]}`))
	if err != nil {
		t.Fatalf("ValidatePayload: %v", err)
	}
	if _, ok := p.(*SeriesPayload); !ok {
		t.Errorf("got %T, want *SeriesPayload", p)
	}
}

// §3: *"Colour belongs to the entity, not to the ordinal."* In Go that is an
// absence — there is no colour on the wire for a producer to set, and none for
// the server to assign — which is what lets the renderer key colour off the
// name. The companion assertion, that removing a series does not recolour the
// rest, lives in the panel's own test because it is a rendering property.
func TestSeriesSchema_HasNoColourField(t *testing.T) {
	t.Parallel()

	doc := strings.ToLower(string(schemas.PanelSeriesV1))
	for _, banned := range []string{`"color":`, `"colour":`, `"fill":`, `"stroke":`, `"palette":`} {
		if strings.Contains(doc, banned) {
			t.Errorf("the published series schema declares %s; colour belongs to the entity and is derived from the name", banned)
		}
	}
}

// ── helpers ───────────────────────────────────────────────────────────────

func errFrom(_ *SeriesPayload, err error) error { return err }

func seriesNames(p *SeriesPayload) []string {
	out := make([]string, 0, len(p.Series))
	for _, s := range p.Series {
		out = append(out, s.Name)
	}
	return out
}

func countNamed(p *SeriesPayload, name string) int {
	n := 0
	for _, s := range p.Series {
		if s.Name == name {
			n++
		}
	}
	return n
}

func buildSeries(t *testing.T, series, labels int) *SeriesPayload {
	t.Helper()
	p := &SeriesPayload{Unit: "ks"}
	for i := 0; i < labels; i++ {
		p.Labels = append(p.Labels, lbl(fmt.Sprintf("l%d", i))...)
	}
	for i := 0; i < series; i++ {
		s := Series{Name: fmt.Sprintf("s%d", i)}
		for j := 0; j < labels; j++ {
			s.Values = append(s.Values, f(float64(i+1)))
		}
		p.Series = append(p.Series, s)
	}
	return p
}

func rawSeries(t *testing.T, labels, series int) []byte {
	t.Helper()
	raw, err := json.Marshal(buildSeries(t, series, labels))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
