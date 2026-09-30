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
	ResponseMode            string           `json:",omitempty"` // empty: bounded buffered response; "sse": explicit v2 streaming grant
	Responses               *ResponsesPolicy `json:",omitempty"` // explicit stateless text-only OpenAI operation
	Native                  *NativePolicy    `json:",omitempty"` // separate stateful scratch-only Codex protocol
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
	if p.NativeSandbox != "" && p.Profile != "brokered-http-v2" {
		return ErrDenied
	}
	if p.Profile == "" {
		if p.Network != nil {
			return ErrDenied
		}
		return nil
	}
	n := p.Network
	if p.NativeSandbox != "" && (p.NativeSandbox != NativeSandboxFingerprint() || len(p.Mounts) != 0 || len(p.Credentials) != 0) {
		return ErrDenied
	}
	versionOK := n != nil && ((p.Profile == "brokered-http-v1" && n.Version == 1) || (p.Profile == "brokered-http-v2" && n.Version == 2))
	if !versionOK || !identifier.MatchString(n.Audience) || len(n.Grants) == 0 || len(n.Grants) > 16 || len(n.Credentials) > 16 {
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
	responses := false
	for _, g := range n.Grants {
		if g.Native != nil {
			if responses || p.Profile != "brokered-http-v2" || p.NativeSandbox == "" || g.Responses != nil || !g.validNative(n.Credentials) || len(n.Grants) != 1 || len(n.Credentials) != 1 {
				return ErrDenied
			}
			responses = true
		}
		if g.Responses != nil {
			if responses || p.Profile != "brokered-http-v2" || !g.validResponses(n.Credentials) {
				return ErrDenied
			}
			responses = true
		}
		maxTimeout := int64(10000)
		if g.ResponseMode == "sse" && p.Profile == "brokered-http-v2" {
			maxTimeout = 300000
		} else if g.ResponseMode != "" {
			return ErrDenied
		}
		u, e := httpsafe.ValidateURL(g.URL)
		if e != nil || u.Scheme != "https" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.Contains(u.Host, "%") || len(g.URL) > 2048 {
			return ErrDenied
		}
		if _, e = url.ParseRequestURI(g.URL); e != nil {
			return ErrDenied
		}
		if !identifier.MatchString(g.ID) || !identifier.MatchString(g.Revision) || ids[g.ID] || (g.Method != "GET" && g.Method != "POST") || g.MaxRequest < 0 || g.MaxRequest > 1<<20 || g.MaxResponse < 1 || g.MaxResponse > 1<<20 || g.TimeoutMillis < 1 || g.TimeoutMillis > maxTimeout {
			return ErrDenied
		}
		if g.CredentialID != "" && !creds[g.CredentialID] {
			return ErrDenied
		}
		ids[g.ID] = true
	}
	if p.NativeSandbox != "" && !responses {
		return ErrDenied
	}
	return nil
}

func narrowNetwork(parent, child Plan) error {
	if parent.Profile != child.Profile || parent.NativeSandbox != child.NativeSandbox {
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
			if c.ID == p.ID && c.Revision == p.Revision && c.URL == p.URL && c.Method == p.Method && c.CredentialID == p.CredentialID && c.ResponseMode == p.ResponseMode && c.MaxRequest <= p.MaxRequest && c.MaxResponse <= p.MaxResponse && c.TimeoutMillis <= p.TimeoutMillis && narrowResponses(p.Responses, c.Responses) && narrowNative(p.Native, c.Native) {
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
			_, subnet, err := net.ParseCIDR(l.String())
			if err != nil {
				return nil, ErrDenied
			}
			// Deny every address on a directly connected interface subnet,
			// including public neighbors/gateways and IPv4-mapped addresses.
			if subnet.Contains(a.IP) {
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
