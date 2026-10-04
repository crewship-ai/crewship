package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/crewship-ai/crewship/internal/provider"
)

// Embed the existing provider contract: only runtime removal is exercised.
type restartCleanupProvider struct {
	provider.ContainerProvider
	removed []string
	err     error
}

func (p *restartCleanupProvider) RemoveCrewRuntime(_ context.Context, id string) error {
	p.removed = append(p.removed, id)
	return p.err
}

func TestRestartCrewAgentsRouterCleanup(t *testing.T) {
	for _, tc := range []struct {
		name, workspace, role, containers string
		cleanupErr                        error
		status, restarted, calls          int
		removed                           bool
	}{
		{name: "owned runtime", workspace: "ws-cpr", role: "ADMIN", containers: `[{"Id":"foreign","Names":["/crewship-2-team-alpha-crew-elsewhere"]},{"Id":"owned-runtime-id","Names":["/crewship-2-team-alpha-crew-cpr"]}]`, status: 200, restarted: 2, calls: 1, removed: true},
		{name: "cleanup failure", workspace: "ws-cpr", role: "ADMIN", containers: `[{"Id":"owned-runtime-id","Names":["/crewship-2-team-alpha-crew-cpr"]}]`, cleanupErr: errors.New("private environment cleanup failed"), status: 500, calls: 1},
		{name: "other workspace", workspace: "ws-other", role: "ADMIN", containers: `[{"Id":"owned-runtime-id","Names":["/crewship-2-team-alpha-crew-cpr"]}]`, status: 404},
		{name: "same slug foreign runtime", workspace: "ws-cpr", role: "ADMIN", containers: `[{"Id":"foreign","Names":["/crewship-2-team-alpha-crew-elsewhere"]}]`, status: 200},
		{name: "no runtime", workspace: "ws-cpr", role: "ADMIN", containers: `[]`, status: 200},
		{name: "forbidden", workspace: "ws-cpr", role: "VIEWER", containers: `[{"Id":"owned-runtime-id","Names":["/crewship-2-team-alpha-crew-cpr"]}]`, status: 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := setupTestDB(t)
			_, crewID := covCPRSeed(t, db, 3)
			if _, err := db.Exec(`UPDATE agents SET deleted_at = '2026-10-04T00:00:00Z' WHERE id = 'ag-cpr-c'`); err != nil {
				t.Fatal(err)
			}
			var rawRemovals atomic.Int32
			cli, _ := covCPRFakeDocker(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch {
				case r.Method == http.MethodDelete:
					rawRemovals.Add(1)
					w.WriteHeader(http.StatusNoContent)
				case strings.HasSuffix(r.URL.Path, "/containers/json"):
					_, _ = w.Write([]byte(tc.containers))
				case strings.HasSuffix(r.URL.Path, "/images/json"):
					_, _ = w.Write([]byte(`[]`))
				default:
					t.Errorf("unexpected Docker request %s %s", r.Method, r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			})
			runtime := &restartCleanupProvider{err: tc.cleanupErr}
			router, err := NewRouter(db, "this-is-a-32-char-test-secret-pad", newTestLogger(), WithDockerClient(cli), WithKeeperContainer(runtime))
			if err != nil {
				t.Fatal(err)
			}
			h := router.Provisioning()
			t.Cleanup(h.Stop)
			rec := httptest.NewRecorder()
			// Use the handler built by production route registration, with resolved
			// request auth/workspace context, so missing router wiring is observable.
			h.RestartCrewAgents(rec, covCPRRequest(tc.workspace, tc.role, crewID))
			if rec.Code != tc.status {
				t.Errorf("status = %d, want %d; body=%s", rec.Code, tc.status, rec.Body.String())
			}
			if len(runtime.removed) != tc.calls {
				t.Errorf("provider removals = %v, want %d calls", runtime.removed, tc.calls)
			}
			for _, id := range runtime.removed {
				if id != "owned-runtime-id" {
					t.Errorf("removed %q, want exact resolved owned ID", id)
				}
			}
			if rawRemovals.Load() != 0 {
				t.Errorf("raw Docker removals = %d, want 0", rawRemovals.Load())
			}
			var body map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if tc.status == http.StatusOK {
				if body["restarted"] != float64(tc.restarted) || body["runtime_removed"] != tc.removed {
					t.Errorf("response = %v, want restarted=%d runtime_removed=%v", body, tc.restarted, tc.removed)
				}
			} else if body["error"] == nil || body["runtime_removed"] != nil || body["restarted"] != nil {
				t.Errorf("failure must retain error envelope without success fields: %v", body)
			}
		})
	}
}
