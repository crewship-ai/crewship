package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
)

// #1348: the sidecar's hybrid memory forward authenticated with only
// X-Internal-Token, so the host resolved scope="own" against the internal
// token's identity for EVERY sibling in a shared crew container. The internal
// hybrid route now takes the acting agent from the sidecar-forwarded
// X-Acting-Agent-Slug header — but only after resolving the slug INSIDE the
// workspace/crew the internal token is cryptographically bound to. The header
// can only ever narrow the token's authority, never widen it: an unknown
// slug, a slug in a sibling crew, a slug in a foreign workspace, a missing
// header, and an unbound (master) token are all refused with 403.
//
// Tests drive the PRODUCTION path: the real requireInternal middleware in
// front of the real SearchInternal handler, over HTTP, with real derived
// tokens — not a hand-rolled context.

const hybridTestMaster = "hybrid-test-master-token"

// stubHybridEmbedder satisfies episodic.Embedder so HybridSearch runs its
// episodic lane. journal_embeddings stays empty, so recall comes from the
// BM25 lane over journal_entries_fts — which is exactly the per-agent-scoped
// query the isolation assertions need.
type stubHybridEmbedder struct{}

func (stubHybridEmbedder) Embed(_ context.Context, _ string) ([]float32, error) {
	return []float32{1, 0}, nil
}
func (stubHybridEmbedder) Dim() int      { return 2 }
func (stubHybridEmbedder) Model() string { return "stub" }

// newInternalHybridFixture seeds two crews in one workspace plus a foreign
// workspace, gives alpha and beta (crew-1) one private episodic entry each,
// and serves requireInternal(SearchInternal) over httptest.
//
// Layout:
//
//	ws-hyb / crew-hyb-1: alpha (agent-hyb-alpha), beta (agent-hyb-beta)
//	ws-hyb / crew-hyb-2: gamma (agent-hyb-gamma)
//	ws-hyb-2           : delta (agent-hyb-delta)
func newInternalHybridFixture(t *testing.T) *httptest.Server {
	t.Helper()
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	execOrFatal(t, db, `INSERT INTO workspaces (id, name, slug) VALUES ('ws-hyb-2', 'Other', 'other-hyb')`)
	// The seeded workspace id is fixed by the helper; alias it for clarity.
	if wsID != "test-workspace-id" {
		t.Fatalf("unexpected seeded workspace id %q", wsID)
	}

	seedCrewRow(t, db, "crew-hyb-1", wsID, "Hybrid One", "hybrid-one")
	seedCrewRow(t, db, "crew-hyb-2", wsID, "Hybrid Two", "hybrid-two")

	execOrFatal(t, db, `INSERT INTO agents (id, workspace_id, crew_id, name, slug) VALUES ('agent-hyb-alpha', ?, 'crew-hyb-1', 'Alpha', 'alpha')`, wsID)
	execOrFatal(t, db, `INSERT INTO agents (id, workspace_id, crew_id, name, slug) VALUES ('agent-hyb-beta', ?, 'crew-hyb-1', 'Beta', 'beta')`, wsID)
	execOrFatal(t, db, `INSERT INTO agents (id, workspace_id, crew_id, name, slug) VALUES ('agent-hyb-gamma', ?, 'crew-hyb-2', 'Gamma', 'gamma')`, wsID)
	execOrFatal(t, db, `INSERT INTO agents (id, workspace_id, name, slug) VALUES ('agent-hyb-delta', 'ws-hyb-2', 'Delta', 'delta')`)

	// One private episodic memory per agent — the journal FTS triggers index
	// these on insert, so the BM25 lane can find them.
	execOrFatal(t, db, `INSERT INTO journal_entries (id, workspace_id, crew_id, agent_id, entry_type, severity, actor_type, summary)
		VALUES ('je-hyb-alpha', ?, 'crew-hyb-1', 'agent-hyb-alpha', 'memory.written', 'info', 'agent', 'alphasecret rollout plan')`, wsID)
	execOrFatal(t, db, `INSERT INTO journal_entries (id, workspace_id, crew_id, agent_id, entry_type, severity, actor_type, summary)
		VALUES ('je-hyb-beta', ?, 'crew-hyb-1', 'agent-hyb-beta', 'memory.written', 'info', 'agent', 'betasecret rollout plan')`, wsID)

	h := NewMemoryHybridSearchHandler(db, newTestLogger())
	h.SetEmbedder(stubHybridEmbedder{})
	ih := NewInternalHandler(db, hybridTestMaster, newTestLogger())
	srv := httptest.NewServer(ih.requireInternal(http.HandlerFunc(h.SearchInternal)))
	t.Cleanup(srv.Close)
	return srv
}

func postInternalHybrid(t *testing.T, srv *httptest.Server, token, actingSlug string, body map[string]any) (*http.Response, string) {
	t.Helper()
	b, _ := json.Marshal(body)
	req, err := http.NewRequest("POST", srv.URL+"/api/v1/internal/memory/search/hybrid", bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Internal-Token", token)
	if actingSlug != "" {
		req.Header.Set("X-Acting-Agent-Slug", actingSlug)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(resp.Body)
	return resp, buf.String()
}

func crew1Token() string {
	return internaltoken.DeriveCrewToken(hybridTestMaster, "test-workspace-id", "crew-hyb-1")
}

// The core isolation invariant: with scope="own", each sibling gets ONLY its
// own episodic slice — beta must never see alpha's entry (the #1348 leak) and
// vice versa. Content-level assertion, not status-only: a handler resolving
// the wrong identity while returning 200 would pass a status check.
func TestInternalHybridSearch_OwnScopeIsPerActingAgent(t *testing.T) {
	srv := newInternalHybridFixture(t)
	for _, tc := range []struct {
		slug       string
		wantHit    string
		wantAbsent string
	}{
		{"beta", "betasecret", "alphasecret"},
		{"alpha", "alphasecret", "betasecret"},
	} {
		t.Run(tc.slug, func(t *testing.T) {
			resp, body := postInternalHybrid(t, srv, crew1Token(), tc.slug,
				map[string]any{"query": "rollout plan", "scope": "own"})
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, want 200: %s", resp.StatusCode, body)
			}
			if !strings.Contains(body, tc.wantHit) {
				t.Errorf("%s did not get its own episodic hit: %s", tc.slug, body)
			}
			if strings.Contains(body, tc.wantAbsent) {
				t.Errorf("%s got a SIBLING's private episodic memory: %s", tc.slug, body)
			}
		})
	}
}

// Empty scope ("" — the sidecar's scope=both translation) defaults to own and
// must narrow to the acting agent the same way.
func TestInternalHybridSearch_EmptyScopeNarrowsToActingAgent(t *testing.T) {
	srv := newInternalHybridFixture(t)
	resp, body := postInternalHybrid(t, srv, crew1Token(), "beta",
		map[string]any{"query": "rollout plan"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.StatusCode, body)
	}
	if strings.Contains(body, "alphasecret") {
		t.Errorf("empty scope leaked a sibling's own-scope memory: %s", body)
	}
}

// A slug outside the token's crew — even one in the SAME workspace — is a
// 403, never a fallback to a wider identity.
func TestInternalHybridSearch_SlugOutsideBoundCrew403(t *testing.T) {
	srv := newInternalHybridFixture(t)
	resp, body := postInternalHybrid(t, srv, crew1Token(), "gamma",
		map[string]any{"query": "rollout plan", "scope": "own"})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("sibling-crew slug: status = %d, want 403: %s", resp.StatusCode, body)
	}
}

// A slug that only exists in a FOREIGN workspace resolves nowhere inside the
// token's binding → 403.
func TestInternalHybridSearch_SlugOutsideWorkspace403(t *testing.T) {
	srv := newInternalHybridFixture(t)
	for _, slug := range []string{"delta", "no-such-agent"} {
		resp, body := postInternalHybrid(t, srv, crew1Token(), slug,
			map[string]any{"query": "rollout plan", "scope": "own"})
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("slug %q: status = %d, want 403: %s", slug, resp.StatusCode, body)
		}
	}
}

// The internal route requires the acting identity: a request without the
// header must not fall back to any ambient identity.
func TestInternalHybridSearch_MissingActingSlug403(t *testing.T) {
	srv := newInternalHybridFixture(t)
	resp, body := postInternalHybrid(t, srv, crew1Token(), "",
		map[string]any{"query": "rollout plan", "scope": "own"})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("missing header: status = %d, want 403: %s", resp.StatusCode, body)
	}
}

// A master-token caller carries no workspace binding to resolve the slug
// inside — refuse rather than guess (narrow-never-widen).
func TestInternalHybridSearch_UnboundMasterToken403(t *testing.T) {
	srv := newInternalHybridFixture(t)
	resp, body := postInternalHybrid(t, srv, hybridTestMaster, "beta",
		map[string]any{"query": "rollout plan", "scope": "own"})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("master token: status = %d, want 403: %s", resp.StatusCode, body)
	}
}

// crew_shared: a body crew_id that disagrees with the token's bound crew is
// the enumerate-a-sibling-crew forgery — 403. An omitted crew_id is filled
// from the binding and succeeds for a member of that crew.
func TestInternalHybridSearch_CrewSharedPinnedToBoundCrew(t *testing.T) {
	srv := newInternalHybridFixture(t)

	resp, body := postInternalHybrid(t, srv, crew1Token(), "beta",
		map[string]any{"query": "rollout plan", "scope": "crew_shared", "crew_id": "crew-hyb-2"})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("foreign crew_id: status = %d, want 403: %s", resp.StatusCode, body)
	}

	resp, body = postInternalHybrid(t, srv, crew1Token(), "beta",
		map[string]any{"query": "rollout plan", "scope": "crew_shared"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("bound-crew crew_shared: status = %d, want 200: %s", resp.StatusCode, body)
	}
}

// A workspace-bound (wsv1) token has no crew binding; the slug still must
// resolve inside the token's workspace, and own-scope still narrows to the
// acting agent.
func TestInternalHybridSearch_WorkspaceBoundTokenNarrows(t *testing.T) {
	srv := newInternalHybridFixture(t)
	wsTok := internaltoken.DeriveWorkspaceToken(hybridTestMaster, "test-workspace-id")

	resp, body := postInternalHybrid(t, srv, wsTok, "beta",
		map[string]any{"query": "rollout plan", "scope": "own"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.StatusCode, body)
	}
	if strings.Contains(body, "alphasecret") {
		t.Errorf("workspace-bound token leaked a sibling's own-scope memory: %s", body)
	}

	resp, body = postInternalHybrid(t, srv, wsTok, "delta",
		map[string]any{"query": "rollout plan", "scope": "own"})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("foreign-workspace slug on ws token: status = %d, want 403: %s", resp.StatusCode, body)
	}
}
