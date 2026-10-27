package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Owning a private artifact does not require a write-scoped credential to read
// it. Exercise the full Router and calibrate the same read-only token against
// the ordinary Page read before checking artifact availability.
func TestArtifactProjectOwnerReadOnlyCredentialCanReadWithoutWriteCapability(t *testing.T) {
	h, workspace, owner, create := artifactProjectFixture(t)
	body := strings.TrimPrefix(artifactProjectBody(0, "Owner read scope"), "{")
	if got := create(owner, http.MethodPost, "artifacts", `{"slug":"three-screens","name":"Owner read scope",`+body); got.Code != 201 {
		t.Fatalf("create owner project: %d %s", got.Code, got.Body)
	}
	fullToken := mintIngressScopedToken(t, h.db, owner, "artifact-owner-read-full-a-review", "*")
	readToken := mintIngressScopedToken(t, h.db, owner, "artifact-owner-read-only-a-review", "pages:read")
	router, err := NewRouter(h.db, "synthetic-artifact-owner-read-scope-a-review", newTestLogger())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(router.Shutdown)
	request := func(token, method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("X-Workspace-ID", workspace)
		req.Header.Set("Content-Type", "application/json")
		out := httptest.NewRecorder()
		router.ServeHTTP(out, req)
		return out
	}
	const page = "/api/v1/pages/three-screens"
	if got := request(fullToken, http.MethodGet, page+"/artifact", ""); got.Code != 200 {
		t.Fatalf("full owner artifact positive calibration: %d %s", got.Code, got.Body)
	}
	if got := request(readToken, http.MethodGet, page, ""); got.Code != 200 {
		t.Fatalf("read-scoped owner ordinary Page positive calibration: %d %s", got.Code, got.Body)
	}
	got := request(readToken, http.MethodGet, page+"/artifact", "")
	if got.Code != 200 {
		t.Errorf("read-scoped owner artifact must be readable: %d %s", got.Code, got.Body)
	} else {
		var metadata struct {
			CanWrite      *bool `json:"can_write"`
			LatestVersion int64 `json:"latest_version"`
		}
		if err = json.Unmarshal(got.Body.Bytes(), &metadata); err != nil {
			t.Fatal(err)
		}
		if metadata.CanWrite == nil || *metadata.CanWrite {
			t.Errorf("read-only credential must advertise can_write=false: %s", got.Body)
		}
		if metadata.LatestVersion != 1 {
			t.Errorf("expected original version1, got %d", metadata.LatestVersion)
		}
	}
	// A read fix must not grant publishing: use a valid successor candidate and
	// inspect durable state even when the preceding availability assertion fails.
	denied := request(readToken, http.MethodPost, page+"/artifact/versions", artifactProjectBody(1, "Read-only token must not publish"))
	if denied.Code != http.StatusForbidden {
		t.Errorf("read-only valid publish must be403: %d %s", denied.Code, denied.Body)
	}
	var latest int64
	if err = h.db.QueryRow(`SELECT latest_version FROM artifact_version_heads WHERE workspace_id=? AND page_id=(SELECT id FROM pages WHERE workspace_id=? AND slug='three-screens')`, workspace, workspace).Scan(&latest); err != nil || latest != 1 {
		t.Fatalf("read-only request changed durable head: latest=%d err=%v", latest, err)
	}
}
