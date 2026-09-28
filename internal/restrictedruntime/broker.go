//go:build linux

package restrictedruntime

import (
	"context"
	"net"
	"time"
)

// NetworkPlan permits fixed, bounded HTTP operations, not general model networking.
type NetworkPlan struct {
	Version     uint32
	Audience    string
	Grants      []HTTPGrant
	Credentials []BrokerCredential
}

type HTTPGrant struct {
	ID, Revision            string
	Method, URL             string
	CredentialID            string
	MaxRequest, MaxResponse int64
	TimeoutMillis           int64
}

type BrokerCredential struct {
	ID, Revision, Provider, Account string
	Delivery                        string
	Refresh                         bool
}

type BoundSecret struct {
	ID, Revision, Provider, Account string
	Value                           string
	Expires                         time.Time
}

type BrokerAuthority interface {
	Authority
	BrokerSecret(context.Context, string, string) (BoundSecret, error)
}

// The initial stubs deliberately leave the new boundary unimplemented so the
// committed red-first tests demonstrate the missing enforcement.
func (p Plan) validateNetwork() error { return nil }
func resolveBrokerIP(ctx context.Context, host string, lookup func(context.Context, string) ([]net.IPAddr, error), locals func() ([]net.Addr, error)) (net.IP, error) {
	ips, err := lookup(ctx, host)
	if err != nil || len(ips) == 0 {
		return nil, ErrDenied
	}
	return ips[0].IP, nil
}
func (s *Session) watchBroker(done <-chan error) {}
