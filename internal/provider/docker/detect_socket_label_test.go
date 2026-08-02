package docker

import (
	"errors"
	"testing"
)

// Every runtime that offers to "set up the Docker socket for you" does the same
// thing: it points /var/run/docker.sock at its own. OrbStack does it from its
// privileged helper, Rancher Desktop does it when administrative access is on,
// Docker Desktop does it for ~/.docker/run/docker.sock. /var/run/docker.sock is
// candidate #1 and matches on path, so whichever of them wins the race is
// reported as plain "docker".
//
// Observed on this machine rather than reasoned about. OrbStack 2.2.1, before
// its privileged helper had been installed:
//
//	Detect OK: runtime=orbstack socket=/Users/…/.orbstack/run/docker.sock
//
// and after, with nothing else changed:
//
//	/var/run/docker.sock -> /Users/…/.orbstack/run/docker.sock  (root:daemon)
//	Detect OK: runtime=docker socket=/var/run/docker.sock
//
// The same product, two labels, decided by install state. That is not cosmetic:
// knownRuntimeGaps switches on DetectResult.Runtime, so a gap recorded against a
// runtime is silently not applied whenever that runtime happens to have taken
// the generic socket — and any gap that IS applied gets attributed to the wrong
// product in the log line the operator reads.
func TestRuntimeLabelFollowsTheSocketSymlink(t *testing.T) {
	t.Parallel()

	const (
		generic  = "/var/run/docker.sock"
		orbSock  = "/home/u/.orbstack/run/docker.sock"
		rdSock   = "/home/u/.rd/docker.sock"
		ddSock   = "/home/u/.docker/run/docker.sock"
		colSock  = "/home/u/.colima/default/docker.sock"
		unknown  = "/opt/something/else.sock"
		homeDir  = "/home/u"
		testUID  = 1000
		testGOOS = "linux"
	)
	all := candidateSocketsFor(testGOOS, homeDir, testUID)
	generic0 := all[0]
	if generic0.path != generic {
		t.Fatalf("expected %s to be candidate #1, got %s", generic, generic0.path)
	}

	resolveTo := func(target string) func(string) (string, error) {
		return func(p string) (string, error) {
			if p == generic {
				return target, nil
			}
			return p, nil
		}
	}

	for _, tc := range []struct {
		name   string
		target string
		want   string
	}{
		{"orbstack owns the generic socket", orbSock, "orbstack"},
		{"rancher owns the generic socket", rdSock, "rancher"},
		{"colima owns the generic socket", colSock, "colima"},
		// Docker Desktop's own path is already labelled docker, so following
		// the link must not change the answer.
		{"docker desktop owns the generic socket", ddSock, "docker"},
		// A link to somewhere we know nothing about must not invent a label.
		{"link to an unknown target", unknown, "docker"},
		// The overwhelmingly common case: not a symlink at all.
		{"plain socket, no link", generic, "docker"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveRuntimeLabel(generic0, all, resolveTo(tc.target))
			if got != tc.want {
				t.Errorf("resolveRuntimeLabel(%s -> %s) = %q, want %q", generic, tc.target, got, tc.want)
			}
		})
	}

	t.Run("an unresolvable path keeps the candidate's own label", func(t *testing.T) {
		fail := func(string) (string, error) { return "", errors.New("nope") }
		if got := resolveRuntimeLabel(generic0, all, fail); got != "docker" {
			t.Errorf("resolveRuntimeLabel with a failing resolver = %q, want docker", got)
		}
	})

	// A non-generic candidate answering on its own path must be left alone —
	// resolving must never demote a specific label to a vaguer one.
	t.Run("a specific candidate keeps its own label", func(t *testing.T) {
		var orb socketCandidate
		for _, c := range all {
			if c.runtime == "orbstack" {
				orb = c
			}
		}
		if orb.path == "" {
			t.Fatal("no orbstack candidate")
		}
		identity := func(p string) (string, error) { return p, nil }
		if got := resolveRuntimeLabel(orb, all, identity); got != "orbstack" {
			t.Errorf("resolveRuntimeLabel(orbstack candidate) = %q, want orbstack", got)
		}
	})
}
