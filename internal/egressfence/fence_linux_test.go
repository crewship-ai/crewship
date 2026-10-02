//go:build linux

package egressfence

import (
	"net/netip"
	"testing"

	"github.com/google/nftables/expr"
)

// The rule order is the security property: the Docker-DNS reject must come
// before the loopback accept (127.0.0.11 is on lo), and the final rule must
// reject so a non-allowed socket fails fast rather than hanging on the drop
// policy.
func TestRulesOrder(t *testing.T) {
	for _, tc := range []struct {
		name  string
		uids  []uint32
		count int
	}{
		{"one uid", []uint32{1002}, 6},
		{"two uids", []uint32{1002, 1003}, 8},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rules := Rules(Spec{AllowUIDs: tc.uids})
			if len(rules) != tc.count {
				t.Fatalf("got %d rules, want %d", len(rules), tc.count)
			}
			dnsReject := len(tc.uids)
			if !endsWithReject(rules[dnsReject]) || !hasPayloadDaddr(rules[dnsReject]) {
				t.Fatalf("rule %d must reject Docker DNS for everyone else", dnsReject)
			}
			lo := dnsReject + 1
			if m, ok := rules[lo][0].(*expr.Meta); !ok || m.Key != expr.MetaKeyOIFNAME {
				t.Fatalf("rule %d must be the loopback accept, after the DNS reject", lo)
			}
			if !endsWithReject(rules[len(rules)-1]) || len(rules[len(rules)-1]) != 1 {
				t.Fatal("last rule must be a bare reject")
			}
			for i := 0; i < len(tc.uids); i++ {
				if _, ok := rules[i][len(rules[i])-1].(*expr.Verdict); !ok {
					t.Fatalf("rule %d must accept Docker DNS for an allowed uid", i)
				}
			}
		})
	}
}

func endsWithReject(r []expr.Any) bool {
	_, ok := r[len(r)-1].(*expr.Reject)
	return ok
}

func hasPayloadDaddr(r []expr.Any) bool {
	for _, e := range r {
		if p, ok := e.(*expr.Payload); ok && p.Offset == 16 && p.Len == 4 {
			return true
		}
	}
	return false
}

// Service endpoints (own crew's services) are accepted exactly — address,
// protocol, port — after the UID accepts and before the final reject.
func TestRulesWithDests(t *testing.T) {
	d1 := Dest{Addr: netip.MustParseAddr("10.231.0.3"), Port: 6379, Proto: "tcp"}
	d2 := Dest{Addr: netip.MustParseAddr("10.231.0.4"), Port: 53, Proto: "udp"}
	rules := Rules(Spec{AllowUIDs: []uint32{1002}, AllowDests: []Dest{d1, d2}})
	if len(rules) != 8 {
		t.Fatalf("got %d rules, want 8", len(rules))
	}
	for _, i := range []int{5, 6} {
		if _, ok := rules[i][len(rules[i])-1].(*expr.Verdict); !ok || len(rules[i]) != 9 {
			t.Fatalf("rule %d must be a full endpoint accept", i)
		}
	}
	if !endsWithReject(rules[7]) {
		t.Fatal("the final reject must stay last")
	}
}
