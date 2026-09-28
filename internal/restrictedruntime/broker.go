//go:build linux

package restrictedruntime

import (
	"context"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/httpsafe"
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

func (p Plan) validateNetwork() error {
	if p.Profile == "" {
		if p.Network != nil {
			return ErrDenied
		}
		return nil
	}
	n := p.Network
	if p.Profile != "brokered-http-v1" || n == nil || n.Version != 1 || !identifier.MatchString(n.Audience) || len(n.Grants) == 0 || len(n.Grants) > 16 || len(n.Credentials) > 16 {
		return ErrDenied
	}
	for _, m := range p.Mounts {
		if !m.ReadOnly {
			return ErrDenied
		}
	}
	for _, c := range p.Credentials {
		if strings.HasPrefix(c.Env, "CREWSHIP_BROKER_") {
			return ErrDenied
		}
	}
	creds := map[string]bool{}
	for _, c := range n.Credentials {
		if !identifier.MatchString(c.ID) || !identifier.MatchString(c.Revision) || !identifier.MatchString(c.Provider) || !identifier.MatchString(c.Account) || creds[c.ID] || c.Delivery != "broker-bearer-v1" || c.Refresh {
			return ErrDenied
		}
		creds[c.ID] = true
	}
	ids := map[string]bool{}
	for _, g := range n.Grants {
		u, e := httpsafe.ValidateURL(g.URL)
		if e != nil || u.Scheme != "https" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.Contains(u.Host, "%") || len(g.URL) > 2048 {
			return ErrDenied
		}
		if _, e = url.ParseRequestURI(g.URL); e != nil {
			return ErrDenied
		}
		if !identifier.MatchString(g.ID) || !identifier.MatchString(g.Revision) || ids[g.ID] || (g.Method != "GET" && g.Method != "POST") || g.MaxRequest < 0 || g.MaxRequest > 1<<20 || g.MaxResponse < 1 || g.MaxResponse > 1<<20 || g.TimeoutMillis < 1 || g.TimeoutMillis > 10000 {
			return ErrDenied
		}
		if g.CredentialID != "" && !creds[g.CredentialID] {
			return ErrDenied
		}
		ids[g.ID] = true
	}
	return nil
}

func narrowNetwork(parent, child Plan) error {
	if parent.Profile != child.Profile {
		return ErrDenied
	}
	if child.Network == nil {
		if parent.Network != nil {
			return ErrDenied
		}
		return nil
	}
	if parent.Network == nil {
		return ErrDenied
	}
	for _, c := range child.Network.Credentials {
		found := false
		for _, p := range parent.Network.Credentials {
			if p == c {
				found = true
			}
		}
		if !found {
			return ErrDenied
		}
	}
	for _, c := range child.Network.Grants {
		found := false
		for _, p := range parent.Network.Grants {
			if c.ID == p.ID && c.Revision == p.Revision && c.URL == p.URL && c.Method == p.Method && c.CredentialID == p.CredentialID && c.MaxRequest <= p.MaxRequest && c.MaxResponse <= p.MaxResponse && c.TimeoutMillis <= p.TimeoutMillis {
				found = true
			}
		}
		if !found {
			return ErrDenied
		}
	}
	return nil
}

func resolveBrokerIP(ctx context.Context, host string, lookup func(context.Context, string) ([]net.IPAddr, error), locals func() ([]net.Addr, error)) (net.IP, error) {
	ips, err := lookup(ctx, host)
	if err != nil || len(ips) == 0 {
		return nil, ErrDenied
	}
	local, err := locals()
	if err != nil {
		return nil, ErrDenied
	}
	for _, a := range ips {
		if a.Zone != "" || a.IP == nil || !a.IP.IsGlobalUnicast() || httpsafe.IsBlockedIP(a.IP) {
			return nil, ErrDenied
		}
		for _, l := range local {
			ip, _, err := net.ParseCIDR(l.String())
			if err != nil {
				return nil, ErrDenied
			}
			if a.IP.Equal(ip) {
				return nil, ErrDenied
			}
		}
	}
	return ips[0].IP, nil
}

func (s *Session) watchBroker(done <-chan error) {
	select {
	case <-s.Done():
		return
	case <-done:
		s.Stop("broker_unavailable")
	}
}
