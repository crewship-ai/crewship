package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

func newTestProvisioningHandler(t *testing.T) *ProvisioningHandler {
	t.Helper()
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	return NewProvisioningHandler(db, logger, nil, nil)
}

func TestCatalogList(t *testing.T) {
	h := newTestProvisioningHandler(t)

	req := httptest.NewRequest("GET", "/api/v1/features/catalog", nil)
	rr := httptest.NewRecorder()

	h.CatalogList(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}

	var resp struct {
		Features []json.RawMessage `json:"features"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Features) == 0 {
		t.Error("expected non-empty feature catalog")
	}
}

func TestCatalogListSearch(t *testing.T) {
	h := newTestProvisioningHandler(t)

	// Search for "python" — should return results.
	req := httptest.NewRequest("GET", "/api/v1/features/catalog?search=python", nil)
	rr := httptest.NewRecorder()

	h.CatalogList(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}

	var resp struct {
		Features []struct {
			Name string `json:"name"`
		} `json:"features"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Features) == 0 {
		t.Error("expected at least one result for 'python' search")
	}

	// Search for nonsense — should return empty list.
	req2 := httptest.NewRequest("GET", "/api/v1/features/catalog?search=zzz_nonexistent_zzz", nil)
	rr2 := httptest.NewRecorder()

	h.CatalogList(rr2, req2)

	if rr2.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr2.Code, http.StatusOK)
	}

	var resp2 struct {
		Features []json.RawMessage `json:"features"`
	}
	if err := json.NewDecoder(rr2.Body).Decode(&resp2); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp2.Features) != 0 {
		t.Errorf("expected empty results for nonsense query, got %d", len(resp2.Features))
	}
}

func TestProvisionStatus_NoCrew(t *testing.T) {
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	h := NewProvisioningHandler(db, logger, nil, nil)

	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/crews/{crewId}/provisioning", h.ProvisionStatus)

	req := httptest.NewRequest("GET", "/api/v1/crews/nonexistent-crew-id/provisioning", nil)
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr := httptest.NewRecorder()

	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d; body: %s", rr.Code, http.StatusNotFound, rr.Body.String())
	}

	var resp struct {
		Error string `json:"error"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Error == "" {
		t.Error("expected non-empty error message in 404 response")
	}
}
