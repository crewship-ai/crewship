package mooring

import (
	"encoding/json"
	"os"
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
	if len(f.Shapes) != 173 || len(f.Semantics) != 33 {
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
