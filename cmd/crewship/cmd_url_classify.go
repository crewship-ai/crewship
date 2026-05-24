package main

import (
	"net"
	"strings"
)

// cmd_url_classify.go holds URL / host classification helpers shared
// between multiple commands. Lives in a build-tag-free file so both
// the CLI-only build (cmd_login.go is always built) and the full
// build (cmd_doctor_security.go is //go:build !clionly) can import.
//
// Previously this code lived in TWO places:
//
//   - cmd_doctor_security.go:isLoopbackHost  (full build only)
//   - cmd_login.go:loginIsLoopback           (every build)
//
// The duplication was intentional at the time — each call site had a
// build-tag complication that made extraction non-obvious. Two
// commits later the implementations were already drifting in comment
// style and one was about to grow a new branch the other wasn't.
// One canonical helper here, both call sites use it.

// isLoopbackHost returns true for hostnames the OS routes locally.
// Accepts literal IPs (127.0.0.0/8, ::1, IPv4-mapped loopback) AND
// the conventional "localhost" name — `--server http://localhost:8080`
// is the documented dev workflow and "localhost" almost always
// resolves to a loopback address.
//
// No DNS lookup: a hostile resolver pointing "localhost" at an
// external IP is a bigger problem than this check can catch, and the
// lookup would silently fail on air-gapped CI.
//
// Empty host is NOT loopback. An empty value means the caller
// couldn't parse a hostname from the URL ("http://:8080",
// "http:///path", etc.) — treating it as loopback would let a
// misconfigured server: silently pass the doctor security audit
// (and the login plaintext warning). Callers MUST reject empty host
// before asking this function, OR rely on isLoopbackHost("") = false
// as the safe default.
func isLoopbackHost(host string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	return ip.IsLoopback()
}
