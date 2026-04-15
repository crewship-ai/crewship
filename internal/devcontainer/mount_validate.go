package devcontainer

import "strings"

// allowedMountSources is the allowlist of host paths that features are
// permitted to bind-mount into the runtime container. Restrictive by
// default — add entries only when a community feature genuinely needs them.
var allowedMountSources = map[string]bool{
	"/var/run/docker.sock":      true, // Docker-outside-of-Docker feature
	"/var/run/docker-host.sock": true, // alternate naming
	"/tmp":                      true, // tmpfs mounts
	"/dev/fuse":                 true, // FUSE-based features
}

// allowedMountPrefixes are prefix matches (more permissive — use sparingly).
var allowedMountPrefixes = []string{
	"/var/lib/docker/", // Docker storage access for DinD-style features
}

// IsAllowedMountSource returns true if the host path is safe to bind-mount
// into a runtime container per Crewship policy.
func IsAllowedMountSource(source string) bool {
	if source == "" {
		return false
	}
	// Exact matches
	if allowedMountSources[source] {
		return true
	}
	// Prefix matches
	for _, prefix := range allowedMountPrefixes {
		if strings.HasPrefix(source, prefix) {
			return true
		}
	}
	// Docker volumes (named volumes, not host paths) are always OK
	// — they start with an identifier, not a path separator.
	// Heuristic: no leading "/" = likely a volume name.
	if !strings.HasPrefix(source, "/") {
		return true
	}
	return false
}
