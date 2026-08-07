package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

// GET /api/v1/system/runtime carries `in_use` per entry and a top-level
// `install_links` map (#1696). The CLI twin dropped both on the floor: its
// systemRuntimeEntry had no InUse field and systemRuntimeInfo had no
// InstallLinks, so `crewship system info -f json` handed back a runtimes[]
// array with nothing marked — the one question the endpoint exists to answer
// (#1707).
//
// The stub below is the four-runtime macOS case from the issue: OrbStack in
// use, Colima and Rancher installed and idle, Apple Containers present.
func stubFourRuntimes(t *testing.T) *clitest.StubServer {
	t.Helper()
	stub := covSetupCli5(t)
	stub.OnGet("/api/v1/system/runtime", clitest.JSONResponse(200, map[string]any{
		"available": true,
		"runtime":   "orbstack",
		"version":   "29.4.0",
		"socket":    "/var/run/docker.sock",
		"runtimes": []map[string]any{
			{"runtime": "orbstack", "version": "29.4.0", "socket": "/var/run/docker.sock", "in_use": true},
			{"runtime": "colima", "version": "29.5.2", "socket": "/Users/u/.colima/default/docker.sock", "in_use": false},
			{"runtime": "rancher", "version": "29.5.3", "socket": "/Users/u/.rd/docker.sock", "in_use": false},
			{"runtime": "apple", "version": "1.2.0", "socket": "", "in_use": false},
		},
		"install_links": map[string]string{
			"docker":   "https://docs.docker.com/get-docker/",
			"podman":   "https://podman.io/docs/installation",
			"orbstack": "https://orbstack.dev/",
		},
	}))
	stub.OnGet("/api/v1/system/license", clitest.ErrorResponse(404, "none"))
	return stub
}

func TestSystemInfo_JSONCarriesInUsePerEntry(t *testing.T) {
	stubFourRuntimes(t)
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
			} `json:"runtimes"`
			InstallLinks map[string]string `json:"install_links"`
		} `json:"runtime"`
	}
	if uerr := json.Unmarshal([]byte(out), &payload); uerr != nil {
		t.Fatalf("--format json stdout is not valid JSON: %v\ngot:\n%s", uerr, out)
	}

	if len(payload.Runtime.Runtimes) != 4 {
		t.Fatalf("expected 4 runtimes, got %d:\n%s", len(payload.Runtime.Runtimes), out)
	}
	inUse := ""
	for _, rt := range payload.Runtime.Runtimes {
		if rt.InUse {
			if inUse != "" {
				t.Errorf("two runtimes marked in_use (%s and %s)", inUse, rt.Runtime)
			}
			inUse = rt.Runtime
		}
	}
	if inUse != "orbstack" {
		t.Errorf("no entry carries in_use=true (got %q) — a consumer reading runtimes[] cannot tell "+
			"which runtime is being driven, which is the whole point of the field:\n%s", inUse, out)
	}
}

func TestSystemInfo_JSONCarriesInstallLinks(t *testing.T) {
	stubFourRuntimes(t)
	flagFormat = "json"

	var err error
	out := covCaptureStdoutCli5(t, func() { err = systemInfoCmd.RunE(systemInfoCmd, nil) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}

	var payload struct {
		Runtime struct {
			InstallLinks map[string]string `json:"install_links"`
		} `json:"runtime"`
	}
	if uerr := json.Unmarshal([]byte(out), &payload); uerr != nil {
		t.Fatalf("--format json stdout is not valid JSON: %v\ngot:\n%s", uerr, out)
	}
	if got := payload.Runtime.InstallLinks["orbstack"]; got != "https://orbstack.dev/" {
		t.Errorf("install_links dropped by the CLI (orbstack = %q) — the server sends them precisely so a "+
			"caller can be told what else it could install:\n%s", got, out)
	}
}

// The human surface has the same duty as the JSON one: say which runtime is
// being driven. It used to assume runtimes[0] was in use and label the rest
// "what you could switch to" — two claims the server does not make and the
// config does not support (container.provider takes docker|apple|auto, so
// there is no orbstack/colima/rancher to switch TO).
func TestSystemInfo_HumanMarksTheRuntimeInUse(t *testing.T) {
	stubFourRuntimes(t)

	var err error
	out := covCaptureStdoutCli5(t, func() { err = systemInfoCmd.RunE(systemInfoCmd, nil) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}

	for _, want := range []string{"orbstack", "colima", "rancher", "apple", "in use"} {
		if !strings.Contains(out, want) {
			t.Errorf("human output missing %q:\n%s", want, out)
		}
	}

	// The marker must sit on the runtime the server marked, not on the first
	// one listed.
	for _, line := range strings.Split(out, "\n") {
		if !strings.Contains(line, "in use") {
			continue
		}
		if !strings.Contains(line, "orbstack") {
			t.Errorf("the 'in use' marker is on the wrong runtime: %q", line)
		}
	}

	// And it must not claim a switch that does not exist.
	if strings.Contains(strings.ToLower(out), "switch to") {
		t.Errorf("output offers a runtime switch the config has no setting for:\n%s", out)
	}
}

// A caller with no workspace selected — or with a role below ADMIN in the one
// selected — gets the redacted `{"available": true}` shape (#865). Rendering
// that as `Runtime:` / `Version:` with empty values reads as "no runtime
// detected", which is the opposite of what the server said.
func TestSystemInfo_RedactedResponseSaysWhyRatherThanPrintingBlanks(t *testing.T) {
	stub := covSetupCli5(t)
	stub.OnGet("/api/v1/system/runtime", clitest.JSONResponse(200, map[string]any{
		"available": true,
	}))
	stub.OnGet("/api/v1/system/license", clitest.ErrorResponse(404, "none"))

	var err error
	out := covCaptureStdoutCli5(t, func() { err = systemInfoCmd.RunE(systemInfoCmd, nil) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}

	if strings.Contains(out, "Runtime:    \n") || strings.Contains(out, "Version:    \n") {
		t.Errorf("redacted response rendered as empty Runtime/Version, which reads as 'nothing detected':\n%s", out)
	}
	lower := strings.ToLower(out)
	for _, want := range []string{"admin", "workspace"} {
		if !strings.Contains(lower, want) {
			t.Errorf("redacted response does not say why the detail is missing (missing %q):\n%s", want, out)
		}
	}
}

// `runtime`/`version`/`socket` are null when runtimes are installed but none is
// in use — the server booted without a container provider. Rendering that as
// blanks would say "nothing detected" beside a list of detected runtimes.
func TestSystemInfo_InstalledButNoneInUseSaysSo(t *testing.T) {
	stub := covSetupCli5(t)
	stub.OnGet("/api/v1/system/runtime", clitest.JSONResponse(200, map[string]any{
		"available": true, "runtime": nil, "version": nil, "socket": nil,
		"runtimes": []map[string]any{
			{"runtime": "docker", "version": "29.3.0", "socket": "/var/run/docker.sock", "in_use": false},
		},
	}))
	stub.OnGet("/api/v1/system/license", clitest.ErrorResponse(404, "none"))

	var err error
	out := covCaptureStdoutCli5(t, func() { err = systemInfoCmd.RunE(systemInfoCmd, nil) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "none in use") {
		t.Errorf("a runtime is installed and none is driven; the output does not say so:\n%s", out)
	}
	if !strings.Contains(out, "docker") {
		t.Errorf("the installed runtime is not named:\n%s", out)
	}
	if strings.Contains(out, "in use)") && !strings.Contains(out, "none in use") {
		t.Errorf("a runtime is marked in use when the server marked none:\n%s", out)
	}
}

// When nothing answered, the blank `Runtime:` / `Version:` lines say nothing
// the availability flag has not, and the install links do the real work.
func TestSystemInfo_NoRuntimeOffersInstallLinksNotBlanks(t *testing.T) {
	stub := covSetupCli5(t)
	stub.OnGet("/api/v1/system/runtime", clitest.JSONResponse(200, map[string]any{
		"available": false, "runtime": nil, "version": nil, "socket": nil, "runtimes": []any{},
		"install_links": map[string]string{
			"docker": "https://docs.docker.com/get-docker/",
			"podman": "https://podman.io/docs/installation",
		},
	}))
	stub.OnGet("/api/v1/system/license", clitest.ErrorResponse(404, "none"))

	var err error
	out := covCaptureStdoutCli5(t, func() { err = systemInfoCmd.RunE(systemInfoCmd, nil) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if strings.Contains(out, "Runtime:") || strings.Contains(out, "Version:") {
		t.Errorf("nothing answered, so there is no runtime or version to label:\n%s", out)
	}
	for _, want := range []string{"https://docs.docker.com/get-docker/", "https://podman.io/docs/installation"} {
		if !strings.Contains(out, want) {
			t.Errorf("install link %q not offered when no runtime answered:\n%s", want, out)
		}
	}
}

// The pre-existing shape — a server that answers without in_use at all — must
// still render, with no runtime marked rather than a wrong one marked.
func TestSystemInfo_ServerWithoutInUseMarksNothing(t *testing.T) {
	stub := covSetupCli5(t)
	stub.OnGet("/api/v1/system/runtime", clitest.JSONResponse(200, map[string]any{
		"available": true, "runtime": "docker", "version": "29.3.0", "socket": "/var/run/docker.sock",
		"runtimes": []map[string]any{
			{"runtime": "docker", "version": "29.3.0", "socket": "/var/run/docker.sock"},
			{"runtime": "podman", "version": "5.2.1", "socket": "/run/podman/podman.sock"},
		},
	}))
	stub.OnGet("/api/v1/system/license", clitest.ErrorResponse(404, "none"))

	var err error
	out := covCaptureStdoutCli5(t, func() { err = systemInfoCmd.RunE(systemInfoCmd, nil) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if strings.Contains(out, "in use") {
		t.Errorf("nothing was marked in_use by the server, so nothing may be marked here:\n%s", out)
	}
	for _, want := range []string{"docker", "podman"} {
		if !strings.Contains(out, want) {
			t.Errorf("human output missing %q:\n%s", want, out)
		}
	}
}
