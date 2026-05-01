package main

import (
	"reflect"
	"testing"
)

func TestUniqueSorted(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{"empty", nil, nil},
		{"single", []string{"a"}, []string{"a"}},
		{"already sorted", []string{"a", "b", "c"}, []string{"a", "b", "c"}},
		{"reversed", []string{"c", "b", "a"}, []string{"a", "b", "c"}},
		{"with duplicates", []string{"b", "a", "b", "c", "a"}, []string{"a", "b", "c"}},
		{"all same", []string{"x", "x", "x"}, []string{"x"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := uniqueSorted(c.in)
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("uniqueSorted(%v) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}
