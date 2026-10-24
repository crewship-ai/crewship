package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pmezard/go-difflib/difflib"

	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/auth/sessions"
	"github.com/crewship-ai/crewship/internal/config"
)

func TestDiffBudgetSharedRouterAdmissionAndAccess(t *testing.T) {
	h, owner, ws, crew := covPCHandler(t)
	const slug = "owned-small-diff"
	for _, prompt := range []string{"hi", "changed owned prompt"} {
		definition := strings.ReplaceAll(covPCDef(slug), `"prompt":"hi"`, `"prompt":"`+prompt+`"`)
		body := covPCSaveBody(slug, crew, map[string]any{"definition": json.RawMessage(definition)})
		req := withWorkspaceUser(httptest.NewRequest("POST", "/x", strings.NewReader(body)), owner, ws, "OWNER")
		rr := httptest.NewRecorder()
		h.Save(rr, req)
		if rr.Code != http.StatusCreated {
			t.Fatalf("valid Save: %d %s", rr.Code, rr.Body.String())
		}
	}
	proposal, path := seedProposalRow(t, h.db, ws, crew, "pending")
	proposalBody, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// The authenticated caller really is MEMBER in the migrated DB.
	if _, err := h.db.Exec(`UPDATE workspace_members SET role='MEMBER' WHERE workspace_id=? AND user_id=?`, ws, owner); err != nil {
		t.Fatal(err)
	}
	const secret = "owned-diff-router-jwt-secret-at-least-32-bytes"
	router, err := NewRouter(h.db, secret, newTestLogger(), WithDiffLimits(config.DiffConfig{MaxInputBytes: 256 << 10, MaxLines: 2000, MaxConcurrent: 1}))
	if err != nil {
		t.Fatal(err)
	}
	validator, err := auth.NewJWTValidator(secret)
	if err != nil {
		t.Fatal(err)
	}
	tokenFor := func(user string) string {
		session, err := sessions.NewDBStore(h.db).Create(t.Context(), user, "owned-diff", "127.0.0.1", auth.RefreshTokenTTL)
		if err != nil {
			t.Fatal(err)
		}
		token, err := validator.IssueAccessToken(user, session.ID, "Owned fixture", user+"@example.test")
		if err != nil {
			t.Fatal(err)
		}
		return token
	}
	memberToken := tokenFor(owner)
	if _, err := h.db.Exec(`INSERT INTO users(id,email,full_name)VALUES('owned-outsider','owned-outsider@example.test','Outsider')`); err != nil {
		t.Fatal(err)
	}
	outsiderToken := tokenFor("owned-outsider")
	if _, err := h.db.Exec(`INSERT INTO workspaces(id,name,slug)VALUES('owned-other-workspace','Other','owned-other-workspace')`); err != nil {
		t.Fatal(err)
	}
	otherCrew := seedTestCrew(t, h.db, "owned-other-workspace")
	otherProposal, _ := seedProposalRow(t, h.db, "owned-other-workspace", otherCrew, "pending")
	pipelineURL := "/api/v1/workspaces/" + ws + "/pipelines/" + slug + "/diff?from=1&to=2"
	proposalURL := "/api/v1/consolidate/proposed/" + proposal + "/diff"
	request := func(ctx context.Context, url, token string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", url, nil).WithContext(ctx)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		req.Header.Set("X-Workspace-ID", ws)
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, req)
		return rr
	}
	entered, continueWork := make(chan struct{}), make(chan struct{})
	var unblock sync.Once
	done := make(chan struct{})
	t.Cleanup(func() {
		unblock.Do(func() { close(continueWork) })
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("held request did not finish")
		}
	})
	var computations atomic.Int32
	router.diffs.compute = func(w io.Writer, d difflib.UnifiedDiff) error {
		if computations.Add(1) == 1 {
			close(entered)
			<-continueWork
		}
		return difflib.WriteUnifiedDiff(w, d)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go func() { defer close(done); request(ctx, pipelineURL, memberToken) }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("actual authenticated MEMBER did not enter diff computation")
	}
	for _, url := range []string{pipelineURL, proposalURL} {
		rr := request(t.Context(), url, memberToken)
		if rr.Code != 429 || rr.Header().Get("Retry-After") != "1" || !strings.Contains(rr.Body.String(), "diff service is busy") {
			t.Fatalf("shared admission: %s status%d %s", url, rr.Code, rr.Body.String())
		}
	}
	for _, tc := range []struct {
		url, token string
		status     int
	}{
		{pipelineURL, "", 401}, {proposalURL, "", 401},
		{pipelineURL, outsiderToken, 403}, {proposalURL, outsiderToken, 403},
		{"/api/v1/workspaces/" + ws + "/pipelines/missing/diff?from=1&to=2", memberToken, 404},
		{"/api/v1/workspaces/" + ws + "/pipelines/" + slug + "/diff?from=1&to=99", memberToken, 404},
		{"/api/v1/consolidate/proposed/" + otherProposal + "/diff", memberToken, 404},
	} {
		if rr := request(t.Context(), tc.url, tc.token); rr.Code != tc.status {
			t.Fatalf("access before busy: %s status%d want%d %s", tc.url, rr.Code, tc.status, rr.Body.String())
		}
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if rr := request(t.Context(), proposalURL, memberToken); rr.Code != 410 {
		t.Fatalf("missing source before busy: %d %s", rr.Code, rr.Body.String())
	}
	if err := os.WriteFile(path, proposalBody, 0600); err != nil {
		t.Fatal(err)
	}
	cancel()
	if rr := request(t.Context(), proposalURL, memberToken); rr.Code != 429 {
		t.Fatalf("cancel freed running compute: %d", rr.Code)
	}
	if computations.Load() != 1 {
		t.Fatalf("rejected request entered compute: %d", computations.Load())
	}
	unblock.Do(func() { close(continueWork) })
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("computation did not release")
	}
	for _, url := range []string{pipelineURL, proposalURL} {
		if rr := request(t.Context(), url, memberToken); rr.Code != 200 {
			t.Fatalf("slot leaked after completion: %s status%d %s", url, rr.Code, rr.Body.String())
		}
	}
}
