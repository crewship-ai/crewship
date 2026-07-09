package config

import (
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
			p := defaultPathsFor(goos, `C:\ProgramData`, "/tmp")
			if p.Base != "/var/lib/crewship" || p.Log != "/var/log/crewship" ||
				p.Bolt != "/var/lib/crewship/state.db" || p.Socket != "/tmp/crewship.sock" {
				t.Errorf("%s defaults changed: %+v", goos, p)
			}
		}
	})

	t.Run("windows under ProgramData + TempDir", func(t *testing.T) {
		p := defaultPathsFor("windows", `C:\ProgramData`, `C:\Users\u\AppData\Local\Temp`)
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
		p := defaultPathsFor("windows", "", `C:\Temp`)
		if !strings.HasPrefix(p.Base, `C:\ProgramData`) {
			t.Errorf("Base = %q, want C:\\ProgramData fallback", p.Base)
		}
	})
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
