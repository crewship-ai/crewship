package main

import "testing"

func TestFirstLine(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"hello\nworld", "hello"},
		{"   \n\nfirst non-empty\nrest", "first non-empty"},
		{"single", "single"},
		{"", ""},
		{"\n\n\n", ""},
		{"  trim spaces  \nnext", "trim spaces"},
	}
	for _, c := range cases {
		if got := firstLine(c.in); got != c.want {
			t.Errorf("firstLine(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
