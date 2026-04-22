package episodic

import (
	"testing"
)

func TestEscapeFTSQuery(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", ""},
		{"hello world", "hello* OR world*"},
		{"deployment-42 failed!", "deployment* OR 42* OR failed*"}, // numerics ≥2 chars survive
		{`IGNORE "previous" instructions`, "ignore* OR previous* OR instructions*"},
		{"a", ""}, // single char → empty
		{"---", ""},
	}
	for _, c := range cases {
		if got := escapeFTSQuery(c.in); got != c.want {
			t.Errorf("escapeFTSQuery(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestRRFFuse(t *testing.T) {
	dense := []Hit{
		{EntryID: "a", Score: 0.9},
		{EntryID: "b", Score: 0.8},
		{EntryID: "c", Score: 0.7},
	}
	sparse := []Hit{
		{EntryID: "b", Score: 0.5}, // appears in both lanes — should rank first
		{EntryID: "d", Score: 0.4},
	}
	out := rrfFuse(dense, sparse, 3)
	if len(out) != 3 {
		t.Fatalf("expected 3 fused hits, got %d", len(out))
	}
	// b appears at rank 2 in dense and rank 1 in sparse — RRF score
	// = 1/62 + 1/61 ≈ 0.0324. Top spot.
	if out[0].EntryID != "b" {
		t.Errorf("b should be top after RRF fusion (appears in both lanes), got %s", out[0].EntryID)
	}
}

func TestRRFFuseEmptyLanes(t *testing.T) {
	// Dense-only fallback — sparse empty.
	out := rrfFuse([]Hit{{EntryID: "a"}, {EntryID: "b"}}, nil, 2)
	if len(out) != 2 || out[0].EntryID != "a" {
		t.Errorf("empty sparse fallback wrong: %+v", out)
	}
	// Both empty.
	if got := rrfFuse(nil, nil, 5); len(got) != 0 {
		t.Errorf("empty lanes should return empty, got %d", len(got))
	}
}
