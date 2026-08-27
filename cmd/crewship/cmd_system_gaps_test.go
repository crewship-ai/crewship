package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

// `system info` is where an operator goes to ask what their container runtime
// is. Until #1672 it could describe the runtime completely and still not
// mention that this one silently drops a crew hardening control — that was said
// once, in a startup WARN, in the server's log, hours earlier.
//
// The gap the fixture carries is the real measured one: podman below 5 reduces
// GroupAdd to the primary gid, so agents lose gid 1002 and every crew-shared
// memory read fails with EACCES. An operator seeing agents "forget things" has
// no path from that symptom to that cause unless a surface they can reach says
// it (#1673).
func stubPodmanWithGap(t *testing.T) *clitest.StubServer {
	t.Helper()
	stub := covSetupCli5(t)
	stub.OnGet("/api/v1/system/runtime", clitest.JSONResponse(200, map[string]any{
		"available": true,
		"in_use":    true,
		"runtime":   "podman",
		"version":   "4.9.3",
		"socket":    "/run/user/501/podman/podman.sock",
		"runtimes": []map[string]any{
			{
				"runtime": "podman", "version": "4.9.3",
				"socket": "/run/user/501/podman/podman.sock", "in_use": true,
				"gaps": []map[string]any{{
					"control": "GroupAdd",
					"detail": "podman 4.9.3 drops supplementary GIDs that have no /etc/group entry; " +
						"agents will not hold gid 1002 and crew-shared memory reads will fail with EACCES. " +
						"Fixed in podman 5; upgrading is the only remedy",
				}},
			},
		},
		"install_links": map[string]string{"podman": "https://podman.io/docs/installation"},
	}))
	stub.OnGet("/api/v1/system/license", clitest.ErrorResponse(404, "none"))
	return stub
}

func TestSystemInfo_HumanOutputNamesTheRuntimeGap(t *testing.T) {
	stubPodmanWithGap(t)
	flagFormat = "table"

	var err error
	out := covCaptureStdoutCli5(t, func() { err = systemInfoCmd.RunE(systemInfoCmd, nil) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}

	// The control, so it can be looked up; and the consequence, because
	// "GroupAdd is not honoured" is not something anyone can connect to the
	// memory failures they are actually seeing.
	for _, want := range []string{"GroupAdd", "crew-shared memory"} {
		if !strings.Contains(out, want) {
			t.Errorf("`system info` never mentions %q, so the gap is still invisible:\n%s", want, out)
		}
	}
}

// The machine shape carries it too — a fleet script asking "which of my hosts
// is on a runtime that drops a control?" must not have to scrape ANSI output.
func TestSystemInfo_JSONCarriesTheRuntimeGap(t *testing.T) {
	stubPodmanWithGap(t)
	flagFormat = "json"

	var err error
	out := covCaptureStdoutCli5(t, func() { err = systemInfoCmd.RunE(systemInfoCmd, nil) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}

	var payload struct {
		Runtime struct {
			Runtimes []struct {
				Runtime string `json:"runtime"`
				InUse   bool   `json:"in_use"`
				Gaps    []struct {
					Control string `json:"control"`
					Detail  string `json:"detail"`
				} `json:"gaps"`
			} `json:"runtimes"`
		} `json:"runtime"`
	}
	if uerr := json.Unmarshal([]byte(out), &payload); uerr != nil {
		t.Fatalf("--format json stdout is not valid JSON: %v\ngot:\n%s", uerr, out)
	}
	if len(payload.Runtime.Runtimes) != 1 {
		t.Fatalf("expected the single podman entry, got %d:\n%s", len(payload.Runtime.Runtimes), out)
	}
	got := payload.Runtime.Runtimes[0]
	if len(got.Gaps) != 1 {
		t.Fatalf("entry carries %d gap(s), want 1 — the CLI twin dropped the field:\n%s", len(got.Gaps), out)
	}
	if got.Gaps[0].Control != "GroupAdd" {
		t.Errorf("gaps[0].control = %q, want GroupAdd", got.Gaps[0].Control)
	}
	if !strings.Contains(got.Gaps[0].Detail, "crew-shared memory") {
		t.Errorf("gaps[0].detail lost the consequence: %q", got.Gaps[0].Detail)
	}
}

// A runtime with nothing measured against it prints no gap section at all.
// An empty "Known gaps:" heading reads as a finding and there isn't one.
func TestSystemInfo_NoGapSectionWhenTheRuntimeHonoursEverything(t *testing.T) {
	stub := covSetupCli5(t)
	stub.OnGet("/api/v1/system/runtime", clitest.JSONResponse(200, map[string]any{
		"available": true, "in_use": true,
		"runtime": "docker", "version": "28.0.4", "socket": "/var/run/docker.sock",
		"runtimes": []map[string]any{
			{"runtime": "docker", "version": "28.0.4", "socket": "/var/run/docker.sock", "in_use": true},
		},
	}))
	stub.OnGet("/api/v1/system/license", clitest.ErrorResponse(404, "none"))
	flagFormat = "table"

	var err error
	out := covCaptureStdoutCli5(t, func() { err = systemInfoCmd.RunE(systemInfoCmd, nil) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if strings.Contains(out, "Known gaps") {
		t.Errorf("docker 28.0.4 got a gap section with nothing in it:\n%s", out)
	}
}
