package api

// Coverage tests for crew_provisioning_restart.go. A fake Docker daemon is
// served over httptest and wired into the handler via the Docker SDK client
// (same pattern as internal/provider/docker/fakeapi_test.go) — no real
// Docker required.

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/docker/docker/client"
)

// covCPRFakeDocker returns a *client.Client pointed at an httptest fake
// Docker daemon plus its server (so tests can Close it early to simulate
// daemon errors).
func covCPRFakeDocker(t *testing.T, handler http.HandlerFunc) (*client.Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	cli, err := client.NewClientWithOpts(
		client.WithHost(srv.URL),
		client.WithVersion("1.43"),
	)
	if err != nil {
		srv.Close()
		t.Fatalf("docker client: %v", err)
	}
	t.Cleanup(func() {
		_ = cli.Close()
		srv.Close()
	})
	return cli, srv
}

// covCPRHandler builds a ProvisioningHandler with just the fields the
// restart endpoint touches. Constructing the struct directly avoids the
// cleanup-ticker goroutine NewProvisioningHandler spawns.
func covCPRHandler(t *testing.T, db *sql.DB, docker *client.Client) *ProvisioningHandler {
	t.Helper()
	return &ProvisioningHandler{
		db:     db,
		logger: slog.New(slog.NewTextHandler(discardWriterCovCPR{}, nil)),
		docker: docker,
	}
}

type discardWriterCovCPR struct{}

func (discardWriterCovCPR) Write(p []byte) (int, error) { return len(p), nil }

// covCPRSeed creates a workspace, crew (slug "alpha") and n live agents.
func covCPRSeed(t *testing.T, db *sql.DB, agents int) (wsID, crewID string) {
	t.Helper()
	wsID, crewID = "ws-cpr", "crew-cpr"
	if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'W', 'w-cpr')`, wsID); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO crews (id, workspace_id, name, slug) VALUES (?, ?, 'Alpha', 'alpha')`, crewID, wsID); err != nil {
		t.Fatalf("seed crew: %v", err)
	}
	for i := 0; i < agents; i++ {
		if _, err := db.Exec(
			`INSERT INTO agents (id, workspace_id, crew_id, name, slug) VALUES (?, ?, ?, ?, ?)`,
			"ag-cpr-"+string(rune('a'+i)), wsID, crewID, "Agent", "agent-"+string(rune('a'+i)),
		); err != nil {
			t.Fatalf("seed agent: %v", err)
		}
	}
	return wsID, crewID
}

func covCPRRequest(wsID, role, crewID string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/crews/"+crewID+"/restart", nil)
	req.SetPathValue("crewId", crewID)
	return req.WithContext(withWorkspace(req.Context(), wsID, role))
}

// --- findCrewContainer ------------------------------------------------------

func TestCovCPRFindCrewContainer(t *testing.T) {
	t.Run("nil docker returns empty", func(t *testing.T) {
		h := covCPRHandler(t, setupTestDB(t), nil)
		id, err := h.findCrewContainer(context.Background(), "alpha")
		if err != nil || id != "" {
			t.Fatalf("want empty/nil, got id=%q err=%v", id, err)
		}
	})

	t.Run("match by name", func(t *testing.T) {
		cli, _ := covCPRFakeDocker(t, func(w http.ResponseWriter, r *http.Request) {
			if !strings.HasSuffix(r.URL.Path, "/containers/json") {
				t.Errorf("unexpected path %s", r.URL.Path)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[
				{"Id":"other","Names":["/something-else"]},
				{"Id":"cid-123","Names":["/crewship-team-alpha"]}
			]`))
		})
		h := covCPRHandler(t, setupTestDB(t), cli)
		id, err := h.findCrewContainer(context.Background(), "alpha")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if id != "cid-123" {
			t.Fatalf("id = %q, want cid-123", id)
		}
	})

	t.Run("no match returns empty", func(t *testing.T) {
		cli, _ := covCPRFakeDocker(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[]`))
		})
		h := covCPRHandler(t, setupTestDB(t), cli)
		id, err := h.findCrewContainer(context.Background(), "alpha")
		if err != nil || id != "" {
			t.Fatalf("want empty/nil, got id=%q err=%v", id, err)
		}
	})

	t.Run("daemon error propagates", func(t *testing.T) {
		cli, _ := covCPRFakeDocker(t, func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, `{"message":"boom"}`, http.StatusInternalServerError)
		})
		h := covCPRHandler(t, setupTestDB(t), cli)
		_, err := h.findCrewContainer(context.Background(), "alpha")
		if err == nil {
			t.Fatal("expected error from daemon failure")
		}
	})
}

// --- agentsPendingRestartCount ----------------------------------------------

func TestCovCPRAgentsPendingRestartCount(t *testing.T) {
	t.Run("no container", func(t *testing.T) {
		db := setupTestDB(t)
		_, crewID := covCPRSeed(t, db, 2)
		cli, _ := covCPRFakeDocker(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[]`))
		})
		h := covCPRHandler(t, db, cli)
		if n := h.agentsPendingRestartCount(context.Background(), crewID, "alpha", "img:new"); n != 0 {
			t.Fatalf("count = %d, want 0", n)
		}
	})

	t.Run("image matches — zero pending", func(t *testing.T) {
		db := setupTestDB(t)
		_, crewID := covCPRSeed(t, db, 2)
		cli, _ := covCPRFakeDocker(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			switch {
			case strings.HasSuffix(r.URL.Path, "/containers/json"):
				_, _ = w.Write([]byte(`[{"Id":"cid-1","Names":["/crewship-team-alpha"]}]`))
			case strings.Contains(r.URL.Path, "/containers/cid-1/json"):
				_, _ = w.Write([]byte(`{"Id":"cid-1","Config":{"Image":"img:current"}}`))
			default:
				t.Errorf("unexpected path %s", r.URL.Path)
			}
		})
		h := covCPRHandler(t, db, cli)
		if n := h.agentsPendingRestartCount(context.Background(), crewID, "alpha", "img:current"); n != 0 {
			t.Fatalf("count = %d, want 0", n)
		}
	})

	t.Run("stale image counts live agents", func(t *testing.T) {
		db := setupTestDB(t)
		_, crewID := covCPRSeed(t, db, 3)
		cli, _ := covCPRFakeDocker(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			switch {
			case strings.HasSuffix(r.URL.Path, "/containers/json"):
				_, _ = w.Write([]byte(`[{"Id":"cid-1","Names":["/crewship-team-alpha"]}]`))
			case strings.Contains(r.URL.Path, "/containers/cid-1/json"):
				_, _ = w.Write([]byte(`{"Id":"cid-1","Config":{"Image":"img:old"}}`))
			}
		})
		h := covCPRHandler(t, db, cli)
		if n := h.agentsPendingRestartCount(context.Background(), crewID, "alpha", "img:new"); n != 3 {
			t.Fatalf("count = %d, want 3", n)
		}
	})

	t.Run("inspect error returns zero", func(t *testing.T) {
		db := setupTestDB(t)
		_, crewID := covCPRSeed(t, db, 2)
		cli, _ := covCPRFakeDocker(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			if strings.HasSuffix(r.URL.Path, "/containers/json") {
				_, _ = w.Write([]byte(`[{"Id":"cid-1","Names":["/crewship-team-alpha"]}]`))
				return
			}
			http.Error(w, `{"message":"inspect failed"}`, http.StatusInternalServerError)
		})
		h := covCPRHandler(t, db, cli)
		if n := h.agentsPendingRestartCount(context.Background(), crewID, "alpha", "img:new"); n != 0 {
			t.Fatalf("count = %d, want 0", n)
		}
	})

	t.Run("list error returns zero", func(t *testing.T) {
		db := setupTestDB(t)
		_, crewID := covCPRSeed(t, db, 2)
		cli, _ := covCPRFakeDocker(t, func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, `{"message":"down"}`, http.StatusInternalServerError)
		})
		h := covCPRHandler(t, db, cli)
		if n := h.agentsPendingRestartCount(context.Background(), crewID, "alpha", "img:new"); n != 0 {
			t.Fatalf("count = %d, want 0", n)
		}
	})
}

// --- RestartCrewAgents -------------------------------------------------------

func TestCovCPRRestartCrewAgents(t *testing.T) {
	t.Run("forbidden role", func(t *testing.T) {
		h := covCPRHandler(t, setupTestDB(t), nil)
		rec := httptest.NewRecorder()
		h.RestartCrewAgents(rec, covCPRRequest("ws-cpr", "VIEWER", "crew-cpr"))
		if rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403", rec.Code)
		}
	})

	t.Run("docker unavailable 503", func(t *testing.T) {
		h := covCPRHandler(t, setupTestDB(t), nil)
		rec := httptest.NewRecorder()
		h.RestartCrewAgents(rec, covCPRRequest("ws-cpr", "ADMIN", "crew-cpr"))
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want 503; body=%s", rec.Code, rec.Body.String())
		}
	})

	t.Run("missing crew id 400", func(t *testing.T) {
		cli, _ := covCPRFakeDocker(t, func(w http.ResponseWriter, r *http.Request) {})
		h := covCPRHandler(t, setupTestDB(t), cli)
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/crews//restart", nil)
		req = req.WithContext(withWorkspace(req.Context(), "ws-cpr", "ADMIN"))
		h.RestartCrewAgents(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", rec.Code)
		}
	})

	t.Run("crew not found 404", func(t *testing.T) {
		db := setupTestDB(t)
		covCPRSeed(t, db, 1)
		cli, _ := covCPRFakeDocker(t, func(w http.ResponseWriter, r *http.Request) {})
		h := covCPRHandler(t, db, cli)
		rec := httptest.NewRecorder()
		h.RestartCrewAgents(rec, covCPRRequest("ws-cpr", "ADMIN", "no-such-crew"))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404; body=%s", rec.Code, rec.Body.String())
		}
	})

	t.Run("no container running — restarted 0", func(t *testing.T) {
		db := setupTestDB(t)
		_, crewID := covCPRSeed(t, db, 2)
		cli, _ := covCPRFakeDocker(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[]`))
		})
		h := covCPRHandler(t, db, cli)
		rec := httptest.NewRecorder()
		h.RestartCrewAgents(rec, covCPRRequest("ws-cpr", "ADMIN", crewID))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
		}
		var resp map[string]int
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if resp["restarted"] != 0 {
			t.Fatalf("restarted = %d, want 0", resp["restarted"])
		}
	})

	t.Run("container removed — restarted count", func(t *testing.T) {
		db := setupTestDB(t)
		_, crewID := covCPRSeed(t, db, 2)
		removed := false
		cli, _ := covCPRFakeDocker(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			switch {
			case strings.HasSuffix(r.URL.Path, "/containers/json"):
				_, _ = w.Write([]byte(`[{"Id":"cid-9","Names":["/crewship-team-alpha"]}]`))
			case r.Method == http.MethodDelete && strings.Contains(r.URL.Path, "/containers/cid-9"):
				if r.URL.Query().Get("force") != "1" {
					t.Errorf("expected force=1 removal, got query %q", r.URL.RawQuery)
				}
				removed = true
				w.WriteHeader(http.StatusNoContent)
			default:
				t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			}
		})
		h := covCPRHandler(t, db, cli)
		rec := httptest.NewRecorder()
		h.RestartCrewAgents(rec, covCPRRequest("ws-cpr", "ADMIN", crewID))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
		}
		if !removed {
			t.Fatal("expected ContainerRemove call")
		}
		var resp map[string]int
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if resp["restarted"] != 2 {
			t.Fatalf("restarted = %d, want 2", resp["restarted"])
		}
	})

	t.Run("list error 500", func(t *testing.T) {
		db := setupTestDB(t)
		_, crewID := covCPRSeed(t, db, 1)
		cli, _ := covCPRFakeDocker(t, func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, `{"message":"down"}`, http.StatusInternalServerError)
		})
		h := covCPRHandler(t, db, cli)
		rec := httptest.NewRecorder()
		h.RestartCrewAgents(rec, covCPRRequest("ws-cpr", "ADMIN", crewID))
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want 500", rec.Code)
		}
	})

	t.Run("remove error 500", func(t *testing.T) {
		db := setupTestDB(t)
		_, crewID := covCPRSeed(t, db, 1)
		cli, _ := covCPRFakeDocker(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			if strings.HasSuffix(r.URL.Path, "/containers/json") {
				_, _ = w.Write([]byte(`[{"Id":"cid-9","Names":["/crewship-team-alpha"]}]`))
				return
			}
			http.Error(w, `{"message":"cannot remove"}`, http.StatusConflict)
		})
		h := covCPRHandler(t, db, cli)
		rec := httptest.NewRecorder()
		h.RestartCrewAgents(rec, covCPRRequest("ws-cpr", "ADMIN", crewID))
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want 500; body=%s", rec.Code, rec.Body.String())
		}
	})
}
