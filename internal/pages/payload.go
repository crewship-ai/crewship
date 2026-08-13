package pages

import (
	"bytes"
	"encoding/json"
	"strings"
	"sync"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v5"

	"github.com/crewship-ai/crewship/schemas"
)

// Payload validation is the trust boundary of a panel.
//
// Everything that arrives here is untrusted, including payloads from the
// platform's own agent: its context may already contain injected content read
// from a container or an integration, so it gets the same treatment as a script
// somebody wrote on a laptop (§8 rule 8). Three lines of defence, in order:
//
//  1. the size cap, checked on bytes before anything is parsed;
//  2. the published JSON Schema, which is closed (`additionalProperties: false`)
//     and carries no field for an image, a URL or a producer timestamp;
//  3. the semantic checks JSON Schema cannot express — a table row keyed by a
//     column that was never declared, for instance.

// Payload is what a validated push decodes to. The interface exists so the
// store can hold "the thing that was pushed" without a type switch at every
// call site.
type Payload interface {
	// Schema reports which member of the closed set this payload satisfies.
	Schema() PanelSchema
}

// ValidatePayload checks raw against the published schema for the named panel
// kind and returns the decoded payload. It is the single entry point every
// write path uses — CLI, sidecar and inbound webhook — so there is one place
// where the rules live and one place they can drift from.
func ValidatePayload(schema PanelSchema, raw []byte) (Payload, error) {
	switch schema {
	case SchemaMetric:
		return ValidateMetric(raw)
	case SchemaSeries:
		return ValidateSeries(raw)
	case SchemaStatus:
		return ValidateStatus(raw)
	case SchemaTable:
		return ValidateTable(raw)
	case SchemaNarrative:
		return ValidateNarrative(raw)
	case SchemaEmbed:
		// Refuses everything on an instance with no embed allow-list, which is
		// every instance by default — see internal/pages/embed.go.
		return ValidateEmbed(raw)
	}
	if schema.Known() {
		return nil, newError(CodeUnknownSchema, schema,
			"%s is reserved but not yet producible; it has no payload schema and no renderer", schema)
	}
	return nil, newError(CodeUnknownSchema, schema,
		"unknown panel schema %q; the set is closed and a new kind is a server release", schema)
}

// MetricPayload is metric.v1: one number.
//
// Every optional numeric field is a pointer, and that is not fastidiousness.
// §9b.4 — the em-dash rule — says `0` is a measured zero and `—` is no basis to
// compute, and the app already draws them differently. float64 has one value
// for both, so a non-pointer field would silently merge the two claims on the
// way in and again on the way out.
type MetricPayload struct {
	// Value is required and nullable: nil is "no basis to compute" and renders
	// as an em dash. It is NOT omitempty — a nil Value must re-encode as
	// `"value": null`, because the client has to be able to tell it apart from
	// a field that was never sent.
	Value *float64 `json:"value"`
	// Unit is the suffix rendered next to the value. One unit per panel.
	Unit string `json:"unit,omitempty"`
	// Delta is the change since the previous push. A zero delta ("no change")
	// and an absent delta ("nothing to compare with") are different, hence the
	// pointer.
	Delta *float64 `json:"delta,omitempty"`
	// Target is the value the metric aims at, rendered as a marker.
	Target *float64 `json:"target,omitempty"`
	// Sparkline is recent history, oldest first. A nil element is a gap the
	// producer knows about, so the line breaks instead of interpolating.
	Sparkline []*float64 `json:"sparkline,omitempty"`
	// DeltaGood says which direction is the good one, and is absent by default
	// (PRD §11b.9). Without it the delta renders muted, with a sign and an
	// arrow and no colour — because a green arrow pointing up is a lie on an
	// error rate, and the payload is the only thing that knows which metric
	// this is. "up" or "down"; the JSON Schema rejects anything else.
	DeltaGood *string `json:"delta_good,omitempty"`
}

// DeltaGoodUp and DeltaGoodDown are the only two values DeltaGood may hold.
// The JSON Schema enforces the enum on the way in; these exist so callers
// compare against a constant rather than a loose string.
const (
	DeltaGoodUp   = "up"
	DeltaGoodDown = "down"
)

// Schema implements Payload.
func (p *MetricPayload) Schema() PanelSchema { return SchemaMetric }

// HasValue reports whether the producer measured anything. A measured 0 has a
// value; a null does not. Renderers branch on this rather than on `Value != 0`.
func (p *MetricPayload) HasValue() bool { return p != nil && p.Value != nil }

// ValidateMetric validates and decodes a metric.v1 payload.
func ValidateMetric(raw []byte) (*MetricPayload, error) {
	if err := checkSize(SchemaMetric, raw); err != nil {
		return nil, err
	}
	if err := validateAgainst(SchemaMetric, schemas.PanelMetricV1, raw); err != nil {
		return nil, err
	}
	var p MetricPayload
	if err := decodeStrict(raw, &p); err != nil {
		return nil, newError(CodeSchemaViolation, SchemaMetric, "%v", err)
	}
	return &p, nil
}

// StatusState is one of the three states a status item may be in. Closed,
// because the renderer maps each to a glyph AND a word — state is never carried
// by colour alone, and these colours are reserved so they can never also mean
// "series 3".
type StatusState string

const (
	StatusOK       StatusState = "ok"
	StatusWarning  StatusState = "warning"
	StatusCritical StatusState = "critical"
)

// StatusItem is one thing being watched. There is no icon, image or link field:
// agent-authored content may not carry URLs or images at all (§8 rules 2, 3).
type StatusItem struct {
	Name  string      `json:"name"`
	State StatusState `json:"state"`
	Label string      `json:"label,omitempty"`
}

// StatusPayload is status.v1: a grid of named things and how each is doing.
type StatusPayload struct {
	// Items is required. An empty array is a measured "nothing matched" and
	// renders the panel's empty-state sentence; it is not the same as never
	// having pushed, which renders an em dash.
	Items []StatusItem `json:"items"`
}

// Schema implements Payload.
func (p *StatusPayload) Schema() PanelSchema { return SchemaStatus }

// Worst returns the most severe state present, and false when there are no
// items. This is the value a wake gate reads — `any(state == "critical")` — so
// it lives next to the type rather than in the automation layer.
func (p *StatusPayload) Worst() (StatusState, bool) {
	if p == nil || len(p.Items) == 0 {
		return "", false
	}
	rank := map[StatusState]int{StatusOK: 0, StatusWarning: 1, StatusCritical: 2}
	worst := StatusOK
	for _, item := range p.Items {
		if rank[item.State] > rank[worst] {
			worst = item.State
		}
	}
	return worst, true
}

// ValidateStatus validates and decodes a status.v1 payload.
func ValidateStatus(raw []byte) (*StatusPayload, error) {
	if err := checkSize(SchemaStatus, raw); err != nil {
		return nil, err
	}
	if err := validateAgainst(SchemaStatus, schemas.PanelStatusV1, raw); err != nil {
		return nil, err
	}
	var p StatusPayload
	if err := decodeStrict(raw, &p); err != nil {
		return nil, newError(CodeSchemaViolation, SchemaStatus, "%v", err)
	}
	return &p, nil
}

// TableColumn declares one column. `key` is what the cells are stored under in
// every row.
type TableColumn struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Align string `json:"align,omitempty"`
}

// Cell is one table cell, held as the raw JSON scalar it arrived as.
//
// Decoding into `any` would be simpler and wrong twice over: it collapses a
// measured 0 and a null into values a renderer has to guess between, and it
// rounds every integer through float64, so a row count above 2^53 comes back
// as a number nobody pushed. Keeping the bytes is the only representation that
// round-trips exactly, which is what the panel's whole credibility rests on.
type Cell struct {
	raw json.RawMessage
}

// UnmarshalJSON implements json.Unmarshaler.
func (c *Cell) UnmarshalJSON(b []byte) error {
	c.raw = append(c.raw[:0], b...)
	return nil
}

// MarshalJSON implements json.Marshaler. A zero Cell encodes as null, which is
// the honest reading of "nothing was there".
func (c Cell) MarshalJSON() ([]byte, error) {
	if len(c.raw) == 0 {
		return []byte("null"), nil
	}
	return c.raw, nil
}

// IsNoData reports whether the cell is JSON null — "we have nothing to look
// at", rendered as an em dash. A measured 0 and an empty string are data.
func (c Cell) IsNoData() bool {
	return len(c.raw) == 0 || string(bytes.TrimSpace(c.raw)) == "null"
}

// Raw returns the cell's JSON bytes. Callers that need a Go value decode it
// themselves, choosing their own number handling.
func (c Cell) Raw() json.RawMessage { return c.raw }

// TableRow is one row, keyed by column key.
type TableRow map[string]Cell

// TablePayload is table.v1: declared columns and keyed rows.
type TablePayload struct {
	Columns []TableColumn `json:"columns"`
	Rows    []TableRow    `json:"rows"`
}

// Schema implements Payload.
func (p *TablePayload) Schema() PanelSchema { return SchemaTable }

// ValidateTable validates and decodes a table.v1 payload, including the
// column/row agreement JSON Schema cannot express.
func ValidateTable(raw []byte) (*TablePayload, error) {
	if err := checkSize(SchemaTable, raw); err != nil {
		return nil, err
	}
	if err := validateAgainst(SchemaTable, schemas.PanelTableV1, raw); err != nil {
		return nil, err
	}
	var p TablePayload
	if err := decodeStrict(raw, &p); err != nil {
		return nil, newError(CodeSchemaViolation, SchemaTable, "%v", err)
	}

	// The columns and the rows have to agree, and neither failure is
	// expressible in JSON Schema because the column keys are data.
	declared := make(map[string]bool, len(p.Columns))
	for _, col := range p.Columns {
		if declared[col.Key] {
			return nil, newError(CodeInconsistentPayload, SchemaTable,
				"column key %q is declared twice; one of the two could never be filled", col.Key)
		}
		declared[col.Key] = true
	}
	for i, row := range p.Rows {
		for key := range row {
			if !declared[key] {
				// Delivered to the client and rendered nowhere — the shape of
				// a leak, not of a cosmetic mistake.
				return nil, newError(CodeInconsistentPayload, SchemaTable,
					"row %d carries %q, which is not a declared column", i, key)
			}
		}
		for _, col := range p.Columns {
			if _, ok := row[col.Key]; !ok {
				// An absent key renders identically to a measured null, so the
				// producer has to say which one it means (§9b.4).
				return nil, newError(CodeInconsistentPayload, SchemaTable,
					"row %d has no %q; send null to mean 'no data' for this cell", i, col.Key)
			}
		}
	}
	return &p, nil
}

// checkSize rejects an oversized payload before anything parses it, so a
// gigabyte of junk costs one length comparison.
func checkSize(schema PanelSchema, raw []byte) error {
	if len(raw) > MaxPayloadBytes {
		return newError(CodeTooLarge, schema,
			"payload is %d bytes; the cap is %d", len(raw), MaxPayloadBytes)
	}
	return nil
}

// decodeStrict decodes into a typed payload with unknown fields refused.
// The published schema already refuses them, so this is the belt to that
// braces: it is what catches a schema and a struct that have drifted apart.
func decodeStrict(raw []byte, into any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(into); err != nil {
		return err
	}
	return nil
}

// compiledPanelSchemas compiles each embedded schema once. The compiler is
// goroutine-safe, so concurrent pushes share one compiled validator.
var compiledPanelSchemas sync.Map // map[PanelSchema]*jsonschema.Schema

func compiledSchema(schema PanelSchema, source []byte) (*jsonschema.Schema, error) {
	if v, ok := compiledPanelSchemas.Load(schema); ok {
		return v.(*jsonschema.Schema), nil
	}
	url := "crewship://panel/" + string(schema)
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource(url, bytes.NewReader(source)); err != nil {
		return nil, err
	}
	compiled, err := compiler.Compile(url)
	if err != nil {
		return nil, err
	}
	compiledPanelSchemas.Store(schema, compiled)
	return compiled, nil
}

// validateAgainst runs the published schema over raw.
//
// The JSON is decoded with UseNumber so validation sees the digits the producer
// actually sent; without it a large integer is compared as a float64 and a
// `multipleOf` or bound check could be answered about a number nobody pushed.
func validateAgainst(schema PanelSchema, source, raw []byte) error {
	compiled, err := compiledSchema(schema, source)
	if err != nil {
		// The embedded schema failed to compile: that is a build-time bug in
		// this repo, not something the producer can fix. Refusing is still
		// correct — a misshapen schema cannot accept anything.
		return newError(CodeSchemaViolation, schema, "published schema failed to compile: %v", err)
	}

	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var doc any
	if err := dec.Decode(&doc); err != nil {
		return newError(CodeInvalidJSON, schema, "%v", err)
	}
	if dec.More() {
		return newError(CodeInvalidJSON, schema, "trailing content after the payload")
	}

	if err := compiled.Validate(doc); err != nil {
		return newError(CodeSchemaViolation, schema, "%s", firstViolation(err))
	}
	return nil
}

// firstViolation trims a jsonschema error chain to something a producer script
// can print. The full chain runs to several KB and its first line is the one
// that names the offending pointer.
func firstViolation(err error) string {
	msg := err.Error()
	if i := strings.IndexByte(msg, '\n'); i >= 0 {
		msg = msg[:i]
	}
	const max = 300
	if len(msg) > max {
		msg = msg[:max] + "..."
	}
	return strings.TrimSpace(msg)
}
