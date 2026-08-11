package config

import (
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// #946: on Windows the Unix defaults (/var/lib/crewship, /tmp/crewship.sock)
// are unwritable or meaningless. defaultPathsFor must return %ProgramData%
// locations there while leaving the Unix defaults byte-identical (cmd_start's
// defaulted-boltpath sentinel and the deb/rpm layout both depend on the
// literals).
func TestDefaultPathsFor(t *testing.T) {
	t.Run("unix literals unchanged", func(t *testing.T) {
		for _, goos := range []string{"linux", "darwin"} {
			p := defaultPathsFor(goos, `C:\ProgramData`, "/tmp", "")
			if p.Base != "/var/lib/crewship" || p.Log != "/var/log/crewship" ||
				p.Bolt != "/var/lib/crewship/state.db" || p.Socket != "/tmp/crewship.sock" {
				t.Errorf("%s defaults changed: %+v", goos, p)
			}
		}
	})

	t.Run("windows under ProgramData + TempDir", func(t *testing.T) {
		p := defaultPathsFor("windows", `C:\ProgramData`, `C:\Users\u\AppData\Local\Temp`, "")
		if p.Base != `C:\ProgramData\crewship` {
			t.Errorf("Base = %q", p.Base)
		}
		if p.Log != `C:\ProgramData\crewship\logs` {
			t.Errorf("Log = %q", p.Log)
		}
		if p.Bolt != `C:\ProgramData\crewship\state.db` {
			t.Errorf("Bolt = %q", p.Bolt)
		}
		if p.Socket != `C:\Users\u\AppData\Local\Temp\crewship.sock` {
			t.Errorf("Socket = %q", p.Socket)
		}
	})

	t.Run("windows falls back when ProgramData env is empty", func(t *testing.T) {
		p := defaultPathsFor("windows", "", `C:\Temp`, "")
		if !strings.HasPrefix(p.Base, `C:\ProgramData`) {
			t.Errorf("Base = %q, want C:\\ProgramData fallback", p.Base)
		}
	})

	// The B-04 derivation is not unix-only: a Windows operator running two
	// services with two data dirs would collide on
	// %ProgramData%\crewship\state.db in exactly the same way.
	t.Run("windows relocated data dir", func(t *testing.T) {
		p := defaultPathsFor("windows", `C:\ProgramData`, `C:\Temp`, `D:\crewship\inst2`)
		if p.Bolt != `D:\crewship\inst2\state.db` {
			t.Errorf("Bolt = %q", p.Bolt)
		}
		if p.Socket != `D:\crewship\inst2\crewship.sock` {
			t.Errorf("Socket = %q", p.Socket)
		}
		// Base and Log stay put: cmd_start owns them, and neither is a
		// lock-bearing file.
		if p.Base != `C:\ProgramData\crewship` {
			t.Errorf("Base = %q, want it left at the ProgramData default", p.Base)
		}
	})
}

// B-04: two crewshipd instances pointed at different data dirs still contended
// for the SAME /var/lib/crewship/state.db flock and the SAME /tmp/crewship.sock,
// because both were global literals independent of the data dir. The second
// process blocked on the bbolt lock forever and never bound its HTTP port.
//
// The contract these cases pin down:
//   - CREWSHIP_DATA_DIR unset, or set to the packaged FHS root, keeps the
//     historical literals byte-identical (deb/rpm layout, cmd_start's
//     defaulted-boltpath sentinel, and the docs all reference them);
//   - any other data dir moves BOTH lock-bearing files under it, so two
//     instances cannot collide by construction.
func TestDefaultPathsHonorDataDir(t *testing.T) {
	tests := []struct {
		name       string
		dataDir    string
		wantBolt   string
		wantSocket string
	}{
		{
			name:       "unset keeps the FHS literals",
			dataDir:    "",
			wantBolt:   "/var/lib/crewship/state.db",
			wantSocket: "/tmp/crewship.sock",
		},
		{
			name:       "packaged FHS data dir keeps the FHS literals",
			dataDir:    "/var/lib/crewship",
			wantBolt:   "/var/lib/crewship/state.db",
			wantSocket: "/tmp/crewship.sock",
		},
		{
			name:       "trailing slash still counts as the packaged root",
			dataDir:    "/var/lib/crewship/",
			wantBolt:   "/var/lib/crewship/state.db",
			wantSocket: "/tmp/crewship.sock",
		},
		{
			name:       "relocated data dir moves both lock-bearing files",
			dataDir:    "/srv/crewship/inst2",
			wantBolt:   "/srv/crewship/inst2/state.db",
			wantSocket: "/srv/crewship/inst2/crewship.sock",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("CREWSHIP_DATA_DIR", tc.dataDir)
			if got := DefaultBoltPath(); got != tc.wantBolt {
				t.Errorf("DefaultBoltPath() = %q, want %q", got, tc.wantBolt)
			}
			if got := DefaultSocketPath(); got != tc.wantSocket {
				t.Errorf("DefaultSocketPath() = %q, want %q", got, tc.wantSocket)
			}
		})
	}
}

// The B-04 property itself, stated directly: two instances with different data
// dirs must not name the same bolt file or the same socket. Asserted through
// the exported accessors because those are what cmd_start's sentinel and the
// API routers actually call.
func TestDefaultPathsDifferPerDataDir(t *testing.T) {
	read := func(dir string) (bolt, sock string) {
		t.Setenv("CREWSHIP_DATA_DIR", dir)
		return DefaultBoltPath(), DefaultSocketPath()
	}
	boltA, sockA := read(t.TempDir())
	boltB, sockB := read(t.TempDir())

	if boltA == boltB {
		t.Errorf("two data dirs share bolt path %q — instances will contend for one flock", boltA)
	}
	if sockA == sockB {
		t.Errorf("two data dirs share socket path %q — the second instance unlinks the first's socket", sockA)
	}
}

// CREWSHIP_DATA_DIR is resolved with filepath.Abs by database.DefaultDataDir,
// so the defaults must resolve it the same way. If they did not, cmd_start's
// `cfg.State.BoltPath == config.DefaultBoltPath()` sentinel would miss and the
// daemon would run with a bolt path relative to its working directory.
func TestDefaultPathsResolveRelativeDataDir(t *testing.T) {
	t.Setenv("CREWSHIP_DATA_DIR", "relative-data-dir")
	got := DefaultBoltPath()
	if !filepath.IsAbs(got) {
		t.Fatalf("DefaultBoltPath() = %q, want an absolute path", got)
	}
	if filepath.Base(filepath.Dir(got)) != "relative-data-dir" {
		t.Errorf("DefaultBoltPath() = %q, want it under .../relative-data-dir", got)
	}
}

// AF_UNIX paths are capped at ~104-108 bytes by the kernel's sockaddr_un, so a
// deep data dir cannot host the socket. The fallback must still be unique per
// data dir — the whole point of B-04 — hence the hashed name in the temp dir.
func TestDefaultPathsSocketFallsBackWhenTooLong(t *testing.T) {
	deep := "/srv/" + strings.Repeat("verylongsegment/", 12) + "data"
	p := defaultPathsFor("linux", "", "/tmp", deep)
	if len(p.Socket) >= 104 {
		t.Errorf("socket %q is %d bytes, want < 104", p.Socket, len(p.Socket))
	}
	if !strings.HasPrefix(p.Socket, "/tmp/") {
		t.Errorf("socket = %q, want the temp-dir fallback", p.Socket)
	}
	other := defaultPathsFor("linux", "", "/tmp", deep+"2")
	if other.Socket == p.Socket {
		t.Errorf("two deep data dirs share fallback socket %q", p.Socket)
	}
	if p.Bolt != filepath.Join(deep, "state.db") {
		t.Errorf("Bolt = %q, want it under the data dir regardless of socket length", p.Bolt)
	}
}

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
			if runtime.GOOS == "windows" {
				t.Skip("unix path literals")
			}
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
	if runtime.GOOS == "windows" {
		t.Skip("unix socket length rules")
	}
	deep := "/home/" + strings.Repeat("verylongsegment/", 12) + ".crewship"
	got := DefaultSocketPathFor(deep)
	if len(got) >= 104 {
		t.Errorf("socket %q is %d bytes, want < 104", got, len(got))
	}
	if other := DefaultSocketPathFor(deep + "2"); other == got {
		t.Errorf("two deep roots share fallback socket %q", got)
	}
}

// DefaultSocketPath is the single source of truth the API routers fall back
// to — it must agree with what Default() puts in IPC.SocketPath so a router
// constructed without an explicit socketPath dials the same socket the
// server listens on.
func TestDefaultSocketPathMatchesDefault(t *testing.T) {
	if got, want := DefaultSocketPath(), Default().IPC.SocketPath; got != want {
		t.Errorf("DefaultSocketPath() = %q, Default().IPC.SocketPath = %q", got, want)
	}
	if got, want := DefaultBoltPath(), Default().State.BoltPath; got != want {
		t.Errorf("DefaultBoltPath() = %q, Default().State.BoltPath = %q", got, want)
	}
}
