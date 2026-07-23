package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
	"github.com/crewship-ai/crewship/internal/provider"
)

// reapFakeProvider is a ContainerProvider + CrewContainerLookup whose /health
// exec returns a per-container token_fp and which records the containers it was
// asked to stop/remove. Everything else is a no-op — enough to drive the
// orphan classifier without a Docker daemon.
type reapFakeProvider struct {
	// running maps crew id → container id for FindCrewContainer.
	running map[string]string
	// healthFP maps container id → the token_fp its sidecar advertises. A
	// missing entry makes /health unparseable (unreachable sidecar → unknown).
	healthFP map[string]string

	mu      sync.Mutex
	stopped []string
	removed []string
}

func (p *reapFakeProvider) FindCrewContainer(_ context.Context, id, _ string) (string, bool, error) {
	if cid, ok := p.running[id]; ok {
		return cid, true, nil
	}
	return "", false, nil
}

func (p *reapFakeProvider) Exec(_ context.Context, cfg provider.ExecConfig) (*provider.ExecResult, error) {
	fp, ok := p.healthFP[cfg.ContainerID]
	body := "not-json"
	if ok {
		body = `{"status":"ok","network_mode":"free","token_fp":"` + fp + `"}`
	}
	return &provider.ExecResult{ExecID: "reap-health", Reader: io.NopCloser(strings.NewReader(body))}, nil
}

func (p *reapFakeProvider) StopCrewRuntime(_ context.Context, id string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stopped = append(p.stopped, id)
	return nil
}
func (p *reapFakeProvider) RemoveCrewRuntime(_ context.Context, id string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.removed = append(p.removed, id)
	return nil
}
func (p *reapFakeProvider) EnsureCrewRuntime(_ context.Context, _ provider.CrewConfig) (string, error) {
	return "", nil
}
func (p *reapFakeProvider) ContainerStatus(_ context.Context, _ string) (*provider.ContainerStatus, error) {
	return nil, nil
}
func (p *reapFakeProvider) ContainerStats(_ context.Context, _ string) (*provider.ContainerMetrics, error) {
	return nil, nil
}
func (p *reapFakeProvider) ExecInspect(_ context.Context, _ string) (bool, int, error) {
	return false, 0, nil
}
func (p *reapFakeProvider) CrewContainerName(_ string, slug string) string { return "crew-" + slug }
func (p *reapFakeProvider) CopyToContainer(_ context.Context, _, _ string, _ io.Reader) error {
	return nil
}

var (
	_ provider.ContainerProvider   = (*reapFakeProvider)(nil)
	_ provider.CrewContainerLookup = (*reapFakeProvider)(nil)
)

const reapNewMaster = "new-master-secret"
const reapOldMaster = "old-rotated-master"

// reapRig seeds two crews in the caller's workspace: c-eng holds a token minted
// under the CURRENT master (healthy) and c-qua holds one minted under an OLD
// (rotated) master (orphaned). Both containers are running.
func reapRig(t *testing.T) (*OrphanContainerHandler, *reapFakeProvider, string, string) {
	t.Helper()
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	if _, err := db.Exec(`INSERT INTO crews (id, workspace_id, name, slug) VALUES (?, ?, 'Engineering', 'engineering')`, "c-eng", wsID); err != nil {
		t.Fatalf("insert crew eng: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO crews (id, workspace_id, name, slug) VALUES (?, ?, 'Quality', 'quality')`, "c-qua", wsID); err != nil {
		t.Fatalf("insert crew qua: %v", err)
	}

	healthyFP := internaltoken.Fingerprint(internaltoken.DeriveCrewToken(reapNewMaster, wsID, "c-eng"))
	staleFP := internaltoken.Fingerprint(internaltoken.DeriveCrewToken(reapOldMaster, wsID, "c-qua"))

	p := &reapFakeProvider{
		running:  map[string]string{"c-eng": "ctr-eng", "c-qua": "ctr-qua"},
		healthFP: map[string]string{"ctr-eng": healthyFP, "ctr-qua": staleFP},
	}
	h := NewOrphanContainerHandler(db, newTestLogger(), p, reapNewMaster)
	return h, p, userID, wsID
}

func TestReapOrphan_DryRunReportsOnlyStale(t *testing.T) {
	h, p, userID, wsID := reapRig(t)

	req := withWorkspaceUser(httptest.NewRequest("POST", "/api/v1/admin/reap-orphan-containers", nil), userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Reap(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200; body=%s", rr.Code, rr.Body.String())
	}
	var resp reapOrphanResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Applied {
		t.Error("dry-run must report Applied=false")
	}
	// Only the stale crew (c-qua) is reported; the healthy c-eng is never listed.
	if resp.Count != 1 || len(resp.Orphans) != 1 || resp.Orphans[0].CrewID != "c-qua" {
		t.Fatalf("orphans = %+v; want exactly c-qua", resp.Orphans)
	}
	if resp.Orphans[0].Reaped {
		t.Error("dry-run must not mark anything Reaped")
	}
	// Dry-run must NOT stop/remove anything.
	if len(p.stopped) != 0 || len(p.removed) != 0 {
		t.Errorf("dry-run touched docker: stopped=%v removed=%v", p.stopped, p.removed)
	}
}

func TestReapOrphan_ApplyReapsStaleOnly(t *testing.T) {
	h, p, userID, wsID := reapRig(t)

	req := withWorkspaceUser(httptest.NewRequest("POST", "/api/v1/admin/reap-orphan-containers?apply=true", nil), userID, wsID, "ADMIN")
	rr := httptest.NewRecorder()
	h.Reap(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200; body=%s", rr.Code, rr.Body.String())
	}
	var resp reapOrphanResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.Applied {
		t.Error("apply=true must report Applied=true")
	}
	if resp.Count != 1 || len(resp.Orphans) != 1 || resp.Orphans[0].CrewID != "c-qua" || !resp.Orphans[0].Reaped {
		t.Fatalf("orphans = %+v; want c-qua Reaped=true", resp.Orphans)
	}
	// ONLY the stale container is reaped — never the healthy one.
	if len(p.removed) != 1 || p.removed[0] != "ctr-qua" {
		t.Errorf("removed = %v; want [ctr-qua] only", p.removed)
	}
	if len(p.stopped) != 1 || p.stopped[0] != "ctr-qua" {
		t.Errorf("stopped = %v; want [ctr-qua] only", p.stopped)
	}
}

func TestReapOrphan_RequiresAdmin(t *testing.T) {
	h, p, userID, wsID := reapRig(t)
	req := withWorkspaceUser(httptest.NewRequest("POST", "/api/v1/admin/reap-orphan-containers?apply=true", nil), userID, wsID, "MEMBER")
	rr := httptest.NewRecorder()
	h.Reap(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Errorf("MEMBER status = %d; want 403", rr.Code)
	}
	if len(p.removed) != 0 {
		t.Error("a forbidden caller must not reap anything")
	}
}

func TestReapOrphan_NilProviderIs503(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewOrphanContainerHandler(db, newTestLogger(), nil, reapNewMaster)
	req := withWorkspaceUser(httptest.NewRequest("POST", "/api/v1/admin/reap-orphan-containers", nil), userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Reap(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("nil provider status = %d; want 503", rr.Code)
	}
}

// TestReapOrphan_EmptyMasterNeverReaps — with no configured master the server
// can't derive an expected fingerprint, so it must (fail-safe) classify every
// container as healthy and reap nothing.
func TestReapOrphan_EmptyMasterNeverReaps(t *testing.T) {
	h, p, userID, wsID := reapRig(t)
	h.master = "" // internal auth unconfigured

	req := withWorkspaceUser(httptest.NewRequest("POST", "/api/v1/admin/reap-orphan-containers?apply=true", nil), userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Reap(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200", rr.Code)
	}
	var resp reapOrphanResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Count != 0 || len(p.removed) != 0 {
		t.Errorf("empty master must reap nothing; count=%d removed=%v", resp.Count, p.removed)
	}
}
