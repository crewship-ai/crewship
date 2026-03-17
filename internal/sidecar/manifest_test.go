package sidecar

import (
	"testing"
)

func TestMergeUnique(t *testing.T) {
	tests := []struct {
		name      string
		existing  []string
		additions []string
		want      []string
	}{
		{"empty", nil, nil, nil},
		{"add to empty", nil, []string{"a", "b"}, []string{"a", "b"}},
		{"no duplicates", []string{"a"}, []string{"b"}, []string{"a", "b"}},
		{"with duplicates", []string{"a", "b"}, []string{"b", "c"}, []string{"a", "b", "c"}},
		{"all duplicates", []string{"a", "b"}, []string{"a", "b"}, []string{"a", "b"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := mergeUnique(tt.existing, tt.additions)
			if len(got) != len(tt.want) {
				t.Fatalf("mergeUnique(%v, %v) = %v, want %v", tt.existing, tt.additions, got, tt.want)
			}
			for i, v := range got {
				if v != tt.want[i] {
					t.Errorf("mergeUnique[%d] = %q, want %q", i, v, tt.want[i])
				}
			}
		})
	}
}
