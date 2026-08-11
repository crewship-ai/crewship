//go:build !clionly

package main

import (
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/config"
)

// #1922 follow-up. `crewship start` re-roots the bolt file under the data dir
// it actually resolved (database.DefaultDataDir → $HOME/.crewship when
// CREWSHIP_DATA_DIR is unset), but left the IPC socket at whatever
// config.Default() produced — and with the var unset that is the shared
// /tmp/crewship.sock. Two instances on one host with nothing but different
// homes therefore got two bolt files and ONE socket: before the
// ensureSocketPathFree guard the second silently unlinked the first's socket;
// with the guard it refuses to start at all. Neither instance had to be
// misconfigured to get there.
//
// startSocketPath is the socket's half of the bolt rewrite, and these cases pin
// down that it decides the same way: an explicit choice always wins, the
// packaged FHS install keeps its historical literal, and everything else
// follows the resolved data dir.
func TestStartSocketPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix path literals")
	}

	tests := []struct {
		name       string
		envSocket  string // CREWSHIP_SOCKET_PATH
		envDataDir string // CREWSHIP_DATA_DIR (drives config.Default())
		current    string // cfg.IPC.SocketPath as config.Load left it
		root       string // dataDir.Root as database.DefaultDataDir resolved it
		want       string
	}{
		{
			// The regression itself: no env vars at all, two users, two homes.
			name:    "unset data dir follows the resolved home root",
			current: "/tmp/crewship.sock",
			root:    "/home/alice/.crewship",
			want:    "/home/alice/.crewship/crewship.sock",
		},
		{
			name:    "a second home resolves somewhere else",
			current: "/tmp/crewship.sock",
			root:    "/home/bob/.crewship",
			want:    "/home/bob/.crewship/crewship.sock",
		},
		{
			// packaging/crewship.service pins CREWSHIP_DATA_DIR to the FHS
			// root; SECURITY.md and README.md have named this socket for
			// years. It must come back byte-identical.
			name:       "packaged FHS install keeps /tmp/crewship.sock",
			envDataDir: "/var/lib/crewship",
			current:    "/tmp/crewship.sock",
			root:       "/var/lib/crewship",
			want:       "/tmp/crewship.sock",
		},
		{
			// The relocated case already worked through config.Default();
			// re-deriving it from the resolved root must be a no-op, not a
			// second, different answer.
			name:       "relocated data dir is idempotent",
			envDataDir: "/srv/crewship/inst2",
			current:    "/srv/crewship/inst2/crewship.sock",
			root:       "/srv/crewship/inst2",
			want:       "/srv/crewship/inst2/crewship.sock",
		},
		{
			name:      "CREWSHIP_SOCKET_PATH wins",
			envSocket: "/run/crewship/pinned.sock",
			current:   "/run/crewship/pinned.sock",
			root:      "/home/alice/.crewship",
			want:      "/run/crewship/pinned.sock",
		},
		{
			// An env-pinned socket stays pinned even when it happens to name
			// the historical default — the operator typed it.
			name:      "CREWSHIP_SOCKET_PATH wins even at the legacy literal",
			envSocket: "/tmp/crewship.sock",
			current:   "/tmp/crewship.sock",
			root:      "/home/alice/.crewship",
			want:      "/tmp/crewship.sock",
		},
		{
			name:    "explicit YAML ipc.socket_path wins",
			current: "/var/run/myco/crewship.sock",
			root:    "/home/alice/.crewship",
			want:    "/var/run/myco/crewship.sock",
		},
		{
			name:    "empty socket path is not an operator choice",
			current: "",
			root:    "/home/alice/.crewship",
			want:    "/home/alice/.crewship/crewship.sock",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("CREWSHIP_SOCKET_PATH", tc.envSocket)
			t.Setenv("CREWSHIP_DATA_DIR", tc.envDataDir)
			if got := startSocketPath(tc.current, tc.root); got != tc.want {
				t.Errorf("startSocketPath(%q, %q) = %q, want %q",
					tc.current, tc.root, got, tc.want)
			}
		})
	}
}

// The B-04 property as `crewship start` actually reaches it: two instances that
// set nothing at all must not name the same socket. Asserted separately from
// the table because it is the invariant, not one row of it.
func TestStartSocketPathDiffersPerHome(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix path literals")
	}
	t.Setenv("CREWSHIP_SOCKET_PATH", "")
	t.Setenv("CREWSHIP_DATA_DIR", "")

	def := config.Default().IPC.SocketPath
	a := startSocketPath(def, "/home/alice/.crewship")
	b := startSocketPath(def, "/home/bob/.crewship")
	if a == b {
		t.Fatalf("two homes share socket %q — the second instance either unlinks "+
			"the first's socket or refuses to start", a)
	}
}

// A deep home dir still cannot host an AF_UNIX socket, so the hashed temp-dir
// fallback must survive the trip through cmd_start.
func TestStartSocketPathHonorsLengthCap(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix socket length rules")
	}
	t.Setenv("CREWSHIP_SOCKET_PATH", "")
	t.Setenv("CREWSHIP_DATA_DIR", "")

	deep := "/home/" + strings.Repeat("verylongsegment/", 12) + ".crewship"
	got := startSocketPath("/tmp/crewship.sock", deep)
	if len(got) >= 104 {
		t.Errorf("socket %q is %d bytes, want < 104", got, len(got))
	}
	if filepath.Dir(got) == deep {
		t.Errorf("socket %q was placed under the deep root anyway", got)
	}
}
