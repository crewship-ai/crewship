package api

// The two ends of a crew's sidecar lifecycle, from the dispatch path's point of
// view.
//
// #1708 — starting: a crew that declares `services:` got them only when the
// chat path happened to be the thing that cold-started it. Every headless path
// (issue start, scheduler, webhook, pipeline, routine) assembled its container
// config through buildCrewRuntimeConfig, which never looked at services_json,
// so the config carried no services and nothing ever asked for them.
//
// #1709 — stopping: RemoveCrewServices and RemoveCrewServiceVolumes had no
// production caller at all, so deleting a crew left its postgres running and
// its data volume on disk forever.

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/crewship-ai/crewship/internal/encryption"
	"github.com/crewship-ai/crewship/internal/provider"
)

const redisServicesJSON = `[{"name":"redis","image":"redis:7-alpine",` +
	`"command":["redis-server","--requirepass","$REDIS_PASSWORD"],` +
	`"env":{"REDIS_PASSWORD":"s3cret"},` +
	`"volumes":[{"name":"redis-data","mount":"/data"}]}]`

func seedCrewWithServices(t *testing.T, db *sql.DB, crewID, wsID, slug string) {
	t.Helper()
	mustExec(t, db, `INSERT INTO crews (id, workspace_id, name, slug, services_json)
		VALUES (?, ?, ?, ?, ?)`, crewID, wsID, slug, slug, redisServicesJSON)
}

// TestCrewRuntimeConfigCarriesDeclaredServices is #1708 at its root: the config
// the dispatch path hands the container provider must name the crew's sidecars,
// because a config with no services is a crew that starts database-less.
func TestCrewRuntimeConfigCarriesDeclaredServices(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedCrewWithServices(t, db, "crew-svc-1", wsID, "data-crew")

	cfg, err := BuildCrewRuntimeConfig(context.Background(), db, "crew-svc-1", wsID)
	if err != nil {
		t.Fatalf("BuildCrewRuntimeConfig: %v", err)
	}
	if len(cfg.Services) != 1 {
		t.Fatalf("Services = %d, want the one the crew declares — a config with none is what let "+
			"`issue start`, the scheduler, webhooks and pipelines run a crew without its "+
			"declared datastores (#1708)", len(cfg.Services))
	}
	svc := cfg.Services[0]
	if svc.Name != "redis" || svc.Image != "redis:7-alpine" {
		t.Errorf("service = %q/%q, want redis/redis:7-alpine", svc.Name, svc.Image)
	}
	if svc.Env["REDIS_PASSWORD"] != "s3cret" {
		t.Errorf("REDIS_PASSWORD = %q, want the literal from services_json — without it redis "+
			"comes up with no password and the crew's clients fail to authenticate", svc.Env["REDIS_PASSWORD"])
	}
	if len(svc.Volumes) != 1 || svc.Volumes[0].Name != "redis-data" {
		t.Errorf("volumes = %+v, want the declared redis-data volume", svc.Volumes)
	}
}

// TestCrewRuntimeConfigResolvesEnvRefsFromTheCrewsCredentials pins the crew
// scope of env_refs: the sidecar's environment feeds the provider's spec hash,
// so it must not depend on which agent triggered the start.
func TestCrewRuntimeConfigResolvesEnvRefsFromTheCrewsCredentials(t *testing.T) {
	ensureEncryptionKey(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	mustExec(t, db, `INSERT INTO crews (id, workspace_id, name, slug, services_json)
		VALUES ('crew-svc-2', ?, 'pg', 'pg', ?)`, wsID,
		`[{"name":"postgres","image":"postgres:16-alpine","env_refs":["POSTGRES_PASSWORD"]}]`)

	enc, err := encryption.Encrypt("hunter2")
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	mustExec(t, db, `INSERT INTO credentials (id, workspace_id, name, type, status, encrypted_value, created_by)
		VALUES ('cred-1', ?, 'POSTGRES_PASSWORD', 'SECRET', 'ACTIVE', ?, ?)`, wsID, enc, userID)
	mustExec(t, db, `INSERT INTO credential_crews (credential_id, crew_id) VALUES ('cred-1','crew-svc-2')`)

	cfg, err := BuildCrewRuntimeConfig(context.Background(), db, "crew-svc-2", wsID)
	if err != nil {
		t.Fatalf("BuildCrewRuntimeConfig: %v", err)
	}
	if len(cfg.Services) != 1 {
		t.Fatalf("Services = %d, want 1", len(cfg.Services))
	}
	if got := cfg.Services[0].Env["POSTGRES_PASSWORD"]; got != "hunter2" {
		t.Errorf("POSTGRES_PASSWORD = %q, want the crew-linked credential's value — postgres "+
			"refuses to start without one", got)
	}
}

// mustEncrypt is the vault write a test needs before it can assert what a
// sidecar reads back out.
func mustEncrypt(t *testing.T, plain string) string {
	t.Helper()
	enc, err := encryption.Encrypt(plain)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	return enc
}

// A crew's env_refs must resolve from the crew's WHOLE delivery set, not just
// the legacy credential_crews link.
//
// credential_bindings (scope CREW / WORKSPACE) is the supported model — it is
// what makes two crews able to bind POSTGRES_PASSWORD to two different secrets.
// Reading only credential_crews meant a crew whose password comes from a
// binding started postgres with NO password: the official image exits
// immediately, EnsureCrewServices fails, and the whole start now hard-fails —
// so a scheduled routine that used to run (just without sidecars) would break
// outright. It also gave the sidecar a different env, and therefore a different
// spec hash, from the one the chat path produces.
func TestCrewRuntimeConfigResolvesEnvRefsFromCrewAndWorkspaceBindings(t *testing.T) {
	ensureEncryptionKey(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	mustExec(t, db, `INSERT INTO crews (id, workspace_id, name, slug, services_json)
		VALUES ('crew-bind', ?, 'pg', 'pg', ?)`, wsID,
		`[{"name":"postgres","image":"postgres:16-alpine","env_refs":["POSTGRES_PASSWORD","SHARED_TOKEN"]}]`)

	// The crew-scoped binding: credential named "pg-main", delivered under the
	// slot POSTGRES_PASSWORD.
	crewSecret := mustEncrypt(t, "from-crew-binding")
	mustExec(t, db, `INSERT INTO credentials (id, workspace_id, name, type, status, encrypted_value, created_by)
		VALUES ('cred-pg', ?, 'pg-main', 'SECRET', 'ACTIVE', ?, ?)`, wsID, crewSecret, userID)
	mustExec(t, db, `INSERT INTO credential_bindings (id, workspace_id, credential_id, scope, crew_id, slot)
		VALUES ('bind-crew', ?, 'cred-pg', 'CREW', 'crew-bind', 'POSTGRES_PASSWORD')`, wsID)

	// A workspace-scoped binding, which every crew in the tenant inherits.
	wsSecret := mustEncrypt(t, "from-workspace-binding")
	mustExec(t, db, `INSERT INTO credentials (id, workspace_id, name, type, status, encrypted_value, created_by)
		VALUES ('cred-shared', ?, 'shared-account', 'SECRET', 'ACTIVE', ?, ?)`, wsID, wsSecret, userID)
	mustExec(t, db, `INSERT INTO credential_bindings (id, workspace_id, credential_id, scope, slot)
		VALUES ('bind-ws', ?, 'cred-shared', 'WORKSPACE', 'SHARED_TOKEN')`, wsID)

	cfg, err := BuildCrewRuntimeConfig(context.Background(), db, "crew-bind", wsID)
	if err != nil {
		t.Fatalf("BuildCrewRuntimeConfig: %v", err)
	}
	if len(cfg.Services) != 1 {
		t.Fatalf("Services = %d, want 1", len(cfg.Services))
	}
	env := cfg.Services[0].Env
	if env["POSTGRES_PASSWORD"] != "from-crew-binding" {
		t.Errorf("POSTGRES_PASSWORD = %q, want the crew-scoped binding's value — postgres refuses to "+
			"start without one, so this is the difference between a working sidecar and a hard failure",
			env["POSTGRES_PASSWORD"])
	}
	if env["SHARED_TOKEN"] != "from-workspace-binding" {
		t.Errorf("SHARED_TOKEN = %q, want the workspace-scoped binding's value", env["SHARED_TOKEN"])
	}
}

// The crew scope stops at the crew: an AGENT-scoped binding must NOT reach a
// sidecar, because that is what made the same crew's postgres differ per agent
// and churn on every change of trigger.
func TestCrewRuntimeConfigIgnoresAgentScopedBindingsForSidecars(t *testing.T) {
	ensureEncryptionKey(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	mustExec(t, db, `INSERT INTO crews (id, workspace_id, name, slug, services_json)
		VALUES ('crew-agent-bind', ?, 'pg', 'pg', ?)`, wsID,
		`[{"name":"postgres","image":"postgres:16-alpine","env_refs":["POSTGRES_PASSWORD"]}]`)
	mustExec(t, db, `INSERT INTO agents (id, workspace_id, crew_id, name, slug, agent_role)
		VALUES ('agent-1', ?, 'crew-agent-bind', 'A', 'a', 'AGENT')`, wsID)

	secret := mustEncrypt(t, "per-agent-value")
	mustExec(t, db, `INSERT INTO credentials (id, workspace_id, name, type, status, encrypted_value, created_by)
		VALUES ('cred-agent', ?, 'agent-only', 'SECRET', 'ACTIVE', ?, ?)`, wsID, secret, userID)
	mustExec(t, db, `INSERT INTO credential_bindings (id, workspace_id, credential_id, scope, agent_id, slot)
		VALUES ('bind-agent', ?, 'cred-agent', 'AGENT', 'agent-1', 'POSTGRES_PASSWORD')`, wsID)

	cfg, err := BuildCrewRuntimeConfig(context.Background(), db, "crew-agent-bind", wsID)
	if err != nil {
		t.Fatalf("BuildCrewRuntimeConfig: %v", err)
	}
	if got := cfg.Services[0].Env["POSTGRES_PASSWORD"]; got != "" {
		t.Errorf("POSTGRES_PASSWORD = %q — an AGENT-scoped binding reached a crew-scoped sidecar, "+
			"which is exactly the per-agent variation that churns the container", got)
	}
}

// ---------- #1709: delete tears the sidecars down ----------

// teardownContainer records the sidecar teardown calls a crew delete makes.
type teardownContainer struct {
	mu             sync.Mutex
	removedSvcs    []string
	removedVolumes []string
	stopped        []string
}

func (c *teardownContainer) EnsureCrewRuntime(context.Context, provider.CrewConfig) (string, error) {
	return "cid", nil
}
func (c *teardownContainer) StopCrewRuntime(context.Context, string) error   { return nil }
func (c *teardownContainer) RemoveCrewRuntime(context.Context, string) error { return nil }
func (c *teardownContainer) ContainerStatus(context.Context, string) (*provider.ContainerStatus, error) {
	return &provider.ContainerStatus{State: "running"}, nil
}
func (c *teardownContainer) ContainerStats(context.Context, string) (*provider.ContainerMetrics, error) {
	return nil, nil
}
func (c *teardownContainer) Exec(context.Context, provider.ExecConfig) (*provider.ExecResult, error) {
	return &provider.ExecResult{Reader: io.NopCloser(nil)}, nil
}
func (c *teardownContainer) ExecInspect(context.Context, string) (bool, int, error) {
	return false, 0, nil
}
func (c *teardownContainer) CrewContainerName(_, slug string) string { return "crewship-crew-" + slug }
func (c *teardownContainer) CopyToContainer(context.Context, string, string, io.Reader) error {
	return nil
}

func (c *teardownContainer) EnsureCrewServices(context.Context, provider.CrewConfig) (map[string]string, error) {
	return nil, nil
}
func (c *teardownContainer) StopCrewServices(_ context.Context, slug string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.stopped = append(c.stopped, slug)
	return nil
}
func (c *teardownContainer) RemoveCrewServices(_ context.Context, slug string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.removedSvcs = append(c.removedSvcs, slug)
	return nil
}
func (c *teardownContainer) RemoveCrewServiceVolumes(_ context.Context, slug string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.removedVolumes = append(c.removedVolumes, slug)
	return nil
}

var _ provider.ContainerProvider = (*teardownContainer)(nil)
var _ provider.SidecarProvider = (*teardownContainer)(nil)

func TestCrewDeleteRemovesSidecarsAndTheirVolumes(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedCrewWithServices(t, db, "crew-del-1", wsID, "data-crew")

	ctr := &teardownContainer{}
	h := NewCrewHandler(db, newTestLogger())
	h.SetContainer(ctr)

	req := httptest.NewRequest("DELETE", "/api/v1/crews/crew-del-1", nil)
	req.SetPathValue("crewId", "crew-del-1")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Delete(rr, req)

	if rr.Code != 200 {
		t.Fatalf("delete status = %d, body %s", rr.Code, rr.Body.String())
	}
	var out map[string]bool
	_ = json.Unmarshal(rr.Body.Bytes(), &out)

	ctr.mu.Lock()
	defer ctr.mu.Unlock()
	if len(ctr.removedSvcs) != 1 || ctr.removedSvcs[0] != "data-crew" {
		t.Errorf("RemoveCrewServices calls = %v, want [data-crew] — a deleted crew that leaves its "+
			"sidecars running leaks a live, still-authenticated database nobody monitors (#1709)",
			ctr.removedSvcs)
	}
	if len(ctr.removedVolumes) != 1 || ctr.removedVolumes[0] != "data-crew" {
		t.Errorf("RemoveCrewServiceVolumes calls = %v, want [data-crew] — the named data volumes "+
			"outlive the crew otherwise and had to be removed by hand (#1709)", ctr.removedVolumes)
	}
}
