//go:build linux

package restrictedruntime

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type brokerFixtureAuthority struct {
	*fixtureAuthority
	material BoundSecret
}

func (a *brokerFixtureAuthority) BrokerSecret(context.Context, string, string) (BoundSecret, error) {
	return a.material, nil
}

func brokerTestSession(t *testing.T, p Plan) (*Session, *brokerFixtureAuthority) {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "docker")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\ncase \"$1\" in inspect) exit 1;; esac\n"), 0700); err != nil {
		t.Fatal(err)
	}
	a := &brokerFixtureAuthority{fixtureAuthority: &fixtureAuthority{plans: map[string]Plan{"h": p}, denied: map[string]bool{}, ttl: 15 * time.Second}}
	m, err := New(filepath.Join(dir, "state"), Docker{Binary: bin}, a, catalogMap{}, Limits{128 << 20, 500000000, 48})
	if err != nil {
		t.Fatal(err)
	}
	s := &Session{manager: m, plan: p, handle: "h", id: "synthetic", done: make(chan struct{}), record: Record{Attempt: p.Attempt, Status: "running", Expires: p.Expires}}
	t.Cleanup(func() { s.Stop("test_finished"); _ = m.Close() })
	return s, a
}

func syntheticBrokerTransport(t *testing.T, server *httptest.Server) *brokerTransport {
	t.Helper()
	tr := defaultBrokerTransport()
	tr.lookup = func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("8.8.8.8")}}, nil
	}
	tr.locals = func() ([]net.Addr, error) { return nil, nil }
	tr.tls = server.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
	tr.dial = func(ctx context.Context, network, addr string) (net.Conn, error) {
		if addr != "8.8.8.8:443" {
			t.Errorf("unpinned dial: %s", addr)
		}
		return (&net.Dialer{}).DialContext(ctx, "tcp", server.Listener.Addr().String())
	}
	return &tr
}

func TestBrokerUpstreamAuthorizationAndLimits(t *testing.T) {
	for _, name := range []string{"allowed", "wrong token", "unknown operation", "expired lease", "revoked", "wrong account", "oversize request", "oversize response", "redirect", "stream", "revoke before response"} {
		t.Run(name, func(t *testing.T) {
			p := connectedPlan()
			p.Network.Grants[0].URL = "https://example.com/echo"
			p.Network.Grants[0].CredentialID = "credential1"
			p.Network.Credentials = []BrokerCredential{{ID: "credential1", Revision: "r1", Provider: "test", Account: "a1", Delivery: "broker-bearer-v1"}}
			s, a := brokerTestSession(t, p)
			a.material = BoundSecret{ID: "credential1", Revision: "r1", Provider: "test", Account: "a1", Value: "synthetic-secret", Expires: time.Now().Add(time.Minute)}
			var calls atomic.Int32
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Header.Get("Authorization") != "Bearer synthetic-secret" || r.URL.Path != "/echo" || r.Method != "POST" {
					t.Error("wrong fixed request/account")
				}
				switch name {
				case "oversize response":
					_, _ = w.Write([]byte(strings.Repeat("x", 1025)))
				case "redirect":
					w.Header().Set("Location", "https://127.0.0.1/internal")
					w.WriteHeader(302)
				case "stream":
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = w.Write([]byte("data: secret\n\n"))
				case "revoke before response":
					a.revoke("h")
					_, _ = w.Write([]byte("private-response"))
				default:
					_, _ = w.Write([]byte("ok synthetic-secret"))
				}
			}))
			defer server.Close()
			tr := syntheticBrokerTransport(t, server)
			req := brokerFrame{Kind: "request", Token: "token", Operation: "echo", Body: []byte("{}")}
			wantCalls := int32(1)
			switch name {
			case "wrong token":
				req.Token = "foreign"
				wantCalls = 0
			case "unknown operation":
				req.Operation = "admin"
				wantCalls = 0
			case "expired lease":
				s.record.Expires = time.Now().Add(-time.Second)
				wantCalls = 0
			case "revoked":
				a.revoke("h")
				wantCalls = 0
			case "wrong account":
				a.material.Account = "a2"
				wantCalls = 0
			case "oversize request":
				req.Body = make([]byte, 1025)
				wantCalls = 0
			}
			out := s.brokerRequest(context.Background(), "token", req, *tr)
			if calls.Load() != wantCalls {
				t.Fatalf("upstream calls=%d want %d", calls.Load(), wantCalls)
			}
			if name == "allowed" {
				if out.Status != 200 || string(out.Body) != "ok [REDACTED]" {
					t.Fatalf("allowed control: %+v", out)
				}
			} else if out.Status < 400 || len(out.Body) != 0 {
				t.Fatalf("denied operation released data: %+v", out)
			}
		})
	}
}

func TestBrokerDNSRebindAcrossActualTLSRequests(t *testing.T) {
	p := connectedPlan()
	p.Network.Grants[0].URL = "https://example.com/echo"
	s, _ := brokerTestSession(t, p)
	var calls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); _, _ = w.Write([]byte("ok")) }))
	defer server.Close()
	tr := syntheticBrokerTransport(t, server)
	req := brokerFrame{Token: "token", Operation: "echo"}
	if got := s.brokerRequest(context.Background(), "token", req, *tr); got.Status != 200 {
		t.Fatal("positive TLS control failed")
	}
	tr.lookup = func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}}, nil
	}
	if got := s.brokerRequest(context.Background(), "token", req, *tr); got.Status != 403 {
		t.Fatal("rebound target accepted")
	}
	if calls.Load() != 1 {
		t.Fatal("rebound request reached upstream")
	}
}

func TestBrokerTLSRetainsHostnameVerification(t *testing.T) {
	p := connectedPlan()
	p.Network.Grants[0].URL = "https://wrong-host.example/echo"
	s, _ := brokerTestSession(t, p)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("unverified TLS reached upstream") }))
	defer server.Close()
	tr := syntheticBrokerTransport(t, server)
	tr.tls = &tls.Config{RootCAs: tr.tls.RootCAs, MinVersion: tls.VersionTLS12}
	if got := s.brokerRequest(context.Background(), "token", brokerFrame{Token: "token", Operation: "echo"}, *tr); got.Status != 502 {
		t.Fatal("TLS hostname mismatch accepted")
	}
}
