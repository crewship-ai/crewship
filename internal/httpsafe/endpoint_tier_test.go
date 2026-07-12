package httpsafe

import (
	"net"
	"testing"
)

// TestEndpointTiers locks the two-tier SSRF model (#961): the opt-in relaxes
// RFC1918/loopback/ULA but NEVER link-local cloud metadata, and IPv4-mapped
// IPv6 forms must be normalised so they can't bypass either tier.
func TestEndpointTiers(t *testing.T) {
	cases := []struct {
		ip           string
		blocked      bool // IsBlockedIP (strict default)
		hardBlocked  bool // IsHardBlockedIP (blocked even with opt-in)
		optInAllowed bool // reachable when allowPrivate=true
	}{
		// Cloud metadata / link-local — blocked in every mode.
		{"169.254.169.254", true, true, false},
		{"169.254.0.1", true, true, false},
		{"::ffff:169.254.169.254", true, true, false}, // IPv4-mapped bypass
		{"fe80::1", true, true, false},
		{"fd00:ec2::254", true, true, false}, // AWS EC2 IMDS IPv6 — ULA-shaped but must stay hard-blocked even with opt-in
		// RFC1918 / loopback / ULA — blocked by default, opened by opt-in.
		{"10.0.0.1", true, false, true},
		{"172.16.5.5", true, false, true},
		{"192.168.1.222", true, false, true},  // the dev2 LAN Ollama
		{"192.168.65.254", true, false, true}, // docker-desktop host-gateway
		{"127.0.0.1", true, false, true},
		{"::1", true, false, true},
		{"::ffff:10.0.0.1", true, false, true}, // IPv4-mapped private
		{"fc00::1", true, false, true},
		// Multicast / reserved — hard-blocked.
		{"224.0.0.1", true, true, false},
		{"255.255.255.255", true, true, false},
		{"0.0.0.0", true, true, false},
		// Public — never blocked.
		{"8.8.8.8", false, false, true},
		{"1.1.1.1", false, false, true},
		{"2606:4700:4700::1111", false, false, true},
	}
	for _, c := range cases {
		ip := net.ParseIP(c.ip)
		if ip == nil {
			t.Fatalf("bad test IP %q", c.ip)
		}
		if got := IsBlockedIP(ip); got != c.blocked {
			t.Errorf("IsBlockedIP(%s) = %v, want %v", c.ip, got, c.blocked)
		}
		if got := IsHardBlockedIP(ip); got != c.hardBlocked {
			t.Errorf("IsHardBlockedIP(%s) = %v, want %v", c.ip, got, c.hardBlocked)
		}
		// allowPrivate=true → reachable iff NOT hard-blocked.
		if got := IsBlockedIPForEndpoint(ip, true); got != !c.optInAllowed {
			t.Errorf("IsBlockedIPForEndpoint(%s, allowPrivate=true) = %v, want %v", c.ip, got, !c.optInAllowed)
		}
		// allowPrivate=false → strict, same as IsBlockedIP.
		if got := IsBlockedIPForEndpoint(ip, false); got != c.blocked {
			t.Errorf("IsBlockedIPForEndpoint(%s, allowPrivate=false) = %v, want %v", c.ip, got, c.blocked)
		}
	}
}

// #988 review: a zoned IPv6 link-local literal must normalize + block.
func TestParseIPStripZone(t *testing.T) {
	if ip := ParseIPStripZone("fe80::1%eth0"); ip == nil || !IsHardBlockedIP(ip) {
		t.Errorf("fe80::1%%eth0 should parse (zone stripped) and be hard-blocked, got %v", ip)
	}
	if ip := ParseIPStripZone("169.254.169.254"); ip == nil || !IsHardBlockedIP(ip) {
		t.Error("plain metadata IP should still parse+block")
	}
	if ip := ParseIPStripZone("not-an-ip.example.com"); ip != nil {
		t.Errorf("a hostname must return nil (defer to resolve-time guard), got %v", ip)
	}
}
