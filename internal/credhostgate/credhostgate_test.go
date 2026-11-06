package credhostgate

import "testing"

func TestPermitsHost(t *testing.T) {
	cases := []struct {
		name, raw, host string
		want            bool
	}{
		{"empty is unbound", "", "api.example.com", true},
		{"empty list is unbound", "[]", "api.example.com", true},
		{"exact host", `["api.example.com"]`, "api.example.com", true},
		{"wildcard", `["*.example.com"]`, "api.example.com", true},
		{"other host", `["api.other.com"]`, "api.example.com", false},
		{"unreadable", `not-json`, "api.example.com", false},
		{"invalid entry", `["*"]`, "api.example.com", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := PermitsHost(tc.raw, tc.host); got != tc.want {
				t.Fatalf("PermitsHost(%q, %q) = %v, want %v", tc.raw, tc.host, got, tc.want)
			}
		})
	}
}
