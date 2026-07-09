package docker

import (
	"strings"
	"testing"
)

// #946: Docker Desktop on Windows serves the API over the named pipe
// \\.\pipe\docker_engine, not a unix socket — candidateSocketsFor must
// offer it (and only it) on windows, and every unix candidate must carry
// a dialable unix:// host URL. The rootless-podman candidate is keyed on
// a real uid; os.Getuid() returns -1 on Windows and used to produce a
// junk /run/user/-1/... path.
func TestCandidateSocketsFor(t *testing.T) {
	t.Run("windows offers the docker_engine named pipe", func(t *testing.T) {
		cands := candidateSocketsFor("windows", `C:\Users\u`, -1)
		if len(cands) == 0 {
			t.Fatal("no windows candidates")
		}
		found := false
		for _, c := range cands {
			if c.host == "npipe:////./pipe/docker_engine" {
				found = true
				if c.path != `\\.\pipe\docker_engine` {
					t.Errorf("npipe stat path = %q", c.path)
				}
			}
			if strings.HasPrefix(c.host, "unix://") {
				t.Errorf("unix socket candidate %q offered on windows", c.host)
			}
		}
		if !found {
			t.Error("npipe:////./pipe/docker_engine not offered on windows")
		}
	})

	t.Run("unix candidates carry unix hosts and skip rootless podman for negative uid", func(t *testing.T) {
		cands := candidateSocketsFor("linux", "/home/u", -1)
		for _, c := range cands {
			if !strings.HasPrefix(c.host, "unix://") {
				t.Errorf("candidate %q host %q not unix://", c.path, c.host)
			}
			if strings.Contains(c.path, "/run/user/-1/") {
				t.Errorf("junk rootless-podman path offered for uid -1: %q", c.path)
			}
		}
	})

	t.Run("unix rootless podman present for a real uid", func(t *testing.T) {
		cands := candidateSocketsFor("linux", "/home/u", 1000)
		found := false
		for _, c := range cands {
			if c.path == "/run/user/1000/podman/podman.sock" {
				found = true
			}
		}
		if !found {
			t.Error("rootless podman candidate missing for uid 1000")
		}
	})
}
