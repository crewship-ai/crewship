package api

// Deleting a crew must not delete another tenant's database.
//
// crews is UNIQUE(workspace_id, slug) — slugs are unique PER WORKSPACE. Sidecar
// containers used to be matched by the label `crewship.crew=<slug>` and their
// volumes by the name prefix `<prefix>-svc-<slug>-vol-`, and NEITHER carried
// the crew id. So workspace A deleting its `data-crew` force-removed workspace
// B's `data-crew` postgres and deleted its data directory, and the confirmation
// prompt — which names only the caller's own crew — said nothing.
//
// #1721 answered that by REFUSING the teardown whenever another crew shared the
// slug: nothing was destroyed and the response said why. #1732 fixed the cause
// instead — sidecar containers, volumes and the crewship.crew-id label all
// carry the globally-unique crew id, and the provider selects on that label by
// exact equality — so the teardown can no longer reach another crew and the
// refusal is gone.
//
// The fake here therefore models an ID-keyed daemon, which is what the docker
// provider now is. Each test asserts BOTH halves, because either one alone
// passes on a broken implementation: the other tenant's resources survive, AND
// the deleted crew's own are actually removed.

import (
	"context"
	"database/sql"
	"io"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/crewship-ai/crewship/internal/provider"
)

// idKeyedDaemon is a container provider whose sidecar teardown behaves like the
// post-#1732 one: it removes by CREW ID, so only the named crew's resources go,
// whatever any other crew is slugged.
type idKeyedDaemon struct {
	mu sync.Mutex
	// containers/volumes are keyed by crew id → owner label ("ws-a", "ws-b").
	containers map[string]string
	volumes    map[string]string
}

func newIDKeyedDaemon() *idKeyedDaemon {
	return &idKeyedDaemon{
		containers: map[string]string{},
		volumes:    map[string]string{},
	}
}

// start records a crew's live sidecar + data volume. The slug is carried only
// as a display value, exactly as crewship.crew is on the real container.
func (d *idKeyedDaemon) start(crewID, owner string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.containers[crewID] = owner
	d.volumes[crewID] = owner
}

func (d *idKeyedDaemon) hasContainer(crewID string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, ok := d.containers[crewID]
	return ok
}

func (d *idKeyedDaemon) hasVolume(crewID string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, ok := d.volumes[crewID]
	return ok
}

func (d *idKeyedDaemon) EnsureCrewRuntime(context.Context, provider.CrewConfig) (string, error) {
	return "cid", nil
}
func (d *idKeyedDaemon) StopCrewRuntime(context.Context, string) error   { return nil }
func (d *idKeyedDaemon) RemoveCrewRuntime(context.Context, string) error { return nil }
func (d *idKeyedDaemon) ContainerStatus(context.Context, string) (*provider.ContainerStatus, error) {
	return &provider.ContainerStatus{State: "running"}, nil
}
func (d *idKeyedDaemon) ContainerStats(context.Context, string) (*provider.ContainerMetrics, error) {
	return nil, nil
}
func (d *idKeyedDaemon) Exec(context.Context, provider.ExecConfig) (*provider.ExecResult, error) {
	return &provider.ExecResult{Reader: io.NopCloser(nil)}, nil
}
func (d *idKeyedDaemon) ExecInspect(context.Context, string) (bool, int, error) {
	return false, 0, nil
}
func (d *idKeyedDaemon) CrewContainerName(id, slug string) string {
	// The id is in this name — audit C1. Sidecars carry it too now (#1732).
	return "crewship-crew-" + slug + "-" + id
}
func (d *idKeyedDaemon) CopyToContainer(context.Context, string, string, io.Reader) error {
	return nil
}
func (d *idKeyedDaemon) EnsureCrewServices(context.Context, provider.CrewConfig) (map[string]string, error) {
	return nil, nil
}
func (d *idKeyedDaemon) StopCrewServices(context.Context, string, string) error { return nil }

// RemoveCrewServices removes only the sidecars labelled with this crew id.
func (d *idKeyedDaemon) RemoveCrewServices(_ context.Context, crewID, _ string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.containers, crewID)
	return nil
}

// RemoveCrewServiceVolumes removes only the volumes labelled with this crew id.
// No name prefix is involved, so a slug that extends another's cannot widen it.
func (d *idKeyedDaemon) RemoveCrewServiceVolumes(_ context.Context, crewID, _ string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.volumes, crewID)
	return nil
}

var _ provider.ContainerProvider = (*idKeyedDaemon)(nil)
var _ provider.SidecarProvider = (*idKeyedDaemon)(nil)
var _ provider.ServiceVolumeRemover = (*idKeyedDaemon)(nil)

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

	daemon := newIDKeyedDaemon()
	daemon.start("crew-mine", "ws-mine")
	daemon.start("crew-theirs", "ws-other")

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

	if !daemon.hasContainer("crew-theirs") {
		t.Errorf("deleting one workspace's crew force-removed the OTHER workspace's live sidecars. " +
			"That is a running database in another tenant, killed by a delete in this one.")
	}
	if !daemon.hasVolume("crew-theirs") {
		t.Errorf("deleting one workspace's crew deleted the OTHER workspace's sidecar data volumes. " +
			"Their Postgres data directory is gone.")
	}
	// The other half: sharing a slug must no longer cost this crew its own
	// cleanup. Before #1732 the teardown refused outright in exactly this case,
	// leaving a postgres and a volume nothing would ever collect.
	if daemon.hasContainer("crew-mine") {
		t.Errorf("the deleted crew's own sidecars survived because ANOTHER workspace happens to use " +
			"the same slug — an id-keyed teardown has no reason to withhold that (#1732)")
	}
	if daemon.hasVolume("crew-mine") {
		t.Errorf("the deleted crew's own sidecar volumes survived because another workspace uses the " +
			"same slug (#1732)")
	}
}

// The old volume sweep was by name PREFIX, so deleting `data` also reached
// `data-vol-x`. Selecting on the crew id label closes it whatever the slugs are.
func TestCrewDeleteDoesNotSweepACrewWhoseSlugExtendsItsVolumePrefix(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedCrewWithServices(t, db, "crew-short", wsID, "data")
	mustExec(t, db, `INSERT INTO crews (id, workspace_id, name, slug, services_json)
		VALUES ('crew-long', ?, 'long', 'data-vol-x', ?)`, wsID, redisServicesJSON)

	daemon := newIDKeyedDaemon()
	daemon.start("crew-short", "short")
	daemon.start("crew-long", "long")

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
	if !daemon.hasVolume("crew-long") {
		t.Error("deleting crew \"data\" swept the volumes of crew \"data-vol-x\" — the volume " +
			"prefix <prefix>-svc-data-vol- matches <prefix>-svc-data-vol-x-vol-<name>")
	}
	if daemon.hasVolume("crew-short") {
		t.Error("the deleted crew's own sidecar volumes survived its delete (#1709)")
	}
}

// A crew whose slug cannot be read cannot have its sidecars named for an
// operator, so the delete must not happen at all.
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

	daemon := newIDKeyedDaemon()
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

// A crew nobody shares a slug with must still be torn down — the naming change
// must not become a blanket refusal that silently reinstates #1709.
func TestCrewDeleteStillRemovesSidecarsWhenTheSlugIsUnambiguous(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedCrewWithServices(t, db, "crew-solo", wsID, "solo-crew")

	daemon := newIDKeyedDaemon()
	daemon.start("crew-solo", "mine")

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
	if daemon.hasContainer("crew-solo") {
		t.Error("the crew's own sidecars survived its delete (#1709)")
	}
	if daemon.hasVolume("crew-solo") {
		t.Error("the crew's own sidecar volumes survived its delete (#1709)")
	}
}
