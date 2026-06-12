package api

// crew_messaging.go coverage top-up #2 — the filesystem failure branches
// (permission-denied reads, symlink escapes, symlink loops, shared-dir-
// is-a-file), the canCommunicate DB-error forks of SendMessage / ReadFile
// / WriteFile, the bound-token DB-error fork, and the logAudit fallback +
// failure paths.
//
// All tests are prefixed TestCov2Msg. Filesystem permission tests skip
// when running as root (root ignores 0o000 modes).

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func cov2MsgSkipIfRoot(t *testing.T) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("permission-bit tests are meaningless as root")
	}
}

// mkSharedDir creates the crew's shared dir under the handler storage root.
func cov2MsgSharedDir(t *testing.T, tmpDir, crewID string) string {
	t.Helper()
	dir := filepath.Join(tmpDir, "crews", crewID, "shared")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir shared: %v", err)
	}
	return dir
}

// --- SendMessage: canCommunicate DB error → 500 ---

func TestCov2MsgSendMessage_ConnCheckDBError500(t *testing.T) {
	h, db, wsID, _, fromCrew, toCrew := covMsgRig(t)
	if _, err := db.Exec(`DROP TABLE crew_connections`); err != nil {
		t.Fatalf("drop: %v", err)
	}
	body := `{"from_crew_id":"` + fromCrew + `","to_crew_id":"` + toCrew + `","workspace_id":"` + wsID + `","content":"hi"}`
	req := httptest.NewRequest("POST", "/api/v1/internal/crew-messages", strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.SendMessage(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500, body=%s", rec.Code, rec.Body.String())
	}
}

// --- ListMessages: a NULL content row fails the scan and is skipped ---

func TestCov2MsgListMessages_ScanErrorRowSkipped(t *testing.T) {
	h, db, wsID, _, fromCrew, toCrew := covMsgRig(t)
	// Replace the table with a lax copy (no NOT NULL on content) so a
	// NULL-content row can exist to trip the Scan error branch.
	if _, err := db.Exec(`DROP TABLE crew_messages`); err != nil {
		t.Fatalf("drop: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE crew_messages (
		id TEXT PRIMARY KEY, workspace_id TEXT, from_crew_id TEXT, to_crew_id TEXT,
		from_agent_id TEXT, content TEXT, metadata TEXT, delivered_at TEXT, created_at TEXT)`); err != nil {
		t.Fatalf("recreate: %v", err)
	}
	// Row 1: scannable. Row 2: NULL content → Scan error → continue.
	if _, err := db.Exec(`INSERT INTO crew_messages (id, workspace_id, from_crew_id, to_crew_id, content, created_at)
		VALUES ('m-ok', ?, ?, ?, 'fine', '2026-01-01T00:00:00Z')`, wsID, fromCrew, toCrew); err != nil {
		t.Fatalf("seed ok row: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO crew_messages (id, workspace_id, from_crew_id, to_crew_id, content, created_at)
		VALUES ('m-null', ?, ?, ?, NULL, '2026-01-02T00:00:00Z')`, wsID, fromCrew, toCrew); err != nil {
		t.Fatalf("seed null row: %v", err)
	}
	req := httptest.NewRequest("GET", "/api/v1/internal/crew-messages?crew_id="+toCrew, nil)
	rec := httptest.NewRecorder()
	h.ListMessages(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "m-ok") {
		t.Errorf("body = %s, want the scannable row", body)
	}
	if strings.Contains(body, "m-null") {
		t.Errorf("body = %s, NULL-content row must be skipped, not returned", body)
	}
}

// --- ReadFile: canCommunicate DB error → 500 ---

func TestCov2MsgReadFile_ConnCheckDBError500(t *testing.T) {
	h, db, _, _, fromCrew, toCrew := covMsgRig(t)
	if _, err := db.Exec(`DROP TABLE crew_connections`); err != nil {
		t.Fatalf("drop: %v", err)
	}
	rec := httptest.NewRecorder()
	h.ReadFile(rec, covMsgReadReq(toCrew, "x.txt", fromCrew))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500, body=%s", rec.Code, rec.Body.String())
	}
}

// --- ReadFile: unreadable directory → 500 from os.ReadDir ---

func TestCov2MsgReadFile_UnreadableDir500(t *testing.T) {
	cov2MsgSkipIfRoot(t)
	h, _, _, tmpDir, fromCrew, toCrew := covMsgRig(t)
	shared := cov2MsgSharedDir(t, tmpDir, toCrew)
	sub := filepath.Join(shared, "locked")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.Chmod(sub, 0o100); err != nil { // exec only: stat+EvalSymlinks OK, ReadDir fails
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(sub, 0o755) })

	rec := httptest.NewRecorder()
	h.ReadFile(rec, covMsgReadReq(toCrew, "locked", fromCrew))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (ReadDir denied), body=%s", rec.Code, rec.Body.String())
	}
}

// --- ReadFile: unreadable file → 500 from os.ReadFile ---

func TestCov2MsgReadFile_UnreadableFile500(t *testing.T) {
	cov2MsgSkipIfRoot(t)
	h, _, _, tmpDir, fromCrew, toCrew := covMsgRig(t)
	shared := cov2MsgSharedDir(t, tmpDir, toCrew)
	f := filepath.Join(shared, "secret.txt")
	if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.Chmod(f, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(f, 0o644) })

	rec := httptest.NewRecorder()
	h.ReadFile(rec, covMsgReadReq(toCrew, "secret.txt", fromCrew))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (ReadFile denied), body=%s", rec.Code, rec.Body.String())
	}
}

// --- ReadFile: symlink escaping the shared dir → traversal rejected ---

func TestCov2MsgReadFile_SymlinkEscapeRejected(t *testing.T) {
	h, _, _, tmpDir, fromCrew, toCrew := covMsgRig(t)
	shared := cov2MsgSharedDir(t, tmpDir, toCrew)
	outside := filepath.Join(tmpDir, "outside.txt")
	if err := os.WriteFile(outside, []byte("loot"), 0o644); err != nil {
		t.Fatalf("write outside: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(shared, "link")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	rec := httptest.NewRecorder()
	h.ReadFile(rec, covMsgReadReq(toCrew, "link", fromCrew))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (symlink escape), body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "path traversal not allowed") {
		t.Errorf("body = %s, want traversal rejection", rec.Body.String())
	}
}

// --- ReadFile: symlink loop → EvalSymlinks fails → internal error ---

func TestCov2MsgReadFile_SymlinkLoop500(t *testing.T) {
	h, _, _, tmpDir, fromCrew, toCrew := covMsgRig(t)
	shared := cov2MsgSharedDir(t, tmpDir, toCrew)
	loop := filepath.Join(shared, "loop")
	if err := os.Symlink(loop, loop); err != nil {
		t.Fatalf("symlink loop: %v", err)
	}

	rec := httptest.NewRecorder()
	h.ReadFile(rec, covMsgReadReq(toCrew, "loop", fromCrew))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (symlink loop), body=%s", rec.Code, rec.Body.String())
	}
}

// --- WriteFile: bad multipart body → 400 ---

func TestCov2MsgWriteFile_BadMultipart400(t *testing.T) {
	h, _, _, _, _, toCrew := covMsgRig(t)
	req := httptest.NewRequest("POST", "/api/v1/internal/crew-files/"+toCrew, strings.NewReader("not multipart"))
	req.SetPathValue("crewId", toCrew)
	req.Header.Set("Content-Type", "multipart/form-data; boundary=zzz")
	rec := httptest.NewRecorder()
	h.WriteFile(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (bad multipart), body=%s", rec.Code, rec.Body.String())
	}
}

// --- WriteFile: canCommunicate DB error → 500 ---

func TestCov2MsgWriteFile_ConnCheckDBError500(t *testing.T) {
	h, db, _, _, fromCrew, toCrew := covMsgRig(t)
	if _, err := db.Exec(`DROP TABLE crew_connections`); err != nil {
		t.Fatalf("drop: %v", err)
	}
	rec := httptest.NewRecorder()
	h.WriteFile(rec, covMsgUpload(t, toCrew, fromCrew, "f.txt", "data", true))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500, body=%s", rec.Code, rec.Body.String())
	}
}

// --- WriteFile: shared dir path blocked by a regular file → 500 ---

// crews/<id>/shared exists as a FILE, so MkdirAll for the incoming
// subdirectory fails with ENOTDIR → "internal error".
func TestCov2MsgWriteFile_SharedDirIsFile500(t *testing.T) {
	h, _, _, tmpDir, fromCrew, toCrew := covMsgRig(t)
	crewDir := filepath.Join(tmpDir, "crews", toCrew)
	if err := os.MkdirAll(crewDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(crewDir, "shared"), []byte("not a dir"), 0o644); err != nil {
		t.Fatalf("write file-as-dir: %v", err)
	}
	rec := httptest.NewRecorder()
	h.WriteFile(rec, covMsgUpload(t, toCrew, fromCrew, "f.txt", "data", true))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (ENOTDIR), body=%s", rec.Code, rec.Body.String())
	}
}

// --- WriteFile: destination dir symlinked outside → traversal rejected ---

func TestCov2MsgWriteFile_SymlinkedIncomingDirRejected(t *testing.T) {
	h, _, _, tmpDir, fromCrew, toCrew := covMsgRig(t)
	shared := cov2MsgSharedDir(t, tmpDir, toCrew)
	outsideDir := filepath.Join(tmpDir, "evil-target")
	if err := os.MkdirAll(outsideDir, 0o755); err != nil {
		t.Fatalf("mkdir outside: %v", err)
	}
	// WriteFile joins incoming/<requester>/<path>; pre-plant
	// incoming/<requester> as a symlink that escapes the shared dir.
	if err := os.MkdirAll(filepath.Join(shared, "incoming"), 0o755); err != nil {
		t.Fatalf("mkdir incoming: %v", err)
	}
	if err := os.Symlink(outsideDir, filepath.Join(shared, "incoming", fromCrew)); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	rec := httptest.NewRecorder()
	h.WriteFile(rec, covMsgUpload(t, toCrew, fromCrew, "f.txt", "data", true))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (dir symlink escape), body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "path traversal not allowed") {
		t.Errorf("body = %s, want traversal rejection", rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(outsideDir, "f.txt")); !os.IsNotExist(err) {
		t.Error("file must NOT be written through the escaping symlink")
	}
}

// --- WriteFile: unwritable destination dir → os.Create 500 ---

func TestCov2MsgWriteFile_UnwritableDir500(t *testing.T) {
	cov2MsgSkipIfRoot(t)
	h, _, _, tmpDir, fromCrew, toCrew := covMsgRig(t)
	shared := cov2MsgSharedDir(t, tmpDir, toCrew)
	dest := filepath.Join(shared, "incoming", fromCrew)
	if err := os.MkdirAll(dest, 0o755); err != nil {
		t.Fatalf("mkdir dest: %v", err)
	}
	if err := os.Chmod(dest, 0o555); err != nil { // read+exec, no write
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dest, 0o755) })

	rec := httptest.NewRecorder()
	h.WriteFile(rec, covMsgUpload(t, toCrew, fromCrew, "f.txt", "data", true))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (create denied), body=%s", rec.Code, rec.Body.String())
	}
}

// --- assertBoundCrewWorkspace: real DB error → 403 (fail closed) ---

func TestCov2MsgBoundToken_DBError403(t *testing.T) {
	h, db, wsID, _, fromCrew, toCrew := covMsgRig(t)
	if _, err := db.Exec(`PRAGMA foreign_keys = OFF`); err != nil {
		t.Fatalf("fk off: %v", err)
	}
	if _, err := db.Exec(`DROP TABLE crews`); err != nil {
		t.Fatalf("drop crews: %v", err)
	}
	body := `{"from_crew_id":"` + fromCrew + `","to_crew_id":"` + toCrew + `","workspace_id":"` + wsID + `","content":"hi"}`
	req := httptest.NewRequest("POST", "/api/v1/internal/crew-messages", strings.NewReader(body))
	req = req.WithContext(context.WithValue(req.Context(), ctxInternalTokenWS, wsID))
	rec := httptest.NewRecorder()
	h.SendMessage(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (fail closed on DB error), body=%s", rec.Code, rec.Body.String())
	}
}

// --- logAudit: workspace fallback resolution + insert failure warn ---

func TestCov2MsgLogAudit_ResolvesWorkspaceFromCrew(t *testing.T) {
	h, db, wsID, _, fromCrew, toCrew := covMsgRig(t)
	req := httptest.NewRequest("GET", "/", nil)
	// Empty workspaceID forces the resolveWorkspaceID(fromCrew) fallback.
	h.logAudit(req, "", "cov2_test_action", fromCrew, toCrew, "", map[string]string{"k": "v"})

	var gotWS string
	if err := db.QueryRow(`SELECT workspace_id FROM crew_audit_log WHERE action = 'cov2_test_action'`).Scan(&gotWS); err != nil {
		t.Fatalf("audit row missing: %v", err)
	}
	if gotWS != wsID {
		t.Errorf("audit workspace_id = %q, want %q (resolved from crew)", gotWS, wsID)
	}
}

func TestCov2MsgLogAudit_InsertFailureIsWarnOnly(t *testing.T) {
	h, db, wsID, _, fromCrew, toCrew := covMsgRig(t)
	if _, err := db.Exec(`DROP TABLE crew_audit_log`); err != nil {
		t.Fatalf("drop: %v", err)
	}
	req := httptest.NewRequest("GET", "/", nil)
	// Must not panic; failure is logged and swallowed.
	h.logAudit(req, wsID, "cov2_audit_fail", fromCrew, toCrew, "", nil)
}
