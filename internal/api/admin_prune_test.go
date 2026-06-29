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

// fakeLegacyProvider stands in for the docker provider, recording the crew set
// it was handed and returning canned results. Implements both optional
// interfaces.
type fakeLegacyProvider struct {
	gotCrews []provider.CrewRef
	removed  []string
	present  bool
	err      error
}

func (f *fakeLegacyProvider) PruneLegacyCrewResources(_ context.Context, crews []provider.CrewRef) ([]string, error) {
	f.gotCrews = crews
	return f.removed, f.err
}

func (f *fakeLegacyProvider) HasLegacyCrewResources(_ context.Context, crews []provider.CrewRef) (bool, error) {
	f.gotCrews = crews
	return f.present, f.err
}

func legacyRig(t *testing.T, pruner provider.LegacyResourcePruner, detector provider.LegacyResourceDetector) (*LegacyResourceHandler, string, string) {
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
	h := NewLegacyResourceHandler(db, newTestLogger(), pruner, detector)
	return h, userID, wsID
}

func TestLegacyPrune_HappyPath(t *testing.T) {
	fp := &fakeLegacyProvider{removed: []string{"crewship-tools-engineering", "crewship-home-engineering"}}
	h, userID, wsID := legacyRig(t, fp, fp)

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
	// Instance-wide: both crews reach the pruner WITH their ids.
	got := map[string]string{}
	for _, c := range fp.gotCrews {
		got[c.Slug] = c.ID
	}
	if got["engineering"] != "c-eng" || got["quality"] != "c-qua" {
		t.Errorf("pruner got crews %v; want engineering=c-eng, quality=c-qua", fp.gotCrews)
	}
}

func TestLegacyPrune_PartialRemovedOnError(t *testing.T) {
	fp := &fakeLegacyProvider{removed: []string{"crewship-tools-engineering"}, err: errors.New("daemon vanished mid-prune")}
	h, userID, wsID := legacyRig(t, fp, fp)

	req := withWorkspaceUser(httptest.NewRequest("POST", "/api/v1/admin/prune-legacy-resources", nil), userID, wsID, "ADMIN")
	rr := httptest.NewRecorder()
	h.Prune(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d; want 500", rr.Code)
	}
	// The partial removed list must survive into the body so the operator can reconcile.
	var body struct {
		Error   string   `json:"error"`
		Removed []string `json:"removed"`
		Count   int      `json:"count"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Count != 1 || len(body.Removed) != 1 || body.Removed[0] != "crewship-tools-engineering" {
		t.Errorf("partial removed not surfaced: %+v", body)
	}
}

func TestLegacyPrune_RequiresAdmin(t *testing.T) {
	fp := &fakeLegacyProvider{}
	h, userID, wsID := legacyRig(t, fp, fp)
	req := withWorkspaceUser(httptest.NewRequest("POST", "/api/v1/admin/prune-legacy-resources", nil), userID, wsID, "MEMBER")
	rr := httptest.NewRecorder()
	h.Prune(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Errorf("MEMBER status = %d; want 403", rr.Code)
	}
}

func TestLegacyPrune_NilPrunerIs503(t *testing.T) {
	h, userID, wsID := legacyRig(t, nil, nil)
	req := withWorkspaceUser(httptest.NewRequest("POST", "/api/v1/admin/prune-legacy-resources", nil), userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Prune(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("nil pruner status = %d; want 503", rr.Code)
	}
}

func TestLegacyDetect_PresentAndClean(t *testing.T) {
	for _, tc := range []struct {
		name    string
		present bool
	}{{"present", true}, {"clean", false}} {
		t.Run(tc.name, func(t *testing.T) {
			fp := &fakeLegacyProvider{present: tc.present}
			h, userID, wsID := legacyRig(t, fp, fp)
			req := withWorkspaceUser(httptest.NewRequest("GET", "/api/v1/admin/legacy-resources", nil), userID, wsID, "ADMIN")
			rr := httptest.NewRecorder()
			h.Detect(rr, req)
			if rr.Code != http.StatusOK {
				t.Fatalf("status = %d; want 200", rr.Code)
			}
			var resp legacyDetectResponse
			if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if resp.Present != tc.present {
				t.Errorf("present = %v; want %v", resp.Present, tc.present)
			}
			// Instance-wide enumeration reached the detector.
			slugs := []string{}
			for _, c := range fp.gotCrews {
				slugs = append(slugs, c.Slug)
			}
			sort.Strings(slugs)
			if len(slugs) != 2 || slugs[0] != "engineering" || slugs[1] != "quality" {
				t.Errorf("detector got slugs %v; want [engineering quality]", slugs)
			}
		})
	}
}

func TestLegacyDetect_RequiresAdmin(t *testing.T) {
	fp := &fakeLegacyProvider{}
	h, userID, wsID := legacyRig(t, fp, fp)
	req := withWorkspaceUser(httptest.NewRequest("GET", "/api/v1/admin/legacy-resources", nil), userID, wsID, "VIEWER")
	rr := httptest.NewRecorder()
	h.Detect(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Errorf("VIEWER status = %d; want 403", rr.Code)
	}
}

func TestLegacyDetect_NilDetectorIs503(t *testing.T) {
	h, userID, wsID := legacyRig(t, nil, nil)
	req := withWorkspaceUser(httptest.NewRequest("GET", "/api/v1/admin/legacy-resources", nil), userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Detect(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("nil detector status = %d; want 503", rr.Code)
	}
}
