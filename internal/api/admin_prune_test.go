package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"

	"github.com/crewship-ai/crewship/internal/provider"
)

// fakePruner records the slugs it was asked to prune and returns a canned
// result, standing in for the docker provider.
type fakePruner struct {
	gotSlugs []string
	removed  []string
	err      error
}

func (f *fakePruner) PruneLegacyCrewResources(_ context.Context, slugs []string) ([]string, error) {
	f.gotSlugs = slugs
	return f.removed, f.err
}

func pruneRig(t *testing.T, pruner provider.LegacyResourcePruner) (*LegacyPruneHandler, string, string) {
	t.Helper()
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	// Two crews so we assert DISTINCT slug enumeration reaches the pruner.
	if _, err := db.Exec(`INSERT INTO crews (id, workspace_id, name, slug) VALUES (?, ?, 'Engineering', 'engineering')`, "c-eng", wsID); err != nil {
		t.Fatalf("insert crew eng: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO crews (id, workspace_id, name, slug) VALUES (?, ?, 'Quality', 'quality')`, "c-qua", wsID); err != nil {
		t.Fatalf("insert crew qua: %v", err)
	}
	h := NewLegacyPruneHandler(db, newTestLogger(), pruner)
	return h, userID, wsID
}

func TestLegacyPrune_HappyPath(t *testing.T) {
	pruner := &fakePruner{removed: []string{"crewship-tools-engineering", "crewship-home-engineering"}}
	h, userID, wsID := pruneRig(t, pruner)

	req := withWorkspaceUser(httptest.NewRequest("POST", "/api/v1/admin/prune-legacy-resources", nil), userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Prune(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200; body=%s", rr.Code, rr.Body.String())
	}
	var resp legacyPruneResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Count != 2 || len(resp.Removed) != 2 {
		t.Errorf("count/removed = %d/%v; want 2", resp.Count, resp.Removed)
	}
	// Both workspace crew slugs reached the pruner.
	sort.Strings(pruner.gotSlugs)
	if len(pruner.gotSlugs) != 2 || pruner.gotSlugs[0] != "engineering" || pruner.gotSlugs[1] != "quality" {
		t.Errorf("pruner got slugs %v; want [engineering quality]", pruner.gotSlugs)
	}
}

func TestLegacyPrune_RequiresAdmin(t *testing.T) {
	h, userID, wsID := pruneRig(t, &fakePruner{})
	req := withWorkspaceUser(httptest.NewRequest("POST", "/api/v1/admin/prune-legacy-resources", nil), userID, wsID, "MEMBER")
	rr := httptest.NewRecorder()
	h.Prune(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Errorf("MEMBER status = %d; want 403", rr.Code)
	}
}

func TestLegacyPrune_NilPrunerIs503(t *testing.T) {
	h, userID, wsID := pruneRig(t, nil) // no docker provider wired
	req := withWorkspaceUser(httptest.NewRequest("POST", "/api/v1/admin/prune-legacy-resources", nil), userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Prune(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("nil pruner status = %d; want 503", rr.Code)
	}
}

func TestLegacyPrune_PipelineErrorIs500(t *testing.T) {
	h, userID, wsID := pruneRig(t, &fakePruner{err: errors.New("daemon unreachable")})
	req := withWorkspaceUser(httptest.NewRequest("POST", "/api/v1/admin/prune-legacy-resources", nil), userID, wsID, "ADMIN")
	rr := httptest.NewRecorder()
	h.Prune(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("pipeline error status = %d; want 500", rr.Code)
	}
}
