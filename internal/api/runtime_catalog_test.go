package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// RuntimeCatalogList (GET /api/v1/runtimes/catalog) returns the runtime
// catalog, falling back to the baked-in list when no fetcher is wired,
// and applies the ?search filter.

func TestRuntimeCatalogList_Fallback(t *testing.T) {
	h := newTestProvisioningHandler(t)
	req := httptest.NewRequest("GET", "/api/v1/runtimes/catalog", nil)
	rr := httptest.NewRecorder()
	h.RuntimeCatalogList(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var resp struct {
		Runtimes []map[string]any `json:"runtimes"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Runtimes) == 0 {
		t.Errorf("expected non-empty fallback runtime catalog")
	}
}

func TestRuntimeCatalogList_SearchNoMatch(t *testing.T) {
	h := newTestProvisioningHandler(t)
	req := httptest.NewRequest("GET", "/api/v1/runtimes/catalog?search=zzz-no-such-runtime", nil)
	rr := httptest.NewRecorder()
	h.RuntimeCatalogList(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var resp struct {
		Runtimes []map[string]any `json:"runtimes"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Runtimes) != 0 {
		t.Errorf("search for nonsense returned %d runtimes, want 0", len(resp.Runtimes))
	}
}
