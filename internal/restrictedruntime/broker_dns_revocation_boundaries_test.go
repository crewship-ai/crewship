//go:build linux

package restrictedruntime

import (
	"context"
	"net"
	"testing"
	"time"
)

func TestBrokerRechecksRevocationAndSecretExpiryAfterResolution(t *testing.T) {
	for _, mode := range []string{"revoked", "secret expired"} {
		t.Run(mode, func(t *testing.T) {
			p := connectedPlan()
			p.Network.Grants[0].CredentialID = "credential1"
			p.Network.Credentials = []BrokerCredential{{ID: "credential1", Revision: "r1", Provider: "test", Account: "a1", Delivery: "broker-bearer-v1"}}
			s, a := brokerTestSession(t, p)
			a.material = BoundSecret{ID: "credential1", Revision: "r1", Provider: "test", Account: "a1", Value: "synthetic-secret", Expires: time.Now().Add(time.Minute)}
			if mode == "secret expired" {
				a.material.Expires = time.Now().Add(50 * time.Millisecond)
			}
			lookups := 0
			tr := brokerTransport{
				lookup: func(context.Context, string) ([]net.IPAddr, error) {
					lookups++
					if mode == "revoked" {
						a.revoke("h")
					} else {
						time.Sleep(time.Until(a.material.Expires) + time.Millisecond)
					}
					return []net.IPAddr{{IP: net.ParseIP("8.8.8.8")}}, nil
				},
				locals: func() ([]net.Addr, error) { return nil, nil },
				dial: func(context.Context, string, string) (net.Conn, error) {
					t.Error("revoked authority reached network")
					return nil, ErrDenied
				},
			}
			got := s.brokerRequest(t.Context(), "token", brokerFrame{Token: "token", Operation: "echo", Body: []byte("{}")}, tr)
			if lookups != 1 || got.Status != 403 || len(got.Body) != 0 {
				t.Fatalf("post-resolution authority not enforced: lookups=%d result=%+v", lookups, got)
			}
		})
	}
}
