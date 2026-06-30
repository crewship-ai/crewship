// Regression guard for the removal of the PUBLIC pipelines test_run surface.
//
// The public POST /api/v1/workspaces/{ws}/pipelines/test_run route + its
// TestRun handler were deleted: you cannot run an agent "dry" (its scripts
// have uninterceptable side effects), so a real run is just /run and the only
// remaining draft validation gate is the internal save gate
// (/api/v1/internal/pipelines/test_run, dry-run). This test confirms the
// public route is no longer wired into the Router — a POST never reaches a
// TestRun handler.
package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/auth/sessions"
)

func TestPipelineTestRunRoute_NotRegistered(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	seedTestWorkspace(t, db, userID)

	const secret = "test-secret-for-jwt-signing-32chars!!"
	r, err := NewRouter(db, secret, newTestLogger())
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}

	v, err := auth.NewJWTValidator(secret)
	if err != nil {
		t.Fatalf("auth.NewJWTValidator: %v", err)
	}
	sess, err := sessions.NewDBStore(db).Create(t.Context(), userID, "test", "127.0.0.1", auth.RefreshTokenTTL)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	tok, err := v.IssueAccessToken(userID, sess.ID, "Test User", "test@example.com")
	if err != nil {
		t.Fatalf("issue access token: %v", err)
	}

	body := `{"definition":{"name":"x","steps":[]},"author_crew_id":"crew_a"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/workspaces/ws_1/pipelines/test_run", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	// With the route gone, net/http resolves the path to the GET {slug}
	// pattern (wrong method) → 405, or 404 when nothing matches. Either way
	// it must NOT be a status the old TestRun handler produced (400/422/503/200).
	switch rr.Code {
	case http.StatusNotFound, http.StatusMethodNotAllowed:
		// good — the public test_run handler is no longer reachable.
	default:
		t.Fatalf("POST .../pipelines/test_run = %d; the public TestRun route should be removed (want 404/405); body=%s",
			rr.Code, rr.Body.String())
	}
}
