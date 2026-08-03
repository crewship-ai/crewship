package memory

import (
	"path"
	"path/filepath"
	"strings"
	"testing"
)

// TestHostAndContainerCrewTopicsDirsAgree is the anti-drift check that
// makes the pair in crewpaths.go worth having: the two functions must
// name the SAME directory viewed from the two sides of the /crew bind.
// Concretely — the container path's offset from ContainerCrewMemoryRoot
// must equal the host path's offset from HostCrewMemoryRoot.
//
// If someone changes one side's layout (drops the crew-slug segment,
// renames topics/), this fails rather than silently reintroducing #1663:
// the consolidator writing one place and buildPinsBlock reading another.
func TestHostAndContainerCrewTopicsDirsAgree(t *testing.T) {
	const (
		basePath = "/var/lib/crewship/output"
		crewID   = "ckcrew_123"
		crewSlug = "alpha-crew"
	)

	hostRoot, err := HostCrewMemoryRoot(basePath, crewID)
	if err != nil {
		t.Fatalf("HostCrewMemoryRoot: %v", err)
	}
	hostTopics, err := HostCrewTopicsDir(basePath, crewID, crewSlug)
	if err != nil {
		t.Fatalf("HostCrewTopicsDir: %v", err)
	}

	hostRel, err := filepath.Rel(hostRoot, hostTopics)
	if err != nil {
		t.Fatalf("filepath.Rel(%q, %q): %v", hostRoot, hostTopics, err)
	}
	containerRel := strings.TrimPrefix(
		ContainerCrewTopicsDir(crewSlug),
		ContainerCrewMemoryRoot+"/",
	)

	if filepath.ToSlash(hostRel) != containerRel {
		t.Fatalf(`host and container crew topics dirs disagree:
  host      %q is %q under %q
  container %q is %q under %q
The consolidator writes the host side and buildPinsBlock reads the container side;
if the two offsets differ the file is written where nothing reads it (#1663).`,
			hostTopics, hostRel, hostRoot,
			ContainerCrewTopicsDir(crewSlug), containerRel, ContainerCrewMemoryRoot)
	}
}

// TestHostCrewMemoryRoot_MatchesTheDockerBindSource pins the host half
// against the layout the docker provider actually creates:
// {OutputBasePath}/crews/{crewID} is bind-mounted at /crew, so
// /crew/shared/.memory is {OutputBasePath}/crews/{crewID}/shared/.memory.
func TestHostCrewMemoryRoot_MatchesTheDockerBindSource(t *testing.T) {
	const (
		basePath = "/var/lib/crewship/output"
		crewID   = "ckcrew_123"
	)
	// The bind source prepareCrewDirs creates, joined with the part of
	// ContainerCrewMemoryRoot that sits below the /crew mount point.
	bindSource := filepath.Join(basePath, "crews", crewID)
	below := strings.TrimPrefix(ContainerCrewMemoryRoot, "/crew/")
	want := filepath.Join(bindSource, filepath.FromSlash(below))

	got, err := HostCrewMemoryRoot(basePath, crewID)
	if err != nil {
		t.Fatalf("HostCrewMemoryRoot: %v", err)
	}
	if got != want {
		t.Fatalf("HostCrewMemoryRoot(%q, %q) = %q, want %q (the /crew bind source)", basePath, crewID, got, want)
	}
}

// TestHostCrewPaths_RejectUnsafeInputs: a crew id or slug that is not a
// safe path component must fail rather than escape the base. The DB
// enforces slug uniqueness, not filesystem safety, so this layer owns it.
func TestHostCrewPaths_RejectUnsafeInputs(t *testing.T) {
	const basePath = "/var/lib/crewship/output"

	cases := []struct {
		name     string
		basePath string
		crewID   string
		crewSlug string
	}{
		{"empty base path", "", "ckcrew_1", "alpha"},
		{"empty crew id", basePath, "", "alpha"},
		{"crew id traversal", basePath, "..", "alpha"},
		{"crew id separator", basePath, "a/b", "alpha"},
		{"empty slug", basePath, "ckcrew_1", ""},
		{"slug traversal", basePath, "ckcrew_1", ".."},
		{"slug separator", basePath, "ckcrew_1", "a/b"},
		{"slug absolute", basePath, "ckcrew_1", "/etc"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := HostCrewTopicsDir(tc.basePath, tc.crewID, tc.crewSlug)
			if err == nil {
				t.Fatalf("HostCrewTopicsDir(%q, %q, %q) = %q, want an error",
					tc.basePath, tc.crewID, tc.crewSlug, got)
			}
			if got != "" {
				t.Errorf("rejected input still returned a path %q — callers may use it", got)
			}
		})
	}
}

// TestContainerCrewMemoryRoot_IsTheMountedPath guards the literal the
// prompt builder depends on. It is not free to change: buildPinsBlock
// cats it inside a live container.
func TestContainerCrewMemoryRoot_IsTheMountedPath(t *testing.T) {
	if ContainerCrewMemoryRoot != "/crew/shared/.memory" {
		t.Fatalf("ContainerCrewMemoryRoot = %q — the crew bind is mounted at /crew and the shared tree lives at shared/.memory under it", ContainerCrewMemoryRoot)
	}
	if got, want := ContainerCrewTopicsDir("alpha-crew"), path.Join("/crew/shared/.memory", "alpha-crew", "topics"); got != want {
		t.Fatalf("ContainerCrewTopicsDir = %q, want %q", got, want)
	}
}

// The agent tier has the same two-addresses problem the crew tier has:
// orchestrator_run.go hands the sidecar "/crew/agents/<slug>/.memory"
// as BasePath, and a host process must resolve the bind SOURCE of that
// exact path. This pins the correspondence so a layout change on either
// side fails here instead of writing memory nobody reads.
func TestHostAgentMemoryRoot_MatchesTheContainerBasePath(t *testing.T) {
	const (
		basePath  = "/var/lib/crewship/output"
		crewID    = "ckcrew_123"
		agentSlug = "alex"
	)

	host, err := HostAgentMemoryRoot(basePath, crewID, agentSlug)
	if err != nil {
		t.Fatalf("HostAgentMemoryRoot: %v", err)
	}

	// The container sees {basePath}/crews/{crewID} at /crew, so the
	// host path must be that bind source plus the container-relative
	// remainder of the agent's BasePath.
	containerBasePath := path.Join("/crew", "agents", agentSlug, ".memory")
	rel := strings.TrimPrefix(containerBasePath, "/crew/")
	want := filepath.Join(basePath, "crews", crewID, filepath.FromSlash(rel))
	if host != want {
		t.Errorf("HostAgentMemoryRoot = %q, want %q (bind source of %q)", host, want, containerBasePath)
	}
}

func TestHostAgentMemoryRoot_RejectsUnsafeInputs(t *testing.T) {
	tests := []struct {
		name              string
		basePath          string
		crewID, agentSlug string
	}{
		{"empty base path", "", "ckcrew_1", "alex"},
		{"traversal in crew id", "/var/lib/crewship", "../../etc", "alex"},
		{"traversal in agent slug", "/var/lib/crewship", "ckcrew_1", ".."},
		{"separator in agent slug", "/var/lib/crewship", "ckcrew_1", "a/b"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got, err := HostAgentMemoryRoot(tt.basePath, tt.crewID, tt.agentSlug); err == nil {
				t.Errorf("HostAgentMemoryRoot() = %q, want an error", got)
			}
		})
	}
}
