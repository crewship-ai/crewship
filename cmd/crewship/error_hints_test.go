package main

import (
	"reflect"
	"testing"
)

func TestLevenshtein(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"", "", 0},
		{"a", "", 1},
		{"", "abc", 3},
		{"kitten", "sitting", 3},
		{"viktor", "vitkor", 2}, // transpose
		{"viktor", "viktor", 0},
		{"viktor", "victor", 1},
		{"abc", "xyz", 3},
	}
	for _, c := range cases {
		if got := levenshtein(c.a, c.b); got != c.want {
			t.Errorf("levenshtein(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestNearestSlugs_Typo(t *testing.T) {
	pool := []string{"viktor", "eva", "piotr", "captain", "lead"}
	got := nearestSlugs("vitkor", pool, 3)
	if len(got) == 0 || got[0] != "viktor" {
		t.Errorf("typo correction: got %v, want viktor first", got)
	}
}

func TestNearestSlugs_NoMatches(t *testing.T) {
	pool := []string{"viktor", "eva"}
	got := nearestSlugs("zzzzzz", pool, 3)
	if len(got) != 0 {
		t.Errorf("expected no matches for distant target, got %v", got)
	}
}

func TestNearestSlugs_RespectsMaxN(t *testing.T) {
	pool := []string{"abc", "abd", "abe", "abf", "abg"}
	got := nearestSlugs("ab", pool, 2)
	if len(got) != 2 {
		t.Errorf("expected 2 matches, got %d: %v", len(got), got)
	}
}

func TestNearestSlugs_DeterministicOrder(t *testing.T) {
	pool := []string{"piotr", "viktor", "vector", "victor"}
	got := nearestSlugs("victor", pool, 3)
	// distances: viktor=1, victor=0, vector=1, piotr=4
	// expected: victor (0), vector (1, alphabetical first), viktor (1)
	want := []string{"victor", "vector", "viktor"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ordering: got %v, want %v", got, want)
	}
}

func TestNearestSlugs_Empty(t *testing.T) {
	if got := nearestSlugs("", []string{"a"}, 3); got != nil {
		t.Errorf("empty target should yield nil, got %v", got)
	}
	if got := nearestSlugs("x", nil, 3); got != nil {
		t.Errorf("empty pool should yield nil, got %v", got)
	}
}

func TestTruncateList(t *testing.T) {
	short := []string{"a", "b", "c"}
	if got := truncateList(short, 5); !reflect.DeepEqual(got, short) {
		t.Errorf("under maxN should pass through: got %v", got)
	}
	long := []string{"a", "b", "c", "d", "e"}
	got := truncateList(long, 3)
	want := []string{"a", "b", "c", "(+2 more)"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("truncated: got %v, want %v", got, want)
	}
}

func TestItoa(t *testing.T) {
	cases := map[int]string{0: "0", 1: "1", 9: "9", 10: "10", 1234: "1234"}
	for in, want := range cases {
		if got := itoa(in); got != want {
			t.Errorf("itoa(%d) = %q, want %q", in, got, want)
		}
	}
}
