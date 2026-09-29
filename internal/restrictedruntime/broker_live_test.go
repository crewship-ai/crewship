//go:build linux && restrictedruntime_live

package restrictedruntime

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

func TestLiveStreamingBroker(t *testing.T) {
	f := live(t)
	p := streamingPlan()
	p.Credentials = []Credential{{ID: "direct", Env: "DIRECT_TOKEN", File: "direct"}}
	p.Network.Grants[0].URL = "https://example.com/stream"
	p.Network.Grants[0].TimeoutMillis = 10000
	p.Network.Grants[0].CredentialID = "broker-key"
	p.Network.Credentials = []BrokerCredential{{ID: "broker-key", Revision: "r1", Provider: "test", Account: "account-a", Delivery: "broker-bearer-v1"}}
	p.Command = []string{"sh", "-c", `printf '%s' "$CREWSHIP_BROKER_TOKEN" > /home/agent/broker-token; exec sleep 3600`}
	f.m.Authority = &brokerFixtureAuthority{fixtureAuthority: f.a, material: BoundSecret{ID: "broker-key", Revision: "r1", Provider: "test", Account: "account-a", Value: "synthetic-stream-secret", Expires: time.Now().Add(time.Minute)}}
	finish := make(chan struct{}, 1)
	revoking := &atomic.Bool{}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer synthetic-stream-secret" || r.URL.Path != "/stream" {
			t.Error("stream reached wrong credential/operation")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: first\n\n")
		w.(http.Flusher).Flush()
		if revoking.Load() {
			select {
			case <-finish:
			case <-r.Context().Done():
				return
			}
			_, _ = io.WriteString(w, "data: REVOKED_STREAM_CANARY\n\n")
			return
		}
		// Keep the upstream open until the test sees a real partial response
		// inside UID 1001. Buffered forwarding cannot pass this positive control.
		select {
		case <-finish:
		case <-r.Context().Done():
			return
		}
		_, _ = io.WriteString(w, "data: synthetic-stream-secret\n\n")
	}))
	t.Cleanup(server.Close)
	f.m.brokerTransport = syntheticBrokerTransport(t, server)
	s := f.start("stream", p, "synthetic-direct")
	call := func(revoke bool) {
		ctx, cancel := context.WithTimeout(f.ctx, 8*time.Second)
		defer cancel()
		cmd := f.d.command(ctx, "exec", "--user", "1001:1001", s.ID(), "sh", "-c", strings.Replace(liveBrokerCall, "-T 3", "-T 8", 1))
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		reader := bufio.NewReader(stdout)
		line, readErr := reader.ReadString('\n')
		if readErr != nil || line != "data: first\n" {
			cancel()
			_ = cmd.Wait()
			t.Fatalf("positive streaming control failed: line=%q err=%v", line, readErr)
		}
		if revoke {
			f.a.revoke("stream")
		}
		finish <- struct{}{}
		rest, readErr := io.ReadAll(reader)
		waitErr := cmd.Wait()
		if strings.Contains(string(rest), "synthetic-stream-secret") || strings.Contains(string(rest), "REVOKED_STREAM_CANARY") {
			t.Fatal("stream released a secret or post-revocation canary")
		}
		if !revoke && (readErr != nil || waitErr != nil || !strings.Contains(string(rest), "[REDACTED]")) {
			t.Fatalf("positive streaming completion failed: read=%v wait=%v body=%q", readErr, waitErr, rest)
		}
	}
	call(false)
	revoking.Store(true)
	call(true)
	select {
	case <-s.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("stream revocation did not terminate the container")
	}
	t.Log("UID-1001 received first SSE event before upstream completion; broker secret redacted; revocation suppressed next event and terminated attempt")
}

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
	neighborReady := false
	for until := time.Now().Add(3 * time.Second); time.Now().Before(until); {
		out, err := f.d.call(f.ctx, nil, "exec", "--user", "1001:1001", b.ID(), "wget", "-q", "-T", "1", "-O", "-", "http://127.0.0.1:9120/count")
		if err == nil && string(out) == "0" {
			neighborReady = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !neighborReady {
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
	f.a.mu.Lock()
	f.a.ttl = -time.Second
	f.a.mu.Unlock()
	if _, err := f.d.call(f.ctx, nil, "exec", "--user", "1001:1001", retry.ID(), "sh", "-c", liveBrokerCall); err == nil {
		t.Fatal("expired authority token succeeded")
	}
	if calls.Load() != 3 {
		t.Fatal("expired authority reached upstream")
	}
	select {
	case <-retry.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("expired authority did not stop attempt")
	}
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
	dns, err := net.ListenPacket("udp4", net.JoinHostPort(gateway, "0"))
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
			var question dnsmessage.Message
			if question.Unpack(buf[:n]) != nil || len(question.Questions) != 1 {
				continue
			}
			q := question.Questions[0]
			var body dnsmessage.ResourceBody = &dnsmessage.AResource{A: [4]byte{8, 8, 8, 8}}
			if q.Type == dnsmessage.TypeAAAA {
				body = &dnsmessage.AAAAResource{AAAA: [16]byte{0x20, 0x01, 0x48, 0x60, 0x48, 0x60, 0, 0, 0, 0, 0, 0, 0, 0, 0x88, 0x88}}
			}
			response := dnsmessage.Message{Header: dnsmessage.Header{ID: question.ID, Response: true, RecursionDesired: true, RecursionAvailable: true}, Questions: question.Questions, Answers: []dnsmessage.Resource{{Header: dnsmessage.ResourceHeader{Name: q.Name, Type: q.Type, Class: q.Class, TTL: 60}, Body: body}}}
			packet, err := response.Pack()
			if err != nil {
				continue
			}
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
