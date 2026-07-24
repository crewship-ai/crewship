package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// #1422 item 5: `routine diff <slug> --from N --to M` — a native unified
// diff between two versions' definitions, so a diff doesn't require an
// external round-trip through `versions show` + a text editor.

func TestDiffVersions_HappyPath(t *testing.T) {
	h, userID, wsID := newPipelineHandlerForCRUDTest(t)
	seedPipelineWithVersions(t, h, wsID, "pln-diff1", "diff-target", 3)

	req := httptest.NewRequest("GET", "/x?from=1&to=3", nil)
	req.SetPathValue("slug", "diff-target")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.DiffVersions(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	var resp versionDiffResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Slug != "diff-target" {
		t.Errorf("slug = %q", resp.Slug)
	}
	if resp.FromVersion != 1 || resp.ToVersion != 3 {
		t.Errorf("from/to = %d/%d, want 1/3", resp.FromVersion, resp.ToVersion)
	}
	if resp.Identical {
		t.Errorf("expected non-identical versions")
	}
	if !strings.Contains(resp.UnifiedDiff, "-  \"version\": 1") && !strings.Contains(resp.UnifiedDiff, `-  "version": 1`) {
		t.Errorf("unified_diff missing removed line for v1:\n%s", resp.UnifiedDiff)
	}
	if !strings.Contains(resp.UnifiedDiff, `"version": 3`) {
		t.Errorf("unified_diff missing added line for v3:\n%s", resp.UnifiedDiff)
	}
}

func TestDiffVersions_IdenticalVersions(t *testing.T) {
	h, userID, wsID := newPipelineHandlerForCRUDTest(t)
	seedPipelineWithVersions(t, h, wsID, "pln-diff2", "diff-same", 2)

	req := httptest.NewRequest("GET", "/x?from=1&to=1", nil)
	req.SetPathValue("slug", "diff-same")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.DiffVersions(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	var resp versionDiffResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.Identical {
		t.Errorf("expected identical=true for from==to")
	}
	if resp.UnifiedDiff != "" {
		t.Errorf("expected empty diff for identical versions, got:\n%s", resp.UnifiedDiff)
	}
}

func TestDiffVersions_MissingParams_400(t *testing.T) {
	h, userID, wsID := newPipelineHandlerForCRUDTest(t)
	seedPipelineWithVersions(t, h, wsID, "pln-diff3", "diff-missing", 2)

	for _, qs := range []string{"", "?from=1", "?to=2", "?from=abc&to=2", "?from=1&to=xyz"} {
		req := httptest.NewRequest("GET", "/x"+qs, nil)
		req.SetPathValue("slug", "diff-missing")
		req = withWorkspaceUser(req, userID, wsID, "OWNER")
		rr := httptest.NewRecorder()
		h.DiffVersions(rr, req)
		if rr.Code != http.StatusBadRequest {
			t.Errorf("qs=%q: status = %d, want 400; body=%s", qs, rr.Code, rr.Body.String())
		}
	}
}

func TestDiffVersions_UnknownPipeline_404(t *testing.T) {
	h, userID, wsID := newPipelineHandlerForCRUDTest(t)

	req := httptest.NewRequest("GET", "/x?from=1&to=2", nil)
	req.SetPathValue("slug", "ghost")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.DiffVersions(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rr.Code, rr.Body.String())
	}
}

func TestDiffVersions_UnknownVersion_404(t *testing.T) {
	h, userID, wsID := newPipelineHandlerForCRUDTest(t)
	seedPipelineWithVersions(t, h, wsID, "pln-diff4", "diff-nover", 2)

	req := httptest.NewRequest("GET", "/x?from=1&to=99", nil)
	req.SetPathValue("slug", "diff-nover")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.DiffVersions(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rr.Code, rr.Body.String())
	}
}
