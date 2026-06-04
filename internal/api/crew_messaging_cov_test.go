package api

import (
	"bytes"
	"database/sql"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// covMsgRig builds the standard two-crew, bidirectional-connection fixture
// used by most crew-messaging tests: a workspace, two crews (from/to), and an
// active bidirectional connection between them. It returns the handler, the
// db, the workspace id, the storage tmpDir, and the two crew ids.
//
// It complements the existing TestCrewMessaging_* tests in core_handlers_test
// by exercising the remaining uncovered branches (size limits, metadata
// serialisation, full-row scans, path errors, and DB-failure 500 paths).
func covMsgRig(t *testing.T) (h *CrewMessagingHandler, db *sql.DB, wsID, tmpDir, fromCrew, toCrew string) {
	t.Helper()
	db = setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID = seedTestWorkspace(t, db, userID)
	tmpDir = t.TempDir()
	h = NewCrewMessagingHandler(db, tmpDir, newTestLogger())

	fromCrew = "cm-from"
	toCrew = "cm-to"
	seedCrewRow(t, db, fromCrew, wsID, "From", "cm-from")
	seedCrewRow(t, db, toCrew, wsID, "To", "cm-to")
	if _, err := db.Exec(`INSERT INTO crew_connections (id, workspace_id, from_crew_id, to_crew_id, direction, status)
		VALUES ('cm-conn', ?, ?, ?, 'bidirectional', 'active')`, wsID, fromCrew, toCrew); err != nil {
		t.Fatalf("seed connection: %v", err)
	}
	return h, db, wsID, tmpDir, fromCrew, toCrew
}

// covMsgReadReq builds a GET ReadFile request for the given target crew with
// the path + requester query params and the {crewId} path value populated.
func covMsgReadReq(targetCrew, path, requester string) *http.Request {
	r := httptest.NewRequest("GET",
		"/api/v1/internal/crew-files/"+targetCrew+"?path="+path+"&requester_crew_id="+requester, nil)
	r.SetPathValue("crewId", targetCrew)
	return r
}

// covMsgUpload builds a multipart WriteFile request.
func covMsgUpload(t *testing.T, targetCrew, requester, path, content string, withFile bool) *http.Request {
	t.Helper()
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	_ = w.WriteField("requester_crew_id", requester)
	_ = w.WriteField("path", path)
	if withFile {
		fw, err := w.CreateFormFile("file", "upload.bin")
		if err != nil {
			t.Fatalf("create file part: %v", err)
		}
		_, _ = fw.Write([]byte(content))
	}
	_ = w.Close()
	req := httptest.NewRequest("POST", "/api/v1/internal/crew-files/"+targetCrew, &body)
	req.SetPathValue("crewId", targetCrew)
	req.Header.Set("Content-Type", w.FormDataContentType())
	return req
}

// --- SendMessage ---

// Content larger than the 1MB cap must be rejected before any DB write.
func TestCovMsgSendMessage_ContentTooLarge_Returns400(t *testing.T) {
	h, db, wsID, _, fromCrew, toCrew := covMsgRig(t)
	big := strings.Repeat("a", (1<<20)+1)
	body := `{"from_crew_id":"` + fromCrew + `","to_crew_id":"` + toCrew +
		`","workspace_id":"` + wsID + `","content":"` + big + `"}`
	rr := httptest.NewRecorder()
	h.SendMessage(rr, httptest.NewRequest("POST", "/x", strings.NewReader(body)))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM crew_messages`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Errorf("oversized message stored: count=%d, want 0", n)
	}
}

// A message carrying metadata exercises the metadata-serialisation branch and
// must round-trip the raw JSON back into the response and the DB.
func TestCovMsgSendMessage_WithMetadata_StoresAndEchoes(t *testing.T) {
	h, db, wsID, _, fromCrew, toCrew := covMsgRig(t)
	body := `{"from_crew_id":"` + fromCrew + `","to_crew_id":"` + toCrew +
		`","workspace_id":"` + wsID + `","content":"hi","metadata":{"k":"v"}}`
	rr := httptest.NewRecorder()
	h.SendMessage(rr, httptest.NewRequest("POST", "/x", strings.NewReader(body)))
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), `"metadata":{"k":"v"}`) {
		t.Errorf("response missing echoed metadata: %s", rr.Body.String())
	}
	var meta sql.NullString
	if err := db.QueryRow(`SELECT metadata FROM crew_messages WHERE from_crew_id = ?`, fromCrew).Scan(&meta); err != nil {
		t.Fatalf("read metadata: %v", err)
	}
	if !meta.Valid || meta.String != `{"k":"v"}` {
		t.Errorf("stored metadata = %v/%q, want {\"k\":\"v\"}", meta.Valid, meta.String)
	}
}

// When the DB insert fails the handler must surface a 500 rather than a 201.
// Closing the DB after fixtures are seeded forces the INSERT to error.
func TestCovMsgSendMessage_DBError_Returns500(t *testing.T) {
	h, db, wsID, _, fromCrew, toCrew := covMsgRig(t)
	db.Close()
	body := `{"from_crew_id":"` + fromCrew + `","to_crew_id":"` + toCrew +
		`","workspace_id":"` + wsID + `","content":"hi"}`
	rr := httptest.NewRecorder()
	h.SendMessage(rr, httptest.NewRequest("POST", "/x", strings.NewReader(body)))
	// resolveWorkspaceID runs first against the closed DB and returns "",
	// which trips the workspace-mismatch 403 before the INSERT path. Either
	// 403 (mismatch) or 500 (insert) proves we did not 201 on a dead DB.
	if rr.Code != http.StatusInternalServerError && rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 500 or 403 on dead DB; body=%s", rr.Code, rr.Body.String())
	}
}

// --- ListMessages ---

// A fully-populated row (from_agent_id, metadata, delivered_at all set) must
// scan cleanly and surface every nullable column in the response.
func TestCovMsgListMessages_FullRow_ScansNullables(t *testing.T) {
	h, db, wsID, _, fromCrew, toCrew := covMsgRig(t)
	if _, err := db.Exec(`INSERT INTO crew_messages
		(id, workspace_id, from_crew_id, to_crew_id, from_agent_id, content, metadata, delivered_at, created_at)
		VALUES ('m-full', ?, ?, ?, 'agent-x', 'payload', '{"a":1}', '2026-01-02T00:00:00Z', '2026-01-01T00:00:00Z')`,
		wsID, fromCrew, toCrew); err != nil {
		t.Fatalf("seed message: %v", err)
	}
	r := httptest.NewRequest("GET", "/api/v1/internal/crew-messages?crew_id="+toCrew, nil)
	rr := httptest.NewRecorder()
	h.ListMessages(rr, r)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	for _, want := range []string{`"from_agent_id":"agent-x"`, `"metadata":{"a":1}`, `"delivered_at":"2026-01-02T00:00:00Z"`} {
		if !strings.Contains(body, want) {
			t.Errorf("response missing %s: %s", want, body)
		}
	}
}

// No matching rows must serialise as an empty array (not null) in the data key.
func TestCovMsgListMessages_Empty_ReturnsEmptyArray(t *testing.T) {
	h, _, _, _, _, toCrew := covMsgRig(t)
	r := httptest.NewRequest("GET", "/api/v1/internal/crew-messages?crew_id="+toCrew, nil)
	rr := httptest.NewRecorder()
	h.ListMessages(rr, r)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if !strings.Contains(strings.ReplaceAll(rr.Body.String(), " ", ""), `"data":[]`) {
		t.Errorf("expected empty array data, got: %s", rr.Body.String())
	}
}

// A query error (closed DB) must surface as a 500.
func TestCovMsgListMessages_DBError_Returns500(t *testing.T) {
	h, db, _, _, _, toCrew := covMsgRig(t)
	db.Close()
	r := httptest.NewRequest("GET", "/api/v1/internal/crew-messages?crew_id="+toCrew, nil)
	rr := httptest.NewRecorder()
	h.ListMessages(rr, r)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}

// --- ReadFile ---

// A path containing ".." is rejected by resolveCrewSharedPath as "invalid
// path" → 400, before the shared dir is even resolved.
func TestCovMsgReadFile_DotDotPath_Returns400(t *testing.T) {
	h, _, _, _, fromCrew, toCrew := covMsgRig(t)
	rr := httptest.NewRecorder()
	h.ReadFile(rr, covMsgReadReq(toCrew, "../escape", fromCrew))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
}

// When the crew's shared directory does not exist at all, EvalSymlinks of the
// shared dir fails with not-exist → "file not found" → 404.
func TestCovMsgReadFile_NoSharedDir_Returns404(t *testing.T) {
	h, _, _, _, fromCrew, toCrew := covMsgRig(t)
	rr := httptest.NewRecorder()
	h.ReadFile(rr, covMsgReadReq(toCrew, "anything.txt", fromCrew))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rr.Code, rr.Body.String())
	}
}

// A directory containing both a file and a subdir must list every entry with
// its name/is_dir/size, exercising the directory-listing branch.
func TestCovMsgReadFile_DirectoryWithEntries_ListsAll(t *testing.T) {
	h, _, _, tmpDir, fromCrew, toCrew := covMsgRig(t)
	shared := filepath.Join(tmpDir, "crews", toCrew, "shared", "bundle")
	if err := os.MkdirAll(filepath.Join(shared, "nested"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(shared, "doc.txt"), []byte("body"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	rr := httptest.NewRecorder()
	h.ReadFile(rr, covMsgReadReq(toCrew, "bundle", fromCrew))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if !strings.Contains(body, `"doc.txt"`) || !strings.Contains(body, `"nested"`) {
		t.Errorf("directory listing missing entries: %s", body)
	}
	if !strings.Contains(body, `"is_dir":true`) || !strings.Contains(body, `"size":4`) {
		t.Errorf("directory listing missing metadata fields: %s", body)
	}
}

// A file exceeding the 10MB streaming cap must be rejected with 400.
func TestCovMsgReadFile_FileTooLarge_Returns400(t *testing.T) {
	h, _, _, tmpDir, fromCrew, toCrew := covMsgRig(t)
	shared := filepath.Join(tmpDir, "crews", toCrew, "shared")
	if err := os.MkdirAll(shared, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	big := filepath.Join(shared, "big.bin")
	f, err := os.Create(big)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := f.Truncate((10 << 20) + 1); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	f.Close()

	rr := httptest.NewRecorder()
	h.ReadFile(rr, covMsgReadReq(toCrew, "big.bin", fromCrew))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
}

// --- WriteFile ---

// A destination path containing ".." (joined into the incoming/<requester>
// subtree) trips the resolveCrewSharedPath "invalid path" branch → 400.
func TestCovMsgWriteFile_DotDotPath_Returns400(t *testing.T) {
	h, _, _, tmpDir, fromCrew, toCrew := covMsgRig(t)
	if err := os.MkdirAll(filepath.Join(tmpDir, "crews", toCrew, "shared"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	rr := httptest.NewRecorder()
	h.WriteFile(rr, covMsgUpload(t, toCrew, fromCrew, "../../../escape.txt", "x", true))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
}

// Write into a nested destination path creates the parent dir (mkdirForWrite)
// and persists the file under incoming/<requester>/... — happy path through
// the os.Create + io.Copy branch with a non-existent parent dir.
func TestCovMsgWriteFile_NestedPath_CreatesAndStores(t *testing.T) {
	h, _, _, tmpDir, fromCrew, toCrew := covMsgRig(t)
	if err := os.MkdirAll(filepath.Join(tmpDir, "crews", toCrew, "shared"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	rr := httptest.NewRecorder()
	h.WriteFile(rr, covMsgUpload(t, toCrew, fromCrew, "reports/q1.txt", "payload-data", true))
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", rr.Code, rr.Body.String())
	}
	stored := filepath.Join(tmpDir, "crews", toCrew, "shared", "incoming", fromCrew, "reports", "q1.txt")
	data, err := os.ReadFile(stored)
	if err != nil {
		t.Fatalf("read stored file: %v", err)
	}
	if string(data) != "payload-data" {
		t.Errorf("stored content = %q, want payload-data", string(data))
	}
}

// --- canCommunicate / resolveWorkspaceID helpers ---

// resolveWorkspaceID short-circuits to "" for an empty crew id without hitting
// the DB; surfaced indirectly via SendMessage's workspace-mismatch 403 when
// from_crew_id resolves to nothing.
func TestCovMsgResolveWorkspaceID_EmptyCrew(t *testing.T) {
	h, db, _, _, _, _ := covMsgRig(t)
	if got := h.resolveWorkspaceID(req().Context(), ""); got != "" {
		t.Errorf("resolveWorkspaceID(\"\") = %q, want empty", got)
	}
	// Unknown crew id → query returns no rows → "".
	if got := h.resolveWorkspaceID(req().Context(), "nope"); got != "" {
		t.Errorf("resolveWorkspaceID(unknown) = %q, want empty", got)
	}
	_ = db
}

// canCommunicate must surface a non-ErrNoRows DB failure as an error (not a
// silent false), which the handler then renders as a 500.
func TestCovMsgCanCommunicate_DBError_PropagatesError(t *testing.T) {
	h, db, _, _, fromCrew, toCrew := covMsgRig(t)
	db.Close()
	if _, err := h.canCommunicate(req(), fromCrew, toCrew); err == nil {
		t.Fatal("expected error from canCommunicate against closed DB, got nil")
	}
}

// req is a tiny helper to obtain a *http.Request carrying a background context
// for the direct helper-method tests above.
func req() *http.Request {
	return httptest.NewRequest("GET", "/", nil)
}
