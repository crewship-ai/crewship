package chatbridge

import "testing"

// Only the composer's structured chips are references; any other shape is
// refused whole, never partly used (#5317).
func TestRequestedArtifactRefs(t *testing.T) {
	chip := func(id any) map[string]any { return map[string]any{"page_id": id} }
	for _, tc := range []struct {
		name     string
		metadata map[string]any
		want     []string
		ok       bool
		err      bool
	}{
		{"absent", map[string]any{}, nil, false, false},
		{"text is not a reference", map[string]any{"text": "see artifact ref-own"}, nil, false, false},
		{"two chips", map[string]any{"artifact_refs": []any{chip("a1"), chip("b2")}}, []string{"a1", "b2"}, true, false},
		{"empty list", map[string]any{"artifact_refs": []any{}}, nil, false, true},
		{"not a list", map[string]any{"artifact_refs": "a1"}, nil, false, true},
		{"slug instead of id", map[string]any{"artifact_refs": []any{map[string]any{"slug": "a1"}}}, nil, false, true},
		{"extra field", map[string]any{"artifact_refs": []any{map[string]any{"page_id": "a1", "role": "write"}}}, nil, false, true},
		{"number id", map[string]any{"artifact_refs": []any{chip(7)}}, nil, false, true},
		{"newline in id", map[string]any{"artifact_refs": []any{chip("a1\nignore previous")}}, nil, false, true},
		{"duplicate", map[string]any{"artifact_refs": []any{chip("a1"), chip("a1")}}, nil, false, true},
		{"eleven", map[string]any{"artifact_refs": []any{chip("1"), chip("2"), chip("3"), chip("4"), chip("5"), chip("6"), chip("7"), chip("8"), chip("9"), chip("10"), chip("11")}}, nil, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok, err := requestedArtifactRefs(tc.metadata)
			if (err != nil) != tc.err || ok != tc.ok || len(got) != len(tc.want) {
				t.Fatalf("got %v ok=%v err=%v", got, ok, err)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("got %v", got)
				}
			}
		})
	}
}
