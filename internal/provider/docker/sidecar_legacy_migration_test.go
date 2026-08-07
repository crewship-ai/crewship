package docker

// The upgrade half of #1732.
//
// Re-keying sidecars onto id-scoped names is only half a fix: every install
// that already ran has sidecars under the pre-#1732 slug-only names. Leaving
// them alone would be the worse bug — the legacy postgres keeps the `postgres`
// alias on the crew bridge next to the new one (the agent's DNS lookup
// round-robins between its real database and an empty one), and the legacy
// data volume ends up with nothing referencing it, which is the same data loss
// as a delete by a slower route.
//
// So the first start after upgrade migrates: legacy container removed (the
// ensure loop recreates it under the id-scoped name moments later), legacy
// volume DATA copied into the id-scoped volume and only then pruned. These
// tests pin all of it, including the cases where it must refuse.

import (
	"context"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/provider"
)

func legacyPostgresSvc() provider.CrewService {
	return provider.CrewService{
		Name:    "postgres",
		Image:   "postgres:16",
		Volumes: []provider.CrewServiceVolume{{Name: "pgdata", Mount: "/var/lib/postgresql/data"}},
	}
}

// A crew upgrading across #1732 keeps its database: the legacy container is
// replaced by an id-scoped one, and the legacy volume's data is copied into the
// id-scoped volume before the legacy volume goes.
func TestLegacySidecarMigration_MovesTheDataAndDropsTheLegacyContainer(t *testing.T) {
	t.Parallel()

	d := &fakeSidecarDaemon{}
	d.containers = []fakeSidecarContainer{{
		ID: "legacy-pg", Name: "crewship-svc-data-crew-postgres", Image: "postgres:16", State: "running",
		Labels: map[string]string{"crewship.crew": "data-crew", "crewship.kind": "sidecar", "crewship.svc": "postgres"},
	}}
	d.volumes = []fakeSidecarVolume{{Name: "crewship-svc-data-crew-vol-pgdata"}}
	p := newCovProvider(t, Config{}, d.handler(t))

	if _, err := p.EnsureCrewServices(context.Background(), provider.CrewConfig{
		ID: tenantACrewID, Slug: sidecarCollidingSlug,
		Services: []provider.CrewService{legacyPostgresSvc()},
	}); err != nil {
		t.Fatalf("EnsureCrewServices: %v", err)
	}

	names, vols, _, _ := d.snapshot()
	for _, n := range names {
		if n == "crewship-svc-data-crew-postgres" {
			t.Error("the legacy slug-only postgres is still running next to the id-scoped one — both hold " +
				"the `postgres` alias on the crew bridge, so the agent round-robins between its real " +
				"database and an empty one")
		}
	}
	wantContainer := "crewship-svc-data-crew-" + tenantACrewID + "-postgres"
	if !containsString(names, wantContainer) {
		t.Errorf("containers = %v, want the id-scoped %q", names, wantContainer)
	}

	wantVolume := "crewship-svc-data-crew-" + tenantACrewID + "-vol-pgdata"
	if !containsString(vols, wantVolume) {
		t.Errorf("volumes = %v, want the id-scoped %q", vols, wantVolume)
	}
	if containsString(vols, "crewship-svc-data-crew-vol-pgdata") {
		t.Error("the legacy volume survived the migration — it is now unreferenced, which strands the data")
	}
	d.mu.Lock()
	removed := append([]string(nil), d.removedVolumes...)
	mounts := append([]string(nil), d.mountSources...)
	d.mu.Unlock()
	if !containsString(removed, "crewship-svc-data-crew-vol-pgdata") {
		t.Errorf("legacy volume was never pruned: removed = %v", removed)
	}
	// The recreated sidecar must mount the migrated volume, not some third name.
	if !containsString(mounts, wantVolume) {
		t.Errorf("the recreated postgres mounts %v, want %q", mounts, wantVolume)
	}
	// And the migrated volume must carry the ownership label, or the teardown
	// that selects on it would never find the volume it just created.
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, v := range d.volumes {
		if v.Name == wantVolume && v.Labels["crewship.crew-id"] != tenantACrewID {
			t.Errorf("migrated volume labels = %v, want crewship.crew-id=%s — an unlabelled volume is "+
				"invisible to the label-keyed teardown", v.Labels, tenantACrewID)
		}
	}
}

// A container whose NAME looks legacy but which carries a crew-id label is
// another crew's LIVE id-scoped sidecar, and must not be touched.
//
// This is not hypothetical: slugs may contain hyphens, so crew "data-crew-<id>"
// produces the legacy name "crewship-svc-data-crew-<id>-postgres", which is
// byte-identical to crew "data-crew"/id "<id>"'s id-scoped container.
func TestLegacySidecarMigration_LeavesAnIDScopedContainerAlone(t *testing.T) {
	t.Parallel()

	victim := "crewship-svc-data-crew-" + tenantBCrewID + "-postgres"
	d := &fakeSidecarDaemon{}
	d.containers = []fakeSidecarContainer{{
		ID: "live-pg", Name: victim, Image: "postgres:16", State: "running",
		Labels: map[string]string{
			"crewship.crew-id": tenantBCrewID, "crewship.crew": sidecarCollidingSlug,
			"crewship.kind": "sidecar", "crewship.svc": "postgres",
		},
	}}
	p := newCovProvider(t, Config{}, d.handler(t))

	// A crew whose slug is literally "data-crew-<tenantBCrewID>": its legacy
	// container name collides exactly with tenant B's live id-scoped one.
	if _, err := p.EnsureCrewServices(context.Background(), provider.CrewConfig{
		ID: "cknamespoof0001", Slug: sidecarCollidingSlug + "-" + tenantBCrewID,
		Services: []provider.CrewService{{Name: "postgres", Image: "postgres:16"}},
	}); err != nil {
		t.Fatalf("EnsureCrewServices: %v", err)
	}

	names, _, _, _ := d.snapshot()
	if !containsString(names, victim) {
		t.Errorf("another crew's LIVE id-scoped postgres %q was removed as if it were a legacy resource — "+
			"slugs may contain hyphens, so the name alone can never prove a container is legacy", victim)
	}
}

// The volume-side twin of the case above, and the one that actually destroys
// data: crew "data-crew-<idB>"'s LEGACY volume name is byte-identical to crew
// "data-crew"/id <idB>'s LIVE id-scoped volume. Migrating on the name alone
// would copy another tenant's database out from under it and then prune it.
// The crewship.crew-id label is the only thing that tells the two apart.
func TestLegacySidecarMigration_LeavesAnIDScopedVolumeAlone(t *testing.T) {
	t.Parallel()

	victim := "crewship-svc-data-crew-" + tenantBCrewID + "-vol-pgdata"
	d := &fakeSidecarDaemon{}
	d.volumes = []fakeSidecarVolume{{
		Name:   victim,
		Labels: map[string]string{"crewship.crew-id": tenantBCrewID, "crewship.kind": "sidecar-volume"},
	}}
	p := newCovProvider(t, Config{}, d.handler(t))

	if _, err := p.EnsureCrewServices(context.Background(), provider.CrewConfig{
		ID: "cknamespoof0002", Slug: sidecarCollidingSlug + "-" + tenantBCrewID,
		Services: []provider.CrewService{legacyPostgresSvc()},
	}); err != nil {
		t.Fatalf("EnsureCrewServices: %v", err)
	}

	_, vols, _, _ := d.snapshot()
	if !containsString(vols, victim) {
		t.Errorf("another crew's LIVE id-scoped data volume %q was migrated away and pruned as if it "+
			"were legacy — that is the tenant's database, moved into a crew that does not own it", victim)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if containsString(d.removedVolumes, victim) {
		t.Errorf("removed volumes = %v, must not include another crew's live volume", d.removedVolumes)
	}
}

// A legacy volume whose id-scoped target already exists is left in place rather
// than clobbering the id-scoped data. Same policy as the C1 crew migration.
func TestLegacySidecarMigration_DoesNotClobberAnExistingIDScopedVolume(t *testing.T) {
	t.Parallel()

	target := "crewship-svc-data-crew-" + tenantACrewID + "-vol-pgdata"
	d := &fakeSidecarDaemon{}
	d.volumes = []fakeSidecarVolume{
		{Name: "crewship-svc-data-crew-vol-pgdata"},
		{Name: target, Labels: map[string]string{"crewship.crew-id": tenantACrewID, "crewship.kind": "sidecar-volume"}},
	}
	p := newCovProvider(t, Config{}, d.handler(t))

	if _, err := p.EnsureCrewServices(context.Background(), provider.CrewConfig{
		ID: tenantACrewID, Slug: sidecarCollidingSlug,
		Services: []provider.CrewService{legacyPostgresSvc()},
	}); err != nil {
		t.Fatalf("EnsureCrewServices: %v", err)
	}

	_, vols, _, _ := d.snapshot()
	if !containsString(vols, "crewship-svc-data-crew-vol-pgdata") {
		t.Error("the legacy volume was removed even though the id-scoped target already held data — " +
			"an ambiguous migration must leave the operator both copies, not pick one")
	}
	if !containsString(vols, target) {
		t.Errorf("the id-scoped volume %q was clobbered by the legacy one", target)
	}
}

// If the daemon cannot enumerate volumes we cannot tell whether an unmigrated
// legacy volume is sitting there, and starting anyway would create a fresh
// empty id-scoped volume that strands it behind an authoritative-looking target
// no later start re-migrates. Fail closed, remove nothing.
func TestLegacySidecarMigration_FailsClosedWhenVolumesCannotBeListed(t *testing.T) {
	t.Parallel()

	d := &fakeSidecarDaemon{volumeErr: true}
	d.containers = []fakeSidecarContainer{{
		ID: "legacy-pg", Name: "crewship-svc-data-crew-postgres", Image: "postgres:16", State: "running",
		Labels: map[string]string{"crewship.crew": "data-crew", "crewship.kind": "sidecar"},
	}}
	p := newCovProvider(t, Config{}, d.handler(t))

	_, err := p.EnsureCrewServices(context.Background(), provider.CrewConfig{
		ID: tenantACrewID, Slug: sidecarCollidingSlug,
		Services: []provider.CrewService{legacyPostgresSvc()},
	})
	if err == nil {
		t.Fatal("expected the crew's services to fail to start rather than strand a legacy volume")
	}
	if !strings.Contains(err.Error(), "list volumes") {
		t.Errorf("error should name the volume enumeration failure: %v", err)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.removedVolumes) != 0 {
		t.Errorf("a fail-closed migration removed volumes anyway: %v", d.removedVolumes)
	}
	if len(d.removedIDs) != 0 {
		t.Errorf("a fail-closed migration removed containers anyway: %v", d.removedIDs)
	}
}

// A copy that does not complete must leave the legacy volume untouched. Data
// loss is the cardinal sin: an operator can retry a failed start, they cannot
// retry a deleted database.
func TestLegacySidecarMigration_KeepsLegacyVolumeWhenTheCopyFails(t *testing.T) {
	t.Parallel()

	d := &fakeSidecarDaemon{waitExit: 1}
	d.volumes = []fakeSidecarVolume{{Name: "crewship-svc-data-crew-vol-pgdata"}}
	p := newCovProvider(t, Config{}, d.handler(t))

	_, err := p.EnsureCrewServices(context.Background(), provider.CrewConfig{
		ID: tenantACrewID, Slug: sidecarCollidingSlug,
		Services: []provider.CrewService{legacyPostgresSvc()},
	})
	if err == nil {
		t.Fatal("expected an error when the volume copy fails")
	}

	_, vols, _, _ := d.snapshot()
	if !containsString(vols, "crewship-svc-data-crew-vol-pgdata") {
		t.Error("the legacy volume was pruned after a copy that did not complete — its data is gone")
	}
}

// The prune has to happen with nothing left holding the legacy volume.
//
// Found live, not by review: on OrbStack 29.4.0 the #1732 migration copied the
// data correctly and then failed to prune the source, twice out of two runs —
//
//	WARN C1 migration copied volume data but failed to prune legacy volume
//	     legacy_volume=cslive-svc-legacy-crew-vol-pg-data
//	     error="remove cslive-svc-legacy-crew-vol-pg-data: volume is in use - [3319acab4691…]"
//
// The container named in that error is the migration's OWN copy helper. It
// mounts the legacy volume at /from, and its removal is a `defer`, so it does
// not run until migrateLegacyVolumeLabeled returns — i.e. after the prune. A
// real daemon refuses to remove a volume any container still references, force
// flag or not, so the prune could never succeed: every upgraded install leaks
// its legacy volume, deterministically.
//
// It passed on the fake because the fake removed volumes unconditionally.
// refcountVolumes makes it behave like the daemon that caught this.
func TestLegacySidecarMigration_PrunesTheLegacyVolumeOnARefcountingDaemon(t *testing.T) {
	t.Parallel()

	d := &fakeSidecarDaemon{refcountVolumes: true}
	d.volumes = []fakeSidecarVolume{{Name: "crewship-svc-data-crew-vol-pgdata"}}
	p := newCovProvider(t, Config{}, d.handler(t))

	if _, err := p.EnsureCrewServices(context.Background(), provider.CrewConfig{
		ID: tenantACrewID, Slug: sidecarCollidingSlug,
		Services: []provider.CrewService{legacyPostgresSvc()},
	}); err != nil {
		t.Fatalf("EnsureCrewServices: %v", err)
	}

	_, vols, _, _ := d.snapshot()
	if containsString(vols, "crewship-svc-data-crew-vol-pgdata") {
		t.Error("the legacy volume survived the migration: the copy helper still mounted it when the prune " +
			"ran, so the daemon refused. The data was copied, but every upgraded install is left with an " +
			"unreferenced legacy volume — and a second crew sharing the slug will migrate that stale copy again")
	}

	// And the target must still be the authoritative, correctly-labelled one:
	// a fix that pruned by skipping the copy would be worse than the leak.
	want := "crewship-svc-data-crew-" + tenantACrewID + "-vol-pgdata"
	if !containsString(vols, want) {
		t.Errorf("volumes = %v, want the migrated %q", vols, want)
	}
}

// No legacy resources on the daemon: the migration must not remove anything, and
// must not stop a normal start.
func TestLegacySidecarMigration_NoOpOnAFreshDaemon(t *testing.T) {
	t.Parallel()

	d := &fakeSidecarDaemon{}
	p := newCovProvider(t, Config{}, d.handler(t))

	if _, err := p.EnsureCrewServices(context.Background(), provider.CrewConfig{
		ID: tenantACrewID, Slug: sidecarCollidingSlug,
		Services: []provider.CrewService{legacyPostgresSvc()},
	}); err != nil {
		t.Fatalf("EnsureCrewServices: %v", err)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.removedVolumes) != 0 || len(d.removedIDs) != 0 {
		t.Errorf("fresh daemon: removed volumes %v / containers %v", d.removedVolumes, d.removedIDs)
	}
}

func containsString(hay []string, needle string) bool {
	for _, s := range hay {
		if s == needle {
			return true
		}
	}
	return false
}
