package docker

import "testing"

// These tests lock in the fix for finding C1 (CRITICAL) from the 2026-06
// security audit (.claude/context/SECURITY-AUDIT-2026-06.md): one crewshipd
// serves many workspaces against a single Docker daemon, but the container name
// and the persistent home/tools named volumes used to be keyed by crew *slug
// only* (docker.go:422-446). Crew slug is unique only per workspace
// (crews UNIQUE(workspace_id, slug)), so two tenants with a crew named the same
// value resolved to the SAME container + the SAME home/tools volumes —
// cross-tenant read of ~/.ssh / secrets / workspace, or cross-tenant DoS.
//
// The fix folds the globally-unique crew id (the same id used to scope the
// per-crew bind mounts) into every container/volume name. These tests assert
// that two tenants sharing a slug now get DISTINCT names, and would FAIL again
// if naming ever regressed to slug-only.

// twoTenantsSameSlug models two different workspaces ("acme", "globex") that
// each created a crew with the identical slug "backend" but distinct crew ids.
const (
	collidingSlug = "backend"
	acmeCrewID    = "ckacmebackend01"
	globexCrewID  = "ckglobexbacke02"
)

func TestTenantCollision_ContainerName_SecureTarget(t *testing.T) {
	p := &Provider{cfg: Config{ContainerPrefix: "crewship"}}

	acme := p.CrewContainerName(acmeCrewID, collidingSlug)
	globex := p.CrewContainerName(globexCrewID, collidingSlug)

	if acme == globex {
		t.Fatalf("C1 REGRESSION: two workspaces with crew slug %q collide on container name %q — names must be namespaced by crew id", collidingSlug, acme)
	}
	// Each name must still carry its own crew id so it is globally unique.
	if !containsSub(acme, acmeCrewID) {
		t.Errorf("acme container name %q must incorporate its crew id %q", acme, acmeCrewID)
	}
	if !containsSub(globex, globexCrewID) {
		t.Errorf("globex container name %q must incorporate its crew id %q", globex, globexCrewID)
	}
}

func TestTenantCollision_VolumeNames_SecureTarget(t *testing.T) {
	p := &Provider{cfg: Config{ContainerPrefix: "crewship"}}

	if p.homeVolumeName(acmeCrewID, collidingSlug) == p.homeVolumeName(globexCrewID, collidingSlug) {
		t.Fatalf("C1 REGRESSION: home volume names collide across workspaces sharing slug %q", collidingSlug)
	}
	if p.toolsVolumeName(acmeCrewID, collidingSlug) == p.toolsVolumeName(globexCrewID, collidingSlug) {
		t.Fatalf("C1 REGRESSION: tools volume names collide across workspaces sharing slug %q", collidingSlug)
	}

	// Sanity: naming is deterministic for a fixed (id, slug) so the
	// existing-container lookup and volume mounts keep resolving to one place.
	want := p.homeVolumeName(acmeCrewID, collidingSlug)
	if got := p.homeVolumeName(acmeCrewID, collidingSlug); got != want {
		t.Errorf("homeVolumeName must be deterministic for a fixed (id, slug): %q != %q", got, want)
	}
}

// TestTenantCollision_SecureTarget documents the required invariant directly:
// container/volume names incorporate the workspace-unique crew id, not the slug
// alone, so identically-named crews in different workspaces never collide.
func TestTenantCollision_SecureTarget(t *testing.T) {
	p := &Provider{cfg: Config{ContainerPrefix: "crewship"}}

	names := map[string]bool{}
	for _, id := range []string{acmeCrewID, globexCrewID} {
		for _, n := range []string{
			p.CrewContainerName(id, collidingSlug),
			p.homeVolumeName(id, collidingSlug),
			p.toolsVolumeName(id, collidingSlug),
		} {
			if names[n] {
				t.Fatalf("C1 REGRESSION: name %q is reused across tenants — not globally unique", n)
			}
			names[n] = true
		}
	}
}

func containsSub(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
