// Package credhosts normalises and checks the hosts a human has approved for a
// credential (credentials.allowed_hosts).
//
// What a host list means here is deliberately narrow. The runtime can check
// that every approved host is reachable under the crew's effective egress
// allowlist, and withhold the credential from delivery when it is not. It does
// NOT confine where a delivered token can be sent: an agent that holds the
// token file can still send it to any host the crew allowlist permits.
package credhosts

import (
	"fmt"
	"net"
	"sort"
	"strings"

	"golang.org/x/net/idna"

	"github.com/crewship-ai/crewship/internal/egressallow"
)

// MaxHosts bounds one credential's approved host list.
const MaxHosts = 32

// Normalize returns the canonical form of one host entry: trimmed, lower-case,
// punycode, one trailing dot removed. Entries are exact hostnames or
// "*.example.com" subdomain wildcards with at least two labels after the star.
// Ports, schemes, paths, userinfo, IP literals and bare "*" are rejected.
func Normalize(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", fmt.Errorf("host is empty")
	}
	// Fold the ideographic and fullwidth dots the way URL parsers do.
	s = strings.NewReplacer("。", ".", "．", ".", "｡", ".").Replace(s)
	s = strings.ToLower(s)
	if strings.HasSuffix(s, ".") {
		s = s[:len(s)-1]
	}
	wild := false
	if strings.HasPrefix(s, "*.") {
		wild = true
		s = s[2:]
	}
	if s == "" || strings.ContainsAny(s, "*:/\\@?#[]% \t\r\n\x00") {
		return "", fmt.Errorf("host %q is not a plain hostname or *.hostname wildcard", raw)
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return "", fmt.Errorf("host %q contains a control character", raw)
		}
	}
	if net.ParseIP(s) != nil {
		return "", fmt.Errorf("host %q is an IP address; use a hostname", raw)
	}
	ascii, err := idna.Lookup.ToASCII(s)
	if err != nil || ascii == "" || len(ascii) > 253 {
		return "", fmt.Errorf("host %q is not a valid hostname", raw)
	}
	labels := strings.Split(ascii, ".")
	minLabels := 2
	if len(labels) < minLabels {
		return "", fmt.Errorf("host %q needs at least two labels", raw)
	}
	for _, l := range labels {
		if l == "" || len(l) > 63 || l[0] == '-' || l[len(l)-1] == '-' {
			return "", fmt.Errorf("host %q is not a valid hostname", raw)
		}
		for _, c := range l {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
				return "", fmt.Errorf("host %q is not a valid hostname", raw)
			}
		}
	}
	// An all-digit last label is how legacy integer/octal IPv4 spellings hide.
	if last := labels[len(labels)-1]; strings.Trim(last, "0123456789") == "" {
		return "", fmt.Errorf("host %q looks like an IP address", raw)
	}
	if wild {
		return "*." + ascii, nil
	}
	return ascii, nil
}

// NormalizeList normalises, de-duplicates and sorts a host list. One bad
// entry rejects the whole list: a partially applied list would silently
// narrow what the human approved.
func NormalizeList(raw []string) ([]string, error) {
	if len(raw) > MaxHosts {
		return nil, fmt.Errorf("at most %d hosts are allowed", MaxHosts)
	}
	seen := make(map[string]struct{}, len(raw))
	out := make([]string, 0, len(raw))
	for _, r := range raw {
		h, err := Normalize(r)
		if err != nil {
			return nil, err
		}
		if _, dup := seen[h]; dup {
			continue
		}
		seen[h] = struct{}{}
		out = append(out, h)
	}
	sort.Strings(out)
	return out, nil
}

// Covered reports whether the allowlist permits every request to host, where
// host is a normalised entry. A wildcard entry is covered only by an allowlist
// wildcard at or above it; an exact allowlist entry never covers a wildcard.
func Covered(al *egressallow.DomainAllowlist, host string) bool {
	// "*" cannot be a real label, so the probe "*.s" matches an allowlist
	// entry only through a wildcard whose suffix contains s.
	return al != nil && al.IsAllowed(host)
}
