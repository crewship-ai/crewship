package api

// #1845 — the API half: read a crew's image freshness, and act on it.
//
// The notification tells an operator their crew is behind. Without these two
// routes the only thing they can do about it is `docker rm` on a host they may
// not have shell on — which is the state the issue describes as "no button".

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/crewship-ai/crewship/internal/provider"
)

// stubFreshness is a provider.CrewImageFreshness whose answers the test picks,
// and which records the CrewConfig it was handed — the freshness verdict is
// only as good as the crew configuration it was asked about.
type stubFreshness struct {
	state   *provider.CrewImageState
	refresh *provider.CrewImageRefresh
	err     error
	sawCfg  provider.CrewConfig
}

func (s *stubFreshness) CrewImageState(_ context.Context, cfg provider.CrewConfig) (*provider.CrewImageState, error) {
	s.sawCfg = cfg
	if s.err != nil {
		return nil, s.err
	}
	return s.state, nil
}

func (s *stubFreshness) RefreshCrewImage(_ context.Context, cfg provider.CrewConfig) (*provider.CrewImageRefresh, error) {
	s.sawCfg = cfg
	if s.err != nil {
		return nil, s.err
	}
	return s.refresh, nil
}

func imgHandler(t *testing.T, fresh provider.CrewImageFreshness) (*CrewImageHandler, string, string) {
	t.Helper()
	db := setupTestDB(t)
	wsID, crewID := "ws-img", "crew-img"
	if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'W', 'w-img')`, wsID); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO crews (id, workspace_id, name, slug, runtime_image, cached_image)
		 VALUES (?, ?, 'Alpha', 'alpha', 'ghcr.io/acme/runtime:latest', '')`, crewID, wsID); err != nil {
		t.Fatalf("seed crew: %v", err)
	}
	h := &CrewImageHandler{
		db:        db,
		logger:    slog.New(slog.NewTextHandler(discardWriterCovCPR{}, nil)),
		freshness: fresh,
	}
	return h, wsID, crewID
}

func imgRequest(method, wsID, role, crewID string) *http.Request {
	req := httptest.NewRequest(method, "/api/v1/crews/"+crewID+"/image", nil)
	req.SetPathValue("crewId", crewID)
	return req.WithContext(withWorkspace(req.Context(), wsID, role))
}

const (
	imgRunningDigest  = "sha256:1111111111111111111111111111111111111111111111111111111111111111"
	imgResolvedDigest = "sha256:2222222222222222222222222222222222222222222222222222222222222222"
)

// TestCrewImageStatus_ReportsBehind is the read the UI renders from.
func TestCrewImageStatus_ReportsBehind(t *testing.T) {
	fresh := &stubFreshness{state: &provider.CrewImageState{
		Image:          "ghcr.io/acme/runtime:latest",
		ContainerID:    "ctr_abcdef012345",
		Running:        true,
		RunningDigest:  imgRunningDigest,
		ResolvedDigest: imgResolvedDigest,
		Behind:         true,
	}}
	h, wsID, crewID := imgHandler(t, fresh)

	w := httptest.NewRecorder()
	h.Status(w, imgRequest(http.MethodGet, wsID, "MEMBER", crewID))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", w.Code, w.Body.String())
	}
	var got struct {
		Image          string `json:"image"`
		Behind         bool   `json:"behind"`
		Running        bool   `json:"running"`
		RunningDigest  string `json:"running_digest"`
		ResolvedDigest string `json:"resolved_digest"`
		Reason         string `json:"reason"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v (%s)", err, w.Body.String())
	}
	if !got.Behind {
		t.Error("behind = false, want true")
	}
	if got.RunningDigest != imgRunningDigest || got.ResolvedDigest != imgResolvedDigest {
		t.Errorf("digests = (%q, %q), want (%q, %q)", got.RunningDigest, got.ResolvedDigest, imgRunningDigest, imgResolvedDigest)
	}
	// The crew's configured image has to reach the provider, or the verdict is
	// about the wrong image.
	if fresh.sawCfg.Image != "ghcr.io/acme/runtime:latest" || fresh.sawCfg.Slug != "alpha" {
		t.Errorf("provider saw %+v, want the crew's own image and slug", fresh.sawCfg)
	}
}

// TestCrewImageStatus_ReadOnlyForViewers: freshness is a read. Requiring a
// mutation role would hide the fact from exactly the people most likely to be
// watching a dashboard.
func TestCrewImageStatus_ReadOnlyForViewers(t *testing.T) {
	fresh := &stubFreshness{state: &provider.CrewImageState{Image: "x", Reason: "no container"}}
	h, wsID, crewID := imgHandler(t, fresh)

	w := httptest.NewRecorder()
	h.Status(w, imgRequest(http.MethodGet, wsID, "VIEWER", crewID))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d for VIEWER, want 200 (body %s)", w.Code, w.Body.String())
	}
}

// TestCrewImageStatus_CrossWorkspaceIs404 — the crew id is guessable and the
// answer names an image and a container. Scope it.
func TestCrewImageStatus_CrossWorkspaceIs404(t *testing.T) {
	fresh := &stubFreshness{state: &provider.CrewImageState{Image: "x"}}
	h, _, crewID := imgHandler(t, fresh)

	w := httptest.NewRecorder()
	h.Status(w, imgRequest(http.MethodGet, "ws-someone-else", "OWNER", crewID))
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d for another workspace's crew, want 404", w.Code)
	}
}

// TestCrewImageStatus_NoProviderIs503 rather than a fabricated "current".
// Saying "you are up to date" when nothing checked is the worst possible
// answer for this endpoint.
func TestCrewImageStatus_NoProviderIs503(t *testing.T) {
	h, wsID, crewID := imgHandler(t, nil)
	h.freshness = nil

	w := httptest.NewRecorder()
	h.Status(w, imgRequest(http.MethodGet, wsID, "OWNER", crewID))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d with no provider, want 503 (body %s)", w.Code, w.Body.String())
	}
}

// TestCrewImageRefresh_RequiresAMutationRole. A refresh pulls from a registry
// and drops a running container out from under whoever is using it.
func TestCrewImageRefresh_RequiresAMutationRole(t *testing.T) {
	fresh := &stubFreshness{refresh: &provider.CrewImageRefresh{Image: "x"}}
	h, wsID, crewID := imgHandler(t, fresh)

	for _, role := range []string{"VIEWER", "MEMBER"} {
		w := httptest.NewRecorder()
		h.Refresh(w, imgRequest(http.MethodPost, wsID, role, crewID))
		if w.Code != http.StatusForbidden {
			t.Errorf("role %s: status = %d, want 403", role, w.Code)
		}
	}
	w := httptest.NewRecorder()
	h.Refresh(w, imgRequest(http.MethodPost, wsID, "MANAGER", crewID))
	if w.Code != http.StatusOK {
		t.Errorf("role MANAGER: status = %d, want 200 (body %s)", w.Code, w.Body.String())
	}
}

// TestCrewImageRefresh_ReportsWhatChanged. "Refreshed" with no numbers leaves
// the operator exactly where the notification found them.
func TestCrewImageRefresh_ReportsWhatChanged(t *testing.T) {
	fresh := &stubFreshness{refresh: &provider.CrewImageRefresh{
		Image:            "ghcr.io/acme/runtime:latest",
		PreviousDigest:   imgRunningDigest,
		NewDigest:        imgResolvedDigest,
		ContainerRemoved: true,
	}}
	h, wsID, crewID := imgHandler(t, fresh)

	w := httptest.NewRecorder()
	h.Refresh(w, imgRequest(http.MethodPost, wsID, "OWNER", crewID))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", w.Code, w.Body.String())
	}
	var got struct {
		Image            string `json:"image"`
		PreviousDigest   string `json:"previous_digest"`
		NewDigest        string `json:"new_digest"`
		ContainerRemoved bool   `json:"container_removed"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v (%s)", err, w.Body.String())
	}
	if got.PreviousDigest != imgRunningDigest || got.NewDigest != imgResolvedDigest {
		t.Errorf("digests = (%q, %q), want (%q, %q)", got.PreviousDigest, got.NewDigest, imgRunningDigest, imgResolvedDigest)
	}
	if !got.ContainerRemoved {
		t.Error("container_removed = false, want true")
	}
}

// TestCrewImageRefresh_ProviderErrorIs500NotSilentSuccess.
func TestCrewImageRefresh_ProviderErrorIs500NotSilentSuccess(t *testing.T) {
	fresh := &stubFreshness{err: errors.New("registry throttled")}
	h, wsID, crewID := imgHandler(t, fresh)

	w := httptest.NewRecorder()
	h.Refresh(w, imgRequest(http.MethodPost, wsID, "OWNER", crewID))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d on a failed pull, want 500 (body %s)", w.Code, w.Body.String())
	}
}
