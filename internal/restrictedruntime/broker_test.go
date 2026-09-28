//go:build linux

package restrictedruntime

import (
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func connectedPlan() Plan {
	p := testPlan()
	p.Mounts = nil
	p.Credentials = nil
	p.Profile = "brokered-http-v1"
	p.Network = &NetworkPlan{Version: 1, Audience: "audience1", Grants: []HTTPGrant{{ID: "echo", Revision: "r1", Method: "POST", URL: "https://upstream.example/echo", MaxRequest: 1024, MaxResponse: 1024, TimeoutMillis: 1000}}}
	return p
}

func TestBrokerRejectsUnsupportedAuthority(t *testing.T) {
	for name, mutate := range map[string]func(*Plan){
		"unknown profile":                func(p *Plan) { p.Profile = "free" },
		"missing network":                func(p *Plan) { p.Network = nil },
		"unknown version":                func(p *Plan) { p.Network.Version = 2 },
		"writable storage without quota": func(p *Plan) { p.Mounts = []Mount{{Resource: "data", Target: "/data/private"}} },
		"unbounded response":             func(p *Plan) { p.Network.Grants[0].MaxResponse = 0 },
		"local destination":              func(p *Plan) { p.Network.Grants[0].URL = "https://127.0.0.1/echo" },
		"query override":                 func(p *Plan) { p.Network.Grants[0].URL += "?url=internal" },
	} {
		t.Run(name, func(t *testing.T) {
			p := connectedPlan()
			mutate(&p)
			if p.validate(time.Now()) == nil {
				t.Fatal("unsupported connected authority accepted")
			}
		})
	}
	if err := connectedPlan().validate(time.Now()); err != nil {
		t.Fatalf("valid control: %v", err)
	}
}

func TestBrokerDNSRebindDeniesPreviouslyRoutableTarget(t *testing.T) {
	current := net.ParseIP("8.8.8.8")
	lookup := func(context.Context, string) ([]net.IPAddr, error) { return []net.IPAddr{{IP: current}}, nil }
	locals := func() ([]net.Addr, error) { return nil, nil }
	got, err := resolveBrokerIP(context.Background(), "upstream.example", lookup, locals)
	if err != nil || !got.Equal(current) {
		t.Fatal("routable positive control rejected")
	}
	for _, ip := range []string{"127.0.0.1", "::1", "::ffff:127.0.0.1", "169.254.169.254", "172.17.0.1"} {
		current = net.ParseIP(ip)
		if _, err := resolveBrokerIP(context.Background(), "upstream.example", lookup, locals); err == nil {
			t.Errorf("DNS rebinding allowed local target %s", ip)
		}
	}
}

func TestBrokerRelayEOFStopsAndRemovesAttempt(t *testing.T) {
	dir := t.TempDir()
	trace := filepath.Join(dir, "calls")
	bin := filepath.Join(dir, "docker")
	script := "#!/bin/sh\necho \"$1\" >> '" + trace + "'\ncase \"$1\" in\ninspect) exit 1;;\nesac\n"
	if err := os.WriteFile(bin, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	p := connectedPlan()
	m, err := New(filepath.Join(dir, "state"), Docker{Binary: bin}, &fixtureAuthority{}, catalogMap{}, Limits{128 << 20, 500000000, 48})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	s := &Session{manager: m, plan: p, id: "owned-container", done: make(chan struct{}), record: Record{Attempt: p.Attempt, Status: "running"}}
	done := make(chan error, 1)
	go s.watchBroker(done)
	done <- io.EOF
	select {
	case <-s.Done():
	case <-time.After(300 * time.Millisecond):
		t.Fatal("relay EOF left attempt running")
	}
	if s.Record().Status != "terminated" {
		t.Fatal("cleanup not confirmed")
	}
	b, err := os.ReadFile(trace)
	if err != nil || !strings.Contains(string(b), "kill\n") || !strings.Contains(string(b), "rm\n") {
		t.Fatal("relay failure did not kill and remove runtime")
	}
}

func TestBrokerDNSRejectsMixedAnswersAndHostInterface(t *testing.T) {
	public := net.ParseIP("8.8.8.8")
	locals := func() ([]net.Addr, error) { return []net.Addr{&net.IPNet{IP: public, Mask: net.CIDRMask(32, 32)}}, nil }
	lookup := func(context.Context, string) ([]net.IPAddr, error) { return []net.IPAddr{{IP: public}}, nil }
	if _, err := resolveBrokerIP(context.Background(), "example.com", lookup, locals); err == nil {
		t.Fatal("host public interface allowed")
	}
	locals = func() ([]net.Addr, error) { return nil, nil }
	lookup = func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: public}, {IP: net.ParseIP("::1")}}, nil
	}
	if _, err := resolveBrokerIP(context.Background(), "example.com", lookup, locals); err == nil {
		t.Fatal("mixed DNS answer allowed")
	}
}

func TestBrokerDelegationCannotWidenOperations(t *testing.T) {
	parent := connectedPlan()
	for name, mutate := range map[string]func(*Plan){
		"destination": func(p *Plan) { p.Network.Grants[0].URL = "https://other.example/echo" },
		"method":      func(p *Plan) { p.Network.Grants[0].Method = "GET" },
		"size":        func(p *Plan) { p.Network.Grants[0].MaxResponse++ },
		"timeout":     func(p *Plan) { p.Network.Grants[0].TimeoutMillis++ },
		"credential":  func(p *Plan) { p.Network.Credentials = []BrokerCredential{{ID: "foreign"}} },
	} {
		t.Run(name, func(t *testing.T) {
			child := connectedPlan()
			child.Expires = parent.Expires
			mutate(&child)
			if Narrow(parent, child) == nil {
				t.Fatal("delegated network authority widened")
			}
		})
	}
	child := connectedPlan()
	child.Expires = parent.Expires
	child.Network.Audience = "child-audience"
	child.Network.Grants[0].MaxResponse = 10
	if err := Narrow(parent, child); err != nil {
		t.Fatal("narrowed child denied", err)
	}
}

func TestBrokerFramesRejectOversizeAndUnknownFields(t *testing.T) {
	for _, packet := range [][]byte{{0, 32, 0, 1}, {0, 0, 0, 0}, append([]byte{0, 0, 0, 14}, []byte(`{"Unknown":1} `)...)} {
		var f brokerFrame
		if err := readBrokerFrame(strings.NewReader(string(packet)), &f); err == nil {
			t.Fatal("malformed frame accepted")
		}
	}
}
