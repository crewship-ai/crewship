package devcontainer

import (
	"strings"
	"testing"
)

// These tests cover finding F3 from the 2026-06 security audit
// (.claude/context/SECURITY-AUDIT-2026-06.md): IsAllowedMountSource
// (internal/devcontainer/mount_validate.go) used to explicitly allow
// /var/run/docker.sock, /var/run/docker-host.sock and the /var/lib/docker/
// prefix, and auto-allowed *any* source that did not begin with "/" on the
// theory that it must be a named Docker volume.
//
// Bind-mounting the Docker socket (or the daemon's storage dir) into a
// non-privileged crew container is a container-escape primitive: a process
// that can talk to docker.sock can launch a new privileged container, mount
// the host root, and take over the box. The /var/lib/docker/ prefix granted
// read/write to every other tenant's layers and volumes. The non-"/" heuristic
// was also weak — a relative path like "../../etc" or a Windows-style "C:\..."
// slipped through as a "volume name".
//
// The fix removes the socket/storage entries from the default allowlist and
// replaces the non-"/" heuristic with strict Docker named-volume validation.
// These tests now assert the SECURE behavior and would fail if the vuln
// regressed.

// TestMount_DockerSock_Rejected asserts the Docker socket is no longer
// bind-mountable into a non-privileged crew.
func TestMount_DockerSock_Rejected(t *testing.T) {
	sockets := []string{
		"/var/run/docker.sock",
		"/var/run/docker-host.sock",
	}
	for _, src := range sockets {
		if IsAllowedMountSource(src) {
			t.Errorf("F3 regression: Docker socket %q is bind-mountable into a non-privileged crew (container-escape primitive)", src)
		}
	}
}

// TestMount_DockerStoragePrefix_Rejected asserts the daemon storage dir is no
// longer bind-mountable via a /var/lib/docker/ prefix match.
func TestMount_DockerStoragePrefix_Rejected(t *testing.T) {
	denied := []string{
		"/var/lib/docker/x",
		"/var/lib/docker/overlay2/foo",
	}
	for _, src := range denied {
		if IsAllowedMountSource(src) {
			t.Errorf("F3 regression: Docker storage path %q is bind-mountable (cross-tenant layer/volume access)", src)
		}
	}
}

// TestMount_NonSlashStrictVolumeName asserts the non-"/" branch now validates
// sources as real Docker named volumes: a legit volume name is accepted, while
// traversal-style, drive-letter, and relative/home-relative forms are rejected.
func TestMount_NonSlashStrictVolumeName(t *testing.T) {
	allowed := []string{
		"my-volume",
		"my_volume",
		"data.vol",
		"vol123",
	}
	for _, src := range allowed {
		if strings.HasPrefix(src, "/") {
			t.Fatalf("test bug: %q starts with / and does not exercise the heuristic", src)
		}
		if !IsAllowedMountSource(src) {
			t.Errorf("legit named volume %q must be accepted", src)
		}
	}

	denied := []string{
		"../../etc",  // relative traversal — not a volume name
		`C:\Windows`, // drive-letter path — not a volume name
		"./secrets",  // relative path
		"~root/.ssh", // home-relative path
		".hidden",    // leading dot is not a valid volume start char
		"-rf",        // leading dash is not a valid volume start char
	}
	for _, src := range denied {
		if strings.HasPrefix(src, "/") {
			t.Fatalf("test bug: %q starts with / and does not exercise the heuristic", src)
		}
		if IsAllowedMountSource(src) {
			t.Errorf("F3 regression: non-volume source %q auto-allowed by the non-slash branch", src)
		}
	}
}

// TestMount_SafePaths_StillAllowed is a positive regression check that the
// remaining intentionally-allowed host paths still pass.
func TestMount_SafePaths_StillAllowed(t *testing.T) {
	for _, src := range []string{"/tmp", "/dev/fuse"} {
		if !IsAllowedMountSource(src) {
			t.Errorf("expected safe path %q to remain allowed", src)
		}
	}
	if IsAllowedMountSource("") {
		t.Error("empty source must be rejected")
	}
}
