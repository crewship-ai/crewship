package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/encryption"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/memory"
	"github.com/crewship-ai/crewship/internal/policy"
)

// ---------------------------------------------------------------------------
// persona_skillgen_templates_cov_test.go
//
// Branch-coverage fill for the three handlers whose happy paths and a few
// error arms were already pinned by their primary test files
// (agent_persona_test.go, skills_generate_test.go, core_handlers_test.go).
//
// This file targets the *remaining* uncovered branches only:
//
//   agent_persona.go
//     - GetAgentPersonaHistory: limit clamp + DB-error 500 + multi-row list
//     - recordCrewVersion / recordVersion write-row path (crew PUT history)
//     - SuggestAgentPersona: DecisionRejected → 403 arm; resolver-nil default;
//       solo agent (crew_id NULL) path; requireStorage 503 short-circuit
//     - replyAgentLookup: 404 (sql.ErrNoRows) and 500 (generic) arms
//     - DeleteCrewPersona happy path
//
//   skills_generate.go
//     - resolveAnthropicProvider: decrypt-failure (garbage ciphertext) arm
//     (the LLM Messages-API call itself is intentionally NOT exercised — it
//      needs a live Anthropic endpoint; see note in the return summary)
//
//   crew_templates.go
//     - autoAssignCredentials: found+insert (API_KEY + AI_CLI_TOKEN) path,
//       empty-workspace journal entry, ORDER/dedup INSERT OR IGNORE
//     - SetJournal (nil + non-nil)
//     - List: cross-workspace filter / custom (non-builtin) template row
//     - Get: workspace-scoped custom template, builtin OR clause
//     - deployCrewTemplate: explicit crew_slug input + empty-slug 4xx
//
// New helpers are prefixed covPST*; all test funcs prefixed TestCovPST*.
// ---------------------------------------------------------------------------

// covPSTRecordingEmitter captures journal entries so the auto-assign tests can
// assert on the emitted entry types without standing up the real Writer.
type covPSTRecordingEmitter struct {
	entries []journal.Entry
}

func (e *covPSTRecordingEmitter) Emit(_ context.Context, entry journal.Entry) (string, error) {
	e.entries = append(e.entries, entry)
	return "rec", nil
}

func (e *covPSTRecordingEmitter) Flush(_ context.Context) error { return nil }

func (e *covPSTRecordingEmitter) typeCount(t journal.EntryType) int {
	n := 0
	for _, entry := range e.entries {
		if entry.Type == t {
			n++
		}
	}
	return n
}

// covPSTSeedAnthropicCred inserts an ACTIVE Anthropic credential of the given
// type so autoAssignCredentials / resolveAnthropicProvider have a row to find.
func covPSTSeedAnthropicCred(t *testing.T, rig *personaTestRig, id, name, credType, plain string) {
	t.Helper()
	// credentials.created_by → users(id); the persona rig doesn't seed a user,
	// so ensure one exists before inserting (INSERT OR IGNORE keeps it idempotent
	// across multiple cred seeds in one test).
	if _, err := rig.h.db.Exec(
		`INSERT OR IGNORE INTO users (id, email, full_name) VALUES ('u1','u1@x','U')`); err != nil {
		t.Fatalf("seed cred user: %v", err)
	}
	seedCredentialEnc(t, rig.h.db, rig.wsID, "u1", id, name, plain)
	if _, err := rig.h.db.Exec(
		`UPDATE credentials SET provider = 'ANTHROPIC', type = ? WHERE id = ?`, credType, id); err != nil {
		t.Fatalf("twist anthropic cred: %v", err)
	}
}

// --- agent_persona.go ------------------------------------------------------

// TestCovPSTPersonaHistory_LimitClampAndOrder pins the limit-clamp branches
// (<=0 and >100 both snap to 20) and the multi-row DESC list path.
func TestCovPSTPersonaHistory_LimitClampAndOrder(t *testing.T) {
	r := newPersonaTestRig(t)

	// Two PUTs → two history rows for the agent persona path.
	for _, content := range []string{"v1 persona", "v2 persona longer"} {
		rec := httptest.NewRecorder()
		r.h.PutAgentPersona(rec, r.authedReq(t, http.MethodPut, "/", map[string]string{"content": content}))
		if rec.Code != http.StatusOK {
			t.Fatalf("PUT: %d %s", rec.Code, rec.Body.String())
		}
	}

	for _, q := range []string{"?limit=0", "?limit=99999", "?limit=abc", ""} {
		rec := httptest.NewRecorder()
		r.h.GetAgentPersonaHistory(rec, r.authedReq(t, http.MethodGet, "/persona/history"+q, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("history %q: %d %s", q, rec.Code, rec.Body.String())
		}
		var got map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("decode %q: %v", q, err)
		}
		entries := got["entries"].([]any)
		if len(entries) != 2 {
			t.Errorf("%q: expected 2 entries, got %d", q, len(entries))
		}
	}
}

// TestCovPSTPersonaHistory_QueryError exercises the 500 arm: dropping the
// memory_versions table makes the SELECT fail so the handler must 500.
func TestCovPSTPersonaHistory_QueryError(t *testing.T) {
	r := newPersonaTestRig(t)
	if _, err := r.h.db.Exec(`DROP TABLE memory_versions`); err != nil {
		t.Fatalf("drop table: %v", err)
	}
	rec := httptest.NewRecorder()
	r.h.GetAgentPersonaHistory(rec, r.authedReq(t, http.MethodGet, "/persona/history", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected 500 on query error, got %d body=%s", rec.Code, rec.Body.String())
	}
}

// TestCovPSTPersona_RequireStorage503 pins the storage-not-configured
// short-circuit across all persona endpoints (outputBasePath = "").
func TestCovPSTPersona_RequireStorage503(t *testing.T) {
	r := newPersonaTestRig(t)
	r.h.outputBasePath = "" // disable storage

	for _, fn := range []struct {
		name string
		call func(http.ResponseWriter, *http.Request)
	}{
		{"GetAgent", r.h.GetAgentPersona},
		{"PutAgent", r.h.PutAgentPersona},
		{"DeleteAgent", r.h.DeleteAgentPersona},
		{"History", r.h.GetAgentPersonaHistory},
		{"Suggest", r.h.SuggestAgentPersona},
		{"GetCrew", r.h.GetCrewPersona},
		{"PutCrew", r.h.PutCrewPersona},
		{"DeleteCrew", r.h.DeleteCrewPersona},
	} {
		rec := httptest.NewRecorder()
		fn.call(rec, r.authedReq(t, http.MethodGet, "/", map[string]string{"content": "x"}))
		if rec.Code != http.StatusServiceUnavailable {
			t.Errorf("%s: expected 503 when storage unconfigured, got %d", fn.name, rec.Code)
		}
	}
}

// TestCovPSTPersona_AgentNotFound404 pins replyAgentLookup's sql.ErrNoRows arm:
// an unknown agentId yields 404 for the read/write/delete/history/suggest paths.
func TestCovPSTPersona_AgentNotFound404(t *testing.T) {
	r := newPersonaTestRig(t)
	r.agentID = "ghost-agent" // unknown id; resolveAgentPaths → sql.ErrNoRows

	for _, fn := range []struct {
		name string
		call func(http.ResponseWriter, *http.Request)
	}{
		{"GetAgent", r.h.GetAgentPersona},
		{"PutAgent", r.h.PutAgentPersona},
		{"DeleteAgent", r.h.DeleteAgentPersona},
		{"History", r.h.GetAgentPersonaHistory},
		{"Suggest", r.h.SuggestAgentPersona},
	} {
		rec := httptest.NewRecorder()
		fn.call(rec, r.authedReq(t, http.MethodGet, "/", map[string]string{"content": "x"}))
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s: expected 404 for unknown agent, got %d body=%s", fn.name, rec.Code, rec.Body.String())
		}
	}
}

// TestCovPSTPersona_CrewNotFound404 pins the crew-flavor sql.ErrNoRows arms.
func TestCovPSTPersona_CrewNotFound404(t *testing.T) {
	r := newPersonaTestRig(t)
	r.crewID = "ghost-crew"

	for _, fn := range []struct {
		name string
		call func(http.ResponseWriter, *http.Request)
	}{
		{"GetCrew", r.h.GetCrewPersona},
		{"PutCrew", r.h.PutCrewPersona},
		{"DeleteCrew", r.h.DeleteCrewPersona},
	} {
		rec := httptest.NewRecorder()
		fn.call(rec, r.authedReq(t, http.MethodGet, "/", map[string]string{"content": "x"}))
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s: expected 404 for unknown crew, got %d body=%s", fn.name, rec.Code, rec.Body.String())
		}
	}
}

// TestCovPSTPersona_LookupError500 forces resolveAgentPaths into a generic
// (non-ErrNoRows) error by dropping the agents table — replyAgentLookup must
// then take its 500 arm.
func TestCovPSTPersona_LookupError500(t *testing.T) {
	r := newPersonaTestRig(t)
	if _, err := r.h.db.Exec(`DROP TABLE agents`); err != nil {
		t.Fatalf("drop agents: %v", err)
	}
	rec := httptest.NewRecorder()
	r.h.GetAgentPersona(rec, r.authedReq(t, http.MethodGet, "/", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected 500 on generic lookup error, got %d", rec.Code)
	}
}

// TestCovPSTPersona_CrewDeleteAndHistory covers PutCrewPersona's
// recordCrewVersion write + DeleteCrewPersona (previously 0% line).
func TestCovPSTPersona_CrewDeleteAndHistory(t *testing.T) {
	r := newPersonaTestRig(t)

	rec := httptest.NewRecorder()
	r.h.PutCrewPersona(rec, r.authedReq(t, http.MethodPut, "/", map[string]string{
		"content": "Crew tone: blunt.",
	}))
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT crew: %d %s", rec.Code, rec.Body.String())
	}

	// recordCrewVersion should have inserted a memory_versions row.
	var cnt int
	if err := r.h.db.QueryRow(`SELECT COUNT(*) FROM memory_versions WHERE tier='persona'`).Scan(&cnt); err != nil {
		t.Fatalf("count versions: %v", err)
	}
	if cnt != 1 {
		t.Errorf("expected 1 crew persona version row, got %d", cnt)
	}

	// DELETE the crew layer.
	rec = httptest.NewRecorder()
	r.h.DeleteCrewPersona(rec, r.authedReq(t, http.MethodDelete, "/", nil))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE crew: %d %s", rec.Code, rec.Body.String())
	}
}

// NOTE: the DecisionRejected → 403 arm of SuggestAgentPersona is NOT exercised
// here — ActionPersonaSuggest never resolves to DecisionRejected at any
// autonomy level in the current policy matrix (strict/guided/trusted →
// inbox_approve, full → auto_journal). The arm exists for forward-compat with
// a future matrix; reaching it today would require monkey-patching the
// resolver, which the rig doesn't expose. Left uncovered deliberately.

// TestCovPSTSuggest_NilResolverDefaultsInbox pins the resolver-nil fallback:
// with no policy resolver wired, the suggestion defaults to inbox-approve.
func TestCovPSTSuggest_NilResolverDefaultsInbox(t *testing.T) {
	r := newPersonaTestRig(t)
	r.h.policyResolver = nil // force the safest-default branch

	rec := httptest.NewRecorder()
	r.h.SuggestAgentPersona(rec, r.authedReq(t, http.MethodPost, "/", map[string]string{
		"content": "Default to inbox when no resolver.",
	}))
	if rec.Code != http.StatusOK {
		t.Fatalf("suggest: %d %s", rec.Code, rec.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got["decision"] != string(policy.DecisionInboxApprove) {
		t.Errorf("expected inbox_approve default; got %v", got["decision"])
	}
	if got["pending"] != true {
		t.Errorf("expected pending=true; got %+v", got)
	}
}

// TestCovPSTSuggest_SoloAgentPath exercises the crew_id IS NULL branch in
// resolveAgentPaths (soloAgentMemoryDir, 0% previously). A solo agent has no
// crew so the resolver is skipped and the inbox-default decision applies.
func TestCovPSTSuggest_SoloAgentPath(t *testing.T) {
	r := newPersonaTestRig(t)
	if _, err := r.h.db.Exec(`INSERT INTO agents (id, workspace_id, crew_id, slug, name, agent_role, role_title)
		VALUES ('solo1','ws1',NULL,'solo','Solo','AGENT','Engineer')`); err != nil {
		t.Fatalf("seed solo agent: %v", err)
	}
	r.agentID = "solo1"

	rec := httptest.NewRecorder()
	r.h.SuggestAgentPersona(rec, r.authedReq(t, http.MethodPost, "/", map[string]string{
		"content": "Solo agent suggestion.",
	}))
	if rec.Code != http.StatusOK {
		t.Fatalf("solo suggest: %d %s", rec.Code, rec.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// crewID == "" so the resolver block is skipped → inbox-approve default.
	if got["decision"] != string(policy.DecisionInboxApprove) {
		t.Errorf("expected inbox_approve for solo agent; got %v", got["decision"])
	}

	// And a GET resolves the solo memory dir without error.
	rec = httptest.NewRecorder()
	r.h.GetAgentPersona(rec, r.authedReq(t, http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("solo GET: %d %s", rec.Code, rec.Body.String())
	}
}

// TestCovPSTSuggest_InvalidJSONAndEmptyContent pins the 400 arms on Suggest.
func TestCovPSTSuggest_InvalidJSONAndEmptyContent(t *testing.T) {
	r := newPersonaTestRig(t)

	// Invalid JSON body.
	badReq := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{not json`))
	badReq.SetPathValue("agentId", r.agentID)
	ctx := context.WithValue(badReq.Context(), ctxWorkspaceID, r.wsID)
	ctx = context.WithValue(ctx, ctxUser, &AuthUser{ID: "u1"})
	rec := httptest.NewRecorder()
	r.h.SuggestAgentPersona(rec, badReq.WithContext(ctx))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("invalid JSON: expected 400, got %d", rec.Code)
	}

	// Empty content.
	rec = httptest.NewRecorder()
	r.h.SuggestAgentPersona(rec, r.authedReq(t, http.MethodPost, "/", map[string]string{"content": "   "}))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("empty content: expected 400, got %d", rec.Code)
	}

	// Oversize content → 413.
	big := strings.Repeat("y", memory.PersonaCapBytes+1)
	rec = httptest.NewRecorder()
	r.h.SuggestAgentPersona(rec, r.authedReq(t, http.MethodPost, "/", map[string]string{"content": big}))
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("oversize content: expected 413, got %d", rec.Code)
	}
}

// --- skills_generate.go ----------------------------------------------------

// TestCovPSTResolveAnthropic_DecryptFailure pins the decrypt-failure arm: a row
// with a non-ciphertext encrypted_value makes encryption.Decrypt fail so the
// resolver returns a wrapped "decrypt credential" error (not the sentinel).
func TestCovPSTResolveAnthropic_DecryptFailure(t *testing.T) {
	h := newSkillGenHandler(t)
	userID := seedTestUser(t, h.db)
	wsID := seedTestWorkspace(t, h.db, userID)

	if _, err := h.db.Exec(`
		INSERT INTO credentials (id, workspace_id, name, encrypted_value, type, provider, scope, status, created_by, created_at, updated_at)
		VALUES ('c-garbage', ?, 'bad', 'not-a-valid-ciphertext', 'API_KEY', 'ANTHROPIC', 'WORKSPACE', 'ACTIVE', ?, datetime('now'), datetime('now'))`,
		wsID, userID); err != nil {
		t.Fatalf("seed garbage cred: %v", err)
	}

	_, err := h.resolveAnthropicProvider(context.Background(), wsID)
	if err == nil {
		t.Fatal("expected decrypt error, got nil")
	}
	if errors.Is(err, errNoActiveAnthropicCredential) {
		t.Fatalf("expected decrypt error, got sentinel: %v", err)
	}
	if !strings.Contains(err.Error(), "decrypt") {
		t.Errorf("expected decrypt error, got %v", err)
	}
}

// --- crew_templates.go -----------------------------------------------------

// TestCovPSTAutoAssign_FoundInsertsAndDedup pins the per-row success path:
// two ANTHROPIC creds (API_KEY + AI_CLI_TOKEN) get linked to the agent, a
// repeat call hits INSERT OR IGNORE (no dup), and no failure entries fire.
func TestCovPSTAutoAssign_FoundInsertsAndDedup(t *testing.T) {
	setTestEncryptionKey(t)
	r := newPersonaTestRig(t)
	covPSTSeedAnthropicCred(t, r, "c-key", "ANTHROPIC_API_KEY", "API_KEY", "sk-ant-x")
	covPSTSeedAnthropicCred(t, r, "c-oauth", "ANTHROPIC_OAUTH", "AI_CLI_TOKEN", "oauth-y")

	emitter := &covPSTRecordingEmitter{}
	now := "2026-06-04T00:00:00Z"

	autoAssignCredentials(context.Background(), r.h.db, r.h.logger, emitter, r.wsID, r.agentID, now)

	var cnt int
	if err := r.h.db.QueryRow(
		`SELECT COUNT(*) FROM agent_credentials WHERE agent_id = ?`, r.agentID).Scan(&cnt); err != nil {
		t.Fatalf("count agent_credentials: %v", err)
	}
	if cnt != 2 {
		t.Fatalf("expected 2 agent_credentials rows, got %d", cnt)
	}

	// Re-run: INSERT OR IGNORE → still 2, no failure entries.
	autoAssignCredentials(context.Background(), r.h.db, r.h.logger, emitter, r.wsID, r.agentID, now)
	if err := r.h.db.QueryRow(
		`SELECT COUNT(*) FROM agent_credentials WHERE agent_id = ?`, r.agentID).Scan(&cnt); err != nil {
		t.Fatalf("recount: %v", err)
	}
	if cnt != 2 {
		t.Errorf("expected idempotent 2 rows after re-run, got %d", cnt)
	}
	if got := emitter.typeCount(journal.EntryCredentialAutoAssignFailed); got != 0 {
		t.Errorf("expected no failure entries on success path, got %d", got)
	}
	if got := emitter.typeCount(journal.EntryCredentialAutoAssignEmpty); got != 0 {
		t.Errorf("expected no empty entries when creds exist, got %d", got)
	}
}

// TestCovPSTAutoAssign_EmptyEmitsEntry pins the credentialsFound==false arm:
// a workspace with no Anthropic creds emits the auto_assign_empty journal entry.
func TestCovPSTAutoAssign_EmptyEmitsEntry(t *testing.T) {
	r := newPersonaTestRig(t)
	emitter := &covPSTRecordingEmitter{}

	autoAssignCredentials(context.Background(), r.h.db, r.h.logger, emitter, r.wsID, r.agentID, "now")

	if got := emitter.typeCount(journal.EntryCredentialAutoAssignEmpty); got != 1 {
		t.Errorf("expected 1 auto_assign_empty entry, got %d", got)
	}
	var cnt int
	if err := r.h.db.QueryRow(
		`SELECT COUNT(*) FROM agent_credentials WHERE agent_id = ?`, r.agentID).Scan(&cnt); err != nil {
		t.Fatalf("count: %v", err)
	}
	if cnt != 0 {
		t.Errorf("expected 0 assignments on empty workspace, got %d", cnt)
	}
}

// TestCovPSTAutoAssign_ListQueryError pins the list-query failure arm: dropping
// the credentials table makes the SELECT fail so emitFailure("list_query") fires.
func TestCovPSTAutoAssign_ListQueryError(t *testing.T) {
	r := newPersonaTestRig(t)
	if _, err := r.h.db.Exec(`DROP TABLE credentials`); err != nil {
		t.Fatalf("drop credentials: %v", err)
	}
	emitter := &covPSTRecordingEmitter{}

	autoAssignCredentials(context.Background(), r.h.db, r.h.logger, emitter, r.wsID, r.agentID, "now")

	if got := emitter.typeCount(journal.EntryCredentialAutoAssignFailed); got != 1 {
		t.Errorf("expected 1 failure entry on list-query error, got %d", got)
	}
}

// TestCovPSTSetJournal_NilAndReal pins both arms of SetJournal.
func TestCovPSTSetJournal_NilAndReal(t *testing.T) {
	db := setupTestDB(t)
	h := NewCrewTemplateHandler(db, newTestLogger())

	emitter := &covPSTRecordingEmitter{}
	h.SetJournal(emitter)
	if _, ok := h.journal.(*covPSTRecordingEmitter); !ok {
		t.Errorf("SetJournal(real) did not attach the emitter; got %T", h.journal)
	}

	h.SetJournal(nil)
	if _, ok := h.journal.(noopEmitter); !ok {
		t.Errorf("SetJournal(nil) should fall back to noopEmitter; got %T", h.journal)
	}
}

// TestCovPSTCrewTemplate_ListGetCustom pins the workspace-scoped custom-template
// branches of List/Get (non-builtin row, cross-workspace exclusion).
func TestCovPSTCrewTemplate_ListGetCustom(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	otherWS := "ws-other-tpl"
	if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'O', 'o-tpl')`, otherWS); err != nil {
		t.Fatalf("seed other ws: %v", err)
	}
	h := NewCrewTemplateHandler(db, newTestLogger())

	agentsJSON := `[{"name":"Dev","slug":"dev","role_title":"Engineer","agent_role":"AGENT"}]`
	// Custom template owned by wsID.
	if _, err := db.Exec(`
		INSERT INTO crew_templates (id, workspace_id, name, slug, description, icon, color, category, agents_json, is_builtin, created_at)
		VALUES ('t-mine', ?, 'Mine', 'mine', 'd', NULL, NULL, 'CUSTOM', ?, 0, datetime('now'))`,
		wsID, agentsJSON); err != nil {
		t.Fatalf("seed mine template: %v", err)
	}
	// Custom template owned by another workspace — must NOT appear.
	if _, err := db.Exec(`
		INSERT INTO crew_templates (id, workspace_id, name, slug, description, icon, color, category, agents_json, is_builtin, created_at)
		VALUES ('t-theirs', ?, 'Theirs', 'theirs', 'd', NULL, NULL, 'CUSTOM', ?, 0, datetime('now'))`,
		otherWS, agentsJSON); err != nil {
		t.Fatalf("seed theirs template: %v", err)
	}

	// List should include 'mine' but exclude 'theirs'.
	req := httptest.NewRequest("GET", "/api/v1/crew-templates", nil)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.List(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("list: %d %s", rr.Code, rr.Body.String())
	}
	var templates []crewTemplateResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &templates); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	var sawMine, sawTheirs bool
	for _, tpl := range templates {
		if tpl.Slug == "mine" {
			sawMine = true
		}
		if tpl.Slug == "theirs" {
			sawTheirs = true
		}
	}
	if !sawMine {
		t.Errorf("custom workspace template 'mine' missing from list")
	}
	if sawTheirs {
		t.Errorf("cross-workspace template 'theirs' leaked into list")
	}

	// Get the custom template.
	getReq := httptest.NewRequest("GET", "/api/v1/crew-templates/mine", nil)
	getReq.SetPathValue("slug", "mine")
	getReq = withWorkspaceUser(getReq, userID, wsID, "OWNER")
	getRR := httptest.NewRecorder()
	h.Get(getRR, getReq)
	if getRR.Code != http.StatusOK {
		t.Fatalf("get mine: %d %s", getRR.Code, getRR.Body.String())
	}
	var got crewTemplateResponse
	if err := json.Unmarshal(getRR.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got.Agents) != 1 || got.Agents[0].Slug != "dev" {
		t.Errorf("expected 1 dev agent in custom template; got %+v", got.Agents)
	}

	// Get the cross-workspace template → 404.
	missReq := httptest.NewRequest("GET", "/api/v1/crew-templates/theirs", nil)
	missReq.SetPathValue("slug", "theirs")
	missReq = withWorkspaceUser(missReq, userID, wsID, "OWNER")
	missRR := httptest.NewRecorder()
	h.Get(missRR, missReq)
	if missRR.Code != http.StatusNotFound {
		t.Errorf("cross-workspace Get: expected 404, got %d", missRR.Code)
	}
}

// TestCovPSTDeploy_ExplicitSlugAndAutoAssign deploys a custom template with an
// explicit crew_slug (the else branch of crewSlug derivation) and asserts the
// post-commit autoAssignCredentials linked the seeded Anthropic key.
func TestCovPSTDeploy_ExplicitSlugAndAutoAssign(t *testing.T) {
	db := setupTestDB(t)
	setTestEncryptionKey(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewCrewTemplateHandler(db, newTestLogger())

	// Seed an ACTIVE Anthropic API key so auto-assign has a row.
	enc := covPSTEncrypt(t, "sk-ant-deploy")
	if _, err := db.Exec(`
		INSERT INTO credentials (id, workspace_id, name, encrypted_value, type, provider, scope, status, created_by, created_at, updated_at)
		VALUES ('c-dep', ?, 'ANTHROPIC_KEY', ?, 'API_KEY', 'ANTHROPIC', 'WORKSPACE', 'ACTIVE', ?, datetime('now'), datetime('now'))`,
		wsID, enc, userID); err != nil {
		t.Fatalf("seed cred: %v", err)
	}

	agentsJSON := `[{"name":"Dev","slug":"dev","role_title":"Engineer","agent_role":"AGENT"}]`
	if _, err := db.Exec(`
		INSERT INTO crew_templates (id, workspace_id, name, slug, description, icon, color, category, agents_json, is_builtin, created_at)
		VALUES ('t-dep', ?, 'Dep', 'dep', 'd', NULL, NULL, 'CUSTOM', ?, 0, datetime('now'))`,
		wsID, agentsJSON); err != nil {
		t.Fatalf("seed template: %v", err)
	}

	depReq := httptest.NewRequest("POST", "/api/v1/crew-templates/dep/deploy",
		bytes.NewBufferString(`{"crew_name":"My Crew","crew_slug":"Custom Slug!"}`))
	depReq.SetPathValue("slug", "dep")
	depReq = withWorkspaceUser(depReq, userID, wsID, "OWNER")
	depRR := httptest.NewRecorder()
	h.Deploy(depRR, depReq)
	if depRR.Code != http.StatusCreated {
		t.Fatalf("deploy: %d %s", depRR.Code, depRR.Body.String())
	}
	var dep deployCrewResult
	if err := json.Unmarshal(depRR.Body.Bytes(), &dep); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// Explicit slug got slugified ("Custom Slug!" → "custom-slug").
	if dep.CrewSlug != "custom-slug" {
		t.Errorf("expected slugified explicit slug 'custom-slug'; got %q", dep.CrewSlug)
	}
	if dep.AgentCount != 1 {
		t.Fatalf("expected 1 agent; got %d", dep.AgentCount)
	}

	// Auto-assign should have linked the Anthropic key to the new agent.
	var cnt int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM agent_credentials WHERE agent_id = ?`, dep.AgentIDs[0]).Scan(&cnt); err != nil {
		t.Fatalf("count agent_credentials: %v", err)
	}
	if cnt != 1 {
		t.Errorf("expected 1 auto-assigned credential; got %d", cnt)
	}
}

// TestCovPSTDeployTemplate_EmptySlug pins the errCrewSlugConflict arm when the
// derived slug is empty (crew_slug of all-punctuation slugifies to "").
func TestCovPSTDeployTemplate_EmptySlug(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	logger := newTestLogger()

	_, err := deployCrewTemplate(context.Background(), db, logger, noopEmitter{},
		wsID, "any-template", "Some Name", "!!!")
	if err == nil {
		t.Fatal("expected error for empty derived slug, got nil")
	}
	if !errors.Is(err, errCrewSlugConflict) {
		t.Errorf("expected errCrewSlugConflict for empty slug; got %v", err)
	}
}

// covPSTEncrypt wraps encryption.Encrypt so the deploy test can seed a
// decryptable credential after setTestEncryptionKey has installed a key.
func covPSTEncrypt(t *testing.T, plain string) string {
	t.Helper()
	enc, err := encryption.Encrypt(plain)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	return enc
}
