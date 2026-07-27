package cli

import (
	"fmt"
	"runtime"
)

// UserAgent returns the value the CLI sends as its User-Agent header on
// every outgoing request: "crewship/<version> (<goos>/<goarch>)", e.g.
// "crewship/0.9.3 (darwin/arm64)". <version> reuses the ldflags-injected
// build version fed via SetClientVersion (see version_skew.go) — the same
// source of truth the skew-detection warning already compares against,
// rather than inventing a second one. A dev build (or a Client used before
// the entrypoint calls SetClientVersion) reports "" there; we substitute
// "dev" so the header never renders as the malformed "crewship/ (...)".
//
// This is the ONLY place the header string is built. Every request-building
// call site in this package must call it — see client.go's NewRequest and
// resolveWorkspaceSlug — so a future call site can't silently ship without
// it. Deliberately just version/os/arch: this string lands in the
// user-visible user_sessions.user_agent column on the server
// (internal/api/nextauth.go), so no hostname, username, or machine ID rides
// along — we're identifying the client build, not fingerprinting the caller.
func UserAgent() string {
	v := currentClientVersion()
	if v == "" {
		v = "dev"
	}
	return fmt.Sprintf("crewship/%s (%s/%s)", v, runtime.GOOS, runtime.GOARCH)
}
