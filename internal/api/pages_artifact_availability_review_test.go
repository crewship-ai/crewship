package api

import (
	"net/http/httptest"
	"testing"
)

func TestArtifactAvailabilityIndependentReadScopeContract(t *testing.T) {
	h, workspace, owner, _ := artifactProjectFixture(t)
	reader := mintIngressScopedToken(t, h.db, owner, "availability-review-reader", "pages:read")
	unrelated := mintIngressScopedToken(t, h.db, owner, "availability-review-unrelated", "issues:read")
	router, err := NewRouter(h.db, "synthetic-availability-review-secret-32", newTestLogger(), WithInternalToken("synthetic-availability-review-host"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(router.Shutdown)
	for _, tc := range []struct {
		token string
		want  int
	}{{reader, 200}, {unrelated, 403}} {
		req := httptest.NewRequest("GET", "/api/v1/pages/artifacts/availability", nil)
		req.Header.Set("Authorization", "Bearer "+tc.token)
		req.Header.Set("X-Workspace-ID", workspace)
		got := httptest.NewRecorder()
		router.ServeHTTP(got, req)
		if got.Code != tc.want {
			t.Errorf("documented pages:read boundary: got%d want%d body%s", got.Code, tc.want, got.Body)
		}
	}
}
