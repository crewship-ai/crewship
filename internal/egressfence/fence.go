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
//	    ct direction reply ct state established,related accept  # replies to inbound connections only
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
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
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

// markerVersion changes whenever the rule shape changes, so a namespace
// fenced by an older binary reads as not-valid and is re-applied.
const markerVersion = "v2"

// marker is the user data Apply stores on rule i and Check expects back. It
// binds each rule to the rule shape version and to this spec's UIDs, so a
// table with the right name and rule count but different content (another
// version, other UIDs, edited rules) does not pass as this fence.
func (s Spec) marker(i int) string {
	uids := make([]string, len(s.AllowUIDs))
	for j, u := range s.AllowUIDs {
		uids[j] = strconv.FormatUint(uint64(u), 10)
	}
	sum := sha256.Sum256([]byte(markerVersion + "|" + strings.Join(uids, ",")))
	return fmt.Sprintf("crewship-fence/%s/%d/%s", markerVersion, i, hex.EncodeToString(sum[:6]))
}

// State is what Check found in the namespace.
type State struct {
	Present bool
	Rules   int
	// Valid means the installed table is exactly the fence for the checked
	// spec (hook, policy, rule count and per-rule markers).
	Valid bool
}

// String renders a State for logs.
func (s State) String() string {
	switch {
	case !s.Present:
		return "absent"
	case !s.Valid:
		return fmt.Sprintf("present but not valid (%d rules)", s.Rules)
	default:
		return fmt.Sprintf("present (%d rules)", s.Rules)
	}
}
