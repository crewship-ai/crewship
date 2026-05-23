//go:build !clionly

package main

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"runtime"
	"strings"

	"github.com/crewship-ai/crewship/internal/cli"
)

// ─── crewship doctor security checks ──────────────────────────────────
//
// Two CLI-side security audits that complement the existing doctor
// surface (container runtime, data-dir perms, etc.). Both are pure
// local-config checks — no network, no DB — so they run cheaply on
// every doctor invocation.
//
// 1. checkCLIConfigServerScheme — warns when the configured server URL
//    is plaintext HTTP against a non-loopback host. The bearer token
//    in cli-config.yaml rides every API request; sending it over
//    cleartext to a LAN-reachable server is the kind of mistake that
//    only surfaces in a postmortem.
//
// 2. checkCLIConfigPerms — verifies cli-config.yaml is 0600
//    (owner-only). The existing data-dir perm check covers the parent
//    directory; this check pins the file containing the bearer token
//    specifically. A 0644 cli-config.yaml on a multi-tenant host
//    leaks the token to any local user.

// checkCLIConfigServerScheme reads the persisted CLI config and
// classifies the configured server URL by scheme:
//
//   - HTTPS                                → PASS
//   - HTTP against localhost/127/[::1]     → PASS (loopback is fine)
//   - HTTP against any other host          → WARN with hint
//   - empty / unset                        → INFO (no server configured)
//   - malformed URL                        → FAIL
//
// The decision to treat HTTPS-against-anything as PASS — including
// self-signed and expired certs — matches the rest of the codebase:
// the Go http.Client uses the system trust store, and a cert problem
// surfaces as a connection error at the first real request. Doctor's
// job here is the categorical "are you yelling the token over
// cleartext" check; cert validation is downstream.
func checkCLIConfigServerScheme(cfg *cli.CLIConfig) checkResult {
	if cfg == nil || strings.TrimSpace(cfg.Server) == "" {
		return checkResult{
			name:   "cli server scheme",
			status: "INFO",
			detail: "no server configured (run 'crewship login --server …')",
		}
	}
	raw := strings.TrimSpace(cfg.Server)
	u, err := url.Parse(raw)
	if err != nil {
		return checkResult{
			name:   "cli server scheme",
			status: "FAIL",
			detail: fmt.Sprintf("malformed server URL %q: %v", raw, err),
			hint:   "fix the 'server:' field in ~/.crewship/cli-config.yaml",
		}
	}
	// An empty u.Hostname() can come from "http://:8080" or "http:///path"
	// — neither targets a real host, and silently passing them as
	// loopback would weaken this audit. Fail loudly so the misconfig
	// surfaces at the doctor invocation, not as a confusing TCP error
	// the first time the CLI actually issues a request.
	if strings.TrimSpace(u.Hostname()) == "" {
		return checkResult{
			name:   "cli server scheme",
			status: "FAIL",
			detail: fmt.Sprintf("server URL %q is missing a host", raw),
			hint:   "set 'server:' to a full URL like https://host[:port]",
		}
	}
	scheme := strings.ToLower(u.Scheme)
	switch scheme {
	case "https":
		return checkResult{
			name:   "cli server scheme",
			status: "PASS",
			detail: u.Host + " (https)",
		}
	case "http":
		if isLoopbackHost(u.Hostname()) {
			return checkResult{
				name:   "cli server scheme",
				status: "PASS",
				detail: u.Host + " (http on loopback — fine)",
			}
		}
		return checkResult{
			name:   "cli server scheme",
			status: "WARN",
			detail: fmt.Sprintf("%s is reached over plaintext HTTP", u.Host),
			hint: "every API call sends your CLI token in the clear. " +
				"Move the server behind TLS (Caddy / nginx + Let's Encrypt) and update the 'server:' field, " +
				"or use SSH port-forwarding so the connection stays loopback.",
		}
	default:
		return checkResult{
			name:   "cli server scheme",
			status: "FAIL",
			detail: fmt.Sprintf("unsupported scheme %q (want http or https)", u.Scheme),
		}
	}
}

// isLoopbackHost returns true for hostnames the OS routes locally.
// We accept literal IPs (127.0.0.1 / ::1) AND the conventional
// "localhost" name, because `crewship login --server http://localhost:8080`
// is the documented dev workflow and "localhost" almost always resolves
// to a loopback address. We do NOT do a DNS lookup — a hostile resolver
// pointing "localhost" at an external IP is a bigger problem than this
// check can catch, and the lookup would silently fail on air-gapped CI.
//
// Empty host is NOT loopback. An empty value means the caller couldn't
// parse a hostname from the URL ("http://:8080", "http:///path", etc.)
// — treating it as loopback would let a misconfigured server: pass the
// security audit silently. Callers should reject empty host BEFORE
// asking this function.
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

// checkCLIConfigPerms verifies cli-config.yaml exists at the default
// path and has mode 0600 (or stricter). Returns:
//
//   - INFO when the file doesn't exist (no token saved → no risk)
//   - PASS when mode is 0600 or stricter (0400)
//   - WARN when group/other bits are set, with the actual mode in the
//     detail so the operator knows what to chmod
//   - FAIL when the path can't be resolved at all (HOMEDIR broken)
//
// We intentionally only warn (not fail) on permissive mode: the user
// is the only one who can fix it, the file contents are still valid,
// and a WARN on every doctor invocation is the kind of nag that
// motivates the chmod without blocking unrelated workflows.
//
// Windows is intentionally not covered — file modes on Windows do not
// map to the unix r/w/x bits the way doctor describes them. The
// existing data-dir-perm check has the same skip; this one mirrors it
// for consistency.
func checkCLIConfigPerms() checkResult {
	// Per-doc-comment: file-mode bits don't map to unix r/w/x on
	// Windows, so the check would produce misleading WARN/FAIL output
	// there. Skip with INFO so the rest of doctor stays useful — same
	// pattern the existing data-dir-perm check uses.
	if runtime.GOOS == "windows" {
		return checkResult{
			name:   "cli config perms",
			status: "INFO",
			detail: "skipped (POSIX perm bits don't apply on Windows)",
		}
	}

	path, err := cli.DefaultConfigPath()
	if err != nil {
		return checkResult{
			name:   "cli config perms",
			status: "FAIL",
			detail: fmt.Sprintf("resolve cli config path: %v", err),
		}
	}
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return checkResult{
				name:   "cli config perms",
				status: "INFO",
				detail: "no cli-config.yaml yet — run 'crewship login' to create one",
			}
		}
		return checkResult{
			name:   "cli config perms",
			status: "FAIL",
			detail: fmt.Sprintf("stat %s: %v", path, err),
		}
	}
	mode := info.Mode().Perm()
	// 0o077 covers all group + other bits. A clean 0600 file has
	// mode & 0o077 == 0; anything broader trips the warn path.
	if mode&0o077 != 0 {
		return checkResult{
			name:   "cli config perms",
			status: "WARN",
			detail: fmt.Sprintf("%s has mode %#o (group/other bits set)", path, mode),
			hint:   fmt.Sprintf("chmod 0600 %s — the file contains your CLI bearer token", path),
		}
	}
	return checkResult{
		name:   "cli config perms",
		status: "PASS",
		detail: fmt.Sprintf("%s mode %#o", path, mode),
	}
}

// runCheckCLIConfigServerScheme is the doctor-side wiring: load
// config once, hand to the testable helper. Kept as a thin shim so
// the helper can be tested with synthetic configs (the test does NOT
// touch the real ~/.crewship/cli-config.yaml).
func runCheckCLIConfigServerScheme() checkResult {
	cfg, err := cli.LoadConfig()
	if err != nil {
		return checkResult{
			name:   "cli server scheme",
			status: "FAIL",
			detail: fmt.Sprintf("load cli config: %v", err),
		}
	}
	return checkCLIConfigServerScheme(cfg)
}
