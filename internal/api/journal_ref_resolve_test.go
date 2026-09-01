package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

// #2206: `agent_id` / `crew_id` on the journal read endpoints were bound
// straight into a SQL equality on the id column, so the search box's own
// placeholder example (`agent:viktor`) and `crewship journal --agent
// morgan` both answered 0 — a name is not an id. The parameters now
// accept an id, a slug or a display name, resolved workspace-scoped
// before the query runs.

// seedRefEntities inserts a crew and an agent with a distinct id, slug
// and name so a test can prove which one the parameter matched on.
func seedRefEntities(t *testing.T, h *JournalHandler, wsID string) {
	t.Helper()
	ctx := context.Background()
	if _, err := h.db.ExecContext(ctx,
		`INSERT INTO crews (id, workspace_id, name, slug) VALUES (?, ?, 'Night Watch', 'night-watch')`,
		"cr_ref", wsID); err != nil {
		t.Fatalf("seed crew: %v", err)
	}
	if _, err := h.db.ExecContext(ctx,
		`INSERT INTO agents (id, workspace_id, crew_id, name, slug) VALUES (?, ?, 'cr_ref', 'Morgan', 'morgan-reyes')`,
		"ag_ref", wsID); err != nil {
		t.Fatalf("seed agent: %v", err)
	}
}

// seedScopedJournalRow inserts an entry carrying crew_id + agent_id.
func seedScopedJournalRow(t *testing.T, h *JournalHandler, id, wsID, crewID, agentID string) {
	t.Helper()
	_, err := h.db.ExecContext(context.Background(), `
		INSERT INTO journal_entries (id, workspace_id, crew_id, agent_id, ts, entry_type, severity, priority, actor_type, summary, payload, refs)
		VALUES (?, ?, ?, ?, ?, 'run.started', 'info', 'normal', 'agent', 'scoped', '{}', '{}')`,
		id, wsID, crewID, agentID, time.Now().UTC().Format("2006-01-02T15:04:05.000Z"))
	if err != nil {
		t.Fatalf("seed scoped entry %s: %v", id, err)
	}
}

// journalCount drives GET /api/v1/journal/count with one filter param
// and returns the total. The value is URL-encoded, so a display name
// with a space in it reaches the handler intact.
func journalCount(t *testing.T, h *JournalHandler, userID, wsID, param, value string) int64 {
	t.Helper()
	query := url.Values{param: []string{value}}.Encode()
	req := httptest.NewRequest("GET", "/api/v1/journal/count?"+query, nil)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Count(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("count?%s: status=%d body=%s", query, rr.Code, rr.Body.String())
	}
	var resp struct {
		Total int64 `json:"total"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v body=%s", err, rr.Body.String())
	}
	return resp.Total
}

func TestJournalHandler_AgentRef_ResolvesSlugAndName(t *testing.T) {
	h, userID, wsID, _ := newJournalHandlerTest(t)
	seedRefEntities(t, h, wsID)
	seedScopedJournalRow(t, h, "j_ref1", wsID, "cr_ref", "ag_ref")
	seedJournalRow(t, h, "j_unscoped", wsID, "run.started", "info", "no agent", time.Time{})

	for _, ref := range []string{"ag_ref", "morgan-reyes", "Morgan", "morgan", "MORGAN"} {
		if n := journalCount(t, h, userID, wsID, "agent_id", ref); n != 1 {
			t.Errorf("agent_id=%s → %d, want 1", ref, n)
		}
	}
}

func TestJournalHandler_CrewRef_ResolvesSlugAndName(t *testing.T) {
	h, userID, wsID, _ := newJournalHandlerTest(t)
	seedRefEntities(t, h, wsID)
	seedScopedJournalRow(t, h, "j_ref1", wsID, "cr_ref", "ag_ref")
	seedJournalRow(t, h, "j_unscoped", wsID, "run.started", "info", "no crew", time.Time{})

	for _, ref := range []string{"cr_ref", "night-watch", "Night Watch"} {
		if n := journalCount(t, h, userID, wsID, "crew_id", ref); n != 1 {
			t.Errorf("crew_id=%s → %d, want 1", ref, n)
		}
	}
}

// A name that matches nothing must stay unmatchable rather than being
// dropped — silently widening the filter would show the whole workspace
// under an agent filter the user believes is applied.
func TestJournalHandler_AgentRef_UnknownStaysUnmatchable(t *testing.T) {
	h, userID, wsID, _ := newJournalHandlerTest(t)
	seedRefEntities(t, h, wsID)
	seedScopedJournalRow(t, h, "j_ref1", wsID, "cr_ref", "ag_ref")

	if n := journalCount(t, h, userID, wsID, "agent_id", "nobody"); n != 0 {
		t.Errorf("agent_id=nobody → %d, want 0", n)
	}
}

// An id whose agent row is gone (hard-deleted, or soft-deleted after the
// entries were written) must still reach that agent's history: journal
// entries outlive the entities they name.
func TestJournalHandler_AgentRef_DanglingIDStillFilters(t *testing.T) {
	h, userID, wsID, _ := newJournalHandlerTest(t)
	seedScopedJournalRow(t, h, "j_ref1", wsID, "cr_gone", "ag_gone")
	seedJournalRow(t, h, "j_other", wsID, "run.started", "info", "other", time.Time{})

	if n := journalCount(t, h, userID, wsID, "agent_id", "ag_gone"); n != 1 {
		t.Errorf("agent_id=ag_gone → %d, want 1 (a dangling id must still filter)", n)
	}
	if n := journalCount(t, h, userID, wsID, "crew_id", "cr_gone"); n != 1 {
		t.Errorf("crew_id=cr_gone → %d, want 1", n)
	}
}

// Resolution is workspace-scoped: another tenant's agent name must not
// resolve to that tenant's id.
func TestJournalHandler_AgentRef_DoesNotResolveCrossWorkspace(t *testing.T) {
	h, userID, wsID, _ := newJournalHandlerTest(t)
	ctx := context.Background()
	if _, err := h.db.ExecContext(ctx,
		`INSERT INTO workspaces (id, name, slug) VALUES ('ws_other', 'Other', 'other')`); err != nil {
		t.Fatalf("seed foreign workspace: %v", err)
	}
	if _, err := h.db.ExecContext(ctx,
		`INSERT INTO agents (id, workspace_id, name, slug) VALUES ('ag_other', 'ws_other', 'Morgan', 'morgan')`); err != nil {
		t.Fatalf("seed foreign agent: %v", err)
	}
	seedScopedJournalRow(t, h, "j_here", wsID, "", "ag_other")

	// "Morgan" exists only in the other workspace, so it must not resolve
	// here — and the row that happens to carry the foreign id is reachable
	// by id alone, not by the foreign name.
	if n := journalCount(t, h, userID, wsID, "agent_id", "Morgan"); n != 0 {
		t.Errorf("agent_id=Morgan → %d, want 0 (cross-workspace name leak)", n)
	}
}

// Multiple agents can share a display name (only slug is unique per
// workspace). An ambiguous name must widen to every match rather than
// picking one arbitrarily.
func TestJournalHandler_AgentRef_AmbiguousNameMatchesAll(t *testing.T) {
	h, userID, wsID, _ := newJournalHandlerTest(t)
	ctx := context.Background()
	for _, a := range []struct{ id, slug string }{{"ag_m1", "morgan-a"}, {"ag_m2", "morgan-b"}} {
		if _, err := h.db.ExecContext(ctx,
			`INSERT INTO agents (id, workspace_id, name, slug) VALUES (?, ?, 'Morgan', ?)`,
			a.id, wsID, a.slug); err != nil {
			t.Fatalf("seed agent %s: %v", a.id, err)
		}
	}
	seedScopedJournalRow(t, h, "j_m1", wsID, "", "ag_m1")
	seedScopedJournalRow(t, h, "j_m2", wsID, "", "ag_m2")

	if n := journalCount(t, h, userID, wsID, "agent_id", "Morgan"); n != 2 {
		t.Errorf("agent_id=Morgan → %d, want 2 (both agents named Morgan)", n)
	}
}
