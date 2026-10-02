// Package egressfence installs the network-layer egress fence (#1368) in a
// crew container's network namespace.
//
// The sidecar's HTTP proxy is an application-layer allowlist: a process that
// ignores HTTP_PROXY (`curl --noproxy '*'`, a raw socket, a UDP DNS query to
// 8.8.8.8) reaches the network anyway. Verified live on dev1 (2026-07-23) and
// dev2 (2026-10-01). The fence makes the namespace itself refuse every
// outbound packet that is not owned by an allowed UID — the sidecar's — so
// the proxy becomes the only way out and its allowlist applies to everything.
//
// Shape (option B in #1368; option A, an internal network with the sidecar as
// a separate gateway container, needs the sidecar split and was rejected for
// now):
//
//	table inet crewship_fence
//	  chain output (hook output, priority filter, policy drop)
//	    ip daddr 127.0.0.11 meta skuid <allowed> accept   # Docker DNS: sidecar only
//	    ip daddr 127.0.0.11 reject                        # agent DNS = tunnel primitive
//	    oifname "lo" accept                               # agent ↔ sidecar on 127.0.0.1:9119
//	    ct state established,related accept               # replies to inbound connections
//	    meta skuid <allowed> accept                       # sidecar egress (allowlist on top)
//	    reject with icmpx admin-prohibited                # fail fast instead of timing out
//
// The fence rests on the UID boundary: it means nothing when the agent can
// become the allowed UID or root, so privileged crews are out of scope and
// must be reported as unfenced by the caller.
//
// Installation runs inside the target namespace (the caller joins it, e.g. a
// short-lived helper container started with --network container:<id> and
// CAP_NET_ADMIN), so the crew container itself never holds NET_ADMIN and the
// agent cannot flush the rules that fence it.
package egressfence

import (
	"errors"
	"fmt"
	"net"
)

// TableName is the nftables table the fence owns. Apply replaces only this
// table; it never touches any other table in the namespace (Docker's own NAT
// rules for the embedded resolver live in separate tables).
const TableName = "crewship_fence"

// ChainName is the output chain inside TableName.
const ChainName = "output"

// DockerDNS is Docker's embedded resolver address inside user-defined
// networks. Only allowed UIDs may query it: a resolver that forwards any name
// upstream is a DNS-tunnel exfiltration channel.
var DockerDNS = net.IPv4(127, 0, 0, 11).To4()

// Spec describes one fence.
type Spec struct {
	// AllowUIDs are the socket owner UIDs whose packets may leave the
	// namespace (the sidecar's). Must not be empty and must not contain 0:
	// allowing root would let any root exec in the container bypass the
	// fence, and root is exactly what a fenced crew must not have.
	AllowUIDs []uint32
}

// ErrNotSupported is returned on platforms without nftables.
var ErrNotSupported = errors.New("egressfence: nftables is only available on linux")

// Validate rejects specs that would install a fence with no meaning.
func (s Spec) Validate() error {
	if len(s.AllowUIDs) == 0 {
		return errors.New("egressfence: at least one allowed uid is required")
	}
	for _, u := range s.AllowUIDs {
		if u == 0 {
			return errors.New("egressfence: uid 0 cannot be allowed; a root-allowed fence is no fence")
		}
	}
	return nil
}

// State is what Check found in the namespace.
type State struct {
	Present bool
	Rules   int
}

// String renders a State for logs.
func (s State) String() string {
	if !s.Present {
		return "absent"
	}
	return fmt.Sprintf("present (%d rules)", s.Rules)
}
