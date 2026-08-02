package docker

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// containerd does not speak the Docker REST API, and no version of it ever
// will: it serves its own gRPC API over HTTP/2 while the moby client this
// provider is built on issues HTTP/1.1 requests. Offering
// /run/containerd/containerd.sock as a Docker-API candidate therefore
// advertises a runtime the product cannot possibly drive.
//
// Measured, not reasoned about. Against containerd v2.3.3 running alone in a
// Linux container (no dockerd, socket present at /run/containerd/containerd.sock,
// probe running as root):
//
//	Detect FAILED after 1.93ms: no Docker-compatible runtime found
//	  (tried Docker, Podman, Colima, OrbStack, Rancher Desktop)
//
// and dialling that socket directly with the moby client returns
//
//	malformed HTTP response "\x00\x00\x06\x04\x00\x00\x00\x00\x00\x00\x05\x00\x00@\x00"
//
// which is an HTTP/2 SETTINGS frame — containerd answering a REST request with
// the start of a gRPC handshake. Confirmed a second time against Rancher
// Desktop 1.24.0 in containerd mode (containerd v2.3.2, nerdctl v2.2.2 both
// working) on macOS 26.
//
// So the candidate is removed, and the failure message is made to say what it
// found instead of reporting a bare "nothing here" on a host that visibly has a
// container runtime installed.
func TestContainerdIsNotOfferedAsADockerAPICandidate(t *testing.T) {
	t.Parallel()

	for _, goos := range []string{"linux", "darwin", "windows"} {
		for _, uid := range []int{-1, 1000} {
			for _, c := range candidateSocketsFor(goos, "/home/u", uid) {
				if strings.Contains(c.path, "containerd") {
					t.Errorf("goos=%s uid=%d offers containerd socket %q (labelled %q) as a Docker-API candidate; "+
						"containerd serves gRPC over HTTP/2 and can never answer the moby client's HTTP/1.1 GET /_ping",
						goos, uid, c.path, c.runtime)
				}
			}
		}
	}
}

// existingContainerdSockets is what turns the failure message from a guess into
// an observation, so it has to report the paths that are really there and only
// those. A real unix socket is used rather than a plain file because that is
// what it will be asked about in production.
func TestExistingContainerdSockets(t *testing.T) {
	t.Parallel()

	// Not t.TempDir(): on macOS that is a ~70-character path under
	// /var/folders, and sockaddr_un.sun_path caps at 104 bytes, so binding
	// there fails with EINVAL and the test would report a defect that belongs
	// to the temp directory rather than to the code under test.
	dir, err := os.MkdirTemp("/tmp", "cs-ctrd")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	live := filepath.Join(dir, "containerd.sock")
	ln, err := net.Listen("unix", live)
	if err != nil {
		t.Fatalf("listen on %s: %v", live, err)
	}
	defer ln.Close()
	absent := filepath.Join(dir, "not-there.sock")

	if got := existingContainerdSockets([]string{absent}); len(got) != 0 {
		t.Errorf("existingContainerdSockets(absent) = %v, want none", got)
	}
	got := existingContainerdSockets([]string{absent, live})
	if len(got) != 1 || got[0] != live {
		t.Errorf("existingContainerdSockets = %v, want exactly [%s]", got, live)
	}

	// One socket must be reported once, however many paths reach it. This is
	// not hypothetical: on the Debian-family images containerd ships in,
	// /var/run is a symlink to /run, so both entries of containerdSocketPaths
	// stat the same inode. Measured — the first cut of this diagnostic said
	// "/run/containerd/containerd.sock, /var/run/containerd/containerd.sock is
	// present", which reads as two runtimes and is wrong about the machine.
	linkDir := filepath.Join(dir, "var-run")
	if err := os.Symlink(dir, linkDir); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	viaLink := filepath.Join(linkDir, "containerd.sock")
	got = existingContainerdSockets([]string{live, viaLink})
	if len(got) != 1 || got[0] != live {
		t.Errorf("existingContainerdSockets(same socket via two paths) = %v, want exactly [%s]", got, live)
	}
}

// The whole point of the diagnostic is the sentence an operator reads when
// nothing answered. Asserted for what it has to be worth to them — the path
// they can go look at, the reason it is useless, and a remedy they can act on —
// rather than for containing some marker substring.
func TestNoRuntimeErrorExplainsContainerd(t *testing.T) {
	t.Parallel()

	t.Run("no containerd present: no claim about it", func(t *testing.T) {
		msg := noRuntimeError(nil).Error()
		if strings.Contains(strings.ToLower(msg), "containerd") {
			t.Errorf("message mentions containerd on a host with none: %q", msg)
		}
	})

	t.Run("containerd present: names it, explains it, offers a way out", func(t *testing.T) {
		const sock = "/run/containerd/containerd.sock"
		msg := noRuntimeError([]string{sock}).Error()

		if !strings.Contains(msg, sock) {
			t.Errorf("message does not name the socket it found (%s): %q", sock, msg)
		}
		// The reason has to be the protocol, because "it did not answer" sends
		// the reader off to restart containerd — which is exactly the advice
		// internal/preflight gives today, and it can never work.
		if !strings.Contains(msg, "gRPC") {
			t.Errorf("message does not say containerd speaks gRPC, so the reader will try to restart it: %q", msg)
		}
		// It must be stated as impossible, not as a transient failure.
		if !strings.Contains(msg, "cannot") {
			t.Errorf("message does not say Crewship cannot drive containerd: %q", msg)
		}
		// A dead end with no exit is not a useful diagnostic. At least one of
		// the three real remedies has to be named.
		remedies := []string{"Docker Engine", "podman system service", "dockerd"}
		found := false
		for _, r := range remedies {
			if strings.Contains(msg, r) {
				found = true
			}
		}
		if !found {
			t.Errorf("message names none of the remedies %v: %q", remedies, msg)
		}
	})
}

// Two genuinely distinct containerd sockets survive de-duplication, and the
// sentence has to stay readable when they do — "a, b is present" reads as a
// single mangled path rather than as two endpoints.
func TestNoRuntimeErrorReadsCorrectlyForTwoSockets(t *testing.T) {
	t.Parallel()

	msg := noRuntimeError([]string{"/run/containerd/containerd.sock", "/opt/ctr/containerd.sock"}).Error()
	if !strings.Contains(msg, "are present") {
		t.Errorf("two sockets are described in the singular: %q", msg)
	}
	if strings.Contains(msg, "is present") {
		t.Errorf("two sockets are described in the singular: %q", msg)
	}
	one := noRuntimeError([]string{"/run/containerd/containerd.sock"}).Error()
	if !strings.Contains(one, "is present") {
		t.Errorf("one socket is described in the plural: %q", one)
	}
}

// DOCKER_HOST is the one way a user can deliberately point Crewship at
// containerd, and it is exactly what somebody following a nerdctl blog post
// will try. Detect short-circuits on it, so the candidate-list diagnostic never
// runs and the raw client error is all they get:
//
//	malformed HTTP response "\x00\x00\x06\x04\x00\x00\x00\x00\x00\x00\x05\x00\x00@\x00"
//
// which says nothing anyone can act on.
func TestContainerdHostHint(t *testing.T) {
	t.Parallel()

	for _, host := range []string{
		"unix:///run/containerd/containerd.sock",
		"unix:///Users/u/.colima/default/containerd.sock",
		"unix:///var/run/containerd/containerd.sock",
	} {
		hint := containerdHostHint(host)
		if hint == "" {
			t.Errorf("no hint for containerd endpoint %q", host)
			continue
		}
		if !strings.Contains(hint, "gRPC") {
			t.Errorf("hint for %q does not name the protocol mismatch: %q", host, hint)
		}
		if !strings.Contains(hint, "cannot") {
			t.Errorf("hint for %q does not say it cannot work: %q", host, hint)
		}
	}

	// A hint on an ordinary Docker endpoint would be a lie, and would attach
	// itself to every unrelated connection failure.
	for _, host := range []string{
		"unix:///var/run/docker.sock",
		"unix:///Users/u/.rd/docker.sock",
		"tcp://10.0.0.5:2375",
		"npipe:////./pipe/docker_engine",
		"",
	} {
		if hint := containerdHostHint(host); hint != "" {
			t.Errorf("containerdHostHint(%q) = %q, want none", host, hint)
		}
	}
}

// The production paths must be the ones containerd actually listens on, or the
// diagnostic never fires where it matters. Pinned as values because a typo here
// is invisible: the check simply never matches and the message silently reverts
// to the unhelpful one.
func TestContainerdSocketPathsAreTheCanonicalOnes(t *testing.T) {
	t.Parallel()

	want := map[string]bool{
		"/run/containerd/containerd.sock":     false,
		"/var/run/containerd/containerd.sock": false,
	}
	for _, p := range containerdSocketPaths {
		if _, ok := want[p]; !ok {
			continue
		}
		want[p] = true
	}
	for p, seen := range want {
		if !seen {
			t.Errorf("containerdSocketPaths does not include %s", p)
		}
	}
	for _, p := range containerdSocketPaths {
		if !filepath.IsAbs(p) {
			t.Errorf("containerd socket path is not absolute: %q", p)
		}
		if _, err := os.Stat(p); err == nil && !strings.Contains(p, "containerd") {
			t.Errorf("path %q does not look like a containerd socket", p)
		}
	}
}
