package editororigin

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Learning a Studio origin trusts headers any signed-in client can send
// outside a browser: Host, Origin, Referer and Sec-Fetch-Site. These tests
// play such a client (a script holding its own session JWT) against the
// real Studio origin, used by an administrator's browser.

const realHost = "crewship.example.test"

// forged is a request a signed-in script sends with headers of its choice.
func forged(host, origin string) *http.Request {
	r := httptest.NewRequest("GET", "/api/v1/me/preferences", nil)
	r.Host = host
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	r.Header.Set("Origin", origin)
	return r
}

// learnAs runs LearnOrigin as the auth middleware does for user.
func learnAs(h *Handler, r *http.Request, user string, admin bool) {
	h.LearnOrigin(r, user, func() bool { return admin })
}

func serveIndex(h *Handler, digest, host string, tlsConn bool) int {
	r := httptest.NewRequest("GET", "/_editor/b/"+digest+"/index.html", nil)
	r.Host = host
	if tlsConn {
		r.TLS = &tls.ConnectionState{}
	}
	r.Header.Set("Sec-Fetch-Dest", "iframe")
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w.Code
}

// redirectCheck is what probing http://<real host> returns behind the usual
// port-80 redirect to https: a confirmed isolation failure.
var redirectCheck = ProxyCheck{Status: ProxyCheckFailed, Isolation: true, Cause: CauseRedirect, Problems: []string{"redirected"}}

// An attacker who makes the server learn http://<real host> must not disable
// the editor on the real https origin, whether Crewship sees the browser's
// requests as https (trusted proxy) or as http (TLS ended at a proxy it does
// not trust).
func TestSchemeVariantCannotDisableTheRealOrigin(t *testing.T) {
	for _, tc := range []struct {
		name    string
		scheme  string
		tlsConn bool
	}{
		{"trusted proxy", "https", true},
		{"untrusted proxy", "http", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			scheme := tc.scheme
			h, digest := sameHostWith(t, Options{Scheme: func(*http.Request) string { return scheme }})
			learnAs(h, signedIn(realHost, "https://"+realHost+"/chat"), "admin", true)
			h.RecordCheck("https://"+realHost, ProxyCheck{Status: ProxyCheckPassed})
			learnAs(h, forged(realHost, "http://"+realHost), "mallory", false)
			if got := h.CheckTargets(); len(got) != 1 || got[0] != "https://"+realHost {
				t.Errorf("learned an http origin for a public host: %v", got)
			}
			// Even if such a state existed, it would not apply to https.
			h.RecordCheck("http://"+realHost, redirectCheck)
			h.RecordCheck("http://"+realHost, redirectCheck)
			if got := serveIndex(h, digest, realHost, tc.tlsConn); got != http.StatusOK {
				t.Errorf("real editor after the http variant: %d", got)
			}
			if got := h.Health(); got != HealthOK {
				t.Errorf("health %s", got)
			}
			// The real origin's own failure still disables it, also for a
			// request Crewship sees as http (fail closed).
			h.RecordCheck("https://"+realHost, redirectCheck)
			if got := serveIndex(h, digest, realHost, tc.tlsConn); got != http.StatusServiceUnavailable {
				t.Fatalf("real origin disabled, editor answered %d", got)
			}
		})
	}
}

// State is per exact origin: another port on the same host is another
// origin.
func TestDisabledStateIsPerExactOrigin(t *testing.T) {
	h, digest := zeroConfigFixture(t)
	learnAs(h, signedIn(realHost, "https://"+realHost+"/chat"), "admin", true)
	learnAs(h, forged(realHost+":8443", "https://"+realHost+":8443"), "mallory", false)
	h.RecordCheck("https://"+realHost+":8443", redirectCheck)
	if got := serveIndex(h, digest, realHost, true); got != http.StatusOK {
		t.Fatalf("another port disabled the real origin: %d", got)
	}
	if got := serveIndex(h, digest, realHost+":8443", true); got != http.StatusServiceUnavailable {
		t.Fatalf("disabled origin served: %d", got)
	}
}

// Metadata, link-local, unspecified, multicast and loopback addresses are
// never learned (so never probed) unless the server itself listens on
// loopback, and then only loopback is added.
func TestLearnOriginRefusesInternalAddresses(t *testing.T) {
	refused := []string{
		"http://169.254.169.254", "https://169.254.169.254", "https://169.254.169.254:8443",
		"https://0.0.0.0", "https://224.0.0.1", "https://255.255.255.255",
		"http://127.0.0.1:6379", "https://127.0.0.1:8443", "https://localhost:8443", "https://app.localhost",
		"https://2852039166", "https://0xa9fea9fe", "https://169.254.43518", "https://127.1",
		"http://10.0.0.5:6379", "http://" + realHost, "http://" + realHost + ":8080",
	}
	for _, loopback := range []bool{false, true} {
		h, _ := sameHostWith(t, Options{LoopbackServer: loopback})
		for i, origin := range refused {
			host := strings.TrimPrefix(strings.TrimPrefix(origin, "https://"), "http://")
			if loopback && (strings.Contains(host, "127.0.0.1") || strings.Contains(host, "localhost")) {
				continue
			}
			learnAs(h, forged(host, origin), fmt.Sprintf("u%d", i), true)
		}
		if got := h.CheckTargets(); len(got) != 0 {
			t.Fatalf("loopback server %v: learned %v", loopback, got)
		}
		if d := h.Detail(); len(d.Origins) != 0 {
			t.Fatalf("loopback server %v: reported %+v", loopback, d.Origins)
		}
	}
	// A server bound to loopback (development) learns its loopback origins.
	h, _ := sameHostWith(t, Options{LoopbackServer: true})
	learnAs(h, forged("127.0.0.1:8080", "http://127.0.0.1:8080"), "a", false)
	learnAs(h, forged("localhost:3000", "http://localhost:3000"), "b", false)
	if got := strings.Join(h.CheckTargets(), ","); got != "http://127.0.0.1:8080,http://localhost:3000" {
		t.Fatalf("loopback development targets %q", got)
	}
	// Private addresses over https are ordinary LAN installations.
	h, _ = zeroConfigFixture(t)
	learnAs(h, forged("10.0.0.5", "https://10.0.0.5"), "a", false)
	if got := h.CheckTargets(); len(got) != 1 {
		t.Fatalf("LAN origin not learned: %v", got)
	}
}

func TestProbeIPAllowed(t *testing.T) {
	for _, tc := range []struct {
		ip                string
		public, loopbackD bool
	}{
		{"169.254.169.254", false, false},
		{"169.254.0.1", false, false},
		{"fd00:ec2::254", false, false},
		{"fe80::1", false, false},
		{"0.0.0.0", false, false},
		{"::", false, false},
		{"224.0.0.1", false, false},
		{"ff02::1", false, false},
		{"255.255.255.255", false, false},
		{"127.0.0.1", false, true},
		{"::1", false, true},
		{"::ffff:169.254.169.254", false, false},
		{"10.0.0.5", true, true},
		{"203.0.113.7", true, true},
		{"2001:db8::1", true, true},
	} {
		ip := net.ParseIP(tc.ip)
		if got := ProbeIPAllowed(ip, false); got != tc.public {
			t.Errorf("%s: allowed %v, want %v", tc.ip, got, tc.public)
		}
		if got := ProbeIPAllowed(ip, true); got != tc.loopbackD {
			t.Errorf("%s with loopback: allowed %v, want %v", tc.ip, got, tc.loopbackD)
		}
	}
	if ProbeIPAllowed(nil, true) {
		t.Fatal("nil IP allowed")
	}
}

// Junk origins cannot keep the real origin from being learned, and cannot
// push it out once learned.
func TestJunkOriginsCannotBlockTheRealOrigin(t *testing.T) {
	real := "https://" + realHost
	t.Run("one user, many origins", func(t *testing.T) {
		h, _ := zeroConfigFixture(t)
		for i := range 8 {
			// Origin and Host differ: reported as a rewritten Host.
			learnAs(h, forged(realHost, fmt.Sprintf("https://junk%d.attacker.example", i)), "mallory", false)
			// Origin and Host agree: a probe target.
			host := fmt.Sprintf("own%d.attacker.example", i)
			learnAs(h, forged(host, "https://"+host), "mallory", false)
		}
		learnAs(h, signedIn(realHost, real+"/chat"), "alice", false)
		if got := h.CheckTargets(); len(got) != 2 || got[0] != real {
			t.Fatalf("targets %v: one per user, the real origin included", got)
		}
		var rewritten int
		for _, o := range h.Detail().Origins {
			if o.Cause == CauseHostRewritten {
				rewritten++
				if o.LearnedFrom != "mallory" {
					t.Fatalf("learned_from %q", o.LearnedFrom)
				}
			}
		}
		if rewritten != 1 {
			t.Fatalf("%d rewritten reports from one user", rewritten)
		}
	})
	t.Run("many users", func(t *testing.T) {
		h, _ := zeroConfigFixture(t)
		for i := range maxLearnedOrigins {
			host := fmt.Sprintf("junk%d.attacker.example", i)
			learnAs(h, forged(host, "https://"+host), fmt.Sprintf("sock%d", i), false)
		}
		learnAs(h, signedIn(realHost, real+"/chat"), "admin", true)
		targets := h.CheckTargets()
		if len(targets) != maxLearnedOrigins || !contains(targets, real) {
			t.Fatalf("administrator's origin not learned over junk: %v", targets)
		}
		// More junk, from more users: the real origin stays.
		for i := range 10 {
			host := fmt.Sprintf("more%d.attacker.example", i)
			learnAs(h, forged(host, "https://"+host), fmt.Sprintf("puppet%d", i), false)
		}
		if !contains(h.CheckTargets(), real) {
			t.Fatalf("real origin evicted: %v", h.CheckTargets())
		}
	})
	t.Run("expiry", func(t *testing.T) {
		now := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
		timeNow = func() time.Time { return now }
		t.Cleanup(func() { timeNow = time.Now })
		h, _ := zeroConfigFixture(t)
		for i := range maxLearnedOrigins {
			host := fmt.Sprintf("junk%d.attacker.example", i)
			learnAs(h, forged(host, "https://"+host), fmt.Sprintf("sock%d", i), false)
		}
		learnAs(h, signedIn(realHost, real+"/chat"), "alice", false)
		if contains(h.CheckTargets(), real) {
			t.Fatal("slots should be full for a regular user")
		}
		now = now.Add(LearnedOriginTTL + time.Minute)
		learnAs(h, signedIn(realHost, real+"/chat"), "alice", false)
		if got := h.CheckTargets(); len(got) != 1 || got[0] != real {
			t.Fatalf("expired junk still holds the slots: %v", got)
		}
		// An origin in use is not expired.
		now = now.Add(LearnedOriginTTL / 2)
		learnAs(h, signedIn(realHost, real+"/chat"), "bob", false)
		now = now.Add(LearnedOriginTTL / 2)
		if got := h.CheckTargets(); len(got) != 1 {
			t.Fatalf("origin in use expired: %v", got)
		}
	})
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// The public /healthz state follows only the configured origin and origins
// an administrator used: an origin a regular user taught (one the attacker
// answers end to end) changes nothing there, only in the admin detail.
func TestAttackerOriginCannotFlipPublicHealth(t *testing.T) {
	h, _ := zeroConfigFixture(t)
	learnAs(h, signedIn(realHost, "https://"+realHost+"/chat"), "admin", true)
	h.RecordCheck("https://"+realHost, ProxyCheck{Status: ProxyCheckPassed})
	learnAs(h, forged("evil.example", "https://evil.example"), "mallory", false)
	learnAs(h, forged(realHost, "https://elsewhere.example"), "mallory", false)
	if got := h.Health(); got != HealthOK {
		t.Fatalf("attacker origin pending/rewritten: health %s", got)
	}
	h.RecordCheck("https://evil.example", redirectCheck)
	if got := h.Health(); got != HealthOK {
		t.Fatalf("attacker origin disabled: health %s", got)
	}
	var found bool
	for _, o := range h.Detail().Origins {
		if o.Origin == "https://evil.example" {
			found = o.Disabled && o.LearnedFrom == "mallory" && !o.AdminSession
		}
	}
	if !found {
		t.Fatalf("attacker origin not in the admin detail: %+v", h.Detail().Origins)
	}
	// An administrator using an origin makes it count.
	learnAs(h, forged("evil.example", "https://evil.example"), "admin2", true)
	if got := h.Health(); got != HealthDisabled {
		t.Fatalf("administrator's disabled origin: health %s", got)
	}
}

// A front that strips RouteHeader and also decodes %2f: the traversal
// reached Crewship outside the editor, but without the marker the check
// cannot see it. That must disable the editor, not pass it by.
func TestMarkerStrippedFailsClosed(t *testing.T) {
	h, _ := zeroConfigFixture(t)
	inner := classFront("nginx-uri", Gate(h, productionStudio()))
	strip := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		inner.ServeHTTP(stripHeader{w, RouteHeader}, r)
	})
	srv := httptest.NewServer(strip)
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c := CheckProxy(ctx, srv.Client(), srv.URL)
	if c.Status != ProxyCheckFailed || !c.Isolation || c.Cause != CauseNoMarker {
		t.Fatalf("marker stripped: %+v", c)
	}
	// Recorded, it disables the origin; the same re-enable rule applies.
	p, digest := sameHostFixture(t)
	p.RecordCheck(studio, c)
	if got := serveIndex(p, digest, "studio.example.test", true); got != http.StatusServiceUnavailable {
		t.Fatalf("editor served with the marker stripped: %d", got)
	}
	for range ReenableAfterPasses {
		p.RecordCheck(studio, ProxyCheck{Status: ProxyCheckPassed})
	}
	if got := serveIndex(p, digest, "studio.example.test", true); got != http.StatusOK {
		t.Fatalf("not re-enabled after %d passes: %d", ReenableAfterPasses, got)
	}
}
