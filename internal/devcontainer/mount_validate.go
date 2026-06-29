package devcontainer

import "regexp"

// allowedMountSources is the allowlist of host paths that features are
// permitted to bind-mount into the runtime container. Restrictive by
// default — add entries only when a community feature genuinely needs them.
//
// SECURITY: the Docker socket (/var/run/docker.sock, /var/run/docker-host.sock)
// and the daemon storage dir (/var/lib/docker/) are deliberately NOT listed.
// Granting any of them to a non-privileged crew is a container-escape primitive:
// a process that can talk to the socket can launch a privileged container, mount
// the host root, and take over the box; the storage dir exposes every other
// tenant's image layers and volumes. Crewship has no operator "privileged crew"
// concept, so these are rejected outright. If one is ever introduced, gate these
// behind it explicitly rather than re-adding them to the default allowlist.
var allowedMountSources = map[string]bool{
	"/tmp":      true, // tmpfs mounts
	"/dev/fuse": true, // FUSE-based features
}

// volumeNameRE matches a valid Docker named volume: it must start with an
// alphanumeric character and may then contain alphanumerics, '_', '.' or '-'.
// This deliberately rejects path-like sources ("../../etc", "./secrets",
// "~root/.ssh", "C:\\Windows") that the old "no leading slash" heuristic let
// through as bogus "volume names".
var volumeNameRE = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]*$`)

// maxVolumeNameLen bounds named-volume sources to Docker's documented limit.
const maxVolumeNameLen = 255

// IsAllowedMountSource returns true if the host path is safe to bind-mount
// into a runtime container per Crewship policy.
func IsAllowedMountSource(source string) bool {
	if source == "" {
		return false
	}
	// Exact-match allowlist of safe host paths.
	if allowedMountSources[source] {
		return true
	}
	// Anything else that looks like a host path (leading "/") is rejected.
	if source[0] == '/' {
		return false
	}
	// Non-path sources are only accepted if they are syntactically valid
	// Docker named volumes — strict charset, bounded length. This blocks
	// relative-traversal, drive-letter, and home-relative forms that are not
	// real volume names.
	if len(source) > maxVolumeNameLen {
		return false
	}
	return volumeNameRE.MatchString(source)
}
