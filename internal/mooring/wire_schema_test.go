package mooring

import (
	"encoding/json"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

func wireSchemaFixture(t *testing.T) *WireSchema {
	t.Helper()
	raw, err := os.ReadFile("../../mooring/protocol/schema.json")
	if err != nil {
		t.Fatal(err)
	}
	s, err := NewWireSchema(raw)
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func TestWireSchemaSharedCases(t *testing.T) {
	s := wireSchemaFixture(t)
	var f struct {
		Shapes []struct {
			ID      string          `json:"id"`
			Ref     string          `json:"schema_ref"`
			Valid   bool            `json:"schema_valid"`
			Message json.RawMessage `json:"message"`
		} `json:"schema_cases"`
		Semantics []struct {
			ID      string          `json:"id"`
			Message json.RawMessage `json:"message"`
		} `json:"semantic_cases"`
	}
	raw, err := os.ReadFile("../../mooring/protocol/conformance-fixtures.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	if len(f.Shapes) != 190 || len(f.Semantics) != 43 {
		t.Fatal("unexpected shared coverage")
	}
	for _, c := range f.Shapes {
		t.Run(c.ID, func(t *testing.T) {
			// Validate generic subschemas independently of envelope admission.
			normalized, err := CanonicalJSON(c.Message)
			if err != nil {
				t.Fatal(err)
			}
			var value any
			if err = json.Unmarshal(normalized, &value); err != nil {
				t.Fatal(err)
			}
			selected := s.root
			if c.Ref != "#" {
				var ok bool
				selected, ok = s.resolve(c.Ref)
				if !ok {
					t.Fatal("fixture reference")
				}
			}
			if got := s.matches(selected, value, 0); got != c.Valid {
				t.Fatalf("shape=%v want%v", got, c.Valid)
			}
		})
	}
	for _, c := range f.Semantics {
		t.Run("shape-only-"+c.ID, func(t *testing.T) {
			if _, err := s.ValidateE3(c.Message); err != nil {
				t.Fatal(err)
			}
		})
	}
}
func TestWireSchemaAuditBeforeNegation(t *testing.T) {
	for _, raw := range []string{
		`{"not":{"pattern":"unreviewed"}}`,
		`{"not":{"unknownKeyword":true}}`,
		`{"$defs":{"unused":{"format":"unreviewed"}}}`,
		`{"oneOf":[{"const":"x"},{"not":{"pattern":"unreviewed"}}]}`,
		`{"$ref":"https://example.test/schema"}`,
		`{"$ref":"#/$defs/missing"}`,
		`{"$defs":{"cycle":{"$ref":"#/$defs/cycle"}},"$ref":"#/$defs/cycle"}`,
		`{"$defs":{"a":{"$ref":"#/$defs/b"},"b":{"not":{"$ref":"#/$defs/a"}}}}`,
	} {
		if _, err := NewWireSchema([]byte(raw)); err == nil {
			t.Fatalf("unsupported schema accepted: %s", raw)
		}
	}
}
func TestWireSchemaIdentifierAndOpaquePayload(t *testing.T) {
	s := wireSchemaFixture(t)
	for _, n := range []int{200, 201} {
		raw, err := json.Marshal(map[string]any{"type": "hello", "request_id": strings.Repeat("😀", n), "supported": []any{map[string]any{"protocol_version": "0.4.0", "model_version": "1.0.0", "engine_version": "1.0.0"}}})
		if err != nil {
			t.Fatal(err)
		}
		_, err = s.ValidateE3(raw)
		if (err == nil) != (n == 200) {
			t.Fatalf("scalar count%d: %v", n, err)
		}
	}
	// NUL is invalid in the schema Identifier, independently of authority.
	raw := []byte(`{"type":"hello","request_id":"a\u0000b","supported":[{"protocol_version":"0.4.0","model_version":"1.0.0","engine_version":"1.0.0"}]}`)
	if _, err := s.ValidateE3(raw); err == nil {
		t.Fatal("identifier NUL accepted")
	}
}

func TestWireSchemaOpaqueApplicationStrings(t *testing.T) {
	s := wireSchemaFixture(t)
	raw := []byte(`{"type":"presence","request_id":"r","scope":{"workspace_id":"w","artifact_id":"a","application_id":"app"},"session_id":"s","participant_ref":"p","ttl_ms":"1","view":{"op_id":"\u0000"}}`)
	if _, err := s.ValidateE3(raw); err != nil {
		t.Fatal(err)
	}
}

// This exact schema vocabulary extension does not select or admit a private
// profile. Unrecognized patterns must still fail before envelope matching.
func TestWireSchemaCanonicalCausalReadIndex(t *testing.T) {
	const schema = `{"type":"string","pattern":"^(0|[1-9][0-9]?|1[0-9]{2}|2[0-4][0-9]|25[0-5])$","not":{"pattern":"[^0-9]"}}`
	s, err := NewWireSchema([]byte(schema))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i <= 1000; i++ {
		raw, err := json.Marshal(strconv.Itoa(i))
		if err != nil {
			t.Fatal(err)
		}
		_, err = s.ValidateE3(raw)
		if (err == nil) != (i <= 255) {
			t.Fatalf("index%d: %v", i, err)
		}
	}
	for _, value := range []any{"", "00", "01", "+1", "-1", "1.0", "1e0", " 1", "1 ", "1\n", "1\r\n", "1\u2028", "1\u2029", "١", "18446744073709551616", 1, nil} {
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.ValidateE3(raw); err == nil {
			t.Fatalf("noncanonical index accepted: %s", raw)
		}
	}
	unknown := strings.Replace(schema, "25[0-5]", "25[0-6]", 1)
	if _, err := NewWireSchema([]byte(unknown)); err == nil {
		t.Fatal("nearby unsupported pattern accepted")
	}
}

func TestWireSchemaRequiredNullableCausalEntry(t *testing.T) {
	const rawSchema = `{"type":"object","properties":{"entry":{"oneOf":[{"type":"object","properties":{"deleted":{"type":"boolean"}},"required":["deleted"],"additionalProperties":false},{"type":"null"}]}},"required":["entry"],"additionalProperties":false}`
	s, err := NewWireSchema([]byte(rawSchema))
	if err != nil {
		t.Fatal("required nullable causal schema rejected", err)
	}
	for _, raw := range []string{`{"entry":null}`, `{"entry":{"deleted":false}}`} {
		if _, err := s.ValidateE3([]byte(raw)); err != nil {
			t.Fatal(raw, err)
		}
	}
	for _, raw := range []string{`{}`, `null`, `{"entry":{}}`, `{"entry":"null"}`, `{"entry":false}`, `{"entry":0}`, `{"entry":{"deleted":null}}`, `{"entry":null,"extra":true}`} {
		if _, err := s.ValidateE3([]byte(raw)); err == nil {
			t.Fatal("invalid nullable entry accepted", raw)
		}
	}
	if _, err := NewWireSchema([]byte(`{"type":"number"}`)); err == nil {
		t.Fatal("unadvertised type became supported")
	}
}

// Compare the specialized large-string checks with the original schema regex,
// including noncanonical padding bits which the schema itself allows.
func TestWireSchemaBase64PatternLanguage(t *testing.T) {
	patterns := []string{`[^A-Za-z0-9+/=]`, `^(?:[A-Za-z0-9+/]{4})*(?:[A-Za-z0-9+/]{2}==|[A-Za-z0-9+/]{3}=)?$`}
	for _, pattern := range patterns {
		compiled := regexp.MustCompile(pattern)
		check := func(text string) {
			t.Helper()
			if got, want := matchWirePattern(pattern, compiled, text), compiled.MatchString(text); got != want {
				t.Fatalf("pattern=%q input=%q got=%v want=%v", pattern, text, got, want)
			}
		}
		var visit func(string, int)
		visit = func(prefix string, left int) {
			check(prefix)
			if left > 0 {
				for _, r := range "A/=\n_é" {
					visit(prefix+string(r), left-1)
				}
			}
		}
		visit("", 6)
		for _, text := range []string{"Zh==", "Zm9=", "AZaz09+/", strings.Repeat("A", 1<<20), strings.Repeat("A", 1<<20) + "\n", strings.Repeat("A", 1<<20) + "=", strings.Repeat("A", 1<<20) + "===="} {
			check(text)
		}
	}
}
func TestWireSchemaDiscriminatorPreservesConstraints(t *testing.T) {
	cases := []struct {
		schema, input string
		valid         bool
	}{
		{`{"properties":{"kind":{"const":"a"},"optional":{"const":null}}}`, `{"kind":"a"}`, true},
		{`{"properties":{"kind":{"const":"a"},"optional":{"const":null}}}`, `{"kind":"a","optional":null}`, true},
		{`{"properties":{"kind":{"const":"a"},"optional":{"const":null}}}`, `{"kind":"a","optional":false}`, false},
		{`{"type":"object","required":["body"],"properties":{"kind":{"const":"a"},"body":{"type":"string"}},"additionalProperties":false}`, `{"kind":"a","body":false}`, false},
		{`{"type":"object","required":["body"],"properties":{"kind":{"const":"a"},"body":{"type":"string"}},"additionalProperties":false}`, `{"kind":"a"}`, false},
		{`{"type":"object","properties":{"kind":{"const":"a"}},"additionalProperties":false}`, `{"kind":"a","extra":true}`, false},
		{`{"oneOf":[{"properties":{"kind":{"const":"a"}}},{"properties":{"kind":{"const":"a"}}}]}`, `{"kind":"a"}`, false},
		{`{"not":{"properties":{"kind":{"const":"a"}}}}`, `{"kind":"a"}`, false},
		{`{"if":{"properties":{"kind":{"const":"a"}}},"then":{"required":["body"]},"else":{"not":{"required":["body"]}}}`, `{"kind":"a"}`, false},
		{`{"if":{"properties":{"kind":{"const":"a"}}},"then":{"required":["body"]},"else":{"not":{"required":["body"]}}}`, `{"kind":"b","body":"x"}`, false},
		{`{"$defs":{"base":{"properties":{"kind":{"const":"a"}}}},"$ref":"#/$defs/base","properties":{"body":{"const":"x"}}}`, `{"kind":"a","body":"y"}`, false},
		{`{"$defs":{"base":{"properties":{"kind":{"const":"a"}}}},"$ref":"#/$defs/base","properties":{"body":{"const":"x"}}}`, `{"kind":"b","body":"x"}`, false},
	}
	for _, tc := range cases {
		s, err := NewWireSchema([]byte(tc.schema))
		if err != nil {
			t.Fatal(err)
		}
		_, err = s.ValidateE3([]byte(tc.input))
		if (err == nil) != tc.valid {
			t.Fatalf("schema=%s input=%s err=%v want=%v", tc.schema, tc.input, err, tc.valid)
		}
	}
}
