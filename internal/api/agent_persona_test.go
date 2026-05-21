package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/database"
	"github.com/crewship-ai/crewship/internal/memory"
	"github.com/crewship-ai/crewship/internal/policy"
)

// personaTestRig spins up a SQLite + tmp output base + handler
// pre-seeded with one workspace, one crew, one agent. Returns the
// handler and the seeded identifiers so each test can mint requests
// against the right paths.
type personaTestRig struct {
	h       *PersonaHandler
	wsID    string
	crewID  string
	agentID string
	output  string
}

func newPersonaTestRig(t *testing.T) *personaTestRig {
	t.Helper()
	dir := t.TempDir()
	dbh, err := database.Open("file:" + filepath.Join(dir, "p.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	silent := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := database.Migrate(context.Background(), dbh.DB, silent); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	t.Cleanup(func() { _ = dbh.Close() })

	if _, err := dbh.Exec(`INSERT INTO workspaces (id, name, slug) VALUES ('ws1','W','w')`); err != nil {
		t.Fatalf("seed ws: %v", err)
	}
	if _, err := dbh.Exec(`INSERT INTO crews (id, workspace_id, name, slug, network_mode, allowed_domains)
		VALUES ('crew1','ws1','C','c','free','[]')`); err != nil {
		t.Fatalf("seed crew: %v", err)
	}
	if _, err := dbh.Exec(`INSERT INTO agents (id, workspace_id, crew_id, slug, name, agent_role, role_title)
		VALUES ('a1','ws1','crew1','alice','Alice','AGENT','Engineer')`); err != nil {
		t.Fatalf("seed agent: %v", err)
	}
	resolver := policy.NewResolver(dbh.DB)
	h := NewPersonaHandler(dbh.DB, silent, dir, resolver)
	return &personaTestRig{h: h, wsID: "ws1", crewID: "crew1", agentID: "a1", output: dir}
}

// authedReq builds a request with the workspace context already set
// (mirrors the wsCtx middleware) so individual tests don't reach
// into middleware internals.
func (r *personaTestRig) authedReq(t *testing.T, method, target string, body any) *http.Request {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatalf("encode body: %v", err)
		}
	}
	req := httptest.NewRequest(method, target, &buf)
	req.SetPathValue("agentId", r.agentID)
	req.SetPathValue("crewId", r.crewID)
	ctx := context.WithValue(req.Context(), ctxWorkspaceID, r.wsID)
	ctx = context.WithValue(ctx, ctxUser, &AuthUser{ID: "u1"})
	return req.WithContext(ctx)
}

// Smoke test the GET → PUT → DELETE round trip.
func TestPersona_AgentRoundTrip(t *testing.T) {
	r := newPersonaTestRig(t)

	// Initial GET returns from_default=true with synthesized content.
	rec := httptest.NewRecorder()
	r.h.GetAgentPersona(rec, r.authedReq(t, http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET initial: status=%d body=%s", rec.Code, rec.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got["from_default"] != true {
		t.Errorf("expected from_default=true on empty persona; got %v", got)
	}
	if !strings.Contains(got["content"].(string), "Engineer") {
		t.Errorf("default should include role title; got %q", got["content"])
	}

	// PUT lands.
	rec = httptest.NewRecorder()
	r.h.PutAgentPersona(rec, r.authedReq(t, http.MethodPut, "/", map[string]string{
		"content": "Be Pavel-shaped: terse, technical, Czech.",
	}))
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT: status=%d body=%s", rec.Code, rec.Body.String())
	}

	// GET now returns the agent layer.
	rec = httptest.NewRecorder()
	r.h.GetAgentPersona(rec, r.authedReq(t, http.MethodGet, "/", nil))
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got["layer"] != "agent" || !strings.Contains(got["content"].(string), "Pavel") {
		t.Errorf("expected agent layer with Pavel content; got %+v", got)
	}

	// History row landed.
	rec = httptest.NewRecorder()
	r.h.GetAgentPersonaHistory(rec, r.authedReq(t, http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("history: %d %s", rec.Code, rec.Body.String())
	}
	var hist map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &hist)
	entries := hist["entries"].([]any)
	if len(entries) != 1 {
		t.Errorf("expected 1 history entry; got %d", len(entries))
	}

	// DELETE drops the agent layer.
	rec = httptest.NewRecorder()
	r.h.DeleteAgentPersona(rec, r.authedReq(t, http.MethodDelete, "/", nil))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE: %d %s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	r.h.GetAgentPersona(rec, r.authedReq(t, http.MethodGet, "/", nil))
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got["from_default"] != true {
		t.Errorf("expected default to resurface after delete; got %+v", got)
	}
}

func TestPersona_PutRejectsOversize(t *testing.T) {
	r := newPersonaTestRig(t)
	big := strings.Repeat("x", memory.PersonaCapBytes+1)
	rec := httptest.NewRecorder()
	r.h.PutAgentPersona(rec, r.authedReq(t, http.MethodPut, "/", map[string]string{"content": big}))
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("expected 413; got %d body=%s", rec.Code, rec.Body.String())
	}
}

// Suggestion flow at guided (default) autonomy: the policy resolver
// returns inbox_approve so the persona file MUST NOT be written;
// instead an audit_logs row with action=persona.suggest_pending
// lands for the inbox to pick up.
func TestPersona_SuggestEnqueuesAtGuidedAutonomy(t *testing.T) {
	r := newPersonaTestRig(t)
	body := map[string]string{
		"content":   "I should be more terse based on session feedback.",
		"rationale": "user kept asking me to summarize",
	}
	rec := httptest.NewRecorder()
	r.h.SuggestAgentPersona(rec, r.authedReq(t, http.MethodPost, "/", body))
	if rec.Code != http.StatusOK {
		t.Fatalf("suggest: %d body=%s", rec.Code, rec.Body.String())
	}
	var got map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got["decision"] != string(policy.DecisionInboxApprove) {
		t.Errorf("expected inbox_approve decision; got %v", got["decision"])
	}
	if got["applied"] != false || got["pending"] != true {
		t.Errorf("expected pending=true applied=false; got %+v", got)
	}

	// audit_logs row landed.
	var cnt int
	if err := r.h.db.QueryRow(`SELECT COUNT(*) FROM audit_logs WHERE action='persona.suggest_pending'`).Scan(&cnt); err != nil {
		t.Fatalf("count audit: %v", err)
	}
	if cnt != 1 {
		t.Errorf("expected 1 pending audit row; got %d", cnt)
	}

	// The on-disk persona MUST stay unwritten.
	paths := memory.PersonaPaths{AgentDir: filepath.Join(r.output, "crews", r.crewID, "agents", "alice", ".memory")}
	resolved, _ := memory.LoadPersona(paths)
	if resolved.Content != "" {
		t.Errorf("suggest at guided MUST NOT write persona; got %q", resolved.Content)
	}
}

// Suggestion at full autonomy: policy returns auto_journal → persona
// is written immediately and version row lands.
func TestPersona_SuggestAutoAppliesAtFullAutonomy(t *testing.T) {
	r := newPersonaTestRig(t)
	// Flip the crew to full autonomy + journal-only behavior.
	if _, err := r.h.db.Exec(`UPDATE crews SET autonomy_level='full' WHERE id=?`, r.crewID); err != nil {
		t.Fatalf("set autonomy: %v", err)
	}
	// Invalidate the resolver so the policy snapshot picks up the change.
	if r.h.policyResolver != nil {
		r.h.policyResolver.Invalidate(r.crewID)
	}

	rec := httptest.NewRecorder()
	r.h.SuggestAgentPersona(rec, r.authedReq(t, http.MethodPost, "/", map[string]string{
		"content": "Be fully autonomous and direct.",
	}))
	if rec.Code != http.StatusOK {
		t.Fatalf("suggest: %d %s", rec.Code, rec.Body.String())
	}
	var got map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got["applied"] != true {
		t.Errorf("expected applied=true at full autonomy; got %+v", got)
	}

	// On-disk persona MUST be written.
	paths := memory.PersonaPaths{AgentDir: filepath.Join(r.output, "crews", r.crewID, "agents", "alice", ".memory")}
	resolved, _ := memory.LoadPersona(paths)
	if !strings.Contains(resolved.Content, "autonomous") {
		t.Errorf("expected persona to land at full autonomy; got %q", resolved.Content)
	}
}

// Crew-flavor round trip — same shape as agent-flavor but writes to
// the shared/.memory path.
func TestPersona_CrewRoundTrip(t *testing.T) {
	r := newPersonaTestRig(t)
	rec := httptest.NewRecorder()
	r.h.PutCrewPersona(rec, r.authedReq(t, http.MethodPut, "/", map[string]string{
		"content": "Crew wide tone: blunt + Czech.",
	}))
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT crew: %d %s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	r.h.GetCrewPersona(rec, r.authedReq(t, http.MethodGet, "/", nil))
	var got map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if !strings.Contains(got["content"].(string), "blunt") {
		t.Errorf("crew GET missing content; got %+v", got)
	}
}
