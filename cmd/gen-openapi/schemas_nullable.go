package main

// nullOnlySchema represents only null using OpenAPI 3.0's nullable mechanism.
// The enum keeps this object-typed arm disjoint from the real object arm in
// oneOf; nullable:true alone would also accept objects and reject them as an
// ambiguous oneOf match. OpenAPI 3.0 has no null primitive type.
func nullOnlySchema() map[string]any {
	return map[string]any{"type": "object", "nullable": true, "enum": []any{nil}}
}
