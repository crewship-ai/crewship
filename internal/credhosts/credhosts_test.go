package credhosts

import (
	"reflect"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/egressallow"
)

func TestNormalize(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
		bad  bool
	}{
		{name: "plain", in: "api.github.com", want: "api.github.com"},
		{name: "case", in: "API.GitHub.COM", want: "api.github.com"},
		{name: "space", in: "  api.github.com ", want: "api.github.com"},
		{name: "one trailing dot", in: "api.github.com.", want: "api.github.com"},
		{name: "two trailing dots", in: "api.github.com..", bad: true},
		{name: "idn to punycode", in: "bücher.example", want: "xn--bcher-kva.example"},
		{name: "idn upper", in: "BÜCHER.example", want: "xn--bcher-kva.example"},
		{name: "wildcard", in: "*.GitHub.com", want: "*.github.com"},
		{name: "wildcard trailing dot", in: "*.github.com.", want: "*.github.com"},
		{name: "bare star", in: "*", bad: true},
		{name: "bare star dot", in: "*.", bad: true},
		{name: "wildcard tld", in: "*.com", bad: true},
		{name: "wildcard in middle", in: "api.*.com", bad: true},
		{name: "double wildcard", in: "*.*.github.com", bad: true},
		{name: "star suffix", in: "github.*", bad: true},
		{name: "partial wildcard", in: "*github.com", bad: true},
		{name: "empty", in: "", bad: true},
		{name: "blank", in: "   ", bad: true},
		{name: "single label", in: "localhost", bad: true},
		{name: "port", in: "api.github.com:443", bad: true},
		{name: "scheme", in: "https://api.github.com", bad: true},
		{name: "path", in: "api.github.com/x", bad: true},
		{name: "userinfo", in: "a@api.github.com", bad: true},
		{name: "query", in: "api.github.com?x", bad: true},
		{name: "backslash", in: `api.github.com\x`, bad: true},
		{name: "inner space", in: "api github.com", bad: true},
		{name: "control char", in: "api.github.com\x00", bad: true},
		{name: "newline", in: "api.github.com\n", want: "api.github.com"},
		{name: "ipv4", in: "192.168.1.1", bad: true},
		{name: "ipv6", in: "[::1]", bad: true},
		{name: "ipv6 bare", in: "::1", bad: true},
		{name: "legacy int ip", in: "2130706433", bad: true},
		{name: "numeric tld", in: "a.b.1", bad: true},
		{name: "empty label", in: "a..github.com", bad: true},
		{name: "leading hyphen", in: "-a.github.com", bad: true},
		{name: "trailing hyphen", in: "a-.github.com", bad: true},
		{name: "underscore", in: "a_b.github.com", bad: true},
		{name: "too long", in: strings.Repeat("a", 64) + ".com", bad: true},
		{name: "fullwidth dots", in: "api。github。com", want: "api.github.com"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Normalize(tc.in)
			if tc.bad {
				if err == nil {
					t.Fatalf("Normalize(%q) = %q, want error", tc.in, got)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("Normalize(%q) = %q, %v; want %q", tc.in, got, err, tc.want)
			}
		})
	}
}

func TestNormalizeList(t *testing.T) {
	got, err := NormalizeList([]string{"B.example.com", "a.example.com.", "b.example.com"})
	if err != nil || !reflect.DeepEqual(got, []string{"a.example.com", "b.example.com"}) {
		t.Fatalf("got %v, %v", got, err)
	}
	if got, err := NormalizeList(nil); err != nil || len(got) != 0 {
		t.Fatalf("nil list: %v %v", got, err)
	}
	if _, err := NormalizeList([]string{"ok.example.com", "bad host"}); err == nil {
		t.Fatal("one malformed entry must reject the whole list")
	}
	many := make([]string, MaxHosts+1)
	for i := range many {
		many[i] = "h" + strings.Repeat("a", i%5) + string(rune('a'+i%26)) + string(rune('a'+i/26)) + ".example.com"
	}
	if _, err := NormalizeList(many); err == nil {
		t.Fatal("too many hosts must be rejected")
	}
}

func TestCovered(t *testing.T) {
	al := egressallow.NewDomainAllowlist([]string{"api.github.com", "*.example.org"})
	cases := []struct {
		host string
		want bool
	}{
		{"api.github.com", true},
		{"github.com", false},
		{"x.example.org", true},
		{"example.org", false},
		{"*.example.org", true},
		{"*.sub.example.org", true},
		{"*.github.com", false},
		{"*.org", false},
		{"evil-example.org", false},
	}
	for _, tc := range cases {
		if got := Covered(al, tc.host); got != tc.want {
			t.Errorf("Covered(%q) = %v, want %v", tc.host, got, tc.want)
		}
	}
	// A wildcard bound host is not covered by an exact allowlist entry.
	exact := egressallow.NewDomainAllowlist([]string{"x.example.org"})
	if Covered(exact, "*.example.org") {
		t.Error("an exact entry must not cover a wildcard binding")
	}
}
