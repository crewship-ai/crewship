package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// pipelines_crud.go — ExportPipeline + ListVersions + GetVersion
// (the three zero-coverage read-side handlers).
//
// These handlers underpin the marketplace export flow + version-history
// UI. They are pure store reads, so we can exercise them against the
// in-memory migrated DB without standing up an orchestrator/runner.
// ---------------------------------------------------------------------------

func newPipelineHandlerForCRUDTest(t *testing.T) (*PipelineHandler, string, string) {
	t.Helper()
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	return NewPipelineHandler(db, logger, nil, nil), userID, wsID
}

// seedPipelineWithVersions inserts a pipelines row plus N pipeline_versions
// rows (version 1..N) so the history-walking tests have data to walk.
// Returns the pipeline id.
func seedPipelineWithVersions(t *testing.T, h *PipelineHandler, wsID, id, slug string, versions int) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := h.db.Exec(`
		INSERT INTO pipelines (id, workspace_id, slug, name, definition_json, definition_hash, dsl_version, head_version, last_test_run_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, '{"name":"x","steps":[]}', 'hash-head', '1.0', ?, ?, ?, ?)`,
		id, wsID, slug, slug, versions, now, now, now); err != nil {
		t.Fatalf("seed pipeline: %v", err)
	}
	for v := 1; v <= versions; v++ {
		summary := ""
		var parent interface{}
		if v > 1 {
			pv := v - 1
			parent = pv
			summary = "version " + slug + ":" + pcrudItoa(v)
		}
		defJSON := `{"name":"x","steps":[],"version":` + pcrudItoa(v) + `}`
		// Stagger created_at so DESC ordering is deterministic — SQLite's
		// subsecond default can return rows in the same microsecond.
		ts := time.Now().UTC().Add(time.Duration(v) * time.Millisecond).Format(time.RFC3339Nano)
		_, err := h.db.Exec(`
			INSERT INTO pipeline_versions
			    (id, pipeline_id, version, definition_json, definition_hash,
			     author_type, author_id, parent_version, change_summary, created_at)
			VALUES (?, ?, ?, ?, ?, 'user', ?, ?, ?, ?)`,
			"plnv_"+id+"_"+pcrudItoa(v), id, v, defJSON, "hash-"+pcrudItoa(v),
			"user-"+pcrudItoa(v), parent, summary, ts)
		if err != nil {
			t.Fatalf("seed version %d: %v", v, err)
		}
	}
}

// pcrudItoa keeps test code terse — strconv adds a noisy import; the
// unprefixed `itoa` name is taken by ratelimit_test.go.
func pcrudItoa(n int) string {
	if n == 0 {
		return "0"
	}
	digits := ""
	if n < 0 {
		digits = "-"
		n = -n
	}
	var rev []byte
	for n > 0 {
		rev = append(rev, byte('0'+n%10))
		n /= 10
	}
	for i := len(rev) - 1; i >= 0; i-- {
		digits += string(rev[i])
	}
	return digits
}

// ---- ExportPipeline ----

func TestExportPipeline_NotFound(t *testing.T) {
	h, userID, wsID := newPipelineHandlerForCRUDTest(t)
	req := httptest.NewRequest("GET", "/x", nil)
	req.SetPathValue("slug", "missing")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.ExportPipeline(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rr.Code)
	}
}

func TestExportPipeline_CrossWorkspace_NotFound(t *testing.T) {
	// Pipeline exists in another workspace; caller's workspace lookup
	// must NOT find it. Store.GetBySlug is workspace-scoped, so this
	// pins the "no cross-workspace leak" contract.
	h, userID, wsA := newPipelineHandlerForCRUDTest(t)
	wsB := "ws-export-foreign"
	if _, err := h.db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'F', 'f-export')`, wsB); err != nil {
		t.Fatalf("seed wsB: %v", err)
	}
	seedPipelineWithVersions(t, h, wsB, "pln-foreign", "shared-slug", 1)

	req := httptest.NewRequest("GET", "/x", nil)
	req.SetPathValue("slug", "shared-slug")
	req = withWorkspaceUser(req, userID, wsA, "OWNER")
	rr := httptest.NewRecorder()
	h.ExportPipeline(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("cross-workspace = %d, want 404", rr.Code)
	}
}

func TestExportPipeline_HappyPath_BundleShape(t *testing.T) {
	h, userID, wsID := newPipelineHandlerForCRUDTest(t)
	seedPipelineWithVersions(t, h, wsID, "pln-exp", "alpha", 3)

	req := httptest.NewRequest("GET", "/x", nil)
	req.SetPathValue("slug", "alpha")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.ExportPipeline(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}

	var bundle map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &bundle); err != nil {
		t.Fatalf("decode bundle: %v", err)
	}
	if bundle["format"] != "crewship-pipeline-bundle/v1" {
		t.Errorf("format = %v, want crewship-pipeline-bundle/v1", bundle["format"])
	}
	pipe, _ := bundle["pipeline"].(map[string]any)
	if pipe["slug"] != "alpha" {
		t.Errorf("pipeline.slug = %v", pipe["slug"])
	}
	if pipe["dsl_version"] != "1.0" {
		t.Errorf("pipeline.dsl_version = %v", pipe["dsl_version"])
	}
	meta, _ := bundle["metadata"].(map[string]any)
	if meta["source_workspace_id"] != wsID {
		t.Errorf("metadata.source_workspace_id = %v", meta["source_workspace_id"])
	}
	if meta["definition_hash"] != "hash-head" {
		t.Errorf("metadata.definition_hash = %v, want hash-head", meta["definition_hash"])
	}
	// Default: no include_history query param means no `history` key.
	if _, hasHistory := bundle["history"]; hasHistory {
		t.Errorf("history present without include_history=1 query param")
	}
}

func TestExportPipeline_IncludeHistory_AttachesVersionsArray(t *testing.T) {
	h, userID, wsID := newPipelineHandlerForCRUDTest(t)
	seedPipelineWithVersions(t, h, wsID, "pln-hist", "betalist", 3)

	req := httptest.NewRequest("GET", "/x?include_history=1", nil)
	req.SetPathValue("slug", "betalist")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.ExportPipeline(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var bundle map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &bundle); err != nil {
		t.Fatalf("decode bundle: %v body=%s", err, rr.Body.String())
	}
	hist, ok := bundle["history"].([]any)
	if !ok {
		t.Fatalf("history missing or wrong type: %T", bundle["history"])
	}
	if len(hist) != 3 {
		t.Fatalf("history len = %d, want 3", len(hist))
	}
	// First history entry should carry definition + hash + change_summary
	// for versions > 1.
	first, _ := hist[0].(map[string]any)
	if first["definition_hash"] == nil || first["definition_hash"] == "" {
		t.Errorf("history[0].definition_hash empty: %+v", first)
	}
	if first["created_at"] == nil {
		t.Errorf("history[0].created_at missing")
	}
}

// ---- ListVersions ----

func TestListVersions_NotFound(t *testing.T) {
	h, userID, wsID := newPipelineHandlerForCRUDTest(t)
	req := httptest.NewRequest("GET", "/x", nil)
	req.SetPathValue("slug", "missing")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.ListVersions(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rr.Code)
	}
}

func TestListVersions_EmptyPipeline_ReturnsEmptyArray(t *testing.T) {
	// Pipeline exists but has zero version rows (synthetic edge — the
	// production save path always writes one, but the handler must not
	// 500 if a manual insert skipped them). Body must be "[]", not null.
	h, userID, wsID := newPipelineHandlerForCRUDTest(t)
	seedPipelineWithVersions(t, h, wsID, "pln-empty", "empty", 0)

	req := httptest.NewRequest("GET", "/x", nil)
	req.SetPathValue("slug", "empty")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.ListVersions(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	if strings.TrimSpace(rr.Body.String()) != "[]" {
		t.Errorf("empty versions body = %q, want \"[]\"", rr.Body.String())
	}
}

func TestListVersions_HappyPath_DESCOrder(t *testing.T) {
	h, userID, wsID := newPipelineHandlerForCRUDTest(t)
	seedPipelineWithVersions(t, h, wsID, "pln-many", "manyv", 4)

	req := httptest.NewRequest("GET", "/x", nil)
	req.SetPathValue("slug", "manyv")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.ListVersions(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var rows []map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &rows); err != nil {
		t.Fatalf("decode rows: %v body=%s", err, rr.Body.String())
	}
	if len(rows) != 4 {
		t.Fatalf("rows = %d, want 4", len(rows))
	}
	// Store.ListVersions sorts by version DESC — newest first.
	wantOrder := []float64{4, 3, 2, 1}
	for i, v := range wantOrder {
		if rows[i]["version"] != v {
			t.Errorf("rows[%d].version = %v, want %v", i, rows[i]["version"], v)
		}
	}
	// parent_version omitted on version 1, present on others.
	if _, hasParent := rows[3]["parent_version"]; hasParent {
		t.Errorf("v1 row should not include parent_version: %+v", rows[3])
	}
	if rows[0]["parent_version"] != float64(3) {
		t.Errorf("v4 parent_version = %v, want 3", rows[0]["parent_version"])
	}
}

func TestListVersions_LimitClamping(t *testing.T) {
	h, userID, wsID := newPipelineHandlerForCRUDTest(t)
	seedPipelineWithVersions(t, h, wsID, "pln-lim", "limited", 5)

	cases := []struct {
		name, limit string
		want        int
	}{
		{"default-100", "", 5},
		{"explicit-2", "2", 2},
		{"explicit-larger-than-rows", "50", 5},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			url := "/x"
			if tc.limit != "" {
				url += "?limit=" + tc.limit
			}
			req := httptest.NewRequest("GET", url, nil)
			req.SetPathValue("slug", "limited")
			req = withWorkspaceUser(req, userID, wsID, "OWNER")
			rr := httptest.NewRecorder()
			h.ListVersions(rr, req)
			if rr.Code != http.StatusOK {
				t.Fatalf("%s: status = %d body=%s", tc.name, rr.Code, rr.Body.String())
			}
			var rows []map[string]any
			if err := json.Unmarshal(rr.Body.Bytes(), &rows); err != nil {
				t.Fatalf("%s: decode rows: %v body=%s", tc.name, err, rr.Body.String())
			}
			if len(rows) != tc.want {
				t.Errorf("%s: rows = %d, want %d", tc.name, len(rows), tc.want)
			}
		})
	}
}

// ---- GetVersion ----

func TestGetVersion_InvalidVersionParam_400(t *testing.T) {
	h, userID, wsID := newPipelineHandlerForCRUDTest(t)
	req := httptest.NewRequest("GET", "/x", nil)
	req.SetPathValue("slug", "any")
	req.SetPathValue("n", "not-a-number")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.GetVersion(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}

func TestGetVersion_PipelineNotFound_404(t *testing.T) {
	h, userID, wsID := newPipelineHandlerForCRUDTest(t)
	req := httptest.NewRequest("GET", "/x", nil)
	req.SetPathValue("slug", "missing")
	req.SetPathValue("n", "1")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.GetVersion(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 (pipeline missing)", rr.Code)
	}
}

func TestGetVersion_VersionNotFound_404(t *testing.T) {
	h, userID, wsID := newPipelineHandlerForCRUDTest(t)
	seedPipelineWithVersions(t, h, wsID, "pln-gv", "havesome", 2)
	req := httptest.NewRequest("GET", "/x", nil)
	req.SetPathValue("slug", "havesome")
	req.SetPathValue("n", "999")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.GetVersion(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 (version missing)", rr.Code)
	}
}

func TestGetVersion_HappyPath_ReturnsFullDSL(t *testing.T) {
	h, userID, wsID := newPipelineHandlerForCRUDTest(t)
	seedPipelineWithVersions(t, h, wsID, "pln-gv2", "alpha2", 3)

	req := httptest.NewRequest("GET", "/x", nil)
	req.SetPathValue("slug", "alpha2")
	req.SetPathValue("n", "2")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.GetVersion(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v body=%s", err, rr.Body.String())
	}
	if got["version"] != float64(2) {
		t.Errorf("version = %v, want 2", got["version"])
	}
	if got["definition_hash"] != "hash-2" {
		t.Errorf("definition_hash = %v, want hash-2", got["definition_hash"])
	}
	if got["author_id"] != "user-2" {
		t.Errorf("author_id = %v, want user-2", got["author_id"])
	}
	if got["parent_version"] != float64(1) {
		t.Errorf("parent_version = %v, want 1", got["parent_version"])
	}
	// definition is the raw JSON; must roundtrip through json.RawMessage
	def, ok := got["definition"].(map[string]any)
	if !ok {
		t.Fatalf("definition not an object: %T", got["definition"])
	}
	if def["version"] != float64(2) {
		t.Errorf("definition.version = %v, want 2 (the seeded fixture)", def["version"])
	}
}
