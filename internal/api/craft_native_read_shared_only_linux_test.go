package api

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The native Craft router needs Linux CLOCK_BOOTTIME.
// A read-only CLI token of a person with Edit keeps every private read: the
// rule is about the person's authority, not the token's write scope. A reader's
// token reaches none of it over the real router, and no other page route serves
// the native bytes to them.
func TestCraftNativeRoutesServeReadersOnlyTheSharedVersion(t *testing.T) {
	t.Setenv("CREWSHIP_ARTIFACT_PROJECTS_ENABLED", "true")
	t.Setenv("CREWSHIP_ARTIFACT_DASHBOARD_DATA_ENABLED", "true")
	t.Setenv("CREWSHIP_ARTIFACT_SLIDES_ENABLED", "true")
	t.Setenv("CREWSHIP_ARTIFACTS_DESIGN_ENABLED", "true")
	s := craftNativeSharedOnlyFixture(t)
	f := s.f
	profile := s.d.applications["wordcraft"]
	craftNativeVersionsUser(t, f, "shared-only-route-read", "Reader", true, "read")
	reader := mintIngressScopedToken(t, f.db, "shared-only-route-read", "native-shared-only-read", "pages:read")
	ownerRead := mintIngressScopedToken(t, f.db, f.userID, "native-shared-only-owner", "pages:read")
	router, err := NewRouter(f.db, "native-shared-only-fixture-secret-32chars", newTestLogger(), WithCraftNativeHTTP(CraftNativeHTTPConfig{Enabled: true}))
	if err != nil {
		t.Fatal(err)
	}
	defer router.Shutdown()
	router.craftNativeDocuments.applications["wordcraft"] = profile
	server := httptest.NewServer(router)
	defer server.Close()
	client := &http.Client{Timeout: 10 * time.Second}
	call := func(method, path, bearer, admission string) (int, string) {
		t.Helper()
		separator := "?"
		if strings.Contains(path, "?") {
			separator = "&"
		}
		req, err := http.NewRequest(method, server.URL+"/api/v1/pages/@native-page"+path+separator+"workspace_id="+f.wsID, strings.NewReader("{}"))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+bearer)
		req.Header.Set("Content-Type", "application/json")
		if admission != "" {
			req.Header.Set("X-Craft-Session", admission)
		}
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		raw, err := io.ReadAll(res.Body)
		if err != nil {
			t.Fatal(err)
		}
		return res.StatusCode, string(raw)
	}
	issue := func(bearer string) string {
		t.Helper()
		status, raw := call(http.MethodPost, "/craft/sessions", bearer, "")
		var admission struct {
			Token string `json:"session_token"`
		}
		if status != 201 || json.Unmarshal([]byte(raw), &admission) != nil {
			t.Fatalf("admission: %d %s", status, raw)
		}
		return admission.Token
	}
	private := []string{"/craft/document", "/craft/document/versions", "/craft/document/versions/1", "/craft/document/versions/3", "/craft/proposals", "/craft/proposals/" + craftNativeProposalID}
	readerAdmission, ownerAdmission := issue(reader), issue(ownerRead)
	for _, path := range private {
		if status, raw := call(http.MethodGet, path, reader, readerAdmission); status != 403 {
			t.Fatalf("reader %s: %d %s", path, status, raw)
		} else {
			assertCraftNativeNoPrivateContent(t, path, raw)
		}
		if status, raw := call(http.MethodGet, path, ownerRead, ownerAdmission); status != 200 {
			t.Fatalf("read-scoped token of the owner %s: %d %s", path, status, raw)
		}
	}
	if status, raw := call(http.MethodGet, "/craft/preview", reader, ""); status != 200 {
		t.Fatalf("reader preview: %d %s", status, raw)
	} else if !strings.Contains(raw, base64.StdEncoding.EncodeToString([]byte(craftNativeSharedContent))) {
		t.Fatalf("preview is not the shared version: %s", raw)
	}
	for _, path := range []string{"", "/access/me", "/artifact", "/artifact/versions", "/artifact/comments", "/artifact/data", "/artifact/data/rows", "/artifact/slides", "/artifact/export.pdf", "/document", "/document/checkpoints", "/design", "/design/versions", "/design/comments", "/slides", "/workbook", "/workbook/commits", "/mooring", "/project", "/project/source", "/project/history", "/export", "/versions"} {
		status, raw := call(http.MethodGet, path, reader, "")
		assertCraftNativeNoPrivateContent(t, "GET page"+path, raw)
		if strings.Contains(raw, base64.StdEncoding.EncodeToString([]byte(craftNativeSharedContent))) || strings.Contains(raw, craftNativeSharedContent) {
			t.Fatalf("GET page%s served bytes outside the preview: %d %s", path, status, raw)
		}
	}
}
