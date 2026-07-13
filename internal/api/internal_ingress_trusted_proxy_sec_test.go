package api

import (
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
)

// #1020: the internal keeper surface must gate on the REAL client behind a
// trusted reverse proxy, fail closed, and never honor a spoofable X-Forwarded-
// For from a direct (untrusted) connection.

func mustParseCIDRs(t *testing.T, ss ...string) []*net.IPNet {
	t.Helper()
	var out []*net.IPNet
	for _, s := range ss {
		_, n, err := net.ParseCIDR(s)
		if err != nil {
			t.Fatalf("parse %q: %v", s, err)
		}
		out = append(out, n)
	}
	return out
}

func TestRightmostUntrustedXFF(t *testing.T) {
	t.Parallel()
	trusted := mustParseCIDRs(t, "127.0.0.0/8", "10.0.0.0/8")
	cases := []struct {
		name    string
		headers []string
		wantIP  string
		wantOK  bool
	}{
		{"single public client", []string{"8.8.8.8"}, "8.8.8.8", true},
		{"attacker-injected leftmost ignored", []string{"10.9.9.9, 8.8.8.8"}, "8.8.8.8", true},
		{"skip trusted proxy hops right-to-left", []string{"192.168.1.5, 10.0.0.2"}, "192.168.1.5", true},
		{"multiple headers flattened", []string{"192.168.1.5", "10.0.0.2"}, "192.168.1.5", true},
		{"empty header fails closed", []string{""}, "", false},
		{"no headers fails closed", nil, "", false},
		{"all-trusted chain fails closed", []string{"10.0.0.1, 127.0.0.1"}, "", false},
		{"garbage hop fails closed", []string{"not-an-ip"}, "", false},
		{"garbage rightmost fails closed even with valid left", []string{"8.8.8.8, garbage"}, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ip, ok := rightmostUntrustedXFF(tc.headers, trusted)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v (ip=%v)", ok, tc.wantOK, ip)
			}
			if tc.wantOK && ip.String() != tc.wantIP {
				t.Errorf("ip = %v, want %s", ip, tc.wantIP)
			}
		})
	}
}

func TestParseInternalTrustedProxies_ExplicitNoAutoTrust(t *testing.T) {
	t.Parallel()
	if got := parseInternalTrustedProxies("", testLogger()); got != nil {
		t.Errorf("empty config = %v, want nil (no auto-trust of any range)", got)
	}
	nets := parseInternalTrustedProxies("127.0.0.1, 10.0.0.0/8, garbage/99, 192.168.1.1", testLogger())
	if len(nets) != 3 {
		t.Fatalf("parsed %d nets, want 3 (garbage skipped): %v", len(nets), nets)
	}
	// The bare IPs became host routes; a private LAN IP is NOT implicitly there.
	if ipInNets(net.ParseIP("172.16.0.1"), nets) {
		t.Error("172.16.0.1 must NOT be trusted — nothing auto-added")
	}
	if !ipInNets(net.ParseIP("127.0.0.1"), nets) || !ipInNets(net.ParseIP("10.1.2.3"), nets) {
		t.Error("explicitly-listed ranges must be trusted")
	}
}

// TestRequireInternal_TrustedProxyGate drives the middleware end-to-end: a
// request reaching the downstream (200) proves it passed the origin gate; a 404
// proves the gate blocked it.
func TestRequireInternal_TrustedProxyGate(t *testing.T) {
	downstream := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	tok := internaltoken.DeriveWorkspaceToken(bindTestMaster, "ws_a")

	cases := []struct {
		name       string
		trustedEnv string
		remoteAddr string
		xff        []string // X-Forwarded-For header lines (nil = header absent)
		want       int
	}{
		// Default (no trusted proxies) — today's behaviour on the direct peer.
		{"default_direct_public_denied", "", "8.8.8.8:1", nil, http.StatusNotFound},
		{"default_direct_private_allowed", "", "10.1.2.3:1", nil, http.StatusOK},
		{"default_loopback_allowed", "", "127.0.0.1:1", nil, http.StatusOK},
		// XFF from a direct (untrusted) peer is NEVER honored — spoofing.
		{"default_public_spoofed_xff_ignored", "", "8.8.8.8:1", []string{"127.0.0.1"}, http.StatusNotFound},
		{"trusted_set_but_direct_public_spoofed_xff_ignored", "127.0.0.1/32", "8.8.8.8:1", []string{"10.0.0.1"}, http.StatusNotFound},
		// Behind a trusted proxy (loopback): gate on the resolved real client.
		{"proxy_public_client_denied", "127.0.0.1/32", "127.0.0.1:1", []string{"8.8.8.8"}, http.StatusNotFound},
		{"proxy_private_client_allowed", "127.0.0.1/32", "127.0.0.1:1", []string{"10.1.2.3"}, http.StatusOK},
		{"proxy_injected_leftmost_still_denied", "127.0.0.1/32", "127.0.0.1:1", []string{"10.0.0.1, 8.8.8.8"}, http.StatusNotFound},
		// Fail-closed: proxy present but no usable client hop.
		{"proxy_empty_xff_failclosed", "127.0.0.1/32", "127.0.0.1:1", []string{""}, http.StatusNotFound},
		{"proxy_garbage_xff_failclosed", "127.0.0.1/32", "127.0.0.1:1", []string{"not-an-ip"}, http.StatusNotFound},
		// Self-call: trusted-proxy IP but NO XFF header → treated as a direct
		// same-host call (crewshipd's loopback self-calls), gated on loopback.
		{"proxy_ip_no_xff_selfcall_allowed", "127.0.0.1/32", "127.0.0.1:1", nil, http.StatusOK},
		// Multi-hop trusted chain resolves to the private client.
		{"proxy_chain_resolves_private_client", "127.0.0.1/32,10.0.0.0/8", "127.0.0.1:1", []string{"192.168.1.5, 10.0.0.2"}, http.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("CREWSHIP_INTERNAL_TRUSTED_PROXIES", tc.trustedEnv)
			t.Setenv("CREWSHIP_INTERNAL_ALLOW_ANY", "") // never the blunt bypass
			h := NewInternalHandler(nil, bindTestMaster, testLogger())

			req := httptest.NewRequest(http.MethodGet, "/x?workspace_id=ws_a", nil)
			req.RemoteAddr = tc.remoteAddr
			req.Header.Set("X-Internal-Token", tok)
			for _, v := range tc.xff {
				req.Header.Add("X-Forwarded-For", v)
			}
			rr := httptest.NewRecorder()
			h.requireInternal(downstream).ServeHTTP(rr, req)

			if rr.Code != tc.want {
				t.Errorf("status = %d, want %d (remote=%s xff=%v trusted=%q)",
					rr.Code, tc.want, tc.remoteAddr, tc.xff, tc.trustedEnv)
			}
		})
	}
}

// TestRequireInternal_XRealIP covers the nginx X-Real-IP fallback (#1020 F1):
// a trusted proxy that sets only X-Real-IP (no XFF) must still resolve the real
// client, not fall through to the proxy's loopback (which would fail open).
func TestRequireInternal_XRealIP(t *testing.T) {
	downstream := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	tok := internaltoken.DeriveWorkspaceToken(bindTestMaster, "ws_a")
	cases := []struct {
		name       string
		trustedEnv string
		remoteAddr string
		xff        string // "" = absent
		xRealIP    string // "" = absent
		want       int
	}{
		{"proxy_xrealip_public_denied", "127.0.0.1/32", "127.0.0.1:1", "", "8.8.8.8", http.StatusNotFound},
		{"proxy_xrealip_private_allowed", "127.0.0.1/32", "127.0.0.1:1", "", "10.1.2.3", http.StatusOK},
		{"proxy_xrealip_garbage_failclosed", "127.0.0.1/32", "127.0.0.1:1", "", "not-an-ip", http.StatusNotFound},
		{"xff_wins_over_xrealip", "127.0.0.1/32", "127.0.0.1:1", "8.8.8.8", "10.1.2.3", http.StatusNotFound}, // XFF public → 404, XRIP ignored
		{"direct_untrusted_xrealip_ignored", "127.0.0.1/32", "8.8.8.8:1", "", "127.0.0.1", http.StatusNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("CREWSHIP_INTERNAL_TRUSTED_PROXIES", tc.trustedEnv)
			t.Setenv("CREWSHIP_INTERNAL_ALLOW_ANY", "")
			h := NewInternalHandler(nil, bindTestMaster, testLogger())
			req := httptest.NewRequest(http.MethodGet, "/x?workspace_id=ws_a", nil)
			req.RemoteAddr = tc.remoteAddr
			req.Header.Set("X-Internal-Token", tok)
			if tc.xff != "" {
				req.Header.Set("X-Forwarded-For", tc.xff)
			}
			if tc.xRealIP != "" {
				req.Header.Set("X-Real-IP", tc.xRealIP)
			}
			rr := httptest.NewRecorder()
			h.requireInternal(downstream).ServeHTTP(rr, req)
			if rr.Code != tc.want {
				t.Errorf("status = %d, want %d", rr.Code, tc.want)
			}
		})
	}
}

// TestRequireInternal_IPv6 exercises the IPv6 paths (loopback ::1, ULA, the
// bare-IP→/128 trusted parse).
func TestRequireInternal_IPv6(t *testing.T) {
	downstream := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	tok := internaltoken.DeriveWorkspaceToken(bindTestMaster, "ws_a")
	cases := []struct {
		name, trustedEnv, remoteAddr, xff string
		want                              int
	}{
		{"direct_v6_loopback_allowed", "", "[::1]:1", "", http.StatusOK},
		{"direct_v6_public_denied", "", "[2001:db8::1]:1", "", http.StatusNotFound},
		{"proxy_v6_ula_client_allowed", "::1/128", "[::1]:1", "fc00::1", http.StatusOK},
		{"proxy_v6_public_client_denied", "::1/128", "[::1]:1", "2001:db8::5", http.StatusNotFound},
		{"proxy_v6_all_trusted_failclosed", "::1/128", "[::1]:1", "::1", http.StatusNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("CREWSHIP_INTERNAL_TRUSTED_PROXIES", tc.trustedEnv)
			t.Setenv("CREWSHIP_INTERNAL_ALLOW_ANY", "")
			h := NewInternalHandler(nil, bindTestMaster, testLogger())
			req := httptest.NewRequest(http.MethodGet, "/x?workspace_id=ws_a", nil)
			req.RemoteAddr = tc.remoteAddr
			req.Header.Set("X-Internal-Token", tok)
			if tc.xff != "" {
				req.Header.Set("X-Forwarded-For", tc.xff)
			}
			rr := httptest.NewRecorder()
			h.requireInternal(downstream).ServeHTTP(rr, req)
			if rr.Code != tc.want {
				t.Errorf("status = %d, want %d", rr.Code, tc.want)
			}
		})
	}
}

// TestRequireInternal_MasterTokenLoopbackPin_WithProxy pins that the F-6
// master-token loopback pin now keys off the XFF-RESOLVED client (#1020 F3):
// the master token is accepted for a self-call (no forwarding header → loopback
// direct peer) but refused when a trusted proxy resolves a non-loopback client.
func TestRequireInternal_MasterTokenLoopbackPin_WithProxy(t *testing.T) {
	downstream := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	cases := []struct {
		name, xff string
		want      int
	}{
		{"master_selfcall_no_xff_allowed", "", http.StatusOK},                            // loopback self-call
		{"master_behind_proxy_private_client_refused", "10.1.2.3", http.StatusForbidden}, // resolved client not loopback → master pin refuses
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("CREWSHIP_INTERNAL_TRUSTED_PROXIES", "127.0.0.1/32")
			t.Setenv("CREWSHIP_INTERNAL_ALLOW_ANY", "")
			h := NewInternalHandler(nil, bindTestMaster, testLogger())
			req := httptest.NewRequest(http.MethodGet, "/x", nil)
			req.RemoteAddr = "127.0.0.1:1"
			req.Header.Set("X-Internal-Token", bindTestMaster) // the MASTER token
			if tc.xff != "" {
				req.Header.Set("X-Forwarded-For", tc.xff)
			}
			rr := httptest.NewRecorder()
			h.requireInternal(downstream).ServeHTTP(rr, req)
			if rr.Code != tc.want {
				t.Errorf("status = %d, want %d (body=%s)", rr.Code, tc.want, rr.Body.String())
			}
		})
	}
}

func TestParseInternalTrustedProxies_RejectsAllZeros(t *testing.T) {
	t.Parallel()
	for _, z := range []string{"0.0.0.0/0", "::/0", "0.0.0.0/0,::/0"} {
		if got := parseInternalTrustedProxies(z, testLogger()); len(got) != 0 {
			t.Errorf("%q: parsed %d nets, want 0 (all-zeros must be rejected)", z, len(got))
		}
	}
	// A mix keeps the valid entry, drops the all-zeros one.
	got := parseInternalTrustedProxies("0.0.0.0/0, 10.0.0.5/32", testLogger())
	if len(got) != 1 || !ipInNets(net.ParseIP("10.0.0.5"), got) {
		t.Errorf("mixed config = %v, want just 10.0.0.5/32", got)
	}
}
