package httpsafe

import (
	"fmt"
	"net"
	"strings"

	"golang.org/x/net/idna"
)

// GitImportPolicy contains only administrator-owned destination data.
// A workspace import request cannot add trusted private hosts.
type GitImportPolicy struct {
	PrivateHosts []string `yaml:"private_hosts"`
}

func CanonicalGitHost(raw string) (string, error) {
	if strings.ContainsAny(raw, "/%@\\?#*[]") {
		return "", fmt.Errorf("git import host must be an exact hostname or unscoped IP")
	}
	host := strings.TrimSuffix(strings.ToLower(raw), ".")
	if ip := net.ParseIP(host); ip != nil {
		return ip.String(), nil
	}
	if strings.Contains(host, ":") {
		return "", fmt.Errorf("git import host must not include a port")
	}
	ascii, err := idna.Lookup.ToASCII(host)
	if err != nil || ascii == "" || len(ascii) > 253 {
		return "", fmt.Errorf("invalid git import hostname")
	}
	for _, label := range strings.Split(ascii, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", fmt.Errorf("invalid git import hostname")
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
				return "", fmt.Errorf("invalid git import hostname")
			}
		}
	}
	// libcurl normalizes legacy integer/octal/hex IPv4 spellings before
	// looking up CURLOPT_RESOLVE. Refuse parser ambiguity before DNS/policy.
	numeric := true
	for _, part := range strings.Split(ascii, ".") {
		digits := part
		hex := strings.HasPrefix(part, "0x")
		if hex {
			digits = part[2:]
		}
		if digits == "" {
			numeric = false
			break
		}
		for _, c := range digits {
			if !(c >= '0' && c <= '9' || hex && c >= 'a' && c <= 'f') {
				numeric = false
				break
			}
		}
	}
	if numeric {
		return "", fmt.Errorf("ambiguous numeric git hostname; use canonical IP spelling")
	}
	return ascii, nil
}

func ValidateGitImportPolicy(policy GitImportPolicy) error {
	for _, host := range policy.PrivateHosts {
		if _, err := CanonicalGitHost(host); err != nil {
			return fmt.Errorf("git private host allowlist: %w", err)
		}
	}
	return nil
}
