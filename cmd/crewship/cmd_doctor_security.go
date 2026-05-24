//go:build !clionly

package main

import (
	"fmt"
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

// checkCLIConfigPerms verifies cli-config.yaml exists at the default
// path and has mode 0600 (or stricter). Returns:
//
//   - INFO when the file doesn't exist (no token saved → no risk)
//   - PASS when mode is 0600 or stricter (0400)
//   - WARN when group/other bits are set, with the actual mode in the
//     detail so the operator knows what to chmod
//   - FAIL when the path can't be resolved at all (HOMEDIR broken)
//
// Without --fix the check WARNs (not FAILs) on permissive mode: the
// user is the only one who can chmod it, the file contents are still
// valid, and a WARN on every doctor invocation is the nag that
// motivates the fix without blocking unrelated workflows.
//
// With --fix the check ATTEMPTS chmod 0600 — same shape the existing
// data-dir-perm check uses for its auto-mkdir, with the same
// best-effort posture (a chmod failure downgrades to WARN with the
// original hint, not FAIL, so a read-only mount or a syscall denial
// doesn't break the rest of doctor).
//
// Windows is intentionally not covered — file modes on Windows do not
// map to the unix r/w/x bits the way doctor describes them. The
// existing data-dir-perm check has the same skip; this one mirrors it
// for consistency.
func checkCLIConfigPerms(fixMode bool) checkResult {
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
		if fixMode {
			// Try to repair. The new mode is 0600 unconditionally —
			// stricter (0400) might be intentional for a read-only
			// token file, but the doctor's job is "is this safe?"
			// not "what does the operator prefer?", and 0600 is the
			// minimum that satisfies the check. Failure surfaces as
			// WARN (not FAIL) so an unfixable filesystem doesn't
			// break the rest of doctor.
			if chmodErr := os.Chmod(path, 0o600); chmodErr != nil {
				return checkResult{
					name:   "cli config perms",
					status: "WARN",
					detail: fmt.Sprintf("%s has mode %#o; auto-fix failed: %v", path, mode, chmodErr),
					hint:   fmt.Sprintf("chmod 0600 %s — the file contains your CLI bearer token", path),
				}
			}
			return checkResult{
				name:   "cli config perms",
				status: "PASS",
				detail: fmt.Sprintf("%s mode %#o → 0600 (fixed via --fix)", path, mode),
			}
		}
		return checkResult{
			name:   "cli config perms",
			status: "WARN",
			detail: fmt.Sprintf("%s has mode %#o (group/other bits set)", path, mode),
			hint:   fmt.Sprintf("chmod 0600 %s — the file contains your CLI bearer token (or re-run with --fix to repair)", path),
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
