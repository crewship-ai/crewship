package cli

import (
	"fmt"
	"os"
	"strings"
	"sync"

	"golang.org/x/mod/semver"
)

// Version-skew detection.
//
// A stale CLI against a newer server is the single most common source of
// confusing API errors (unknown fields silently dropped, 404s on new routes,
// changed validation). The server stamps every API response with
// X-Crewship-Server-Version (internal/api VersionHeader middleware); the
// client compares it against its own ldflags-injected version once per
// process and prints a one-line stderr hint naming the remedy. No extra
// round-trip — the signal rides responses the command makes anyway.

var (
	skewMu            sync.Mutex
	skewClientVersion string
	skewWarned        bool
)

// SetClientVersion records the running binary's version (main.version,
// ldflags-injected; "dev" for local builds) for skew comparison. Called once
// from the CLI entrypoint.
func SetClientVersion(v string) {
	skewMu.Lock()
	defer skewMu.Unlock()
	skewClientVersion = v
}

// maybeWarnVersionSkew compares the server-reported version against the
// client's and warns AT MOST once per process. Silent whenever a meaningful
// comparison isn't possible (either side empty/dev/non-semver) or the
// operator opted out via CREWSHIP_SKIP_UPDATE_CHECK — the same switch that
// silences the release update probe, since both answer "stop telling me to
// upgrade".
func maybeWarnVersionSkew(serverVersion string) {
	if serverVersion == "" {
		return
	}
	skewMu.Lock()
	defer skewMu.Unlock()
	if skewWarned || skewClientVersion == "" {
		return
	}
	if os.Getenv("CREWSHIP_SKIP_UPDATE_CHECK") != "" {
		return
	}
	client := normalizeSemver(skewClientVersion)
	server := normalizeSemver(serverVersion)
	if client == "" || server == "" {
		return // dev / non-release build on either side — no meaningful skew
	}
	if semver.Compare(client, server) == 0 {
		return
	}
	skewWarned = true
	remedy := "run 'crewship self-update'"
	if semver.Compare(client, server) > 0 {
		remedy = "upgrade the server (see docs/guides/upgrades)"
	}
	fmt.Fprintf(os.Stderr,
		"warning: server is %s but this CLI is %s — confusing API errors may be version skew; %s (silence with CREWSHIP_SKIP_UPDATE_CHECK=1)\n",
		strings.TrimPrefix(server, "v"), strings.TrimPrefix(client, "v"), remedy)
}

// normalizeSemver returns a canonical "vX.Y.Z[-pre]" or "" when v isn't a
// release version (e.g. "dev", "main", "").
func normalizeSemver(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	if !strings.HasPrefix(v, "v") {
		v = "v" + v
	}
	if !semver.IsValid(v) {
		return ""
	}
	return v
}
