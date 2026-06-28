package pipeline

import "testing"

func TestHostInEgressTargets(t *testing.T) {
	cases := []struct {
		host    string
		targets []string
		want    bool
	}{
		{"httpbin.org", nil, true},                               // empty targets → no restriction
		{"httpbin.org", []string{}, true},                        // empty slice → no restriction
		{"httpbin.org", []string{"httpbin.org"}, true},           // exact match
		{"example.com", []string{"httpbin.org"}, false},          // not in list → blocked
		{"cdn.discordapp.com", []string{"discordapp.com"}, true}, // subdomain allowed
		{"evilhttpbin.org", []string{"httpbin.org"}, false},      // suffix-bypass blocked (no leading dot)
		{"HTTPBIN.ORG", []string{"httpbin.org"}, true},           // case-insensitive
		{"httpbin.org.", []string{"httpbin.org"}, true},          // trailing-dot normalized
		{"a.b.x.com", []string{"x.com"}, true},                   // deep subdomain
		{"notx.com", []string{"x.com"}, false},                   // anti-bypass
	}
	for _, c := range cases {
		if got := hostInEgressTargets(c.host, c.targets); got != c.want {
			t.Errorf("hostInEgressTargets(%q, %v) = %v, want %v", c.host, c.targets, got, c.want)
		}
	}
}
