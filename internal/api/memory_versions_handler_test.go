package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/crewship-ai/crewship/internal/memory"
)

// seedMemoryVersion records one memory_versions row + blob via the
// production path so each handler test starts from realistic state.
// Returns sha for the row.
func seedMemoryVersion(t *testing.T, db *sql.DB, blobRoot, workspaceID, path, body, writtenBy string) string {
	t.Helper()
	res, err := memory.RecordVersion(context.Background(), db, memory.VersionRecord{
		WorkspaceID: workspaceID,
		Path:        path,
		Tier:        memory.TierLearned,
		Content:     []byte(body),
		WrittenBy:   writtenBy,
		BlobRoot:    blobRoot,
	})
	if err != nil {
		t.Fatalf("seed memory version: %v", err)
	}
	return res.Sha256
}

func newMemVerHandlerTest(t *testing.T) (*MemoryVersionsHandler, *sql.DB, string, string, string) {
	t.Helper()
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	blobRoot := t.TempDir()
	h := NewMemoryVersionsHandler(db, newTestLogger())
	h.SetBlobRoot(blobRoot)
	return h, db, userID, wsID, blobRoot
}

func TestMemoryVersions_List_HappyPath(t *testing.T) {
	h, db, userID, wsID, blobRoot := newMemVerHandlerTest(t)
	seedMemoryVersion(t, db, blobRoot, wsID, "crew:c1/learned-X.md", "v1", "a1")
	seedMemoryVersion(t, db, blobRoot, wsID, "crew:c1/learned-X.md", "v2", "a1")

	req := httptest.NewRequest("GET", "/api/v1/memory/versions?path=crew:c1/learned-X.md", nil)
	req = withWorkspaceUser(req, userID, wsID, "MEMBER")
	rr := httptest.NewRecorder()
	h.List(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rr.Code, rr.Body.String())
	}
	var resp struct {
		Count   int                   `json:"count"`
		Entries []memory.VersionEntry `json:"entries"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Count != 2 {
		t.Errorf("count = %d, want 2", resp.Count)
	}
	// Newest-first.
	wantSha := sha256.Sum256([]byte("v2"))
	if resp.Entries[0].Sha256 != hex.EncodeToString(wantSha[:]) {
		t.Errorf("entries[0].Sha256 = %q, want sha(v2)", resp.Entries[0].Sha256)
	}
}

func TestMemoryVersions_List_MissingPath_400(t *testing.T) {
	h, _, userID, wsID, _ := newMemVerHandlerTest(t)
	req := httptest.NewRequest("GET", "/api/v1/memory/versions", nil)
	req = withWorkspaceUser(req, userID, wsID, "MEMBER")
	rr := httptest.NewRecorder()
	h.List(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}

func TestMemoryVersions_List_CrossWorkspace_Empty(t *testing.T) {
	h, db, _, wsID, blobRoot := newMemVerHandlerTest(t)

	// Seed in OTHER workspace.
	otherWS := "ws_other"
	if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'Other', 'other')`, otherWS); err != nil {
		t.Fatalf("seed other ws: %v", err)
	}
	seedMemoryVersion(t, db, blobRoot, otherWS, "crew:o1/learned.md", "leak", "a1")

	// New user that belongs to wsID, NOT otherWS.
	if _, err := db.Exec(`INSERT INTO users (id, email, full_name) VALUES ('viewer','v@x','V')`); err != nil {
		t.Fatalf("seed user: %v", err)
	}

	req := httptest.NewRequest("GET", "/api/v1/memory/versions?path=crew:o1/learned.md", nil)
	req = withWorkspaceUser(req, "viewer", wsID, "MEMBER")
	rr := httptest.NewRecorder()
	h.List(rr, req)

	// Workspace-anchored query — caller in wsID sees zero rows for
	// the other-workspace path. 200 OK with empty list, not 404, so
	// the existence of the cross-workspace row isn't observable.
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 with empty list", rr.Code)
	}
	var resp struct {
		Count int `json:"count"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp.Count != 0 {
		t.Errorf("cross-workspace list returned %d entries, want 0", resp.Count)
	}
}

func TestMemoryVersions_Show_StreamsContent(t *testing.T) {
	h, db, userID, wsID, blobRoot := newMemVerHandlerTest(t)
	sha := seedMemoryVersion(t, db, blobRoot, wsID, "crew:c1/AGENT.md", "hello show", "a1")

	req := httptest.NewRequest("GET", "/api/v1/memory/versions/"+sha+"?path=crew:c1/AGENT.md", nil)
	req.SetPathValue("sha", sha)
	req = withWorkspaceUser(req, userID, wsID, "MEMBER")
	rr := httptest.NewRecorder()
	h.Show(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get("Content-Type"); got != "application/octet-stream" {
		t.Errorf("Content-Type = %q, want application/octet-stream", got)
	}
	if got := rr.Header().Get("X-Memory-Version-Sha"); got != sha {
		t.Errorf("X-Memory-Version-Sha = %q, want %q", got, sha)
	}
	if rr.Body.String() != "hello show" {
		t.Errorf("body = %q, want %q", rr.Body.String(), "hello show")
	}
}

func TestMemoryVersions_Show_UnknownSha_404(t *testing.T) {
	h, _, userID, wsID, _ := newMemVerHandlerTest(t)
	req := httptest.NewRequest("GET", "/api/v1/memory/versions/deadbeef?path=any.md", nil)
	req.SetPathValue("sha", "deadbeef")
	req = withWorkspaceUser(req, userID, wsID, "MEMBER")
	rr := httptest.NewRecorder()
	h.Show(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rr.Code)
	}
}

func TestMemoryVersions_Restore_HappyPath(t *testing.T) {
	h, db, userID, wsID, blobRoot := newMemVerHandlerTest(t)
	// Two versions; we'll restore back to v1.
	v1Sha := seedMemoryVersion(t, db, blobRoot, wsID, "crew:c1/learned-X.md", "v1 content", "a1")
	seedMemoryVersion(t, db, blobRoot, wsID, "crew:c1/learned-X.md", "v2 content (wrong)", "a1")

	canonicalDir := t.TempDir()
	canonicalPath := filepath.Join(canonicalDir, "learned-X.md")
	body := map[string]string{
		"path":           "crew:c1/learned-X.md",
		"canonical_path": canonicalPath,
		"tier":           "learned",
	}
	bodyBytes, _ := json.Marshal(body)

	req := httptest.NewRequest("POST", "/api/v1/memory/versions/"+v1Sha+"/restore", bytes.NewReader(bodyBytes))
	req.SetPathValue("sha", v1Sha)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Restore(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rr.Code, rr.Body.String())
	}
	// Canonical file now matches v1.
	got, err := os.ReadFile(canonicalPath)
	if err != nil {
		t.Fatalf("read canonical: %v", err)
	}
	if string(got) != "v1 content" {
		t.Errorf("canonical = %q, want v1 content", got)
	}
	// A fresh memory_versions row landed for the restore event.
	var rowCount int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM memory_versions WHERE workspace_id = ? AND path = ?`,
		wsID, "crew:c1/learned-X.md").Scan(&rowCount); err != nil {
		t.Fatalf("count: %v", err)
	}
	if rowCount != 3 {
		t.Errorf("post-restore row count = %d, want 3", rowCount)
	}
}

func TestMemoryVersions_Restore_NonOwner_403(t *testing.T) {
	h, db, userID, wsID, blobRoot := newMemVerHandlerTest(t)
	sha := seedMemoryVersion(t, db, blobRoot, wsID, "crew:c1/AGENT.md", "x", "a1")

	body, _ := json.Marshal(map[string]string{
		"path":           "crew:c1/AGENT.md",
		"canonical_path": filepath.Join(t.TempDir(), "out.md"),
		"tier":           "agent",
	})
	req := httptest.NewRequest("POST", "/api/v1/memory/versions/"+sha+"/restore", bytes.NewReader(body))
	req.SetPathValue("sha", sha)
	req = withWorkspaceUser(req, userID, wsID, "MEMBER")
	rr := httptest.NewRecorder()
	h.Restore(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rr.Code)
	}
}

func TestMemoryVersions_Restore_InvalidTier_400(t *testing.T) {
	h, db, userID, wsID, blobRoot := newMemVerHandlerTest(t)
	sha := seedMemoryVersion(t, db, blobRoot, wsID, "crew:c1/AGENT.md", "x", "a1")

	body, _ := json.Marshal(map[string]string{
		"path":           "crew:c1/AGENT.md",
		"canonical_path": filepath.Join(t.TempDir(), "out.md"),
		"tier":           "bogus",
	})
	req := httptest.NewRequest("POST", "/api/v1/memory/versions/"+sha+"/restore", bytes.NewReader(body))
	req.SetPathValue("sha", sha)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Restore(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}

func TestMemoryVersions_Restore_NoBlobRoot_503(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewMemoryVersionsHandler(db, newTestLogger())
	// SetBlobRoot intentionally NOT called

	body, _ := json.Marshal(map[string]string{
		"path":           "crew:c1/AGENT.md",
		"canonical_path": filepath.Join(t.TempDir(), "out.md"),
		"tier":           "agent",
	})
	req := httptest.NewRequest("POST", "/api/v1/memory/versions/abc/restore", bytes.NewReader(body))
	req.SetPathValue("sha", "abc")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Restore(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rr.Code)
	}
}

func TestMemoryVersions_Restore_MalformedJSON_400(t *testing.T) {
	h, _, userID, wsID, _ := newMemVerHandlerTest(t)
	req := httptest.NewRequest("POST", "/api/v1/memory/versions/abc/restore", bytes.NewReader([]byte("not json")))
	req.SetPathValue("sha", "abc")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Restore(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}
