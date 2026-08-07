package api

// A resize says, on the response to the request that made it, that it is not
// in effect yet (#1681).
//
// `crewship crew update <slug> --memory-mb 8192` wrote the row and answered
// 200. Both limits are applied at ContainerCreate and nowhere else, so a crew
// whose container was already up kept the old cgroup limit — and the 200, the
// CLI's "updated" line and every later `crew get` all reported the new figure.
// Nothing reported the gap, which is what made it a trap rather than a delay:
// the operator had no reason to look.
//
// The notice fires only for a RUNNING crew, because that is the only state in
// which the change is pending on something. A stopped container is rebuilt
// with the new limits on its next wake, so telling its operator to go and
// restart something would be advice to do work the platform already does.

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// crewResizeIPC points the handler at a fake crewshipd reporting status.
func crewResizeIPC(t *testing.T, h *CrewHandler, status string) {
	t.Helper()
	sock := startFakeIPC(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"status": status})
	}))
	h.SetSocketPath(sock)
}

func TestCrewUpdate_ResizeOfARunningCrewSaysItIsNotInEffectYet(t *testing.T) {
	cases := []struct {
		name string
		body string
		want []string
	}{
		{
			name: "memory",
			body: `{"container_memory_mb":8192}`,
			want: []string{"container_memory_mb", "recreated", "container-status", "restart-agents"},
		},
		{
			name: "cpus",
			body: `{"container_cpus":4}`,
			want: []string{"container_cpus", "recreated", "container-status"},
		},
		{
			name: "both at once, in one notice",
			body: `{"container_memory_mb":8192,"container_cpus":4}`,
			want: []string{"container_memory_mb and container_cpus", "recreated"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, db, userID, wsID := crewResNew(t)
			crewID := seedCrewRow(t, db, "crew-resize", wsID, "Resize", "resize")
			crewResizeIPC(t, h, "running")

			rr := covCruDoUpdate(h, crewID, userID, wsID, "OWNER", tc.body)
			if rr.Code != http.StatusOK {
				t.Fatalf("update = %d, want 200 (body %s)", rr.Code, rr.Body.String())
			}
			warnings := crewResWarnings(t, rr.Body.Bytes())
			if len(warnings) != 1 {
				t.Fatalf("want exactly one resize notice, got %d: %v", len(warnings), warnings)
			}
			for _, want := range tc.want {
				if !strings.Contains(warnings[0], want) {
					t.Errorf("notice %q missing %q — the response to a resize is the one place the "+
						"operator is certain to be looking", warnings[0], want)
				}
			}
		})
	}
}

// A stopped crew is not told to go and restart anything: the provider rebuilds
// a stopped container whose limits no longer match on its next wake, so
// nothing is pending on the operator.
func TestCrewUpdate_ResizeOfAStoppedCrewIsQuiet(t *testing.T) {
	h, db, userID, wsID := crewResNew(t)
	crewID := seedCrewRow(t, db, "crew-stopped", wsID, "Stopped", "stopped")
	crewResizeIPC(t, h, "stopped")

	rr := covCruDoUpdate(h, crewID, userID, wsID, "OWNER", `{"container_memory_mb":8192}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("update = %d, want 200 (body %s)", rr.Code, rr.Body.String())
	}
	if w := crewResWarnings(t, rr.Body.Bytes()); len(w) != 0 {
		t.Errorf("a stopped crew was told its resize is pending on a recreate: %v", w)
	}
}

// An edit that cannot go stale must not carry the notice, or it becomes
// boilerplate on every PATCH and stops being read. Renaming a crew changes
// nothing a container was created with — even on a running crew.
func TestCrewUpdate_NonResizeCarriesNoRecreateNotice(t *testing.T) {
	h, db, userID, wsID := crewResNew(t)
	crewID := seedCrewRow(t, db, "crew-rename", wsID, "Rename", "rename")
	crewResizeIPC(t, h, "running")

	rr := covCruDoUpdate(h, crewID, userID, wsID, "OWNER", `{"name":"Renamed"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("update = %d, want 200 (body %s)", rr.Code, rr.Body.String())
	}
	joined := strings.Join(crewResWarnings(t, rr.Body.Bytes()), "\n")
	if strings.Contains(joined, "recreated") {
		t.Errorf("a rename was told its container has to be recreated: %q", joined)
	}
}

// An instance with no crewshipd socket has no container runtime this process
// can see, and must not invent a stale-limits notice for it.
func TestCrewUpdate_ResizeIsQuietWithoutARuntime(t *testing.T) {
	h, db, userID, wsID := crewResNew(t)
	crewID := seedCrewRow(t, db, "crew-noipc", wsID, "NoIPC", "noipc")

	rr := covCruDoUpdate(h, crewID, userID, wsID, "OWNER", `{"container_memory_mb":8192}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("update = %d, want 200 (body %s)", rr.Code, rr.Body.String())
	}
	if w := crewResWarnings(t, rr.Body.Bytes()); len(w) != 0 {
		t.Errorf("an instance with no IPC socket warned about a container it cannot see: %v", w)
	}
}
