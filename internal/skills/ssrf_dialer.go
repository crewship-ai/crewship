package skills

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"syscall"
	"time"
)

// ErrSSRFBlocked is returned by the dialer when a TCP connection target is
// in a forbidden CIDR. Callers map it to a fixed user-facing message rather
// than echoing the resolved IP, so the SSRF error path doesn't double as a
// network mapper (see ClassifyFetchError, sanitiseSkillImportError).
var ErrSSRFBlocked = errors.New("ssrf: target IP is in a private, loopback, or otherwise blocked range")

// blockedCIDRs is the set of address ranges the skill importer's HTTP client
// will refuse to dial. The set covers IPv4 loopback, RFC1918, link-local
// (incl. AWS/GCP metadata 169.254.169.254), CGNAT, IETF reserved blocks,
// and the IPv6 equivalents (incl. IPv4-mapped IPv6 of all of the above).
//
// We layer this in addition to ValidateImportURL because that function
// works on the *parsed hostname* — if the hostname is a public domain whose
// A record points at 127.0.0.1 (e.g. localtest.me, lvh.me, 127.0.0.1.nip.io)
// or an internal split-horizon name, ValidateImportURL waves it through.
// The dial-time check sees the actual IP `net.ResolveIP` returned and
// closes that gap. It also defeats DNS-rebinding attacks: even if a
// validate-time DNS lookup returned a public IP, the dialer re-resolves
// (Go's default Dialer does), and the TOCTOU window slams shut here.
var blockedCIDRs = mustCIDRs(
	// IPv4
	"0.0.0.0/8",          // "this network" (some kernels alias to loopback)
	"10.0.0.0/8",         // RFC1918
	"100.64.0.0/10",      // CGNAT (RFC6598)
	"127.0.0.0/8",        // loopback
	"169.254.0.0/16",     // link-local incl. cloud metadata
	"172.16.0.0/12",      // RFC1918
	"192.0.0.0/24",       // IETF protocol assignments
	"192.0.2.0/24",       // documentation
	"192.168.0.0/16",     // RFC1918
	"198.18.0.0/15",      // benchmarking
	"198.51.100.0/24",    // documentation
	"203.0.113.0/24",     // documentation
	"224.0.0.0/4",        // multicast
	"240.0.0.0/4",        // future use / broadcast
	"255.255.255.255/32", // broadcast
	// IPv6
	"::/128",       // unspecified
	"::1/128",      // loopback
	"64:ff9b::/96", // NAT64
	"100::/64",     // discard
	"fc00::/7",     // unique local
	"fe80::/10",    // link-local
	"ff00::/8",     // multicast
	// NOTE: IPv4-mapped IPv6 (`::ffff:x.x.x.x`) is handled by the
	// `ip.To4()` normalisation in isBlockedIP rather than its own CIDR.
	// A literal `::ffff:0:0/96` CIDR would accidentally swallow every
	// IPv4 address because net.IPNet.Contains comparing a v4 IP against
	// a v4-mapped v6 mask reduces to a /0 — catching public IPs too.
)

func mustCIDRs(specs ...string) []*net.IPNet {
	out := make([]*net.IPNet, 0, len(specs))
	for _, s := range specs {
		_, n, err := net.ParseCIDR(s)
		if err != nil {
			panic("skills: invalid hardcoded CIDR " + s + ": " + err.Error())
		}
		out = append(out, n)
	}
	return out
}

// isBlockedIP reports whether the given IP literal is one we refuse to dial.
// Returns false for nil / unparseable input so callers don't have to special
// case the nothing-to-check path; the wrapper Resolver / Dialer always
// supplies a resolved IP.
func isBlockedIP(ip net.IP) bool {
	if ip == nil {
		return false
	}
	// Normalise IPv4-in-IPv6 to its v4 form so /32 v4 entries match.
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}
	for _, c := range blockedCIDRs {
		if c.Contains(ip) {
			return true
		}
	}
	return false
}

// safeDialContext is the connect-time guard. Go's net.Dialer.DialContext
// resolves the host (via the system resolver, honouring /etc/hosts and DNS),
// connects, and exposes the chosen address through the Control hook for
// the syscall about to be made. We inspect that address — if it's in a
// blocked range, we refuse the connect.
//
// This catches three classes of bypass that a string-level URL validator
// can't:
//   - public-DNS-aliases-to-loopback (localtest.me / 127.0.0.1.nip.io)
//   - DNS rebinding (the validate-time lookup returns a public IP, the
//     dial-time lookup returns a private one — Go's Dialer does the second
//     lookup and we see the real address here)
//   - split-horizon DNS where an external-looking hostname resolves to an
//     internal IP from inside the deployment
func safeDialContext() func(ctx context.Context, network, address string) (net.Conn, error) {
	d := &net.Dialer{
		Timeout: 30 * time.Second,
		Control: func(network, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				host = address
			}
			ip := net.ParseIP(host)
			if ip == nil {
				// The resolver phase of DialContext should have left us a
				// literal IP here. If it didn't (custom dialer wiring that
				// passes a hostname through), refuse rather than silently
				// dial — fail closed.
				return fmt.Errorf("ssrf: refused to dial non-IP address %q (resolver bypass?)", address)
			}
			if isBlockedIP(ip) {
				return ErrSSRFBlocked
			}
			return nil
		},
	}
	return d.DialContext
}

// ClassifyFetchError maps an opaque error from the HTTP client into a
// short, non-leaking message safe to return to the caller. The full error
// is still useful for ops — call sites should log it under slog before
// using the classified form in the API response.
//
// Categories:
//   - "blocked"      — SSRF guard fired (private/loopback IP)
//   - "dns_failed"   — hostname did not resolve
//   - "tls_failed"   — TLS handshake or cert validation failed
//   - "timeout"      — connection or read timed out
//   - "unreachable"  — connection refused / network unreachable
//   - "http_error:N" — upstream returned non-2xx (caller may format)
//   - "fetch_failed" — anything else
//
// Anything more specific would re-introduce the leak F-002 closed.
func ClassifyFetchError(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, ErrSSRFBlocked) {
		return "blocked"
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "no such host"), strings.Contains(msg, "lookup"):
		return "dns_failed"
	case strings.Contains(msg, "tls:"), strings.Contains(msg, "x509:"):
		return "tls_failed"
	case strings.Contains(msg, "deadline exceeded"), strings.Contains(msg, "timeout"), strings.Contains(msg, "Timeout"):
		return "timeout"
	case strings.Contains(msg, "connection refused"), strings.Contains(msg, "network is unreachable"), strings.Contains(msg, "no route to host"):
		return "unreachable"
	default:
		return "fetch_failed"
	}
}
