package pages

import "testing"

// TestValidateMetric_DeltaGoodReachesTheStruct closes the half-landed half of
// PRD §11b.9. The JSON Schema gained `delta_good` before MetricPayload did, so
// for a while a producer sending it passed schema validation and then failed
// decodeStrict with CodeSchemaViolation — a worse outcome than the original
// bug, because the error blamed the schema for a field the schema allowed.
//
// The rule this protects: a delta renders muted unless the payload says which
// way is good. A green arrow pointing up is a lie on an error rate, and only
// the producer knows which metric this is.
func TestValidateMetric_DeltaGoodReachesTheStruct(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
		want *string
		ok   bool
	}{
		{"up", `{"value":12,"delta":3,"delta_good":"up"}`, ptr(DeltaGoodUp), true},
		{"down", `{"value":12,"delta":3,"delta_good":"down"}`, ptr(DeltaGoodDown), true},
		{"absent is the default, and is not an error", `{"value":12,"delta":3}`, nil, true},
		{"sideways is refused by the enum", `{"value":12,"delta_good":"sideways"}`, nil, false},
		{"camelCase is not the wire name", `{"value":12,"deltaGood":"up"}`, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := ValidateMetric([]byte(tc.raw))
			if tc.ok != (err == nil) {
				t.Fatalf("ValidateMetric(%s) error = %v, want ok=%v", tc.raw, err, tc.ok)
			}
			if !tc.ok {
				return
			}
			switch {
			case tc.want == nil && p.DeltaGood != nil:
				t.Errorf("DeltaGood = %q, want absent", *p.DeltaGood)
			case tc.want != nil && p.DeltaGood == nil:
				t.Errorf("DeltaGood absent, want %q", *tc.want)
			case tc.want != nil && *p.DeltaGood != *tc.want:
				t.Errorf("DeltaGood = %q, want %q", *p.DeltaGood, *tc.want)
			}
		})
	}
}

func ptr(s string) *string { return &s }
