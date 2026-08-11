//go:build unix

// The DefaultSocketPathFor cases whose inputs and expectations are unix path
// literals: the packaged /var/lib/crewship root, $HOME/.crewship data dirs, and
// the sockaddr_un length cap that has no windows analogue. They live behind a
// `unix` build tag rather than a runtime.GOOS skip because a skipped test
// prints the same `ok` as a passing one — the compile-time guard is the same
// exclusion without the false green. The GOOS-parameterised half of the same
// derivation (windows under %ProgramData%) is covered by TestDefaultPathsFor in
// paths_test.go, which passes the GOOS in and therefore runs everywhere.

package config

import (
	"strings"
	"testing"
)

// #1922 follow-up: CREWSHIP_DATA_DIR is not the only thing that moves an
// instance's data dir. With the var unset, database.DefaultDataDir resolves to
// $HOME/.crewship — NOT to the packaged /var/lib/crewship — so every such
// instance got the shared /tmp/crewship.sock from DefaultSocketPath() while
// cmd_start re-rooted its bolt file under the home dir. DefaultSocketPathFor
// takes the ALREADY-RESOLVED root instead of re-reading the env var, which is
// what lets cmd_start derive the socket from the same directory as the bolt
// file. The rules it applies must be identical to the env-var path's.
func TestDefaultSocketPathFor(t *testing.T) {
	// Set deliberately: the resolved root is the only input that may matter.
	// If the env var leaked into the result, the unset-var case — the one this
	// function exists for — would be wrong again.
	t.Setenv("CREWSHIP_DATA_DIR", "/some/unrelated/dir")

	tests := []struct {
		name string
		root string
		want string
	}{
		{
			name: "packaged FHS root keeps the historical literal",
			root: "/var/lib/crewship",
			want: "/tmp/crewship.sock",
		},
		{
			name: "trailing slash still counts as the packaged root",
			root: "/var/lib/crewship/",
			want: "/tmp/crewship.sock",
		},
		{
			name: "home data dir gets its own socket",
			root: "/home/alice/.crewship",
			want: "/home/alice/.crewship/crewship.sock",
		},
		{
			name: "a second home data dir gets a different one",
			root: "/home/bob/.crewship",
			want: "/home/bob/.crewship/crewship.sock",
		},
		{
			name: "empty root falls back to the platform default",
			root: "",
			want: DefaultSocketPath(),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := DefaultSocketPathFor(tc.root); got != tc.want {
				t.Errorf("DefaultSocketPathFor(%q) = %q, want %q", tc.root, got, tc.want)
			}
		})
	}
}

// The sockaddr_un ceiling applies to the resolved-root form too: a deep home
// dir must still get a short, per-root-unique socket rather than a path the
// kernel will refuse to bind.
func TestDefaultSocketPathForHonorsLengthCap(t *testing.T) {
	deep := "/home/" + strings.Repeat("verylongsegment/", 12) + ".crewship"
	got := DefaultSocketPathFor(deep)
	if len(got) >= 104 {
		t.Errorf("socket %q is %d bytes, want < 104", got, len(got))
	}
	if other := DefaultSocketPathFor(deep + "2"); other == got {
		t.Errorf("two deep roots share fallback socket %q", got)
	}
}
