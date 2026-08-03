package api

// Deleting a crew must not delete another tenant's database.
//
// crews is UNIQUE(workspace_id, slug) — slugs are unique PER WORKSPACE. Sidecar
// containers are matched by the label `crewship.crew=<slug>` and their volumes
// by the name prefix `<prefix>-svc-<slug>-vol-`, and NEITHER carries the crew
// id. CrewContainerName does carry it, deliberately, "to prevent cross-tenant
// container collisions — audit C1"; the sidecars never got that treatment.
//
// So workspace A deleting its `data-crew` force-removes workspace B's
// `data-crew` postgres and deletes its data directory, and the confirmation
// prompt — which names only the caller's own crew — says nothing.
//
// The fake here models what a slug-keyed daemon actually does: removal by slug
// takes every crew's resources under that slug. The assertion is therefore
// about the OTHER tenant's resources still existing, not about which functions
// were called — a test that deleted one crew and checked its own volumes were
// gone would pass on the broken code.

import (
	"context"
	"database/sql"
	"io"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/crewship-ai/crewship/internal/provider"
)

// slugKeyedDaemon is a container provider whose sidecar teardown behaves like
// the real one: it removes by SLUG, so anything any crew owns under that slug
// goes. Resources are held per (slug, owner) so a test can ask what survived.
type slugKeyedDaemon struct {
	mu sync.Mutex
	// containers/volumes are keyed by slug → owner label ("ws-a", "ws-b").
	containers map[string][]string
	volumes    map[string][]string
}

func newSlugKeyedDaemon() *slugKeyedDaemon {
	return &slugKeyedDaemon{
		containers: map[string][]string{},
		volumes:    map[string][]string{},
	}
}

func (d *slugKeyedDaemon) start(slug, owner string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.containers[slug] = append(d.containers[slug], owner)
	d.volumes[slug] = append(d.volumes[slug], owner)
}

func (d *slugKeyedDaemon) survivingContainers(slug string) []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := append([]string(nil), d.containers[slug]...)
	return out
}

func (d *slugKeyedDaemon) survivingVolumes(slug string) []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := append([]string(nil), d.volumes[slug]...)
	return out
}

func (d *slugKeyedDaemon) EnsureCrewRuntime(context.Context, provider.CrewConfig) (string, error) {
	return "cid", nil
}
func (d *slugKeyedDaemon) StopCrewRuntime(context.Context, string) error   { return nil }
func (d *slugKeyedDaemon) RemoveCrewRuntime(context.Context, string) error { return nil }
func (d *slugKeyedDaemon) ContainerStatus(context.Context, string) (*provider.ContainerStatus, error) {
	return &provider.ContainerStatus{State: "running"}, nil
}
func (d *slugKeyedDaemon) ContainerStats(context.Context, string) (*provider.ContainerMetrics, error) {
	return nil, nil
}
func (d *slugKeyedDaemon) Exec(context.Context, provider.ExecConfig) (*provider.ExecResult, error) {
	return &provider.ExecResult{Reader: io.NopCloser(nil)}, nil
}
func (d *slugKeyedDaemon) ExecInspect(context.Context, string) (bool, int, error) {
	return false, 0, nil
}
func (d *slugKeyedDaemon) CrewContainerName(id, slug string) string {
	// The id IS in this name — that is audit C1, and the contrast with the
	// sidecar naming below is the whole finding.
	return "crewship-crew-" + slug + "-" + id
}
func (d *slugKeyedDaemon) CopyToContainer(context.Context, string, string, io.Reader) error {
	return nil
}
func (d *slugKeyedDaemon) EnsureCrewServices(context.Context, provider.CrewConfig) (map[string]string, error) {
	return nil, nil
}
func (d *slugKeyedDaemon) StopCrewServices(context.Context, string) error { return nil }

// RemoveCrewServices removes every sidecar labelled with this slug — including
// the ones another workspace's identically-slugged crew owns.
func (d *slugKeyedDaemon) RemoveCrewServices(_ context.Context, slug string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.containers, slug)
	return nil
}

// RemoveCrewServiceVolumes removes by NAME PREFIX, so it also sweeps a crew
// whose slug begins with "<slug>-vol".
func (d *slugKeyedDaemon) RemoveCrewServiceVolumes(_ context.Context, slug string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	for owned := range d.volumes {
		if owned == slug || strings.HasPrefix(owned, slug+"-vol") {
			delete(d.volumes, owned)
		}
	}
	return nil
}

var _ provider.ContainerProvider = (*slugKeyedDaemon)(nil)
var _ provider.SidecarProvider = (*slugKeyedDaemon)(nil)
var _ provider.ServiceVolumeRemover = (*slugKeyedDaemon)(nil)

// seedSecondWorkspaceCrew creates a crew in a DIFFERENT workspace with the same
// slug — legal, because crews is UNIQUE(workspace_id, slug).
func seedSecondWorkspaceCrew(t *testing.T, db *sql.DB, crewID, slug string) string {
	t.Helper()
	mustExec(t, db, `INSERT INTO workspaces (id, name, slug) VALUES ('ws-other','Other','ws-other')`)
	mustExec(t, db, `INSERT INTO crews (id, workspace_id, name, slug, services_json)
		VALUES (?, 'ws-other', ?, ?, ?)`, crewID, slug, slug, redisServicesJSON)
	return "ws-other"
}

func TestCrewDeleteLeavesAnotherWorkspacesIdenticallyNamedCrewAlone(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedCrewWithServices(t, db, "crew-mine", wsID, "data-crew")
	seedSecondWorkspaceCrew(t, db, "crew-theirs", "data-crew")

	daemon := newSlugKeyedDaemon()
	daemon.start("data-crew", "ws-mine")
	daemon.start("data-crew", "ws-other")

	h := NewCrewHandler(db, newTestLogger())
	h.SetContainer(daemon)

	req := httptest.NewRequest("DELETE", "/api/v1/crews/crew-mine", nil)
	req.SetPathValue("crewId", "crew-mine")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Delete(rr, req)

	if rr.Code != 200 {
		t.Fatalf("delete status = %d, body %s", rr.Code, rr.Body.String())
	}

	if got := daemon.survivingContainers("data-crew"); len(got) == 0 {
		t.Errorf("deleting one workspace's crew force-removed the OTHER workspace's live sidecars " +
			"(both carry the label crewship.crew=data-crew). That is a running database in another " +
			"tenant, killed by a delete in this one.")
	}
	if got := daemon.survivingVolumes("data-crew"); len(got) == 0 {
		t.Errorf("deleting one workspace's crew deleted the OTHER workspace's sidecar data volumes " +
			"(both are named <prefix>-svc-data-crew-vol-*). Their Postgres data directory is gone.")
	}
	// And the operator has to be told the teardown did not happen, or they are
	// left believing volumes were removed that are still on disk.
	if !strings.Contains(rr.Body.String(), "sidecar") {
		t.Errorf("the response says nothing about the skipped sidecar teardown: %s", rr.Body.String())
	}
}

// The volume sweep is by name PREFIX, so `data` reaches `data-vol-x`.
func TestCrewDeleteDoesNotSweepACrewWhoseSlugExtendsItsVolumePrefix(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedCrewWithServices(t, db, "crew-short", wsID, "data")
	mustExec(t, db, `INSERT INTO crews (id, workspace_id, name, slug, services_json)
		VALUES ('crew-long', ?, 'long', 'data-vol-x', ?)`, wsID, redisServicesJSON)

	daemon := newSlugKeyedDaemon()
	daemon.start("data", "short")
	daemon.start("data-vol-x", "long")

	h := NewCrewHandler(db, newTestLogger())
	h.SetContainer(daemon)

	req := httptest.NewRequest("DELETE", "/api/v1/crews/crew-short", nil)
	req.SetPathValue("crewId", "crew-short")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Delete(rr, req)

	if rr.Code != 200 {
		t.Fatalf("delete status = %d, body %s", rr.Code, rr.Body.String())
	}
	if got := daemon.survivingVolumes("data-vol-x"); len(got) == 0 {
		t.Error("deleting crew \"data\" swept the volumes of crew \"data-vol-x\" — the volume " +
			"prefix <prefix>-svc-data-vol- matches <prefix>-svc-data-vol-x-vol-<name>")
	}
}

// A crew whose slug cannot be read cannot have its sidecars removed — the
// teardown is keyed on the slug — so the delete must not happen at all.
//
// The alternative is the outcome this whole change is meant to stop: the crew
// gone from every read surface, {"success":true} on the wire, and a Postgres
// container plus its data volume on disk with no caller left that can name
// them — while the operator has just answered a prompt saying the volumes were
// being deleted.
func TestCrewDeleteRefusesWhenTheCrewHasNoSlugToTearDownBy(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	mustExec(t, db, `INSERT INTO crews (id, workspace_id, name, slug, services_json)
		VALUES ('crew-noslug', ?, 'No Slug', '', ?)`, wsID, redisServicesJSON)

	daemon := newSlugKeyedDaemon()
	h := NewCrewHandler(db, newTestLogger())
	h.SetContainer(daemon)

	req := httptest.NewRequest("DELETE", "/api/v1/crews/crew-noslug", nil)
	req.SetPathValue("crewId", "crew-noslug")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Delete(rr, req)

	if rr.Code == 200 {
		t.Error("the crew was deleted even though its sidecars could never be found again — a " +
			"successful-looking delete that strands containers and volumes forever is worse than a 500")
	}
	var deletedAt sql.NullString
	if err := db.QueryRow(`SELECT deleted_at FROM crews WHERE id = 'crew-noslug'`).Scan(&deletedAt); err != nil {
		t.Fatalf("read crew: %v", err)
	}
	if deletedAt.Valid {
		t.Error("the crew row was soft-deleted by a request that answered with an error — the " +
			"operator will retry, and there is nothing left to retry against")
	}
}

// An unambiguous slug must still be torn down — the guard must not become a
// blanket refusal that silently reinstates #1709.
func TestCrewDeleteStillRemovesSidecarsWhenTheSlugIsUnambiguous(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedCrewWithServices(t, db, "crew-solo", wsID, "solo-crew")

	daemon := newSlugKeyedDaemon()
	daemon.start("solo-crew", "mine")

	h := NewCrewHandler(db, newTestLogger())
	h.SetContainer(daemon)

	req := httptest.NewRequest("DELETE", "/api/v1/crews/crew-solo", nil)
	req.SetPathValue("crewId", "crew-solo")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Delete(rr, req)

	if rr.Code != 200 {
		t.Fatalf("delete status = %d, body %s", rr.Code, rr.Body.String())
	}
	if got := daemon.survivingContainers("solo-crew"); len(got) != 0 {
		t.Errorf("the crew's own sidecars survived its delete: %v (#1709)", got)
	}
	if got := daemon.survivingVolumes("solo-crew"); len(got) != 0 {
		t.Errorf("the crew's own sidecar volumes survived its delete: %v (#1709)", got)
	}
}
