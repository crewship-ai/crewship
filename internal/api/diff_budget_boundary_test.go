package api

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/auth/sessions"
	"github.com/crewship-ai/crewship/internal/consolidate"
)

// Both versions go through the real user Save path with valid DSL. Unique
// short lines and one changed value keep the baseline fixture cheap.
func TestVersionDiff_BoundedValidDefinitions(t *testing.T) {
	h, user, ws, crew := covPCHandler(t)
	const slug = "bounded-valid-diff"
	items := make([]string, 6000)
	for i := range items {
		items[i] = fmt.Sprintf("owned-item-%05d", i)
	}
	for version := 1; version <= 2; version++ {
		if version == 2 {
			items[3000] = "owned-one-changed-value"
		}
		definition := map[string]any{"name": slug, "inputs": []any{map[string]any{"name": "items", "type": "array", "default": items}}, "steps": []any{map[string]any{"id": "a", "type": "agent_run", "agent_slug": "agent_lead", "prompt": "hi"}}}
		raw, err := json.Marshal(definition)
		if err != nil {
			t.Fatal(err)
		}
		body := covPCSaveBody(slug, crew, map[string]any{"definition": json.RawMessage(raw)})
		if len(body) >= 1<<20 {
			t.Fatal("fixture exceeds Save body cap")
		}
		req := withWorkspaceUser(httptest.NewRequest("POST", "/api/v1/pipelines", strings.NewReader(body)), user, ws, "OWNER")
		rr := httptest.NewRecorder()
		h.Save(rr, req)
		if rr.Code != http.StatusCreated {
			t.Fatalf("save version%d: %d %s", version, rr.Code, rr.Body.String())
		}
	}
	p, err := h.store.GetBySlug(t.Context(), ws, slug)
	if err != nil {
		t.Fatal(err)
	}
	a, err := h.store.GetVersion(t.Context(), p.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	b, err := h.store.GetVersion(t.Context(), p.ID, 2)
	if err != nil {
		t.Fatal(err)
	}
	if a.DefinitionHash == b.DefinitionHash {
		t.Fatal("fixture versions must differ")
	}
	if strings.Count(prettyJSONOrRaw(a.DefinitionJSON), "\n") < 6000 {
		t.Fatal("fixture unexpectedly small")
	}
	rr := requestDiffBoundary(t, h.db, user, ws, "/api/v1/workspaces/"+ws+"/pipelines/"+slug+"/diff?from=1&to=2")
	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("valid large version diff returned%d, want413; body length%d", rr.Code, rr.Body.Len())
	}
	if !strings.Contains(rr.Body.String(), "diff is too large") {
		t.Fatalf("missing explicit diff refusal: %s", rr.Body.String())
	}
}

func TestProposedDiff_BoundedCanonicalLines(t *testing.T) {
	h, db, user, ws, crew := newProposedHandlerTest(t)
	id, proposalPath := seedProposalRow(t, db, ws, crew, "pending")
	var body strings.Builder
	for i := 0; i < 6000; i++ {
		fmt.Fprintf(&body, "owned-canonical-line-%05d\n", i)
	}
	path := consolidate.CanonicalPathForProposal(proposalPath, time.Now().UTC())
	if err := os.WriteFile(path, []byte(body.String()), 0600); err != nil {
		t.Fatal(err)
	}
	_ = h // setup owns the database and staged proposal directory
	rr := requestDiffBoundary(t, db, user, ws, "/api/v1/consolidate/proposed/"+id+"/diff")
	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("canonical diff returned%d, want413; body length%d", rr.Code, rr.Body.Len())
	}
}

// Real route registration, database-backed MEMBER and issued session/JWT. This
// does not inject authorization context into a directly invoked diff method.
func requestDiffBoundary(t *testing.T, db *sql.DB, user, ws, url string) *httptest.ResponseRecorder {
	t.Helper()
	changed, e := db.Exec(`UPDATE workspace_members SET role='MEMBER' WHERE workspace_id=? AND user_id=?`, ws, user)
	if e != nil {
		t.Fatal(e)
	}
	if n, e := changed.RowsAffected(); e != nil || n != 1 {
		t.Fatalf("member fixture rows%d error%v", n, e)
	}
	const secret = "owned-night-diff-boundary-jwt-secret-at-least-32bytes"
	router, e := NewRouter(db, secret, newTestLogger())
	if e != nil {
		t.Fatal(e)
	}
	validator, e := auth.NewJWTValidator(secret)
	if e != nil {
		t.Fatal(e)
	}
	session, e := sessions.NewDBStore(db).Create(t.Context(), user, "owned-diff-boundary", "127.0.0.1", auth.RefreshTokenTTL)
	if e != nil {
		t.Fatal(e)
	}
	token, e := validator.IssueAccessToken(user, session.ID, "Owned fixture", user+"@example.test")
	if e != nil {
		t.Fatal(e)
	}
	req := httptest.NewRequest("GET", url, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-Workspace-ID", ws)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	return rr
}
