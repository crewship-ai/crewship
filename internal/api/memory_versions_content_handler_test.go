package api

// Coverage for the memory_versions content endpoint (Iter 8).
// Verifies the full failure-mode tree: auth gates, missing row,
// cross-workspace probe, missing blob, sha mismatch, oversize,
// path-traversal defence.

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

var memContentCounter atomic.Int64

type contentRig struct {
	h        *MemoryVersionsContentHandler
	db       *sql.DB
	userID   string
	wsID     string
	blobRoot string
}

func contentTestRig(t *testing.T) contentRig {
	t.Helper()
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	blobRoot := t.TempDir()
	h := NewMemoryVersionsContentHandler(db, newTestLogger(), blobRoot)
	return contentRig{h: h, db: db, userID: userID, wsID: wsID, blobRoot: blobRoot}
}

// seedContentRow writes a real blob to disk + the corresponding
// memory_versions row. The sha is computed from `content` so
// the integrity check passes. Returns the row id.
func seedContentRow(t *testing.T, r contentRig, path, tier string, content []byte) string {
	t.Helper()
	sum := sha256.Sum256(content)
	sha := hex.EncodeToString(sum[:])
	shard := filepath.Join(r.blobRoot, sha[:2])
	if err := os.MkdirAll(shard, 0o755); err != nil {
		t.Fatalf("mkdir shard: %v", err)
	}
	blobPath := filepath.Join(shard, sha)
	if err := os.WriteFile(blobPath, content, 0o644); err != nil {
		t.Fatalf("write blob: %v", err)
	}
	id := fmt.Sprintf("mv_content_%d", memContentCounter.Add(1))
	if _, err := r.db.Exec(`
		INSERT INTO memory_versions
		(id, workspace_id, path, tier, sha256, bytes, payload_ref, written_at, written_by)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, r.wsID, path, tier, sha, len(content), blobPath,
		time.Now().UTC().Format(time.RFC3339Nano), "audit-watcher",
	); err != nil {
		t.Fatalf("seed row: %v", err)
	}
	return id
}

func contentRequest(t *testing.T, r contentRig, role, versionID string) *httptest.ResponseRecorder {
	t.Helper()
	req := withWorkspaceUser(
		httptest.NewRequest("GET", "/api/v1/admin/memory/versions/"+versionID+"/content", nil),
		r.userID, r.wsID, role,
	)
	req.SetPathValue("id", versionID)
	rr := httptest.NewRecorder()
	r.h.Content(rr, req)
	return rr
}

func TestMemoryVersionContent_HappyPath_BodyAndIntegrityHeaders(t *testing.T) {
	r := contentTestRig(t)
	body := []byte("# Notes\n\nHello world.\n")
	id := seedContentRow(t, r, "agent:martin/AGENT.md", "agent", body)

	rr := contentRequest(t, r, "OWNER", id)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	if got := rr.Body.String(); got != string(body) {
		t.Errorf("body = %q, want %q", got, body)
	}
	// Integrity + provenance headers.
	sum := sha256.Sum256(body)
	wantSha := hex.EncodeToString(sum[:])
	if got := rr.Header().Get("X-Memory-Sha256"); got != wantSha {
		t.Errorf("X-Memory-Sha256 = %q, want %q", got, wantSha)
	}
	if got := rr.Header().Get("X-Memory-Bytes"); got != fmt.Sprintf("%d", len(body)) {
		t.Errorf("X-Memory-Bytes = %q, want %d", got, len(body))
	}
	if got := rr.Header().Get("X-Memory-Tier"); got != "agent" {
		t.Errorf("X-Memory-Tier = %q, want agent", got)
	}
	if got := rr.Header().Get("X-Memory-Path"); got != "agent:martin/AGENT.md" {
		t.Errorf("X-Memory-Path = %q, want canonical path", got)
	}
	if got := rr.Header().Get("X-Memory-Written-By"); got != "audit-watcher" {
		t.Errorf("X-Memory-Written-By = %q, want audit-watcher", got)
	}
	// .md → text/markdown
	if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/markdown") {
		t.Errorf("Content-Type = %q, want text/markdown for .md path", ct)
	}
}

func TestMemoryVersionContent_NonMarkdownPath_OctetStream(t *testing.T) {
	r := contentTestRig(t)
	id := seedContentRow(t, r, "agent:martin/data.bin", "agent", []byte("binary"))
	rr := contentRequest(t, r, "OWNER", id)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if ct := rr.Header().Get("Content-Type"); ct != "application/octet-stream" {
		t.Errorf("Content-Type = %q; want application/octet-stream for non-.md path", ct)
	}
}

func TestMemoryVersionContent_Preconditions(t *testing.T) {
	r := contentTestRig(t)
	id := seedContentRow(t, r, "agent:a/AGENT.md", "agent", []byte("x"))

	cases := []struct {
		name string
		role string
		ws   string
		id   string
		want int
	}{
		{name: "member_forbidden", role: "MEMBER", ws: r.wsID, id: id, want: http.StatusForbidden},
		{name: "missing_workspace", role: "OWNER", ws: "", id: id, want: http.StatusBadRequest},
		{name: "missing_id", role: "OWNER", ws: r.wsID, id: "", want: http.StatusBadRequest},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/api/v1/admin/memory/versions/"+tc.id+"/content", nil)
			req.SetPathValue("id", tc.id)
			ctx := withUser(req.Context(), &AuthUser{ID: r.userID, Email: r.userID + "@example.com"})
			ctx = withWorkspace(ctx, tc.ws, tc.role)
			rr := httptest.NewRecorder()
			r.h.Content(rr, req.WithContext(ctx))
			if rr.Code != tc.want {
				t.Fatalf("status = %d, want %d", rr.Code, tc.want)
			}
		})
	}
}

func TestMemoryVersionContent_UnknownID_Returns404(t *testing.T) {
	r := contentTestRig(t)
	rr := contentRequest(t, r, "OWNER", "mv_nosuch")
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rr.Code)
	}
}

func TestMemoryVersionContent_CrossWorkspace_Returns404NotLeaky(t *testing.T) {
	r := contentTestRig(t)
	id := seedContentRow(t, r, "agent:a/AGENT.md", "agent", []byte("secret"))

	// Inline second tenant — cross-workspace probe MUST 404,
	// not 403 / 200. 403 would leak that the id exists; 200
	// would leak the content.
	otherUserID := "test-other-user-id"
	otherWS := "test-other-workspace-id"
	if _, err := r.db.Exec(`INSERT INTO users (id, email, full_name) VALUES (?, 'b@example.com', 'B')`, otherUserID); err != nil {
		t.Fatalf("seed other user: %v", err)
	}
	if _, err := r.db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'B', 'b')`, otherWS); err != nil {
		t.Fatalf("seed other ws: %v", err)
	}
	if _, err := r.db.Exec(`INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES ('m_b', ?, ?, 'OWNER')`, otherWS, otherUserID); err != nil {
		t.Fatalf("seed other member: %v", err)
	}

	req := httptest.NewRequest("GET", "/api/v1/admin/memory/versions/"+id+"/content", nil)
	req.SetPathValue("id", id)
	req = withWorkspaceUser(req, otherUserID, otherWS, "OWNER")
	rr := httptest.NewRecorder()
	r.h.Content(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (cross-workspace probe)", rr.Code)
	}
}

func TestMemoryVersionContent_MissingBlob_Returns410(t *testing.T) {
	r := contentTestRig(t)
	body := []byte("survives until disk")
	id := seedContentRow(t, r, "agent:a/AGENT.md", "agent", body)

	// Look up the blob path from the row and delete the file
	// out-of-band — simulates retention sweep / container
	// rebuild / restore-from-backup race.
	var payloadRef string
	if err := r.db.QueryRow(`SELECT payload_ref FROM memory_versions WHERE id = ?`, id).Scan(&payloadRef); err != nil {
		t.Fatalf("read payload_ref: %v", err)
	}
	if err := os.Remove(payloadRef); err != nil {
		t.Fatalf("rm blob: %v", err)
	}

	rr := contentRequest(t, r, "OWNER", id)
	if rr.Code != http.StatusGone {
		t.Fatalf("status = %d, want 410 (blob missing on disk)", rr.Code)
	}
}

func TestMemoryVersionContent_ShaMismatch_Returns500(t *testing.T) {
	r := contentTestRig(t)
	id := seedContentRow(t, r, "agent:a/AGENT.md", "agent", []byte("original content"))

	// Tamper with the blob on disk — emulates a corruption.
	var payloadRef string
	if err := r.db.QueryRow(`SELECT payload_ref FROM memory_versions WHERE id = ?`, id).Scan(&payloadRef); err != nil {
		t.Fatalf("read payload_ref: %v", err)
	}
	if err := os.WriteFile(payloadRef, []byte("TAMPERED"), 0o644); err != nil {
		t.Fatalf("tamper blob: %v", err)
	}

	rr := contentRequest(t, r, "OWNER", id)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 on sha mismatch", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "sha mismatch") {
		t.Errorf("error body should mention sha mismatch; got %q", rr.Body.String())
	}
}

func TestMemoryVersionContent_OversizeRow_Returns413(t *testing.T) {
	r := contentTestRig(t)
	// Seed a row claiming a size above the cap. The blob
	// itself doesn't need to be that big — the cap is enforced
	// from the stored bytes column before the read happens,
	// which is exactly the design (don't let a corrupt row
	// trigger a multi-GB read).
	body := []byte("small in reality")
	sum := sha256.Sum256(body)
	sha := hex.EncodeToString(sum[:])
	shard := filepath.Join(r.blobRoot, sha[:2])
	_ = os.MkdirAll(shard, 0o755)
	blobPath := filepath.Join(shard, sha)
	_ = os.WriteFile(blobPath, body, 0o644)
	id := fmt.Sprintf("mv_oversize_%d", memContentCounter.Add(1))
	if _, err := r.db.Exec(`
		INSERT INTO memory_versions
		(id, workspace_id, path, tier, sha256, bytes, payload_ref, written_at)
		VALUES (?, ?, 'agent:big/file.md', 'agent', ?, ?, ?, ?)`,
		id, r.wsID, sha, memVersionsContentMaxBytes+1, blobPath,
		time.Now().UTC().Format(time.RFC3339Nano),
	); err != nil {
		t.Fatalf("seed oversize row: %v", err)
	}

	rr := contentRequest(t, r, "OWNER", id)
	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", rr.Code)
	}
}

func TestMemoryVersionContent_PayloadOutsideBlobRoot_Returns500(t *testing.T) {
	// Defence-in-depth: a row whose payload_ref points OUTSIDE
	// blobRoot must be refused. A malicious or corrupted INSERT
	// trying to use payload_ref as a path-traversal vector
	// (e.g. /etc/passwd) cannot exfiltrate host files through
	// this endpoint.
	r := contentTestRig(t)
	// Write a fake file outside the blob root.
	outside := filepath.Join(t.TempDir(), "outsider")
	body := []byte("outside the blob root")
	if err := os.WriteFile(outside, body, 0o644); err != nil {
		t.Fatalf("seed outsider: %v", err)
	}
	sum := sha256.Sum256(body)
	sha := hex.EncodeToString(sum[:])
	id := fmt.Sprintf("mv_traversal_%d", memContentCounter.Add(1))
	if _, err := r.db.Exec(`
		INSERT INTO memory_versions
		(id, workspace_id, path, tier, sha256, bytes, payload_ref, written_at)
		VALUES (?, ?, 'agent:evil/AGENT.md', 'agent', ?, ?, ?, ?)`,
		id, r.wsID, sha, len(body), outside,
		time.Now().UTC().Format(time.RFC3339Nano),
	); err != nil {
		t.Fatalf("seed row: %v", err)
	}

	rr := contentRequest(t, r, "OWNER", id)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 on outside-root payload_ref", rr.Code)
	}
}

func TestMemoryVersionContent_BlobRootUnset_Returns503(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	// Blob root deliberately empty — simulates lite-mode
	// deployment with versioning disabled.
	h := NewMemoryVersionsContentHandler(db, newTestLogger(), "")

	req := withWorkspaceUser(
		httptest.NewRequest("GET", "/api/v1/admin/memory/versions/any/content", nil),
		userID, wsID, "OWNER",
	)
	req.SetPathValue("id", "any")
	rr := httptest.NewRecorder()
	h.Content(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 when blob root not configured", rr.Code)
	}
}
