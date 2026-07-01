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

// fakeRuntimePruner records the crew set it was handed and returns canned
// results.
type fakeRuntimePruner struct {
	gotCrews []provider.CrewRef
	removed  []string
	err      error
}

func (f *fakeRuntimePruner) PruneCrewRuntimes(_ context.Context, crews []provider.CrewRef) ([]string, error) {
	f.gotCrews = crews
	return f.removed, f.err
}

// runtimeRig builds a handler over a DB seeded with two crews in the target
// workspace AND one crew in a DIFFERENT workspace that must NOT be enumerated
// (workspace scoping is the whole point vs the instance-wide legacy pruner).
func runtimeRig(t *testing.T, pruner provider.CrewRuntimePruner) (*CrewRuntimeHandler, string, string) {
	t.Helper()
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	// A second workspace, inserted directly (seedTestWorkspace uses fixed ids).
	// Its crew must NOT be enumerated by the workspace-scoped teardown.
	const otherWS = "other-workspace-id"
	if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'Other WS', 'other-ws')`, otherWS); err != nil {
		t.Fatalf("insert other workspace: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO crews (id, workspace_id, name, slug) VALUES (?, ?, 'Engineering', 'engineering')`, "c-eng", wsID); err != nil {
		t.Fatalf("insert crew eng: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO crews (id, workspace_id, name, slug) VALUES (?, ?, 'Quality', 'quality')`, "c-qua", wsID); err != nil {
		t.Fatalf("insert crew qua: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO crews (id, workspace_id, name, slug) VALUES (?, ?, 'Other', 'other')`, "c-other", otherWS); err != nil {
		t.Fatalf("insert crew other: %v", err)
	}
	h := NewCrewRuntimeHandler(db, newTestLogger(), pruner)
	return h, userID, wsID
}

func TestCrewRuntimePrune_HappyPath_WorkspaceScoped(t *testing.T) {
	fp := &fakeRuntimePruner{removed: []string{"crewship-team-engineering-c-eng", "crewship-home-engineering-c-eng"}}
	h, userID, wsID := runtimeRig(t, fp)

	req := withWorkspaceUser(httptest.NewRequest("POST", "/api/v1/admin/prune-crew-runtimes", nil), userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Prune(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200; body=%s", rr.Code, rr.Body.String())
	}
	var resp crewRuntimePruneResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Count != 2 || len(resp.Removed) != 2 {
		t.Errorf("count/removed = %d/%v; want 2", resp.Count, resp.Removed)
	}
	// Only the TWO crews of the context workspace reach the pruner (never the
	// crew in the other workspace) — with their ids AND slugs.
	got := map[string]string{}
	for _, c := range fp.gotCrews {
		got[c.Slug] = c.ID
	}
	if len(got) != 2 || got["engineering"] != "c-eng" || got["quality"] != "c-qua" {
		slugs := make([]string, 0, len(fp.gotCrews))
		for _, c := range fp.gotCrews {
			slugs = append(slugs, c.Slug)
		}
		sort.Strings(slugs)
		t.Errorf("pruner got crews %v; want engineering=c-eng, quality=c-qua only (workspace-scoped)", slugs)
	}
}

func TestCrewRuntimePrune_PartialRemovedOnError(t *testing.T) {
	fp := &fakeRuntimePruner{removed: []string{"crewship-team-engineering-c-eng"}, err: errors.New("daemon vanished mid-prune")}
	h, userID, wsID := runtimeRig(t, fp)

	req := withWorkspaceUser(httptest.NewRequest("POST", "/api/v1/admin/prune-crew-runtimes", nil), userID, wsID, "ADMIN")
	rr := httptest.NewRecorder()
	h.Prune(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d; want 500", rr.Code)
	}
	var body struct {
		Error   string   `json:"error"`
		Removed []string `json:"removed"`
		Count   int      `json:"count"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Count != 1 || len(body.Removed) != 1 || body.Removed[0] != "crewship-team-engineering-c-eng" {
		t.Errorf("partial removed not surfaced: %+v", body)
	}
}

func TestCrewRuntimePrune_RequiresAdmin(t *testing.T) {
	fp := &fakeRuntimePruner{}
	h, userID, wsID := runtimeRig(t, fp)
	req := withWorkspaceUser(httptest.NewRequest("POST", "/api/v1/admin/prune-crew-runtimes", nil), userID, wsID, "MEMBER")
	rr := httptest.NewRecorder()
	h.Prune(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Errorf("MEMBER status = %d; want 403", rr.Code)
	}
	if len(fp.gotCrews) != 0 {
		t.Errorf("pruner must not run for a forbidden caller")
	}
}

func TestCrewRuntimePrune_NilPrunerIs503(t *testing.T) {
	h, userID, wsID := runtimeRig(t, nil)
	req := withWorkspaceUser(httptest.NewRequest("POST", "/api/v1/admin/prune-crew-runtimes", nil), userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Prune(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("nil pruner status = %d; want 503", rr.Code)
	}
}
