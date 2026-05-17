package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/crewship-ai/crewship/internal/memory"
)

// stubHybridProvider holds a single workspace's engine for the
// hybrid search handler tests. Returns nil for any workspace not
// in the map so the typed-nil-interface gotcha is exercised
// alongside the happy path.
type stubHybridProvider struct {
	engines map[string]*memory.Engine
}

type stubHolder struct{ eng *memory.Engine }

func (h stubHolder) Engine() *memory.Engine { return h.eng }

func (s stubHybridProvider) For(workspaceID string) WorkspaceEngineHolder {
	eng, ok := s.engines[workspaceID]
	if !ok || eng == nil {
		return nil
	}
	return stubHolder{eng: eng}
}

func TestMemoryHybridSearch_FTSOnly_HappyPath(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	// Build a workspace engine with one chunk.
	dir := t.TempDir()
	if err := writeTempFile(dir, "AGENT.md", "outlands shard custom thievery design\n"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	eng, err := memory.New(dir, memory.DefaultConfig())
	if err != nil {
		t.Fatalf("eng: %v", err)
	}
	defer eng.Close()
	if err := eng.Reindex(); err != nil {
		t.Fatalf("reindex: %v", err)
	}

	h := NewMemoryHybridSearchHandler(db, newTestLogger())
	h.SetWorkspaceProvider(stubHybridProvider{engines: map[string]*memory.Engine{wsID: eng}})

	body, _ := json.Marshal(map[string]any{"query": "outlands", "limit": 5})
	req := httptest.NewRequest("POST", "/api/v1/memory/search/hybrid", bytes.NewReader(body))
	req = withWorkspaceUser(req, userID, wsID, "MEMBER")
	rr := httptest.NewRecorder()
	h.Search(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rr.Code, rr.Body.String())
	}
	var resp struct {
		Count int                `json:"count"`
		Hits  []memory.HybridHit `json:"hits"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp.Count == 0 {
		t.Errorf("expected at least one FTS hit, got 0")
	}
	for _, hit := range resp.Hits {
		if hit.Source != "fts" {
			t.Errorf("source = %q, want fts (no embedder wired)", hit.Source)
		}
	}
}

func TestMemoryHybridSearch_MissingWorkspace_401(t *testing.T) {
	db := setupTestDB(t)
	h := NewMemoryHybridSearchHandler(db, newTestLogger())

	body, _ := json.Marshal(map[string]any{"query": "x"})
	req := httptest.NewRequest("POST", "/api/v1/memory/search/hybrid", bytes.NewReader(body))
	// no withWorkspaceUser — context has no workspace
	rr := httptest.NewRecorder()
	h.Search(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rr.Code)
	}
}

func TestMemoryHybridSearch_MissingQuery_400(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewMemoryHybridSearchHandler(db, newTestLogger())

	body, _ := json.Marshal(map[string]any{"limit": 5})
	req := httptest.NewRequest("POST", "/api/v1/memory/search/hybrid", bytes.NewReader(body))
	req = withWorkspaceUser(req, userID, wsID, "MEMBER")
	rr := httptest.NewRecorder()
	h.Search(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}

func TestMemoryHybridSearch_BadJSON_400(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewMemoryHybridSearchHandler(db, newTestLogger())

	req := httptest.NewRequest("POST", "/api/v1/memory/search/hybrid", bytes.NewReader([]byte("not json")))
	req = withWorkspaceUser(req, userID, wsID, "MEMBER")
	rr := httptest.NewRecorder()
	h.Search(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}

func TestMemoryHybridSearch_NoEngines_EmptyResult(t *testing.T) {
	// Neither provider nor embedder wired → memory.HybridSearch
	// returns empty. The handler surfaces that as 200 + count=0,
	// NOT 503 — "no hits" is a legitimate result of a search.
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewMemoryHybridSearchHandler(db, newTestLogger())

	body, _ := json.Marshal(map[string]any{"query": "anything", "limit": 5})
	req := httptest.NewRequest("POST", "/api/v1/memory/search/hybrid", bytes.NewReader(body))
	req = withWorkspaceUser(req, userID, wsID, "MEMBER")
	rr := httptest.NewRecorder()
	h.Search(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 with empty hits", rr.Code)
	}
	var resp struct {
		Count int `json:"count"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp.Count != 0 {
		t.Errorf("count = %d, want 0 with no engines wired", resp.Count)
	}
}

// writeTempFile is a thin os.WriteFile + MkdirAll helper so the
// hybrid tests can plant markdown content in a temp dir before the
// memory.Engine reindexes it.
func writeTempFile(dir, name, body string) error {
	full := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return err
	}
	return os.WriteFile(full, []byte(body), 0o644)
}
