package main

// `crewship system info` printed an empty Runtime and Version for everyone.
// Not a formatting bug: the command cleared the workspace before asking, so
// the server could not resolve the caller as ADMIN+ and answered with the
// redacted availability-only shape it gives non-admins (#865). The output
// then read as "no runtime detected" when the real answer was "you did not
// ask as someone allowed to know".

import (
	"net/http"
	"strings"
	"testing"
)

func TestSystemInfo_AsksWithinTheWorkspaceSoAdminsGetHostDetail(t *testing.T) {
	stub := covStub(t)
	var runtimeQuery string
	stub.OnGet("/api/v1/system/runtime", func(r *http.Request, _ []byte) (int, []byte, string) {
		runtimeQuery = r.URL.RawQuery
		return 200, []byte(`{"available":true,"runtime":"podman","version":"5.2.1","socket":"/run/podman/podman.sock",
			"runtimes":[{"runtime":"podman","version":"5.2.1","socket":"/run/podman/podman.sock"}]}`), "application/json"
	})
	stub.OnGet("/api/v1/system/license", func(*http.Request, []byte) (int, []byte, string) {
		return 200, []byte(`{"edition":"community","max_crews":15,"max_agents_per_crew":10,"max_members":5}`), "application/json"
	})

	covResetFlags(t, systemInfoCmd)
	out := covCaptureAll(t, func() {
		if err := systemInfoCmd.RunE(systemInfoCmd, nil); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})

	if !strings.Contains(runtimeQuery, "workspace_id=") {
		t.Errorf("runtime request was not workspace-scoped (query %q) — an admin cannot be resolved without it", runtimeQuery)
	}
	for _, want := range []string{"podman", "5.2.1"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

// Several runtimes can be installed at once — Docker Desktop and Podman on
// the same laptop is the normal case for anyone testing both. The command
// should say which one is in use AND what else is there, or switching
// between them is invisible.
func TestSystemInfo_ListsEveryDetectedRuntime(t *testing.T) {
	stub := covStub(t)
	stub.OnGet("/api/v1/system/runtime", func(*http.Request, []byte) (int, []byte, string) {
		return 200, []byte(`{"available":true,"runtime":"docker","version":"29.3.0","socket":"/var/run/docker.sock",
			"runtimes":[
				{"runtime":"docker","version":"29.3.0","socket":"/var/run/docker.sock"},
				{"runtime":"podman","version":"5.2.1","socket":"/run/podman/podman.sock"}
			]}`), "application/json"
	})
	stub.OnGet("/api/v1/system/license", func(*http.Request, []byte) (int, []byte, string) {
		return 200, []byte(`{"edition":"community"}`), "application/json"
	})

	covResetFlags(t, systemInfoCmd)
	out := covCaptureAll(t, func() {
		if err := systemInfoCmd.RunE(systemInfoCmd, nil); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	if !strings.Contains(out, "podman") {
		t.Errorf("the second runtime is invisible:\n%s", out)
	}
}
