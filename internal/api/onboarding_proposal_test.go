package api

// Tests for the onboarding proposal store and its apply path
// (docs/prd/conversational-onboarding.md §5.6, §8.2).
//
// TestProposal_CardRendersFromTheSameStructApplyExecutes is the highest-value
// test in this file (§8.2): it proves the applied crew matches the stored
// proposal payload FIELD FOR FIELD, and that Apply cannot be made to
// re-derive any of those fields from its own request body — the mutation the
// PRD names as the one this test must fail against.

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/database"
	"github.com/crewship-ai/crewship/internal/journal"
)

// opFixture returns a ready OnboardingProposalHandler plus a seeded user and
// workspace, following the *_cov_test.go convention used across this
// package (e.g. covCT2Fixture in crew_templates_cov2_test.go).
func opFixture(t *testing.T) (*OnboardingProposalHandler, string, string) {
	t.Helper()
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	return NewOnboardingProposalHandler(db, newTestLogger()), userID, wsID
}

// opSeedTemplate inserts a workspace-scoped crew_templates row with the given
// agent roster, mirroring covCT2SeedTemplate's shape (crew_templates_cov2_test.go)
// plus an icon column so the crew-icon field on the payload has something to
// carry.
func opSeedTemplate(t *testing.T, db *sql.DB, wsID, slug string, agents []database.CrewTemplateAgent) {
	t.Helper()
	b, err := json.Marshal(agents)
	if err != nil {
		t.Fatalf("marshal agents: %v", err)
	}
	execOrFatal(t, db, `INSERT INTO crew_templates
		(id, name, slug, category, agents_json, is_builtin, workspace_id, icon)
		VALUES (?, ?, ?, 'CUSTOM', ?, 0, ?, ?)`,
		"ct-"+slug, "Template "+slug, slug, string(b), wsID, "🤖")
}

// opTwoAgentRoster is the standard two-agent template fixture most tests
// build a proposal from.
func opTwoAgentRoster() []database.CrewTemplateAgent {
	return []database.CrewTemplateAgent{
		{
			Name: "Lead", Slug: "lead", RoleTitle: "Lead Engineer", AgentRole: "AGENT",
			CLIAdapter: "CLAUDE_CODE", LLMProvider: "ANTHROPIC", LLMModel: "claude-default",
			ToolProfile: "CODING", SystemPrompt: "Lead the work.",
		},
		{
			Name: "Helper", Slug: "helper", RoleTitle: "Support Engineer", AgentRole: "AGENT",
			CLIAdapter: "CLAUDE_CODE", LLMProvider: "ANTHROPIC", LLMModel: "claude-default",
			ToolProfile: "CODING", SystemPrompt: "Support the lead.",
		},
	}
}

// opCreate drives Create with a JSON body, returning the decoded response.
func opCreate(t *testing.T, h *OnboardingProposalHandler, userID, wsID string, body map[string]string) (*httptest.ResponseRecorder, onboardingProposalResponse) {
	t.Helper()
	strBody := make(map[string]string, len(body))
	for k, v := range body {
		strBody[k] = v
	}
	req := withWorkspaceUser(httptest.NewRequest("POST", "/api/v1/onboarding/proposals", jsonBody(strBody)), userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	var out onboardingProposalResponse
	if rr.Code == http.StatusCreated {
		if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode create response: %v; body=%s", err, rr.Body.String())
		}
	}
	return rr, out
}

// opApply drives Apply for proposalID, optionally with a request body (used
// by the integrity test to prove the body is ignored). role defaults to
// OWNER when empty.
func opApply(h *OnboardingProposalHandler, userID, wsID, proposalID, role string, body map[string]string) *httptest.ResponseRecorder {
	if role == "" {
		role = "OWNER"
	}
	path := "/api/v1/onboarding/proposals/" + proposalID + "/apply"
	req := httptest.NewRequest("POST", path, jsonBody(body))
	req = withWorkspaceUser(req, userID, wsID, role)
	req.SetPathValue("id", proposalID)
	rr := httptest.NewRecorder()
	h.Apply(rr, req)
	return rr
}

// ---------------------------------------------------------------------------
// Create
// ---------------------------------------------------------------------------

func TestOnboardingProposalCreate_HappyPath_ResolvesTemplateAndOverride(t *testing.T) {
	h, userID, wsID := opFixture(t)
	opSeedTemplate(t, h.db, wsID, "eng-crew", opTwoAgentRoster())

	rr, proposal := opCreate(t, h, userID, wsID, map[string]string{
		"crew_name":     "My Crew",
		"template_slug": "eng-crew",
		"llm_provider":  "ANTHROPIC",
		"llm_model":     "claude-override",
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if proposal.ID == "" {
		t.Fatal("expected a non-empty proposal id")
	}
	if proposal.Status != onboardingProposalStatusPending {
		t.Errorf("status = %q, want PENDING", proposal.Status)
	}
	if proposal.Payload.CrewSlug != "my-crew" {
		t.Errorf("crew slug = %q, want my-crew", proposal.Payload.CrewSlug)
	}
	if proposal.Payload.TemplateSlug != "eng-crew" {
		t.Errorf("template slug = %q, want eng-crew", proposal.Payload.TemplateSlug)
	}
	if len(proposal.Payload.Agents) != 2 {
		t.Fatalf("agents = %d, want 2", len(proposal.Payload.Agents))
	}
	for _, a := range proposal.Payload.Agents {
		if a.LLMModel != "claude-override" {
			t.Errorf("agent %s llm_model = %q, want claude-override (override should apply — provider matches)", a.Name, a.LLMModel)
		}
		if a.Slug != "lead-my-crew" && a.Slug != "helper-my-crew" {
			t.Errorf("unexpected resolved agent slug %q", a.Slug)
		}
	}

	// The row actually landed, PENDING, with the same payload the response
	// carried.
	var status, payloadJSON string
	if err := h.db.QueryRow(`SELECT status, payload_json FROM onboarding_proposals WHERE id = ?`, proposal.ID).
		Scan(&status, &payloadJSON); err != nil {
		t.Fatalf("query stored proposal: %v", err)
	}
	if status != onboardingProposalStatusPending {
		t.Errorf("stored status = %q, want PENDING", status)
	}
	var stored onboardingProposalPayload
	if err := json.Unmarshal([]byte(payloadJSON), &stored); err != nil {
		t.Fatalf("unmarshal stored payload: %v", err)
	}
	if stored.CrewSlug != proposal.Payload.CrewSlug || len(stored.Agents) != len(proposal.Payload.Agents) {
		t.Errorf("stored payload %+v does not match returned payload %+v", stored, proposal.Payload)
	}
}

func TestOnboardingProposalCreate_ModelOverrideSkipsMismatchedProvider(t *testing.T) {
	// deployOverrides.modelFor only applies when the override's provider
	// matches the agent's own provider (crew_templates.go) — a Gemini model
	// id landing on a CLAUDE_CODE agent breaks it outright. The proposal
	// must mirror that fallback, not blindly stamp the override everywhere.
	h, userID, wsID := opFixture(t)
	opSeedTemplate(t, h.db, wsID, "mixed-crew", []database.CrewTemplateAgent{
		{Name: "A", Slug: "a", RoleTitle: "A", AgentRole: "AGENT", CLIAdapter: "CLAUDE_CODE", LLMProvider: "ANTHROPIC", LLMModel: "anthropic-default", ToolProfile: "CODING", SystemPrompt: "do a"},
		{Name: "B", Slug: "b", RoleTitle: "B", AgentRole: "AGENT", CLIAdapter: "CLAUDE_CODE", LLMProvider: "OPENAI", LLMModel: "openai-default", ToolProfile: "CODING", SystemPrompt: "do b"},
	})

	_, proposal := opCreate(t, h, userID, wsID, map[string]string{
		"crew_name":     "Mixed",
		"template_slug": "mixed-crew",
		"llm_provider":  "ANTHROPIC",
		"llm_model":     "claude-override",
	})
	var gotA, gotB string
	for _, a := range proposal.Payload.Agents {
		switch a.Name {
		case "A":
			gotA = a.LLMModel
		case "B":
			gotB = a.LLMModel
		}
	}
	if gotA != "claude-override" {
		t.Errorf("agent A (ANTHROPIC) llm_model = %q, want claude-override", gotA)
	}
	if gotB != "openai-default" {
		t.Errorf("agent B (OPENAI) llm_model = %q, want its own template default (override provider mismatch)", gotB)
	}
}

func TestOnboardingProposalCreate_TemplateNotFound(t *testing.T) {
	h, userID, wsID := opFixture(t)
	rr, _ := opCreate(t, h, userID, wsID, map[string]string{
		"crew_name":     "Ghost",
		"template_slug": "does-not-exist",
	})
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rr.Code, rr.Body.String())
	}
}

func TestOnboardingProposalCreate_RequiresCrewNameAndTemplateSlug(t *testing.T) {
	h, userID, wsID := opFixture(t)

	rr, _ := opCreate(t, h, userID, wsID, map[string]string{"template_slug": "x"})
	if rr.Code != http.StatusBadRequest {
		t.Errorf("missing crew_name: status = %d, want 400", rr.Code)
	}

	rr, _ = opCreate(t, h, userID, wsID, map[string]string{"crew_name": "x"})
	if rr.Code != http.StatusBadRequest {
		t.Errorf("missing template_slug: status = %d, want 400", rr.Code)
	}
}

func TestOnboardingProposalCreate_UnknownProviderRejected(t *testing.T) {
	h, userID, wsID := opFixture(t)
	opSeedTemplate(t, h.db, wsID, "eng-crew", opTwoAgentRoster())
	rr, _ := opCreate(t, h, userID, wsID, map[string]string{
		"crew_name": "X", "template_slug": "eng-crew", "llm_provider": "NOT-A-PROVIDER",
	})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
}

func TestOnboardingProposalCreate_Unauthenticated(t *testing.T) {
	h, _, wsID := opFixture(t)
	req := httptest.NewRequest("POST", "/api/v1/onboarding/proposals", jsonBody(map[string]string{
		"crew_name": "X", "template_slug": "y",
	}))
	req = req.WithContext(withWorkspace(req.Context(), wsID, "OWNER")) // no user in context
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rr.Code)
	}
}

// ---------------------------------------------------------------------------
// Get
// ---------------------------------------------------------------------------

func TestOnboardingProposalGet_RoundTrips(t *testing.T) {
	h, userID, wsID := opFixture(t)
	opSeedTemplate(t, h.db, wsID, "eng-crew", opTwoAgentRoster())
	_, created := opCreate(t, h, userID, wsID, map[string]string{
		"crew_name": "Round Trip", "template_slug": "eng-crew",
	})

	req := withWorkspaceUser(httptest.NewRequest("GET", "/api/v1/onboarding/proposals/"+created.ID, nil), userID, wsID, "MEMBER")
	req.SetPathValue("id", created.ID)
	rr := httptest.NewRecorder()
	h.Get(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rr.Code, rr.Body.String())
	}
	var got onboardingProposalResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.ID != created.ID || got.Payload.CrewSlug != created.Payload.CrewSlug {
		t.Errorf("got %+v, want to match created %+v", got, created)
	}
}

func TestOnboardingProposalGet_NotFound(t *testing.T) {
	h, userID, wsID := opFixture(t)
	req := withWorkspaceUser(httptest.NewRequest("GET", "/api/v1/onboarding/proposals/does-not-exist", nil), userID, wsID, "OWNER")
	req.SetPathValue("id", "does-not-exist")
	rr := httptest.NewRecorder()
	h.Get(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rr.Code)
	}
}

func TestOnboardingProposalGet_ScopedToWorkspace(t *testing.T) {
	// A proposal created in workspace A must not be readable through
	// workspace B's context — same isolation every workspace-scoped
	// resource in this package gets.
	h, userID, wsA := opFixture(t)
	opSeedTemplate(t, h.db, wsA, "eng-crew", opTwoAgentRoster())
	_, created := opCreate(t, h, userID, wsA, map[string]string{
		"crew_name": "A-Only", "template_slug": "eng-crew",
	})

	wsB := "other-workspace"
	execOrFatal(t, h.db, `INSERT INTO workspaces (id, name, slug) VALUES (?, 'Other', 'other')`, wsB)
	execOrFatal(t, h.db, `INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES ('m2', ?, ?, 'OWNER')`, wsB, userID)

	req := withWorkspaceUser(httptest.NewRequest("GET", "/api/v1/onboarding/proposals/"+created.ID, nil), userID, wsB, "OWNER")
	req.SetPathValue("id", created.ID)
	rr := httptest.NewRecorder()
	h.Get(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("cross-workspace get status = %d, want 404", rr.Code)
	}
}

// ---------------------------------------------------------------------------
// Apply — the one write path
// ---------------------------------------------------------------------------

// opRecordingEmitter records every journal entry so tests can assert the
// apply path emits one naming the proposal id (task #3).
type opRecordingEmitter struct {
	entries []journal.Entry
}

func (e *opRecordingEmitter) Emit(_ context.Context, entry journal.Entry) (string, error) {
	e.entries = append(e.entries, entry)
	return "je-" + string(entry.Type), nil
}
func (e *opRecordingEmitter) Flush(context.Context) error { return nil }

func TestOnboardingProposalApply_CreatesCrewWritesAuditAndJournal(t *testing.T) {
	h, userID, wsID := opFixture(t)
	emitter := &opRecordingEmitter{}
	h.SetJournal(emitter)
	opSeedTemplate(t, h.db, wsID, "eng-crew", opTwoAgentRoster())

	_, proposal := opCreate(t, h, userID, wsID, map[string]string{
		"crew_name": "Applied Crew", "template_slug": "eng-crew", "llm_model": "claude-override",
	})

	rr := opApply(h, userID, wsID, proposal.ID, "OWNER", nil)
	if rr.Code != http.StatusCreated {
		t.Fatalf("apply status = %d, body=%s", rr.Code, rr.Body.String())
	}
	var applied onboardingProposalApplyResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &applied); err != nil {
		t.Fatalf("decode apply response: %v", err)
	}
	if applied.AlreadyApplied {
		t.Error("fresh apply reported AlreadyApplied = true")
	}
	if applied.Crew.CrewID == "" || applied.Crew.AgentCount != 2 {
		t.Fatalf("unexpected crew result: %+v", applied.Crew)
	}

	// The crew genuinely exists.
	var crewCount int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM crews WHERE id = ? AND deleted_at IS NULL`, applied.Crew.CrewID).Scan(&crewCount); err != nil {
		t.Fatalf("query crew: %v", err)
	}
	if crewCount != 1 {
		t.Errorf("crew count = %d, want 1", crewCount)
	}

	// Proposal row flipped to APPLIED with the terminal fields set.
	var status string
	var appliedAt, appliedCrewID, appliedResult sql.NullString
	if err := h.db.QueryRow(`SELECT status, applied_at, applied_crew_id, applied_result_json FROM onboarding_proposals WHERE id = ?`, proposal.ID).
		Scan(&status, &appliedAt, &appliedCrewID, &appliedResult); err != nil {
		t.Fatalf("query proposal row: %v", err)
	}
	if status != onboardingProposalStatusApplied {
		t.Errorf("status = %q, want APPLIED", status)
	}
	if !appliedAt.Valid || !appliedCrewID.Valid || !appliedResult.Valid {
		t.Errorf("expected all terminal fields set, got applied_at=%v crew_id=%v result=%v", appliedAt, appliedCrewID, appliedResult)
	}
	if appliedCrewID.String != applied.Crew.CrewID {
		t.Errorf("stored applied_crew_id = %q, want %q", appliedCrewID.String, applied.Crew.CrewID)
	}

	// Audit row naming the proposal id.
	var auditCount int
	var auditMetadata string
	if err := h.db.QueryRow(`SELECT COUNT(*), COALESCE(MAX(metadata), '') FROM audit_logs
		WHERE action = 'onboarding.proposal_apply' AND entity_id = ?`, applied.Crew.CrewID).
		Scan(&auditCount, &auditMetadata); err != nil {
		t.Fatalf("query audit_logs: %v", err)
	}
	if auditCount != 1 {
		t.Fatalf("audit_logs rows = %d, want 1", auditCount)
	}
	if !strings.Contains(auditMetadata, proposal.ID) {
		t.Errorf("audit metadata %q does not name proposal id %q", auditMetadata, proposal.ID)
	}

	// Journal entry naming the proposal id.
	var found *journal.Entry
	for i := range emitter.entries {
		if emitter.entries[i].Type == journalEntryOnboardingProposalApplied {
			found = &emitter.entries[i]
		}
	}
	if found == nil {
		t.Fatal("no journalEntryOnboardingProposalApplied entry emitted")
	}
	if found.Payload["proposal_id"] != proposal.ID {
		t.Errorf("journal payload proposal_id = %v, want %q", found.Payload["proposal_id"], proposal.ID)
	}
}

func TestOnboardingProposalApply_Idempotent_SecondCallReturnsFirstResultNoSecondCrew(t *testing.T) {
	h, userID, wsID := opFixture(t)
	opSeedTemplate(t, h.db, wsID, "eng-crew", opTwoAgentRoster())
	_, proposal := opCreate(t, h, userID, wsID, map[string]string{
		"crew_name": "Idempotent Crew", "template_slug": "eng-crew",
	})

	first := opApply(h, userID, wsID, proposal.ID, "OWNER", nil)
	if first.Code != http.StatusCreated {
		t.Fatalf("first apply status = %d, body=%s", first.Code, first.Body.String())
	}
	var firstResult onboardingProposalApplyResponse
	if err := json.Unmarshal(first.Body.Bytes(), &firstResult); err != nil {
		t.Fatalf("decode first apply: %v", err)
	}

	second := opApply(h, userID, wsID, proposal.ID, "OWNER", nil)
	if second.Code != http.StatusOK {
		t.Fatalf("second apply status = %d, want 200 (replay); body=%s", second.Code, second.Body.String())
	}
	var secondResult onboardingProposalApplyResponse
	if err := json.Unmarshal(second.Body.Bytes(), &secondResult); err != nil {
		t.Fatalf("decode second apply: %v", err)
	}
	if !secondResult.AlreadyApplied {
		t.Error("second apply did not report AlreadyApplied = true")
	}
	if secondResult.Crew.CrewID != firstResult.Crew.CrewID {
		t.Errorf("second apply crew id = %q, want the first result's %q", secondResult.Crew.CrewID, firstResult.Crew.CrewID)
	}

	var crewCount int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM crews WHERE slug = ? AND workspace_id = ?`, firstResult.Crew.CrewSlug, wsID).Scan(&crewCount); err != nil {
		t.Fatalf("count crews: %v", err)
	}
	if crewCount != 1 {
		t.Errorf("crews with slug %q = %d, want exactly 1 (no second crew from the replayed apply)", firstResult.Crew.CrewSlug, crewCount)
	}
}

func TestOnboardingProposalApply_NotFound(t *testing.T) {
	h, userID, wsID := opFixture(t)
	rr := opApply(h, userID, wsID, "does-not-exist", "OWNER", nil)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rr.Code)
	}
}

func TestOnboardingProposalApply_Unauthenticated(t *testing.T) {
	h, userID, wsID := opFixture(t)
	opSeedTemplate(t, h.db, wsID, "eng-crew", opTwoAgentRoster())
	_, proposal := opCreate(t, h, userID, wsID, map[string]string{
		"crew_name": "X", "template_slug": "eng-crew",
	})

	req := httptest.NewRequest("POST", "/api/v1/onboarding/proposals/"+proposal.ID+"/apply", nil)
	req = req.WithContext(withWorkspace(req.Context(), wsID, "OWNER")) // no user
	req.SetPathValue("id", proposal.ID)
	rr := httptest.NewRecorder()
	h.Apply(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rr.Code)
	}
}

func TestOnboardingProposalApply_UsesSnapshotWhenTemplateIsGone(t *testing.T) {
	h, userID, wsID := opFixture(t)
	opSeedTemplate(t, h.db, wsID, "eng-crew", opTwoAgentRoster())
	_, proposal := opCreate(t, h, userID, wsID, map[string]string{
		"crew_name": "X", "template_slug": "eng-crew",
	})

	execOrFatal(t, h.db, `DELETE FROM crew_templates WHERE slug = ? AND workspace_id = ?`, "eng-crew", wsID)

	rr := opApply(h, userID, wsID, proposal.ID, "OWNER", nil)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 from stored snapshot; body=%s", rr.Code, rr.Body.String())
	}
}

// TestProposal_CardRendersFromTheSameStructApplyExecutes is docs/prd/
// conversational-onboarding.md §8.2's named test: the applied crew must
// equal the rendered card FIELD FOR FIELD, and Apply must not be able to
// re-derive any field from anything OTHER than the stored proposal row —
// specifically not from its own request body, which is the only "live
// state" an attacker controls at click time in this design.
func TestProposal_CardRendersFromTheSameStructApplyExecutes(t *testing.T) {
	h, userID, wsID := opFixture(t)
	opSeedTemplate(t, h.db, wsID, "card-tmpl", opTwoAgentRoster())
	// A second, unrelated template — if Apply were tricked into reading
	// template_slug from the request body instead of the stored payload,
	// this is what it would deploy instead.
	opSeedTemplate(t, h.db, wsID, "evil-tmpl", []database.CrewTemplateAgent{
		{Name: "Evil", Slug: "evil", RoleTitle: "Evil", AgentRole: "AGENT", CLIAdapter: "CLAUDE_CODE", LLMProvider: "OPENAI", LLMModel: "evil-model", ToolProfile: "CODING", SystemPrompt: "be evil"},
	})

	// Propose: render the card.
	_, card := opCreate(t, h, userID, wsID, map[string]string{
		"crew_name":     "Card Crew",
		"template_slug": "card-tmpl",
		"llm_provider":  "ANTHROPIC",
		"llm_model":     "claude-pinned",
	})
	if len(card.Payload.Agents) != 2 {
		t.Fatalf("card agents = %d, want 2", len(card.Payload.Agents))
	}
	// Mutate the source template after the card was rendered. Apply must not
	// silently re-plan from this newer row: human approval covered the stored
	// snapshot, not whatever the same slug happens to mean later.
	mutated, err := json.Marshal([]database.CrewTemplateAgent{
		{Name: "Changed after approval", Slug: "changed", RoleTitle: "Changed", AgentRole: "LEAD", CLIAdapter: "CODEX_CLI", LLMProvider: "OPENAI", LLMModel: "evil-model", ToolProfile: "FULL", SystemPrompt: "not approved"},
	})
	if err != nil {
		t.Fatalf("marshal mutated template: %v", err)
	}
	execOrFatal(t, h.db, `UPDATE crew_templates SET agents_json = ? WHERE slug = ? AND workspace_id = ?`, string(mutated), "card-tmpl", wsID)

	// Click Apply — but the request carries a mutation attempt: a
	// different template, crew name, and model than what was proposed.
	// This is the mutation §8.2 requires the test to fail against.
	maliciousBody := map[string]string{
		"template_slug": "evil-tmpl",
		"crew_name":     "Evil Crew",
		"crew_slug":     "evil-crew",
		"llm_provider":  "OPENAI",
		"llm_model":     "evil-model",
	}
	rr := opApply(h, userID, wsID, card.ID, "OWNER", maliciousBody)
	if rr.Code != http.StatusCreated {
		t.Fatalf("apply status = %d, body=%s", rr.Code, rr.Body.String())
	}
	var applied onboardingProposalApplyResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &applied); err != nil {
		t.Fatalf("decode apply response: %v", err)
	}

	// Crew identity must be the CARD's, never the request body's.
	if applied.Crew.CrewName != card.Payload.CrewName {
		t.Errorf("applied crew name = %q, want card's %q (request body must be ignored)", applied.Crew.CrewName, card.Payload.CrewName)
	}
	if applied.Crew.CrewSlug != card.Payload.CrewSlug {
		t.Errorf("applied crew slug = %q, want card's %q", applied.Crew.CrewSlug, card.Payload.CrewSlug)
	}
	if applied.Crew.AgentCount != len(card.Payload.Agents) {
		t.Errorf("applied agent count = %d, want card's %d", applied.Crew.AgentCount, len(card.Payload.Agents))
	}

	// The evil template must never have been deployed at all.
	var evilCrewCount int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM crews WHERE slug = 'evil-crew' AND workspace_id = ?`, wsID).Scan(&evilCrewCount); err != nil {
		t.Fatalf("count evil crews: %v", err)
	}
	if evilCrewCount != 0 {
		t.Fatal("apply deployed the request body's template_slug instead of the stored one")
	}

	// Field-for-field: every created agent row equals the card's entry for
	// that agent (matched by slug, since row order is not a guarantee).
	rows, err := h.db.Query(`
		SELECT name, slug, role_title, agent_role, cli_adapter, llm_provider,
		       llm_model, tool_profile, system_prompt_legacy
		FROM agents WHERE crew_id = ?`, applied.Crew.CrewID)
	if err != nil {
		t.Fatalf("query agents: %v", err)
	}
	defer rows.Close()

	cardBySlug := make(map[string]onboardingProposalAgent, len(card.Payload.Agents))
	for _, a := range card.Payload.Agents {
		cardBySlug[a.Slug] = a
	}

	var seen []string
	for rows.Next() {
		var name, slug, roleTitle, agentRole, cliAdapter, llmProvider, llmModel, toolProfile, systemPrompt string
		if err := rows.Scan(&name, &slug, &roleTitle, &agentRole, &cliAdapter, &llmProvider, &llmModel, &toolProfile, &systemPrompt); err != nil {
			t.Fatalf("scan agent row: %v", err)
		}
		seen = append(seen, slug)
		want, ok := cardBySlug[slug]
		if !ok {
			t.Fatalf("created agent slug %q is not on the card at all", slug)
		}
		if name != want.Name {
			t.Errorf("agent %s: name = %q, want card's %q", slug, name, want.Name)
		}
		if roleTitle != want.RoleTitle {
			t.Errorf("agent %s: role_title = %q, want card's %q", slug, roleTitle, want.RoleTitle)
		}
		if agentRole != want.AgentRole {
			t.Errorf("agent %s: agent_role = %q, want stored %q", slug, agentRole, want.AgentRole)
		}
		if cliAdapter != want.CLIAdapter {
			t.Errorf("agent %s: cli_adapter = %q, want stored %q", slug, cliAdapter, want.CLIAdapter)
		}
		if llmProvider != want.LLMProvider {
			t.Errorf("agent %s: llm_provider = %q, want card's %q", slug, llmProvider, want.LLMProvider)
		}
		if llmModel != want.LLMModel {
			t.Errorf("agent %s: llm_model = %q, want card's %q", slug, llmModel, want.LLMModel)
		}
		if toolProfile != want.ToolProfile {
			t.Errorf("agent %s: tool_profile = %q, want stored %q", slug, toolProfile, want.ToolProfile)
		}
		if systemPrompt != want.SystemPrompt {
			t.Errorf("agent %s: system_prompt = %q, want card's %q", slug, systemPrompt, want.SystemPrompt)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate agent rows: %v", err)
	}
	if len(seen) != len(card.Payload.Agents) {
		t.Errorf("created %d agents, card named %d", len(seen), len(card.Payload.Agents))
	}
}
