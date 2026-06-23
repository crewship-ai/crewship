package api

// Coverage tests for memory_versions_handler.go — error and guard
// branches not exercised by memory_versions_handler_test.go.

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

// --- restoreCanonicalPathSafe ------------------------------------------------

func TestCovMVRestoreCanonicalPathSafe(t *testing.T) {
	root := t.TempDir()
	blobRoot := filepath.Join(root, "versions")

	cases := []struct {
		name          string
		canonicalPath string
		blobRoot      string
		want          bool
	}{
		{"empty path", "", blobRoot, false},
		{"whitespace path", "   ", blobRoot, false},
		{"dotdot rejected", filepath.Join(root, "..", "etc", "passwd"), blobRoot, false},
		{"empty blob root fails closed", filepath.Join(root, "x.md"), "", false},
		{"inside root ok", filepath.Join(root, "learned", "x.md"), blobRoot, true},
		{"outside root rejected", "/etc/passwd", blobRoot, false},
		{"sibling prefix rejected", root + "-evil/x.md", blobRoot, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := restoreCanonicalPathSafe(tc.canonicalPath, tc.blobRoot); got != tc.want {
				t.Errorf("restoreCanonicalPathSafe(%q, %q) = %v, want %v",
					tc.canonicalPath, tc.blobRoot, got, tc.want)
			}
		})
	}
}

// --- List ---------------------------------------------------------------------

func TestCovMVList_NoWorkspace401(t *testing.T) {
	h, _, _, _, _ := newMemVerHandlerTest(t)
	req := httptest.NewRequest("GET", "/api/v1/memory/versions?path=x", nil)
	rr := httptest.NewRecorder()
	h.List(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rr.Code)
	}
}

func TestCovMVList_LimitParsing(t *testing.T) {
	h, db, userID, wsID, blobRoot := newMemVerHandlerTest(t)
	for i := 0; i < 3; i++ {
		seedMemoryVersion(t, db, blobRoot, wsID, "crew:c1/l.md", "v"+string(rune('0'+i)), "a1")
	}

	// limit=1 honoured.
	req := httptest.NewRequest("GET", "/api/v1/memory/versions?path=crew:c1/l.md&limit=1", nil)
	req = withWorkspaceUser(req, userID, wsID, "MEMBER")
	rr := httptest.NewRecorder()
	h.List(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), `"count":1`) {
		t.Errorf("limit=1 not honoured: %s", rr.Body.String())
	}

	// Invalid limit falls back to default (all 3 visible).
	req2 := httptest.NewRequest("GET", "/api/v1/memory/versions?path=crew:c1/l.md&limit=bogus", nil)
	req2 = withWorkspaceUser(req2, userID, wsID, "MEMBER")
	rr2 := httptest.NewRecorder()
	h.List(rr2, req2)
	if rr2.Code != http.StatusOK {
		t.Fatalf("status = %d", rr2.Code)
	}
	if !strings.Contains(rr2.Body.String(), `"count":3`) {
		t.Errorf("invalid limit should fall back to default: %s", rr2.Body.String())
	}
}

func TestCovMVList_DBError500(t *testing.T) {
	h, db, userID, wsID, _ := newMemVerHandlerTest(t)
	db.Close()
	req := httptest.NewRequest("GET", "/api/v1/memory/versions?path=crew:c1/l.md", nil)
	req = withWorkspaceUser(req, userID, wsID, "MEMBER")
	rr := httptest.NewRecorder()
	h.List(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rr.Code)
	}
}

// --- Show ---------------------------------------------------------------------

func TestCovMVShow_Guards(t *testing.T) {
	h, _, userID, wsID, _ := newMemVerHandlerTest(t)

	t.Run("no workspace 401", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/v1/memory/versions/abc?path=x", nil)
		rr := httptest.NewRecorder()
		h.Show(rr, req)
		if rr.Code != http.StatusUnauthorized {
			t.Errorf("status = %d, want 401", rr.Code)
		}
	})

	t.Run("missing sha 400", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/v1/memory/versions/?path=x", nil)
		req = withWorkspaceUser(req, userID, wsID, "MEMBER")
		rr := httptest.NewRecorder()
		h.Show(rr, req)
		if rr.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", rr.Code)
		}
	})

	t.Run("missing path 400", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/v1/memory/versions/abc", nil)
		req.SetPathValue("sha", "abc")
		req = withWorkspaceUser(req, userID, wsID, "MEMBER")
		rr := httptest.NewRecorder()
		h.Show(rr, req)
		if rr.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", rr.Code)
		}
	})
}

func TestCovMVShow_DBError500(t *testing.T) {
	h, db, userID, wsID, _ := newMemVerHandlerTest(t)
	db.Close()
	req := httptest.NewRequest("GET", "/api/v1/memory/versions/abc?path=crew:c1/l.md", nil)
	req.SetPathValue("sha", "abc")
	req = withWorkspaceUser(req, userID, wsID, "MEMBER")
	rr := httptest.NewRecorder()
	h.Show(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}

// --- Restore -------------------------------------------------------------------

func TestCovMVRestore_Guards(t *testing.T) {
	h, db, userID, wsID, blobRoot := newMemVerHandlerTest(t)
	sha := seedMemoryVersion(t, db, blobRoot, wsID, "crew:c1/l.md", "old content", "a1")
	memRoot := filepath.Dir(blobRoot)
	goodBody := `{"path":"crew:c1/l.md","canonical_path":"` + filepath.Join(memRoot, "l.md") + `","tier":"learned"}`

	mk := func(body, sha string) *http.Request {
		req := httptest.NewRequest("POST", "/api/v1/memory/versions/"+sha+"/restore", strings.NewReader(body))
		if sha != "" {
			req.SetPathValue("sha", sha)
		}
		return req
	}

	t.Run("no workspace 401", func(t *testing.T) {
		rr := httptest.NewRecorder()
		h.Restore(rr, mk(goodBody, sha))
		if rr.Code != http.StatusUnauthorized {
			t.Errorf("status = %d, want 401", rr.Code)
		}
	})

	t.Run("no user 401", func(t *testing.T) {
		req := mk(goodBody, sha)
		req = req.WithContext(withWorkspace(req.Context(), wsID, "OWNER"))
		rr := httptest.NewRecorder()
		h.Restore(rr, req)
		if rr.Code != http.StatusUnauthorized {
			t.Errorf("status = %d, want 401", rr.Code)
		}
	})

	t.Run("member forbidden 403", func(t *testing.T) {
		req := withWorkspaceUser(mk(goodBody, sha), userID, wsID, "MEMBER")
		rr := httptest.NewRecorder()
		h.Restore(rr, req)
		if rr.Code != http.StatusForbidden {
			t.Errorf("status = %d, want 403", rr.Code)
		}
	})

	t.Run("missing sha 400", func(t *testing.T) {
		req := withWorkspaceUser(mk(goodBody, ""), userID, wsID, "OWNER")
		rr := httptest.NewRecorder()
		h.Restore(rr, req)
		if rr.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", rr.Code)
		}
	})

	t.Run("invalid JSON 400", func(t *testing.T) {
		req := withWorkspaceUser(mk(`{not json`, sha), userID, wsID, "OWNER")
		rr := httptest.NewRecorder()
		h.Restore(rr, req)
		if rr.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", rr.Code)
		}
	})

	t.Run("missing body fields 400", func(t *testing.T) {
		req := withWorkspaceUser(mk(`{"path":"x"}`, sha), userID, wsID, "OWNER")
		rr := httptest.NewRecorder()
		h.Restore(rr, req)
		if rr.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", rr.Code)
		}
	})

	t.Run("invalid tier 400", func(t *testing.T) {
		body := `{"path":"crew:c1/l.md","canonical_path":"` + filepath.Join(memRoot, "l.md") + `","tier":"bogus"}`
		req := withWorkspaceUser(mk(body, sha), userID, wsID, "OWNER")
		rr := httptest.NewRecorder()
		h.Restore(rr, req)
		if rr.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", rr.Code)
		}
	})

	t.Run("canonical path outside root 400", func(t *testing.T) {
		body := `{"path":"crew:c1/l.md","canonical_path":"/etc/passwd","tier":"learned"}`
		req := withWorkspaceUser(mk(body, sha), userID, wsID, "OWNER")
		rr := httptest.NewRecorder()
		h.Restore(rr, req)
		if rr.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400; body=%s", rr.Code, rr.Body.String())
		}
	})

	t.Run("unknown sha 404", func(t *testing.T) {
		req := withWorkspaceUser(mk(goodBody, strings.Repeat("0", 64)), userID, wsID, "OWNER")
		rr := httptest.NewRecorder()
		h.Restore(rr, req)
		if rr.Code != http.StatusNotFound {
			t.Errorf("status = %d, want 404; body=%s", rr.Code, rr.Body.String())
		}
	})
}

func TestCovMVRestore_BlobRootUnconfigured503(t *testing.T) {
	h, _, userID, wsID, _ := newMemVerHandlerTest(t)
	h.SetBlobRoot("")
	req := httptest.NewRequest("POST", "/api/v1/memory/versions/abc/restore", strings.NewReader(`{}`))
	req.SetPathValue("sha", "abc")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Restore(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rr.Code)
	}
}

func TestCovMVRestore_DBError500(t *testing.T) {
	h, db, userID, wsID, blobRoot := newMemVerHandlerTest(t)
	sha := seedMemoryVersion(t, db, blobRoot, wsID, "crew:c1/l.md", "old content", "a1")
	memRoot := filepath.Dir(blobRoot)
	db.Close()
	body := `{"path":"crew:c1/l.md","canonical_path":"` + filepath.Join(memRoot, "l.md") + `","tier":"learned"}`
	req := httptest.NewRequest("POST", "/api/v1/memory/versions/"+sha+"/restore", strings.NewReader(body))
	req.SetPathValue("sha", sha)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Restore(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}
