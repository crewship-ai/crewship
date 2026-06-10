package api

// PR-F24 hardening, round 2 — residual closure for the workspace-bound
// X-Internal-Token (findings R-1, R-2).
//
// R-1: RecordMCPToolCall reads body.workspace_id and inserts an audit
// row without consulting the token binding — a ws-A bound token could
// write mcp_tool_calls rows attributed to ws-B.
//
// R-2: the cross-crew messaging surface (SendMessage / ListMessages /
// ReadFile / WriteFile) authorizes purely via active crew_connections
// rows and never consults the binding — a captured ws-A bound token
// could read/send messages and read/write shared files for crews of a
// foreign workspace, as long as those crews are connected to each
// other.
//
// Same exploit shape as the F-1…F-6 suite: seed two workspaces, drive
// the handler through the real requireInternal chain with a ws-A-bound
// token, and assert the ws-B row/file is unreachable — while the
// legitimate same-workspace sidecar path keeps working.

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// seedCrewPairs adds a second crew to each workspace plus active
// bidirectional connections (A↔A2 in ws-A, B↔B2 in ws-B) so the
// pre-fix canCommunicate gate passes for both tenants.
func seedCrewPairs(t *testing.T, h *InternalHandler, ids scopeIDs) (crewA2, crewB2 string) {
	t.Helper()
	crewA2, crewB2 = "crew_a2", "crew_b2"
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := h.db.ExecContext(context.Background(), q, args...); err != nil {
			t.Fatalf("seed exec failed: %v\nquery: %s", err, q)
		}
	}
	for _, c := range []struct{ id, ws, slug string }{
		{crewA2, ids.wsA, "crewa2"}, {crewB2, ids.wsB, "crewb2"},
	} {
		exec(`INSERT INTO crews (id, workspace_id, name, slug, issue_prefix) VALUES (?, ?, ?, ?, 'PRE')`,
			c.id, c.ws, c.id, c.slug)
	}
	for _, conn := range []struct{ id, ws, from, to string }{
		{"conn_a", ids.wsA, ids.crewA, crewA2},
		{"conn_b", ids.wsB, ids.crewB, crewB2},
	} {
		exec(`INSERT INTO crew_connections (id, workspace_id, from_crew_id, to_crew_id, direction, status, created_at, updated_at)
		      VALUES (?, ?, ?, ?, 'bidirectional', 'active', datetime('now'), datetime('now'))`,
			conn.id, conn.ws, conn.from, conn.to)
	}
	return crewA2, crewB2
}

// ---------------------------------------------------------------------------
// R-1 MEDIUM — RecordMCPToolCall must reject a foreign body workspace.
// ws-A token POSTing an mcp_tool_calls audit row into ws-B must 403 and
// leave no row behind.
// ---------------------------------------------------------------------------
func TestSecBinding_R1_MCPToolCallForeignBody403(t *testing.T) {
	h, ids := seedScope(t)
	body, _ := json.Marshal(map[string]string{
		"workspace_id":  ids.wsB,
		"agent_id":      ids.agentB,
		"crew_id":       ids.crewB,
		"mcp_server_id": "mcp_x",
		"tool_name":     "exec",
		"status":        "success",
	})
	rr := httptest.NewRecorder()
	req := boundReq(http.MethodPost, "/x", body, scopeMaster, ids.wsA)
	h.requireInternal(http.HandlerFunc(h.RecordMCPToolCall)).ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (ws-A token must not write ws-B mcp_tool_calls); body=%s",
			rr.Code, rr.Body.String())
	}
	var n int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM mcp_tool_calls WHERE workspace_id = ?`, ids.wsB).Scan(&n); err != nil {
		t.Fatalf("count mcp_tool_calls: %v", err)
	}
	if n != 0 {
		t.Fatalf("ws-B mcp_tool_calls row written cross-tenant: got %d rows, want 0", n)
	}
}

// Own-workspace audit write still works.
func TestSecBinding_R1_MCPToolCallOwnWorkspace201(t *testing.T) {
	h, ids := seedScope(t)
	body, _ := json.Marshal(map[string]string{
		"workspace_id":  ids.wsA,
		"agent_id":      ids.agentA,
		"crew_id":       ids.crewA,
		"mcp_server_id": "mcp_x",
		"tool_name":     "exec",
		"status":        "success",
	})
	rr := httptest.NewRecorder()
	req := boundReq(http.MethodPost, "/x", body, scopeMaster, ids.wsA)
	h.requireInternal(http.HandlerFunc(h.RecordMCPToolCall)).ServeHTTP(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 for own-workspace audit write; body=%s", rr.Code, rr.Body.String())
	}
}

// ---------------------------------------------------------------------------
// R-2 MEDIUM — crew messaging surface must honor the token binding.
// ---------------------------------------------------------------------------

// ws-A token sending a message between two connected ws-B crews → 403,
// no row stored.
func TestSecBinding_R2_SendMessageForeignCrew403(t *testing.T) {
	h, ids := seedScope(t)
	_, crewB2 := seedCrewPairs(t, h, ids)
	mh := NewCrewMessagingHandler(h.db, t.TempDir(), testLogger())

	body, _ := json.Marshal(map[string]string{
		"from_crew_id": ids.crewB,
		"to_crew_id":   crewB2,
		"workspace_id": ids.wsB,
		"content":      "forged",
	})
	rr := httptest.NewRecorder()
	req := boundReq(http.MethodPost, "/x", body, scopeMaster, ids.wsA)
	h.requireInternal(http.HandlerFunc(mh.SendMessage)).ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (ws-A token must not send as a ws-B crew); body=%s",
			rr.Code, rr.Body.String())
	}
	var n int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM crew_messages WHERE workspace_id = ?`, ids.wsB).Scan(&n); err != nil {
		t.Fatalf("count crew_messages: %v", err)
	}
	if n != 0 {
		t.Fatalf("ws-B crew_messages row written cross-tenant: got %d rows, want 0", n)
	}
}

// The legitimate sidecar path — a ws-A bound token sending between two
// connected ws-A crews — must keep working.
func TestSecBinding_R2_SendMessageSameWorkspaceOK(t *testing.T) {
	h, ids := seedScope(t)
	crewA2, _ := seedCrewPairs(t, h, ids)
	mh := NewCrewMessagingHandler(h.db, t.TempDir(), testLogger())

	body, _ := json.Marshal(map[string]string{
		"from_crew_id": ids.crewA,
		"to_crew_id":   crewA2,
		"workspace_id": ids.wsA,
		"content":      "hello neighbor",
	})
	rr := httptest.NewRecorder()
	req := boundReq(http.MethodPost, "/x", body, scopeMaster, ids.wsA)
	h.requireInternal(http.HandlerFunc(mh.SendMessage)).ServeHTTP(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 for same-workspace send; body=%s", rr.Code, rr.Body.String())
	}
}

// ws-A token listing a ws-B crew's inbox → 403, no message content leaked.
func TestSecBinding_R2_ListMessagesForeignCrew403(t *testing.T) {
	h, ids := seedScope(t)
	_, crewB2 := seedCrewPairs(t, h, ids)
	mh := NewCrewMessagingHandler(h.db, t.TempDir(), testLogger())

	if _, err := h.db.Exec(`
		INSERT INTO crew_messages (id, workspace_id, from_crew_id, to_crew_id, content, created_at)
		VALUES ('msg_b1', ?, ?, ?, 'ws-b-secret-payload', datetime('now'))`,
		ids.wsB, ids.crewB, crewB2); err != nil {
		t.Fatalf("seed ws-B message: %v", err)
	}

	rr := httptest.NewRecorder()
	req := boundReq(http.MethodGet, "/x?crew_id="+crewB2, nil, scopeMaster, ids.wsA)
	h.requireInternal(http.HandlerFunc(mh.ListMessages)).ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (ws-A token must not list a ws-B crew's messages); body=%s",
			rr.Code, rr.Body.String())
	}
	if bytes.Contains(rr.Body.Bytes(), []byte("ws-b-secret-payload")) {
		t.Fatalf("ws-B message content leaked across tenant boundary: %s", rr.Body.String())
	}
}

// Same-workspace inbox listing with a bound token keeps working.
func TestSecBinding_R2_ListMessagesSameWorkspaceOK(t *testing.T) {
	h, ids := seedScope(t)
	crewA2, _ := seedCrewPairs(t, h, ids)
	mh := NewCrewMessagingHandler(h.db, t.TempDir(), testLogger())

	if _, err := h.db.Exec(`
		INSERT INTO crew_messages (id, workspace_id, from_crew_id, to_crew_id, content, created_at)
		VALUES ('msg_a1', ?, ?, ?, 'hello-a2', datetime('now'))`,
		ids.wsA, ids.crewA, crewA2); err != nil {
		t.Fatalf("seed ws-A message: %v", err)
	}

	rr := httptest.NewRecorder()
	req := boundReq(http.MethodGet, "/x?crew_id="+crewA2, nil, scopeMaster, ids.wsA)
	h.requireInternal(http.HandlerFunc(mh.ListMessages)).ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 for same-workspace list; body=%s", rr.Code, rr.Body.String())
	}
	if !bytes.Contains(rr.Body.Bytes(), []byte("hello-a2")) {
		t.Fatalf("own-workspace message missing from list: %s", rr.Body.String())
	}
}

// ws-A token reading a ws-B crew's shared file → 403, content not leaked.
func TestSecBinding_R2_ReadFileForeignCrew403(t *testing.T) {
	h, ids := seedScope(t)
	_, crewB2 := seedCrewPairs(t, h, ids)
	storage := t.TempDir()
	mh := NewCrewMessagingHandler(h.db, storage, testLogger())

	sharedDir := filepath.Join(storage, "crews", crewB2, "shared")
	if err := os.MkdirAll(sharedDir, 0o755); err != nil {
		t.Fatalf("mkdir shared dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sharedDir, "secret.txt"), []byte("ws-b-file-secret"), 0o644); err != nil {
		t.Fatalf("write seed file: %v", err)
	}

	rr := httptest.NewRecorder()
	req := setPathValue(
		boundReq(http.MethodGet, "/x?path=secret.txt&requester_crew_id="+ids.crewB, nil, scopeMaster, ids.wsA),
		"crewId", crewB2)
	h.requireInternal(http.HandlerFunc(mh.ReadFile)).ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (ws-A token must not read ws-B crew files); body=%s",
			rr.Code, rr.Body.String())
	}
	if bytes.Contains(rr.Body.Bytes(), []byte("ws-b-file-secret")) {
		t.Fatalf("ws-B file content leaked across tenant boundary: %s", rr.Body.String())
	}
}

// ws-A token writing into a ws-B crew's shared dir → 403, nothing on disk.
func TestSecBinding_R2_WriteFileForeignCrew403(t *testing.T) {
	h, ids := seedScope(t)
	_, crewB2 := seedCrewPairs(t, h, ids)
	storage := t.TempDir()
	mh := NewCrewMessagingHandler(h.db, storage, testLogger())
	if err := os.MkdirAll(filepath.Join(storage, "crews", crewB2, "shared"), 0o755); err != nil {
		t.Fatalf("mkdir shared dir: %v", err)
	}

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("requester_crew_id", ids.crewB)
	_ = mw.WriteField("path", "payload.txt")
	fw, _ := mw.CreateFormFile("file", "payload.txt")
	_, _ = fw.Write([]byte("planted"))
	_ = mw.Close()

	rr := httptest.NewRecorder()
	req := setPathValue(
		boundReq(http.MethodPost, "/x", buf.Bytes(), scopeMaster, ids.wsA),
		"crewId", crewB2)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	h.requireInternal(http.HandlerFunc(mh.WriteFile)).ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (ws-A token must not write ws-B crew files); body=%s",
			rr.Code, rr.Body.String())
	}
	planted := filepath.Join(storage, "crews", crewB2, "shared", "incoming", ids.crewB, "payload.txt")
	if _, err := os.Stat(planted); err == nil {
		t.Fatalf("cross-tenant file write landed on disk: %s", planted)
	}
}

// Same-workspace shared-file round-trip with a bound token keeps working:
// crew A writes into connected crew A2's incoming dir, then reads it back.
func TestSecBinding_R2_FileSameWorkspaceRoundTripOK(t *testing.T) {
	h, ids := seedScope(t)
	crewA2, _ := seedCrewPairs(t, h, ids)
	storage := t.TempDir()
	mh := NewCrewMessagingHandler(h.db, storage, testLogger())
	if err := os.MkdirAll(filepath.Join(storage, "crews", crewA2, "shared"), 0o755); err != nil {
		t.Fatalf("mkdir shared dir: %v", err)
	}

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("requester_crew_id", ids.crewA)
	_ = mw.WriteField("path", "report.txt")
	fw, _ := mw.CreateFormFile("file", "report.txt")
	_, _ = fw.Write([]byte("quarterly numbers"))
	_ = mw.Close()

	rr := httptest.NewRecorder()
	req := setPathValue(
		boundReq(http.MethodPost, "/x", buf.Bytes(), scopeMaster, ids.wsA),
		"crewId", crewA2)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	h.requireInternal(http.HandlerFunc(mh.WriteFile)).ServeHTTP(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 for same-workspace write; body=%s", rr.Code, rr.Body.String())
	}

	rr = httptest.NewRecorder()
	req = setPathValue(
		boundReq(http.MethodGet, "/x?path=incoming/"+ids.crewA+"/report.txt&requester_crew_id="+ids.crewA,
			nil, scopeMaster, ids.wsA),
		"crewId", crewA2)
	h.requireInternal(http.HandlerFunc(mh.ReadFile)).ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 for same-workspace read-back; body=%s", rr.Code, rr.Body.String())
	}
	if !bytes.Contains(rr.Body.Bytes(), []byte("quarterly numbers")) {
		t.Fatalf("same-workspace read-back returned wrong content: %s", rr.Body.String())
	}
}
