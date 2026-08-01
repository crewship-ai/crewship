package memory

import (
	"fmt"
	"path"

	"github.com/crewship-ai/crewship/internal/safepath"
)

// The crew-shared memory tree has two addresses, and which one is
// correct depends entirely on which side of the bind mount the caller
// runs on:
//
//   - Inside a crew container it is ContainerCrewMemoryRoot, an absolute
//     path that only exists because the docker provider bind-mounts host
//     {Storage.BasePath}/crews/{crewID} at /crew
//     (internal/provider/docker/docker.go buildMounts, host dir prepared
//     by prepareCrewDirs in docker_container.go).
//   - On the host — where crewshipd, the consolidator, and every HTTP
//     handler actually run — it is HostCrewMemoryRoot(basePath, crewID).
//
// Writing the container literal from a host process does not fail: it
// silently creates /crew/... at the host filesystem root, outside every
// bind source, where no container will ever read it. That is exactly how
// the consolidator's pins.md and learned-*.md went missing (#1663). Both
// halves live in this file so the correspondence between them is one
// diff away from being checked, rather than two literals in two packages
// that nothing ties together.

// ContainerCrewMemoryRoot is where the crew-shared memory tree appears
// INSIDE a crew container. Only in-container path construction may use
// it — a host process wanting the same directory must call
// HostCrewMemoryRoot.
const ContainerCrewMemoryRoot = "/crew/shared/.memory"

// crewTopicsSubdir is the per-crew subdirectory the consolidator writes
// learned-*.md, pins.md and .proposed/ into. It is keyed by crew SLUG
// even though the enclosing bind source is already keyed by crew ID —
// redundant, but it is the path the prompt builder reads
// (orchestrator.buildPinsBlock), so it is load-bearing.
const crewTopicsSubdir = "topics"

// HostCrewMemoryRoot resolves the host directory that a crew container
// sees at ContainerCrewMemoryRoot. basePath is cfg.Storage.BasePath —
// the same value the docker provider receives as OutputBasePath.
//
// Returns an error rather than a best-effort path when basePath is empty
// or crewID is not a safe path component: a caller that cannot resolve
// the real directory must skip the write, not write somewhere else.
func HostCrewMemoryRoot(basePath, crewID string) (string, error) {
	if basePath == "" {
		return "", fmt.Errorf("crew memory root: storage base path not configured")
	}
	p, err := safepath.JoinUnder(basePath, "crews", crewID, "shared", ".memory")
	if err != nil {
		return "", fmt.Errorf("crew memory root: %w", err)
	}
	return p, nil
}

// HostCrewTopicsDir resolves the host directory the consolidator writes
// its per-crew output into — the twin of ContainerCrewTopicsDir.
func HostCrewTopicsDir(basePath, crewID, crewSlug string) (string, error) {
	root, err := HostCrewMemoryRoot(basePath, crewID)
	if err != nil {
		return "", err
	}
	p, err := safepath.JoinUnder(root, crewSlug, crewTopicsSubdir)
	if err != nil {
		return "", fmt.Errorf("crew topics dir: %w", err)
	}
	return p, nil
}

// ContainerCrewTopicsDir is the in-container address of the directory
// HostCrewTopicsDir resolves on the host. Used by the prompt builder,
// which reads through a container exec and therefore genuinely wants the
// container path.
//
// No validation: an unsafe slug can only produce a path this process
// hands to `cat` inside a container, and the host-side twin — which does
// touch the filesystem — rejects it there.
func ContainerCrewTopicsDir(crewSlug string) string {
	return path.Join(ContainerCrewMemoryRoot, crewSlug, crewTopicsSubdir)
}
