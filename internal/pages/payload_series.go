package pages

import (
	"strings"

	"github.com/crewship-ai/crewship/schemas"
)

// series.v1 — named series over shared category labels, bar form only in v1.
//
// §3 attaches four rules to this schema, and three of them are structural
// rather than advisory. They are enforced here and in the published schema
// rather than documented and hoped for, because every one of them is a lie a
// chart tells silently:
//
//	One unit per panel. Two units is two panels; dual axes are the most common
//	chart error and cannot be defended at render time. The shape gives a
//	producer one `unit` and gives a series none, so the ordinary way to do it
//	is impossible; ValidateSeries closes the remaining door, which is a `unit`
//	string that names two ("ms and %").
//
//	Max 5 series, sixth merges into "other". A merge, not a rejection: a
//	producer whose dimension grew from four values to nine keeps pushing and
//	keeps getting a readable chart. MergeOverflow is the rule as a function.
//
//	Colour belongs to the entity, not the ordinal. There is no `color` field —
//	the renderer derives colour from `name`, so a filter that removes a series
//	cannot recolour the rest, and a producer cannot spend a reserved status
//	colour on a series of its own.
//
//	Legend always; direct labels at <= 4 series. That one is presentation and
//	lives in the panel component.
//
// The fourth structural rule is §9b.4's, applied per data point: a value is
// *float64 rather than float64 because `0` is a measured zero that draws a bar
// and `null` is no basis to compute that draws none. float64 has one value for
// both.

const (
	// MaxRenderableSeries is §3's five. Beyond it the palette has no sixth
	// colour that is not either a reserved status colour or a repeat, and a
	// chart whose two series share a colour is a chart that cannot be read.
	MaxRenderableSeries = 5

	// OverflowSeriesName is what the sixth series and everything after it are
	// summed into.
	OverflowSeriesName = "other"

	// maxSeriesPerPayload and maxSeriesLabels mirror the published schema.
	maxSeriesPerPayload = 24
	maxSeriesLabels     = 50
)

// Series is one named series. No unit (one unit per panel) and no colour
// (colour belongs to the entity, and the renderer derives it from Name).
type Series struct {
	Name string `json:"name"`
	// Values holds one point per label, in the same order. A nil element is
	// "no basis to compute" for that point alone.
	Values []*float64 `json:"values"`
}

// SeriesPayload is series.v1.
type SeriesPayload struct {
	Unit   string   `json:"unit"`
	Labels []string `json:"labels"`
	Series []Series `json:"series"`
}

// Schema implements Payload.
func (p *SeriesPayload) Schema() PanelSchema { return SchemaSeries }

// ValidateSeries validates and decodes a series.v1 payload, including the
// agreements JSON Schema cannot express: labels and values are two arrays that
// have to be the same length, series names have to be distinct, and a unit has
// to be one unit.
func ValidateSeries(raw []byte) (*SeriesPayload, error) {
	if err := checkSize(SchemaSeries, raw); err != nil {
		return nil, err
	}
	if err := validateAgainst(SchemaSeries, schemas.PanelSeriesV1, raw); err != nil {
		return nil, err
	}
	var p SeriesPayload
	if err := decodeStrict(raw, &p); err != nil {
		return nil, newError(CodeSchemaViolation, SchemaSeries, "%v", err)
	}

	if len(p.Labels) == 0 {
		return nil, newError(CodeSchemaViolation, SchemaSeries,
			"payload declares no labels; a bar needs a category to stand on")
	}
	if len(p.Labels) > maxSeriesLabels {
		return nil, newError(CodeSchemaViolation, SchemaSeries,
			"payload declares %d labels; the cap is %d, past which the honest rendering is a table",
			len(p.Labels), maxSeriesLabels)
	}
	if len(p.Series) == 0 {
		return nil, newError(CodeSchemaViolation, SchemaSeries,
			"payload declares no series; an empty chart is a producer that stopped halfway, "+
				"and a measured nothing is a series of nulls")
	}
	if len(p.Series) > maxSeriesPerPayload {
		return nil, newError(CodeSchemaViolation, SchemaSeries,
			"payload declares %d series; the cap is %d — beyond that the producer is aggregating "+
				"in the wrong place, and the panel would merge all but five into one bar anyway",
			len(p.Series), maxSeriesPerPayload)
	}

	if err := checkSingleUnit(p.Unit); err != nil {
		return nil, err
	}

	seen := make(map[string]bool, len(p.Series))
	for i := range p.Series {
		s := &p.Series[i]
		if seen[s.Name] {
			// Colour is keyed by name, so two series sharing one would share a
			// colour and a legend row: the second could never be read.
			return nil, newError(CodeInconsistentPayload, SchemaSeries,
				"series name %q appears twice; the legend and the colour are both keyed by name, "+
					"so one of the two could never be read", s.Name)
		}
		seen[s.Name] = true

		if len(s.Values) != len(p.Labels) {
			// Not cosmetic: a short array shifts every bar after the gap onto
			// the wrong category, and the chart goes on looking correct.
			return nil, newError(CodeInconsistentPayload, SchemaSeries,
				"series %q carries %d values for %d labels; send null for a point with no data, "+
					"because a short array silently moves every later bar onto the wrong category",
				s.Name, len(s.Values), len(p.Labels))
		}
	}
	return &p, nil
}

// unitSeparators are the ways a producer writes two units into a field that
// holds one. A slash is NOT one of them — "req/s" and "MB/day" are single
// compound units and are exactly what this panel is for.
var unitSeparators = []string{",", ";", "|", "&", " and ", " a ", " nebo ", " or ", " / ", " + "}

// checkSingleUnit is §3's "one unit per panel" at the only place it can still
// be broken. The schema already denies a series its own unit, so a producer
// determined to plot milliseconds against percent has one field left to put
// the second unit in.
func checkSingleUnit(unit string) error {
	trimmed := strings.TrimSpace(unit)
	if trimmed == "" {
		return newError(CodeSchemaViolation, SchemaSeries,
			"unit is empty; every value on this panel is measured in something and the axis has to say what")
	}
	lower := strings.ToLower(" " + trimmed + " ")
	for _, sep := range unitSeparators {
		if strings.Contains(lower, sep) {
			return newError(CodeInconsistentPayload, SchemaSeries,
				"unit %q names more than one unit (%q); §3 says one unit per panel and two units is "+
					"two panels — a single tile cannot say which number the unit belongs to",
				unit, strings.TrimSpace(sep))
		}
	}
	return nil
}

// MergeOverflow returns the payload the renderer draws: at most
// MaxRenderableSeries series in total, with the overflow summed element-wise
// into OverflowSeriesName (§3).
//
// "Max 5 series, sixth merges into other" is a bound on what is DRAWN, because
// the bound comes from the palette: there are five chart colours, and a sixth
// series would have to either repeat one or take a reserved status colour. So
// a payload of six or more keeps its first four under their own names and
// merges everything from the fifth on, which lands on five bars per category
// and five colours — not six.
//
// It is a copy. The stored payload keeps every series the producer pushed —
// merging on the way in would destroy data to satisfy a palette, and a later
// release with a wider palette could never get it back.
//
// Two decisions worth stating, because both are visible on screen:
//
//   - The sum of a point where every merged series is null is null, not 0.
//     Summing nothing into a measured zero would invent a measurement, and
//     §9b.4 is the rule that this whole package exists to keep. A point where
//     SOME are numbers is the sum of those numbers: we measured part of the
//     bucket, and reporting nothing would discard what we did measure.
//   - If a kept series is already named "other", the overflow is folded INTO
//     it rather than appended beside it. Two legend rows reading "other" is a
//     chart with a duplicate key, which is the thing the duplicate-name check
//     refuses at the boundary.
func (p *SeriesPayload) MergeOverflow() *SeriesPayload {
	if p == nil {
		return nil
	}
	if len(p.Series) <= MaxRenderableSeries {
		return p
	}

	// One slot of the five is spent on "other", so four keep their own name.
	const namedSlots = MaxRenderableSeries - 1

	kept := make([]Series, 0, MaxRenderableSeries)
	for _, s := range p.Series[:namedSlots] {
		kept = append(kept, Series{Name: s.Name, Values: append([]*float64(nil), s.Values...)})
	}

	width := len(p.Labels)
	sums := make([]float64, width)
	any := make([]bool, width)
	for _, s := range p.Series[namedSlots:] {
		for i, v := range s.Values {
			if i >= width || v == nil {
				continue
			}
			sums[i] += *v
			any[i] = true
		}
	}

	// A pre-existing "other" is part of the same bucket, not a rival to it.
	target := -1
	for i := range kept {
		if kept[i].Name == OverflowSeriesName {
			target = i
			break
		}
	}
	if target == -1 {
		kept = append(kept, Series{Name: OverflowSeriesName, Values: make([]*float64, width)})
		target = len(kept) - 1
	}

	values := kept[target].Values
	for len(values) < width {
		values = append(values, nil)
	}
	for i := 0; i < width; i++ {
		if !any[i] {
			continue
		}
		total := sums[i]
		if values[i] != nil {
			total += *values[i]
		}
		v := total
		values[i] = &v
	}
	kept[target].Values = values

	return &SeriesPayload{Unit: p.Unit, Labels: append([]string(nil), p.Labels...), Series: kept}
}
