package pages

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/schemas"
)

// Payload validation is the whole trust boundary of a panel. The producer is a
// script in a crew container, a routine, or an agent whose context may already
// contain injected content (§8 rule 8) — so everything that arrives here is
// untrusted, and the only thing standing between it and a human reading a
// number is this package.
//
// Three properties are being pinned:
//
//   - The vocabulary is CLOSED (§3). An unknown schema is refused, not stored
//     and rendered by a fallback, because "we accepted it and drew nothing" is
//     how a page silently stops telling the truth.
//   - The payload shape is closed too. additionalProperties: false is what
//     stops a producer supplying its own `produced_at` — freshness is computed
//     server-side from the stored timestamp and never from the wire (§4 rule 2).
//   - `0` and "no data" are different values (§9b.4). This is the em-dash rule,
//     and it is the reason the payload types use pointers rather than the
//     zero value.

func TestValidatePayload_UnknownSchemaIsRefused(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		schema string
	}{
		{"a plausible Grafana panel type", "gauge.v1"},
		{"a future version of a known schema", "metric.v2"},
		{"the empty string", ""},
		{"a schema name with the right shape but no implementation", "heatmap.v1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ValidatePayload(PanelSchema(tc.schema), []byte(`{"value":1}`))
			if err == nil {
				t.Fatalf("schema %q was accepted; §3 says the set of five is closed and a new panel "+
					"kind is a server release, never a user-supplied string", tc.schema)
			}
			var ve *ValidationError
			if !errors.As(err, &ve) {
				t.Fatalf("want *ValidationError, got %T: %v", err, err)
			}
			if ve.Code != CodeUnknownSchema {
				t.Errorf("code = %q, want %q", ve.Code, CodeUnknownSchema)
			}
		})
	}
}

// embed.v1 is reserved in the migration's CHECK so admitting it later is not a
// breaking change (§3.1) — but it needs a second origin and a sandbox proxy,
// not a payload type, and a payload sent to it must be refused rather than
// stored against a panel nothing can render.
func TestValidatePayload_ReservedSchemasAreNotYetProducible(t *testing.T) {
	t.Parallel()

	for _, s := range []PanelSchema{SchemaEmbed} {
		t.Run(string(s), func(t *testing.T) {
			if !s.Known() {
				t.Errorf("%s should be a known member of the closed set — the migration reserves the name", s)
			}
			if s.Producible() {
				t.Errorf("%s reports as producible, but §3.1 places the sandboxed embed at v1.2", s)
			}
			if _, err := ValidatePayload(s, []byte(`{}`)); err == nil {
				t.Errorf("a payload for %s was accepted before the schema exists", s)
			}
		})
	}
}

// The other five are producible: a schema with a published payload document and
// a renderer. This is the half of the closed set a producer may push to, and it
// is asserted as a whole so a schema cannot be given a renderer without being
// given a payload schema, or the reverse.
func TestPanelSchema_ProducibleSet(t *testing.T) {
	t.Parallel()

	for _, s := range []PanelSchema{SchemaMetric, SchemaSeries, SchemaStatus, SchemaTable, SchemaNarrative} {
		if !s.Producible() {
			t.Errorf("%s is not producible; it has a published payload schema and a renderer", s)
		}
		// Producible means ValidatePayload dispatches somewhere real: the
		// error for an empty object must be about the CONTENT, never
		// "reserved but not yet producible".
		_, err := ValidatePayload(s, []byte(`{}`))
		var ve *ValidationError
		if errors.As(err, &ve) && ve.Code == CodeUnknownSchema {
			t.Errorf("%s is producible but ValidatePayload still reports %s", s, CodeUnknownSchema)
		}
	}
}

func TestValidatePayload_OversizePayloadIsRefused(t *testing.T) {
	t.Parallel()

	// A status payload whose items push it just past the 64 KiB cap (§10b.3).
	// The label is the only free-text field on the schema, so it is where a
	// runaway producer actually blows the budget.
	var b bytes.Buffer
	b.WriteString(`{"items":[`)
	for i := 0; b.Len() < MaxPayloadBytes; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(`{"name":"svc","state":"ok","label":"` + strings.Repeat("x", 512) + `"}`)
	}
	b.WriteString(`]}`)

	if b.Len() <= MaxPayloadBytes {
		t.Fatalf("fixture is only %d bytes; it has to exceed the %d-byte cap to test it", b.Len(), MaxPayloadBytes)
	}

	_, err := ValidatePayload(SchemaStatus, b.Bytes())
	if err == nil {
		t.Fatal("an oversized payload was accepted; §10b.3 caps a panel payload at 64 KiB")
	}
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("want *ValidationError, got %T: %v", err, err)
	}
	if ve.Code != CodeTooLarge {
		t.Errorf("code = %q, want %q — the handler turns this code into the 422 rejection envelope (§10)",
			ve.Code, CodeTooLarge)
	}

	// The cap is checked BEFORE the schema compile and the decode, so a
	// gigabyte of junk costs a length comparison and nothing else.
	if _, err := ValidatePayload(SchemaStatus, bytes.Repeat([]byte("{"), MaxPayloadBytes+1)); err == nil {
		t.Error("oversized junk was accepted")
	}
}

// The table below is the per-schema contract: for each of the three v0 schemas,
// the payloads that must be accepted and the ones that must not.
func TestValidatePayload_PerSchemaContract(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		schema   PanelSchema
		payload  string
		wantCode ErrorCode // "" = must be accepted
		why      string
	}{
		// ---- metric.v1 -------------------------------------------------
		{
			name: "metric/a measured value", schema: SchemaMetric,
			payload: `{"value":42.5,"unit":"ms"}`,
			why:     "§3: {value, unit?, delta?, target?, sparkline?[]}",
		},
		{
			name: "metric/a measured zero", schema: SchemaMetric,
			payload: `{"value":0}`,
			why:     "§9b.4: 0 is 'we looked and there was nothing'",
		},
		{
			name: "metric/no basis to compute", schema: SchemaMetric,
			payload: `{"value":null}`,
			why:     "§9b.4: null is 'we have nothing to look at' and renders as an em dash",
		},
		{
			name: "metric/value is required", schema: SchemaMetric,
			payload: `{"unit":"ms"}`, wantCode: CodeSchemaViolation,
			why: "a metric panel with no value field is not a metric panel with no data — it is a bug",
		},
		{
			name: "metric/value must be a number", schema: SchemaMetric,
			payload: `{"value":"42"}`, wantCode: CodeSchemaViolation,
			why: "a stringly-typed number sorts and formats wrong everywhere downstream",
		},
		{
			name: "metric/a producer may not supply its own timestamp", schema: SchemaMetric,
			payload: `{"value":1,"produced_at":"2020-01-01T00:00:00Z"}`, wantCode: CodeSchemaViolation,
			why: "§4 rule 2: freshness is computed server-side from the stored produced_at. " +
				"A producer-supplied one is a panel that can claim to be fresh forever",
		},
		{
			name: "metric/no free-form links", schema: SchemaMetric,
			payload: `{"value":1,"link":"https://evil.example/?x=secret"}`, wantCode: CodeSchemaViolation,
			why: "§8 rule 3: Slack AI's private-channel exfiltration was a rendered link",
		},
		{
			name: "metric/a sparkline is bounded", schema: SchemaMetric,
			payload:  `{"value":1,"sparkline":[` + strings.TrimSuffix(strings.Repeat("1,", 300), ",") + `]}`,
			wantCode: CodeSchemaViolation,
			why:      "the ring keeps 200 payloads (§10b.3); a longer sparkline is not history, it is payload bloat",
		},

		// ---- status.v1 -------------------------------------------------
		{
			name: "status/a grid of services", schema: SchemaStatus,
			payload: `{"items":[{"name":"api","state":"ok","label":"200 OK"},{"name":"db","state":"critical","label":"down"}]}`,
			why:     "§3: {items[{name, state: ok|warning|critical, label}]}",
		},
		{
			name: "status/nothing to report is not nothing to say", schema: SchemaStatus,
			payload: `{"items":[]}`,
			why:     "an empty grid is a measured 'no services matched', distinct from never having pushed",
		},
		{
			name: "status/state comes from the closed set", schema: SchemaStatus,
			payload: `{"items":[{"name":"api","state":"degraded"}]}`, wantCode: CodeSchemaViolation,
			why: "§3: state carries a glyph and text, and the renderer's map has three keys",
		},
		{
			name: "status/an item needs a state", schema: SchemaStatus,
			payload: `{"items":[{"name":"api"}]}`, wantCode: CodeSchemaViolation,
			why: "a stateless item renders as an uncoloured row that reads as 'fine'",
		},
		{
			name: "status/an item needs a name", schema: SchemaStatus,
			payload: `{"items":[{"state":"ok"}]}`, wantCode: CodeSchemaViolation,
			why: "the name is what the reader acts on",
		},
		{
			name: "status/items must be an array", schema: SchemaStatus,
			payload: `{"items":{"api":"ok"}}`, wantCode: CodeSchemaViolation,
			why: "wrong type",
		},
		{
			name: "status/no images, at any level", schema: SchemaStatus,
			payload:  `{"items":[{"name":"api","state":"ok","icon":"https://camo.example/x.png"}]}`,
			wantCode: CodeSchemaViolation,
			why: "§8 rule 2: CamoLeak exfiltrated through a trusted first-party image proxy, " +
				"so images are absent from the schema rather than sanitised",
		},

		// ---- table.v1 --------------------------------------------------
		{
			name: "table/columns and keyed rows", schema: SchemaTable,
			payload: `{"columns":[{"key":"svc","label":"Service"},{"key":"n","label":"Count","align":"right"}],` +
				`"rows":[{"svc":"api","n":12},{"svc":"db","n":0}]}`,
			why: "§3: {columns[{key,label,align?}], rows[]}",
		},
		{
			name: "table/an empty result set", schema: SchemaTable,
			payload: `{"columns":[{"key":"svc","label":"Service"}],"rows":[]}`,
			why:     "zero rows is a measured answer; the panel renders its empty-state sentence (§9b.3)",
		},
		{
			name: "table/a cell may be explicitly null", schema: SchemaTable,
			payload: `{"columns":[{"key":"svc","label":"Service"},{"key":"n","label":"Count"}],` +
				`"rows":[{"svc":"api","n":null}]}`,
			why: "§9b.4: null is the no-data glyph, and it must be sayable per cell",
		},
		{
			name: "table/a row may not carry an undeclared column", schema: SchemaTable,
			payload:  `{"columns":[{"key":"svc","label":"Service"}],"rows":[{"svc":"api","secret":"hunter2"}]}`,
			wantCode: CodeInconsistentPayload,
			why:      "a key with no column renders nowhere — the data is delivered to the client and never shown",
		},
		{
			name: "table/a row may not omit a declared column", schema: SchemaTable,
			payload:  `{"columns":[{"key":"svc","label":"S"},{"key":"n","label":"N"}],"rows":[{"svc":"api"}]}`,
			wantCode: CodeInconsistentPayload,
			why:      "absence reads identically to no-data; the producer has to say which one it means",
		},
		{
			name: "table/column keys are unique", schema: SchemaTable,
			payload:  `{"columns":[{"key":"svc","label":"A"},{"key":"svc","label":"B"}],"rows":[]}`,
			wantCode: CodeInconsistentPayload,
			why:      "two columns with one key means one of them can never be filled",
		},
		{
			name: "table/a cell holds a scalar, never a structure", schema: SchemaTable,
			payload:  `{"columns":[{"key":"svc","label":"S"}],"rows":[{"svc":{"nested":true}}]}`,
			wantCode: CodeSchemaViolation,
			why:      "§8 rule 1: the agent fills a schema, it never emits markup or a nested render tree",
		},
		{
			name: "table/columns are required", schema: SchemaTable,
			payload: `{"rows":[]}`, wantCode: CodeSchemaViolation,
			why: "rows with no columns cannot be rendered at all",
		},
		{
			name: "table/rows are required", schema: SchemaTable,
			payload: `{"columns":[{"key":"svc","label":"S"}]}`, wantCode: CodeSchemaViolation,
			why: "an absent rows array is a producer that failed halfway, not an empty table",
		},

		// ---- shared ----------------------------------------------------
		{
			name: "any/malformed JSON", schema: SchemaMetric,
			payload: `{"value":`, wantCode: CodeInvalidJSON,
			why: "a truncated push is distinguishable from a schema violation so the producer can be fixed",
		},
		{
			name: "any/a JSON array is not a payload", schema: SchemaStatus,
			payload: `[{"name":"api","state":"ok"}]`, wantCode: CodeSchemaViolation,
			why: "the envelope is an object; a bare array is a producer that forgot the field name",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ValidatePayload(tc.schema, []byte(tc.payload))
			if tc.wantCode == "" {
				if err != nil {
					t.Fatalf("payload was refused but must be accepted (%s): %v", tc.why, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("payload was accepted but must be refused — %s", tc.why)
			}
			var ve *ValidationError
			if !errors.As(err, &ve) {
				t.Fatalf("want *ValidationError, got %T: %v", err, err)
			}
			if ve.Code != tc.wantCode {
				t.Errorf("code = %q, want %q (%s): %v", ve.Code, tc.wantCode, tc.why, err)
			}
		})
	}
}

// The em-dash rule, end to end. §9b.4: the app already separates "we looked and
// there was nothing" (0) from "we have nothing to look at" (—), and Pages
// inherits it verbatim. A round trip through the payload types is where that
// distinction is most likely to be lost, because the obvious Go type for a
// number — float64 — has exactly one value for both.
func TestValidatePayload_MeasuredZeroAndNoDataAreDistinct(t *testing.T) {
	t.Parallel()

	t.Run("metric", func(t *testing.T) {
		zero, err := ValidateMetric([]byte(`{"value":0,"delta":0}`))
		if err != nil {
			t.Fatalf("measured zero refused: %v", err)
		}
		none, err := ValidateMetric([]byte(`{"value":null}`))
		if err != nil {
			t.Fatalf("no-data refused: %v", err)
		}

		if !zero.HasValue() {
			t.Error("a measured 0 reports as having no value; it renders full-contrast 0, not an em dash")
		}
		if none.HasValue() {
			t.Error("a null value reports as having a value; it would render as 0, which is a different claim")
		}
		if zero.Value == nil || *zero.Value != 0 {
			t.Errorf("measured zero decoded as %v, want 0", zero.Value)
		}
		if none.Value != nil {
			t.Errorf("no-data decoded as %v, want nil", none.Value)
		}
		if zero.Delta == nil || *zero.Delta != 0 {
			t.Error("delta 0 (no change, measured) collapsed into 'no delta'")
		}

		// Re-encode: what goes back to the client must still carry the
		// distinction, or the frontend inherits a lie.
		roundTrip := func(p *MetricPayload) string {
			t.Helper()
			b, err := json.Marshal(p)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			return string(b)
		}
		if got, want := roundTrip(zero), `{"value":0,"delta":0}`; got != want {
			t.Errorf("measured zero re-encoded as %s, want %s", got, want)
		}
		if got, want := roundTrip(none), `{"value":null}`; got != want {
			t.Errorf("no-data re-encoded as %s, want %s", got, want)
		}
	})

	t.Run("table cell", func(t *testing.T) {
		payload := `{"columns":[{"key":"n","label":"N"}],"rows":[{"n":0},{"n":null}]}`
		p, err := ValidateTable([]byte(payload))
		if err != nil {
			t.Fatalf("refused: %v", err)
		}
		if len(p.Rows) != 2 {
			t.Fatalf("got %d rows, want 2", len(p.Rows))
		}
		if p.Rows[0]["n"].IsNoData() {
			t.Error("a measured 0 cell reports as no-data; it would render as an em dash")
		}
		if !p.Rows[1]["n"].IsNoData() {
			t.Error("a null cell reports as data; it would render as 0")
		}

		b, err := json.Marshal(p)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if string(b) != payload {
			t.Errorf("round trip changed the payload:\n got %s\nwant %s", b, payload)
		}
	})
}

// Large integers are a metric's most common real value (a row count, a queue
// depth). A cell that round-trips through float64 loses precision above 2^53
// and reports a number nobody pushed.
func TestValidateTable_LargeIntegerCellsKeepTheirDigits(t *testing.T) {
	t.Parallel()

	const payload = `{"columns":[{"key":"n","label":"N"}],"rows":[{"n":9007199254740993}]}`
	p, err := ValidateTable([]byte(payload))
	if err != nil {
		t.Fatalf("refused: %v", err)
	}
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(b) != payload {
		t.Errorf("round trip mangled a 2^53+1 cell:\n got %s\nwant %s", b, payload)
	}
}

// ValidatePayload dispatches on the schema; the three typed entry points are
// what the store and the handler actually call. They must agree, or a payload
// accepted by one path is rejected by the other.
func TestValidatePayload_DispatchMatchesTheTypedEntryPoints(t *testing.T) {
	t.Parallel()

	cases := []struct {
		schema  PanelSchema
		payload string
	}{
		{SchemaMetric, `{"value":1}`},
		{SchemaStatus, `{"items":[{"name":"a","state":"ok"}]}`},
		{SchemaTable, `{"columns":[{"key":"a","label":"A"}],"rows":[{"a":1}]}`},
	}
	for _, tc := range cases {
		t.Run(string(tc.schema), func(t *testing.T) {
			got, err := ValidatePayload(tc.schema, []byte(tc.payload))
			if err != nil {
				t.Fatalf("ValidatePayload: %v", err)
			}
			if got.Schema() != tc.schema {
				t.Errorf("payload reports schema %q, want %q", got.Schema(), tc.schema)
			}
		})
	}
}

// The published schema is the artefact producers, editors and any future CLI
// `crewship page schema` consume, and it has to carry the rules on its own —
// not lean on the Go decoder that happens to run after it. Every test above
// goes through ValidatePayload, where DisallowUnknownFields would mask a schema
// that had quietly stopped being closed. This one validates against the
// embedded JSON directly.
func TestPublishedSchemas_AreClosedOnTheirOwn(t *testing.T) {
	t.Parallel()

	cases := []struct {
		schema  PanelSchema
		source  []byte
		payload string
		why     string
	}{
		{SchemaMetric, schemas.PanelMetricV1, `{"value":1,"produced_at":"2020-01-01T00:00:00Z"}`,
			"§4 rule 2: a producer-supplied timestamp is a panel that can claim to be fresh forever"},
		{SchemaMetric, schemas.PanelMetricV1, `{"value":1,"image":"https://camo.example/x.png"}`,
			"§8 rule 2: no images in agent-authored content. Not sanitised — absent from the schema"},
		{SchemaStatus, schemas.PanelStatusV1, `{"items":[],"url":"https://evil.example"}`,
			"§8 rule 3: no free-form links"},
		{SchemaTable, schemas.PanelTableV1, `{"columns":[{"key":"a","label":"A"}],"rows":[],"html":"<b>x</b>"}`,
			"§8 rule 1: the agent fills a schema, it never emits markup"},
		{SchemaTable, schemas.PanelTableV1, `{"columns":[{"key":"a","label":"A","onclick":"x"}],"rows":[]}`,
			"a closed envelope with an open sub-object is an open envelope"},
	}
	for _, tc := range cases {
		t.Run(tc.why, func(t *testing.T) {
			if err := validateAgainst(tc.schema, tc.source, []byte(tc.payload)); err == nil {
				t.Errorf("%s accepted %s — %s", tc.schema, tc.payload, tc.why)
			}
		})
	}
}
