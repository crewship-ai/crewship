//go:build linux && restrictedruntime_live

package restrictedruntime

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func connectedLive(t *testing.T) (*liveFixture, Plan, *atomic.Int32, *atomic.Bool) {
	t.Helper()
	f := live(t)
	p := connectedPlan()
	p.Credentials = []Credential{{ID: "direct", Env: "DIRECT_TOKEN", File: "direct"}}
	p.Network.Grants[0].URL = "https://example.com/echo"
	p.Network.Grants[0].CredentialID = "broker-key"
	p.Network.Credentials = []BrokerCredential{{ID: "broker-key", Revision: "r1", Provider: "test", Account: "account-a", Delivery: "broker-bearer-v1"}}
	p.Command = []string{"sh", "-c", `printf '%s' "$CREWSHIP_BROKER_TOKEN" > /home/agent/broker-token; exec sleep 3600`}
	f.m.Authority = &brokerFixtureAuthority{fixtureAuthority: f.a, material: BoundSecret{ID: "broker-key", Revision: "r1", Provider: "test", Account: "account-a", Value: "synthetic-broker-only-secret", Expires: time.Now().Add(time.Hour)}}
	calls := &atomic.Int32{}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != "POST" || r.URL.Path != "/echo" || r.Header.Get("Authorization") != "Bearer synthetic-broker-only-secret" {
			t.Error("wrong fixed upstream identity")
		}
		_, _ = w.Write([]byte("fixed-operation-ok"))
	}))
	t.Cleanup(server.Close)
	tr := syntheticBrokerTransport(t, server)
	blocked := &atomic.Bool{}
	tr.lookup = func(context.Context, string) ([]net.IPAddr, error) {
		ip := "8.8.8.8"
		if blocked.Load() {
			ip = "127.0.0.1"
		}
		return []net.IPAddr{{IP: net.ParseIP(ip)}}, nil
	}
	f.m.brokerTransport = tr
	return f, p, calls, blocked
}

const liveBrokerCall = `wget -q -T 3 -O - --post-data='{}' --header="Authorization: Bearer $(cat /home/agent/broker-token)" http://127.0.0.1:9121/v1/operations/echo`

func TestLiveFixedOperationBroker(t *testing.T) {
	f, p, calls, blocked := connectedLive(t)
	started := time.Now()
	s := f.start("a", p, "synthetic-direct-a")
	t.Logf("connected_start_ms=%.3f", float64(time.Since(started).Microseconds())/1000)
	before := time.Now()
	if got := f.shell(s, liveBrokerCall); got != "fixed-operation-ok" {
		t.Fatal("allowed TLS upstream failed")
	}
	t.Logf("connected_call_ms=%.3f", float64(time.Since(before).Microseconds())/1000)
	f.shell(s, `set -eu; deny() { if "$@"; then exit 91; fi; }; deny wget -q -T 2 -O /dev/null --post-data='{}' --header='Authorization: Bearer wrong' http://127.0.0.1:9121/v1/operations/echo; deny wget -q -T 2 -O /dev/null http://127.0.0.1:9121/credentials; needle=$(printf 'synthetic-broker-only-%s' secret); if grep -R -a -q "$needle" /secrets /home/agent /tmp /data 2>/dev/null; then exit 92; fi; for p in /proc/[0-9]*; do if cat "$p/environ" "$p/cmdline" 2>/dev/null | grep -q "$needle"; then exit 93; fi; done; echo broker-boundaries-ok`)
	if calls.Load() != 1 {
		t.Fatal("denied token/operation reached upstream")
	}
	// Same agent, a second isolated client. A real token from another attempt fails.
	other := p
	other.Attempt = "other"
	other.Principal = "h2"
	other.Scope = "h2"
	other.OriginID = "chat2"
	network := *p.Network
	network.Audience = "audience2"
	other.Network = &network
	b := f.start("b", other, "synthetic-direct-b")
	// Neighbor service is live in B's network namespace, absent in A's.
	f.background(b, "/opt/crewship-runner", []string{"mock"}, map[string]string{"Token": "synthetic-neighbor", "Account": "neighbor-b"}, "1002:1002")
	if f.shell(b, `wget -q -T 2 -O - http://127.0.0.1:9120/count`) != "0" {
		t.Fatal("neighbor service positive control failed")
	}
	f.shell(s, `if wget -q -T 2 -O /dev/null http://127.0.0.1:9120/count; then exit 97; fi`)
	foreign := strings.TrimSpace(f.shell(b, "cat /home/agent/broker-token"))
	f.shell(s, "if wget -q -T 2 -O /dev/null --post-data='{}' --header='Authorization: Bearer "+foreign+"' http://127.0.0.1:9121/v1/operations/echo; then exit 94; fi")
	if got := f.shell(b, liveBrokerCall); got != "fixed-operation-ok" {
		t.Fatal("other client positive control failed")
	}
	if calls.Load() != 2 {
		t.Fatal("foreign token reached upstream")
	}
	// A previously routable DNS answer changes to loopback: no new TLS call.
	blocked.Store(true)
	f.shell(s, "if "+liveBrokerCall+"; then exit 95; fi")
	if calls.Load() != 2 {
		t.Fatal("DNS rebound target received request")
	}
	blocked.Store(false)
	f.a.revoke("a")
	// Request can race whole-container termination; neither success nor an extra
	// upstream request is acceptable. Transport failure is expected after stop.
	if _, err := f.d.call(f.ctx, nil, "exec", "--user", "1001:1001", s.ID(), "sh", "-c", liveBrokerCall); err == nil {
		t.Fatal("revoked request succeeded")
	}
	if calls.Load() != 2 {
		t.Fatal("revoked request reached upstream")
	}
	select {
	case <-s.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("revoked broker did not stop attempt")
	}
	if s.Record().Status != "terminated" {
		t.Fatal(s.Record())
	}
	// Retry has a new attempt/generation and token; the old token is unusable.
	oldToken := foreign
	b.Stop("retry")
	other.Attempt = "retry"
	other.Generation++
	retry := f.start("retry", other, "synthetic-retry")
	f.shell(retry, "if wget -q -T 2 -O /dev/null --post-data='{}' --header='Authorization: Bearer "+oldToken+"' http://127.0.0.1:9121/v1/operations/echo; then exit 96; fi")
	if got := f.shell(retry, liveBrokerCall); got != "fixed-operation-ok" {
		t.Fatal("fresh retry token denied")
	}
	if calls.Load() != 3 {
		t.Fatal("retry token count mismatch")
	}
	f.measure(retry)
}

func TestLiveFixedBrokerRelayFailure(t *testing.T) {
	f, p, calls, _ := connectedLive(t)
	s := f.start("relay-failure", p, "synthetic-direct")
	if f.shell(s, liveBrokerCall) != "fixed-operation-ok" {
		t.Fatal("positive control failed")
	}
	at := time.Now()
	// Close the private host-to-broker channel; the real broker observes EOF.
	if err := s.broker.stdin.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-s.Done():
	case <-time.After(4 * time.Second):
		t.Fatal("relay EOF left attempt running")
	}
	if s.Record().Status != "terminated" {
		t.Fatal(s.Record())
	}
	if _, err := f.d.call(f.ctx, nil, "inspect", s.ID()); err == nil {
		t.Fatal("relay failure retained container")
	}
	if calls.Load() != 1 {
		t.Fatal("relay failure admitted extra operation")
	}
	t.Logf("relay_failure_to_removed_ms=%.3f", float64(time.Since(at).Microseconds())/1000)
}

func TestLiveFixedBrokerDirectNetworkDenied(t *testing.T) {
	f, p, _, _ := connectedLive(t)
	s := f.start("network", p, "synthetic-direct")
	// Owned host endpoints are reachable controls before proving they are
	// unreachable from the agent's network=none namespace.
	v4, err := net.Listen("tcp4", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	defer v4.Close()
	v6, err := net.Listen("tcp6", "[::]:0")
	if err != nil {
		t.Fatal("IPv6 positive control unavailable", err)
	}
	defer v6.Close()
	for _, listener := range []net.Listener{v4, v6} {
		host := "127.0.0.1"
		if listener == v6 {
			host = "::1"
		}
		conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, fmt.Sprint(listener.Addr().(*net.TCPAddr).Port)), time.Second)
		if err != nil {
			t.Fatal("host endpoint positive control", err)
		}
		conn.Close()
	}
	port4 := v4.Addr().(*net.TCPAddr).Port
	port6 := v6.Addr().(*net.TCPAddr).Port
	gateway := strings.TrimSpace(string(f.must(nil, "network", "inspect", "bridge", "--format", "{{(index .IPAM.Config 0).Gateway}}")))
	if net.ParseIP(gateway) == nil {
		t.Fatal("bridge gateway unavailable")
	}
	hostControl, err := net.DialTimeout("tcp", net.JoinHostPort(gateway, fmt.Sprint(port4)), time.Second)
	if err != nil {
		t.Fatal("gateway endpoint positive control unavailable", err)
	}
	hostControl.Close()
	// A real synthetic DNS responder is reachable from the host on the gateway,
	// but an agent cannot send it even a query through its private namespace.
	dns, err := net.ListenPacket("udp4", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	defer dns.Close()
	go func() {
		buf := make([]byte, 512)
		for {
			n, addr, e := dns.ReadFrom(buf)
			if e != nil {
				return
			}
			if n < 16 {
				continue
			}
			end := 12
			for end < n && buf[end] != 0 {
				end += 1 + int(buf[end])
			}
			end += 5
			if end > n {
				continue
			}
			packet := append([]byte(nil), buf[:end]...)
			packet[10], packet[11] = 0, 0
			packet[2] = 0x81
			packet[3] = 0x80
			packet[6] = 0
			packet[7] = 1
			kind := packet[end-3]
			value := []byte{8, 8, 8, 8}
			if kind == 28 {
				value = net.ParseIP("2001:4860:4860::8888").To16()
			}
			packet = append(packet, 0xc0, 0x0c, 0, kind, 0, 1, 0, 0, 0, 60, 0, byte(len(value)))
			packet = append(packet, value...)
			_, _ = dns.WriteTo(packet, addr)
		}
	}()
	dnsAddress := net.JoinHostPort(gateway, fmt.Sprint(dns.LocalAddr().(*net.UDPAddr).Port))
	resolver := &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "udp", dnsAddress)
	}}
	dnsCtx, cancel := context.WithTimeout(f.ctx, time.Second)
	defer cancel()
	if ips, err := resolver.LookupHost(dnsCtx, "probe.invalid"); err != nil || len(ips) == 0 {
		t.Fatal("synthetic DNS positive control failed", err)
	}
	cfg, _ := json.Marshal(map[string]string{"Address": dnsAddress})
	if _, err := f.d.call(f.ctx, cfg, "exec", "-i", "--user", "1001:1001", s.ID(), "/opt/crewship-runner", "dns-probe"); err == nil {
		t.Fatal("agent reached DNS on host gateway")
	}
	out := f.shell(s, fmt.Sprintf(`set -eu; deny() { if "$@"; then exit 91; fi; }; test "$(ls /sys/class/net)" = lo; deny nc -z -w 1 127.0.0.1 %d; deny nc -z -w 1 ::1 %d; deny nc -z -w 1 %s %d; deny nc -z -w 1 8.8.8.8 443; deny nc -z -w 1 2606:4700:4700::1111 443; deny nslookup example.com 172.17.0.1; echo network-denied`, port4, port6, gateway, port4))
	if !strings.Contains(out, "network-denied") {
		t.Fatal("direct network boundary failed")
	}
	if f.shell(s, liveBrokerCall) != "fixed-operation-ok" {
		t.Fatal("broker unavailable while direct network denied")
	}
}
