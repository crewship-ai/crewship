package orchestrator

// The sidecar health check could not tell "the sidecar is down" apart from
// "I have no way to ask".
//
// It probed with `wget`, falling back to `curl`. debian:bookworm-slim — the
// compiled-in default RuntimeImage (internal/config/config.go), the value in
// docs/configuration/environment.mdx, and what scripts/e2e-devcontainer-test.sh
// passes — ships neither. Both branches exited 127 "command not found", the
// else branch fired, and a sidecar that was up and serving on 127.0.0.1:9119
// was reported as "sidecar health check failed (exit 1)". Out of the box, on
// the default image, no agent could answer a message.
//
// The fix is to probe with the sidecar binary itself, which the container
// provider bind-mounts read-only at /usr/local/bin/crewship-sidecar — the one
// executable that is present by construction rather than by luck.

import (
	"regexp"
	"strings"
	"testing"
)

func TestSidecarLaunchScriptProbesWithTheBindMountedBinary(t *testing.T) {
	script := sidecarLaunchScript("Y3JlZHM=")

	t.Run("probes with crewship-sidecar first", func(t *testing.T) {
		if !strings.Contains(script, "crewship-sidecar --health-check") {
			t.Error("health check does not use the bind-mounted binary; on the default " +
				"runtime image no probe tool exists and a healthy sidecar reads as failed")
		}
	})

	t.Run("the binary probe is tried before wget and curl", func(t *testing.T) {
		// Order is the whole fix. Keeping wget/curl is fine — they are
		// fallbacks for a container still running a sidecar that predates
		// --health-check — but if either is consulted first, an image that
		// happens to have a broken wget reintroduces the same class of
		// false negative.
		probe := strings.Index(script, "crewship-sidecar --health-check")
		wget := strings.Index(script, "wget")
		curl := strings.Index(script, "curl")
		if probe < 0 {
			t.Fatal("no --health-check probe in the script")
		}
		if wget >= 0 && probe > wget {
			t.Error("wget is consulted before the bind-mounted binary")
		}
		if curl >= 0 && probe > curl {
			t.Error("curl is consulted before the bind-mounted binary")
		}
	})

	t.Run("keeps the older-sidecar fallbacks", func(t *testing.T) {
		// A running container holds whatever sidecar was mounted when it
		// started. Dropping these would break an upgrade-in-place: the new
		// server would probe with a flag the old binary rejects, and every
		// warm container would go unhealthy at once.
		for _, tool := range []string{"wget", "curl"} {
			if !strings.Contains(script, tool) {
				t.Errorf("fallback %q removed — warm containers running a pre-flag "+
					"sidecar would all fail their next health check", tool)
			}
		}
	})

	t.Run("still exits non-zero when nothing answers", func(t *testing.T) {
		if !strings.Contains(script, "exit 1") {
			t.Error("a genuinely dead sidecar must still fail the exec")
		}
	})

	t.Run("the failure message points at the sidecar log", func(t *testing.T) {
		// The old message was the bare sentence "sidecar health check failed",
		// which is what the operator saw in the chat window and in the server
		// log — with no hint that the sidecar's own log sits inside the
		// container and says it started fine.
		tail := script[strings.LastIndex(script, "else"):]
		if !strings.Contains(tail, sidecarLogPath) {
			t.Errorf("failure branch does not name %s; the log that disproves the "+
				"message is the first thing anyone needs", sidecarLogPath)
		}
	})
}

// The credential payload rides stdin, never argv. Re-asserted here because
// this test constructs the script and would be the natural place to "simplify"
// that away.
func TestSidecarLaunchScriptKeepsCredentialsOffArgv(t *testing.T) {
	script := sidecarLaunchScript("U0VDUkVU")
	launch := regexp.MustCompile(`crewship-sidecar --addr [^\n]*`).FindString(script)
	if launch == "" {
		t.Fatal("no sidecar launch line found")
	}
	if strings.Contains(launch, "U0VDUkVU") {
		t.Error("credentials appear on the sidecar's argv, where any process in the " +
			"container can read them from /proc")
	}
	if !strings.Contains(script, "base64 -d | crewship-sidecar") {
		t.Error("credentials are no longer piped in on stdin")
	}
}
