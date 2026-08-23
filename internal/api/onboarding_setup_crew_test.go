package api

// Tests for the onboarding setup crew: onboarding_setup_crew.go, plus the
// Status/Complete/Setup wiring in onboarding.go and the List filter in
// crews_query.go. See docs/prd/conversational-onboarding.md §4, §5.3, §8.1.

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/database"
)

// ---- ensureOnboardingSetupCrew: autonomy level ----

func TestEnsureOnboardingSetupCrew_NewCrewGetsCurrentAutonomyLevel(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	info, err := ensureOnboardingSetupCrew(context.Background(), db, testLogger(), wsID, userID)
	if err != nil {
		t.Fatalf("ensureOnboardingSetupCrew: %v", err)
	}
	var autonomy string
	if err := db.QueryRow("SELECT autonomy_level FROM crews WHERE id = ?", info.CrewID).Scan(&autonomy); err != nil {
		t.Fatalf("read autonomy_level: %v", err)
	}
	if autonomy != setupCrewAutonomyLevel {
		t.Errorf("autonomy_level = %q, want %q", autonomy, setupCrewAutonomyLevel)
	}
}

// TestEnsureOnboardingSetupCrew_HealsStaleStrictAutonomy pins the narrow,
// one-time heal: a setup crew left over from before this constant was
// raised (still 'strict', the old default) picks up the current value on
// the next call — the whole point of the guard, since a workspace created
// under an older build must not carry a stale autonomy level forever.
func TestEnsureOnboardingSetupCrew_HealsStaleStrictAutonomy(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	if _, err := ensureOnboardingSetupCrew(context.Background(), db, testLogger(), wsID, userID); err != nil {
		t.Fatalf("ensureOnboardingSetupCrew (create): %v", err)
	}
	if _, err := db.Exec("UPDATE crews SET autonomy_level = 'strict' WHERE workspace_id = ? AND slug = ?",
		wsID, setupCrewSlug); err != nil {
		t.Fatalf("force stale autonomy_level: %v", err)
	}

	info, err := ensureOnboardingSetupCrew(context.Background(), db, testLogger(), wsID, userID)
	if err != nil {
		t.Fatalf("ensureOnboardingSetupCrew (heal): %v", err)
	}
	var autonomy string
	if err := db.QueryRow("SELECT autonomy_level FROM crews WHERE id = ?", info.CrewID).Scan(&autonomy); err != nil {
		t.Fatalf("read autonomy_level: %v", err)
	}
	if autonomy != setupCrewAutonomyLevel {
		t.Errorf("autonomy_level = %q after heal, want %q", autonomy, setupCrewAutonomyLevel)
	}
}

// TestEnsureOnboardingSetupCrew_PreservesOperatorChosenAutonomy is the other
// half of the same guard: a crew an operator has explicitly moved to some
// OTHER level (guided here — deliberately not 'strict', so the heal's WHERE
// clause cannot match it) must never be silently overwritten back to the
// constant on a later call. Self-healing a stale default must not double as
// "always sync to the constant".
func TestEnsureOnboardingSetupCrew_PreservesOperatorChosenAutonomy(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	if _, err := ensureOnboardingSetupCrew(context.Background(), db, testLogger(), wsID, userID); err != nil {
		t.Fatalf("ensureOnboardingSetupCrew (create): %v", err)
	}
	if _, err := db.Exec("UPDATE crews SET autonomy_level = 'guided' WHERE workspace_id = ? AND slug = ?",
		wsID, setupCrewSlug); err != nil {
		t.Fatalf("simulate operator override: %v", err)
	}

	info, err := ensureOnboardingSetupCrew(context.Background(), db, testLogger(), wsID, userID)
	if err != nil {
		t.Fatalf("ensureOnboardingSetupCrew (second call): %v", err)
	}
	var autonomy string
	if err := db.QueryRow("SELECT autonomy_level FROM crews WHERE id = ?", info.CrewID).Scan(&autonomy); err != nil {
		t.Fatalf("read autonomy_level: %v", err)
	}
	if autonomy != "guided" {
		t.Errorf("autonomy_level = %q, want operator's own 'guided' preserved", autonomy)
	}
}

// TestSetupAgentModelOutranksCreatedCrewDefault pins the split between the
// two model constants, which is the whole reason they are separate names.
//
// The Guide reasons on the stronger model because its output is a spec a
// human cannot easily check; a created crew's agents stay on the cheaper
// default because re-tiering every agent every crew ever creates is a
// pricing decision, not a side effect of making onboarding smarter. Collapse
// the two constants back together in either direction and this fails.
func TestSetupAgentModelOutranksCreatedCrewDefault(t *testing.T) {
	if setupAgentModel == crewAgentDefaultModel {
		t.Fatalf("setupAgentModel and crewAgentDefaultModel are both %q — "+
			"the Guide's model and a created crew's default are separate decisions; "+
			"see their doc comments before collapsing them", setupAgentModel)
	}

	// The Guide itself, end to end through the real row it writes.
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	info, err := ensureOnboardingSetupCrew(context.Background(), db, testLogger(), wsID, userID)
	if err != nil {
		t.Fatalf("ensureOnboardingSetupCrew: %v", err)
	}
	var guideModel string
	if err := db.QueryRow("SELECT llm_model FROM agents WHERE id = ?", info.AgentID).Scan(&guideModel); err != nil {
		t.Fatalf("read guide llm_model: %v", err)
	}
	if guideModel != setupAgentModel {
		t.Errorf("guide llm_model = %q, want %q", guideModel, setupAgentModel)
	}

	// A crew created FROM the Guide's own proposal path must not inherit it.
	// providerRuntimeDefaults is what planCustomAgents reads when a proposal
	// carries no explicit llm_model, so this is the real branch, not a proxy.
	if _, model := providerRuntimeDefaults("ANTHROPIC"); model != crewAgentDefaultModel {
		t.Errorf("providerRuntimeDefaults(ANTHROPIC) model = %q, want %q "+
			"(a created crew's agents must not inherit the Guide's tier)", model, crewAgentDefaultModel)
	}
}

// TestSetupPrompt_MarkerDoesNotProposeTheGuidesOwnModel is the regression
// guard for a real cost bug: the proposal marker interpolated {{SETUP_MODEL}}
// — the GUIDE's model — as the llm_model for the crew being proposed. Raising
// the Guide to opus therefore put every crew it created on opus too, including
// agents whose whole job is polling a URL on a schedule, forever.
//
// The marker must offer the crew default instead, and the prompt must tell the
// Guide to choose a cheaper tier when the work allows it.
func TestSetupPrompt_MarkerDoesNotProposeTheGuidesOwnModel(t *testing.T) {
	prompt := renderSetupAgentPrompt(setupAgentSystemPromptTemplate, "ANTHROPIC", setupAgentModel)

	markerLine := ""
	for _, line := range strings.Split(prompt, "\n") {
		if strings.Contains(line, "crewship:onboarding-proposal") && strings.Contains(line, "llm_model") {
			markerLine = line
			break
		}
	}
	if markerLine == "" {
		t.Fatal("could not find the proposal marker template in the rendered prompt")
	}
	if !strings.Contains(markerLine, `"llm_model":"`+crewAgentDefaultModel+`"`) {
		t.Errorf("marker does not offer the crew default model %q:\n%s", crewAgentDefaultModel, markerLine)
	}
	if strings.Contains(markerLine, setupAgentModel) {
		t.Errorf("marker still hands the crew the GUIDE's own model %q — that is the cost regression:\n%s",
			setupAgentModel, markerLine)
	}
	// The menu must actually reach the model, cheapest tier included, or the
	// "pick something cheaper" instruction has nothing to point at.
	for _, want := range []string{"claude-haiku-4-5", "claude-sonnet-5", "claude-opus-5"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("rendered prompt is missing model tier %q", want)
		}
	}
	if strings.Contains(prompt, "{{CREW_MODEL") {
		t.Error("an unexpanded {{CREW_MODEL…}} placeholder reached the prompt")
	}
}

// TestSetupPrompt_ForbidsToolCallsWhileProposing pins the instruction added
// after a real session showed the Guide spending three tool calls
// (discover_capabilities, list_routines, then validate_manifest twice) before
// proposing a two-agent crew in a brand-new workspace — where there are no
// routines to list and no manifest to validate. The user saw "Worked · 3
// steps" and an expensive pause in place of an answer, and reasonably asked
// what it was doing.
func TestSetupPrompt_ForbidsToolCallsWhileProposing(t *testing.T) {
	prompt := renderSetupAgentPrompt(setupAgentSystemPromptTemplate, "ANTHROPIC", setupAgentModel)
	if !strings.Contains(prompt, "CALL NO TOOLS WHILE PROPOSING A CREW") {
		t.Error("prompt no longer forbids tool calls during the crew proposal")
	}
	// The rule has to name the tools it is about, or the model has to infer
	// which of its ~20 advertised tools the sentence covers.
	for _, tool := range []string{"discover_capabilities", "list_routines", "validate_manifest"} {
		idx := strings.Index(prompt, "CALL NO TOOLS WHILE PROPOSING A CREW")
		if !strings.Contains(prompt[idx:idx+900], tool) {
			t.Errorf("the no-tools rule does not name %q, so it reads as advice rather than a list", tool)
		}
	}
}

// TestSetupPrompt_MarkerModelMatchesProvider guards a bug the suite caught:
// {{CREW_MODEL}} was interpolated as the bare ANTHROPIC default, so an
// OpenAI/Google workspace was handed a marker telling the Guide to propose a
// CLAUDE model. Every field would have been individually valid and the
// combination unrunnable — the exact failure resolveSetupAgentRuntime's
// docstring describes.
//
// This test USED to hardcode {"OPENAI", "gpt-5.5"} and
// {"GOOGLE", "gemini-2.5-pro"} — the values providerRuntimeDefaults returns —
// and it passed while the product was broken, because it asserted against the
// same wrong table the code read from. Neither id is in llm.CuratedModels, so
// validateCrewModel rejected the model the prompt had just told the Guide to
// emit and substituted the Anthropic default onto an OpenAI crew. A test that
// pins a literal copied from the implementation cannot catch the
// implementation being wrong; the assertion is now the PROPERTY that matters —
// whatever the marker suggests, the validator must accept unchanged.
func TestSetupPrompt_MarkerModelMatchesProvider(t *testing.T) {
	for _, provider := range []string{"ANTHROPIC", "OPENAI", "GOOGLE"} {
		t.Run(provider, func(t *testing.T) {
			tc := struct{ provider, wantModel string }{provider, crewDefaultModelForProvider(provider)}
			if tc.wantModel == "" {
				t.Fatalf("%s has a curated catalogue but no suggested model", provider)
			}
			if resolved, substituted := validateCrewModel(provider, tc.wantModel); substituted {
				t.Fatalf("the marker suggests %q for %s, which the validator swaps for %q",
					tc.wantModel, provider, resolved)
			}
			_, setupModel := providerRuntimeDefaults(tc.provider)
			prompt := renderSetupAgentPrompt(setupAgentSystemPromptTemplate, tc.provider, setupModel)
			var marker string
			for _, line := range strings.Split(prompt, "\n") {
				if strings.Contains(line, "crewship:onboarding-proposal") && strings.Contains(line, "llm_model") {
					marker = line
					break
				}
			}
			if marker == "" {
				t.Fatal("no marker template in the rendered prompt")
			}
			if !strings.Contains(marker, `"llm_model":"`+tc.wantModel+`"`) {
				t.Errorf("%s marker does not offer %q:\n%s", tc.provider, tc.wantModel, marker)
			}
		})
	}
}

// TestRuntimeToolsAreAClosedEnum is the safety property behind letting the
// Guide ask for container tools at all. Authoring devcontainer_config would
// have been arbitrary root code execution — postCreateCommand is run as raw
// shell during the image build and feature refs have no registry allowlist —
// so the Guide's authority is deliberately reduced to picking names from the
// in-binary runtime catalogue, with the server pinning every version.
func TestRuntimeToolsAreAClosedEnum(t *testing.T) {
	t.Run("known name resolves with a server-pinned version", func(t *testing.T) {
		tool, version, ok := resolveProposalTool("Python")
		if !ok || tool != "python" || version == "" {
			t.Fatalf("resolveProposalTool(Python) = (%q, %q, %v)", tool, version, ok)
		}
	})
	t.Run("unknown name is refused", func(t *testing.T) {
		if _, _, ok := resolveProposalTool("totally-made-up"); ok {
			t.Error("an uncatalogued tool resolved")
		}
	})
	t.Run("composes a mise config and drops what it cannot resolve", func(t *testing.T) {
		cfg, resolved, dropped := composeProposalMiseConfig([]string{"python", "not-a-tool", "jq"})
		if !strings.Contains(cfg, `"python"`) {
			t.Errorf("composed config missing python: %s", cfg)
		}
		if len(resolved) == 0 {
			t.Error("nothing resolved")
		}
		if len(dropped) == 0 {
			t.Error("an uncatalogued name was not reported as dropped")
		}
		if strings.Contains(cfg, "not-a-tool") {
			t.Errorf("an uncatalogued name reached the config: %s", cfg)
		}
	})
	t.Run("no tools means no config, so no image build is forced", func(t *testing.T) {
		// crewNeedsProvision treats ANY non-empty mise_config as "build an
		// image first", so an empty-but-present value would commit every
		// proposal crew to a cold build for zero tools.
		if cfg, _, _ := composeProposalMiseConfig(nil); cfg != "" {
			t.Errorf("expected empty config, got %q", cfg)
		}
		if cfg, _, _ := composeProposalMiseConfig([]string{"nope", "also-nope"}); cfg != "" {
			t.Errorf("all-unresolvable list should compose nothing, got %q", cfg)
		}
	})
	t.Run("caps the number of tools", func(t *testing.T) {
		many := []string{"python", "node", "go", "rust", "ruby", "java", "php"}
		cfg, resolved, dropped := composeProposalMiseConfig(many)
		if len(resolved) > onboardingProposalMaxTools {
			t.Errorf("resolved %d tools, cap is %d: %s", len(resolved), onboardingProposalMaxTools, cfg)
		}
		if len(dropped) == 0 {
			t.Error("over-cap entries were not reported as dropped")
		}
	})
}

func TestValidateCrewModel(t *testing.T) {
	cases := []struct {
		name        string
		provider    string
		model       string
		want        string
		wantSwapped bool
	}{
		{"known cheap tier passes", "ANTHROPIC", "claude-haiku-4-5", "claude-haiku-4-5", false},
		{"known default passes", "ANTHROPIC", "claude-sonnet-5", "claude-sonnet-5", false},
		{"known top tier passes", "ANTHROPIC", "claude-opus-5", "claude-opus-5", false},
		{"case-insensitive", "ANTHROPIC", "Claude-Sonnet-5", "claude-sonnet-5", false},
		{"empty means no override", "ANTHROPIC", "", "", false},
		{"hallucinated id falls back", "ANTHROPIC", "claude-omega-9", crewAgentDefaultModel, true},
		{"date-suffixed alias falls back", "ANTHROPIC", "claude-sonnet-5-20260101", crewAgentDefaultModel, true},
		// Ollama ships no curated catalogue — its models live on the daemon, so
		// passing through is the honest answer and substituting a Claude id
		// would be strictly worse.
		{"uncatalogued provider passes through", "OLLAMA", "llama3.2", "llama3.2", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, swapped := validateCrewModel(tc.provider, tc.model)
			if got != tc.want || swapped != tc.wantSwapped {
				t.Errorf("validateCrewModel(%q, %q) = (%q, %v), want (%q, %v)",
					tc.provider, tc.model, got, swapped, tc.want, tc.wantSwapped)
			}
		})
	}
}

// ---- ensureOnboardingSetupCrew: slug, network mode, kind ----

func TestEnsureOnboardingSetupCrew_FixedSlugRestrictedNetworkAndKind(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	info, err := ensureOnboardingSetupCrew(context.Background(), db, testLogger(), wsID, userID)
	if err != nil {
		t.Fatalf("ensureOnboardingSetupCrew: %v", err)
	}
	if info.CrewID == "" || info.AgentID == "" || info.ChatID == "" {
		t.Fatalf("expected non-empty ids, got %+v", info)
	}

	var slug, networkMode, kind string
	if err := db.QueryRow("SELECT slug, network_mode, kind FROM crews WHERE id = ?", info.CrewID).
		Scan(&slug, &networkMode, &kind); err != nil {
		t.Fatalf("read crew: %v", err)
	}
	if slug != setupCrewSlug {
		t.Errorf("slug = %q, want %q", slug, setupCrewSlug)
	}
	if networkMode != database.DefaultCrewNetworkMode {
		t.Errorf("network_mode = %q, want %q", networkMode, database.DefaultCrewNetworkMode)
	}
	if kind != setupCrewKindSetup {
		t.Errorf("kind = %q, want %q", kind, setupCrewKindSetup)
	}

	// The crew gets its devcontainer from the chokepoint default, not a
	// value written at INSERT — devcontainer_config must stay NULL here.
	var devcontainer sql.NullString
	if err := db.QueryRow("SELECT devcontainer_config FROM crews WHERE id = ?", info.CrewID).Scan(&devcontainer); err != nil {
		t.Fatalf("read devcontainer_config: %v", err)
	}
	if devcontainer.Valid {
		t.Errorf("devcontainer_config = %q, want NULL (chokepoint default applies at read time)", devcontainer.String)
	}

	var chatAgentID string
	if err := db.QueryRow("SELECT agent_id FROM chats WHERE id = ?", info.ChatID).Scan(&chatAgentID); err != nil {
		t.Fatalf("read chat: %v", err)
	}
	if chatAgentID != info.AgentID {
		t.Errorf("chat.agent_id = %q, want %q", chatAgentID, info.AgentID)
	}
}

// ---- Slug collision proof: the fixed slug is unreachable through the
//      validation every user-facing slug (crew or agent) must pass. ----

func TestSetupSlugs_RejectedByPublicSlugValidation(t *testing.T) {
	if validSlugFormat(setupCrewSlug) {
		t.Errorf("setupCrewSlug %q passes validSlugFormat — a user could create a colliding crew", setupCrewSlug)
	}
	if validSlugFormat(setupAgentSlug) {
		t.Errorf("setupAgentSlug %q passes validSlugFormat — a user could create a colliding agent", setupAgentSlug)
	}
	// And the two slug-generating helpers (onboarding's own makeSlug, and
	// crew_templates.go's slugify) can never emit a leading underscore
	// either, so a *derived* slug cannot collide even without going
	// through validSlugFormat.
	if strings.HasPrefix(makeSlug("_crewship-setup"), "_") {
		t.Error("makeSlug can produce a leading underscore — the collision proof no longer holds")
	}
	if strings.HasPrefix(slugify("_crewship-setup"), "_") {
		t.Error("slugify can produce a leading underscore — the collision proof no longer holds")
	}
}

// ---- Idempotency: a second call (a second onboarding "session") does not
//      create a second setup crew. ----

func TestEnsureOnboardingSetupCrew_SecondCallConvergesOnSameRows(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	first, err := ensureOnboardingSetupCrew(context.Background(), db, testLogger(), wsID, userID)
	if err != nil {
		t.Fatalf("first call: %v", err)
	}
	second, err := ensureOnboardingSetupCrew(context.Background(), db, testLogger(), wsID, userID)
	if err != nil {
		t.Fatalf("second call: %v", err)
	}

	if first.CrewID != second.CrewID || first.AgentID != second.AgentID || first.ChatID != second.ChatID {
		t.Errorf("second call produced different rows: first=%+v second=%+v", first, second)
	}

	var crewCount, agentCount, chatCount int
	if err := db.QueryRow("SELECT COUNT(*) FROM crews WHERE workspace_id = ? AND kind = ?", wsID, setupCrewKindSetup).Scan(&crewCount); err != nil {
		t.Fatalf("count crews: %v", err)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM agents WHERE workspace_id = ? AND slug = ?", wsID, setupAgentSlug).Scan(&agentCount); err != nil {
		t.Fatalf("count agents: %v", err)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM chats WHERE agent_id = ?", first.AgentID).Scan(&chatCount); err != nil {
		t.Fatalf("count chats: %v", err)
	}
	if crewCount != 1 {
		t.Errorf("crew count = %d, want 1", crewCount)
	}
	if agentCount != 1 {
		t.Errorf("agent count = %d, want 1", agentCount)
	}
	if chatCount != 1 {
		t.Errorf("chat count = %d, want 1", chatCount)
	}
}

// ---- Visibility: CrewHandler.List excludes the setup crew ----

func TestCrewList_ExcludesSetupCrew(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedCrewRow(t, db, "real-crew-1", wsID, "Real Crew", "real-crew")

	if _, err := ensureOnboardingSetupCrew(context.Background(), db, testLogger(), wsID, userID); err != nil {
		t.Fatalf("ensureOnboardingSetupCrew: %v", err)
	}

	h := &CrewHandler{db: db, logger: testLogger()}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/crews", nil)
	req = req.WithContext(withWorkspace(req.Context(), wsID, "OWNER"))
	w := httptest.NewRecorder()
	h.List(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	var got []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("crew list = %d entries, want 1 (setup crew must be excluded): %+v", len(got), got)
	}
	if got[0]["slug"] != "real-crew" {
		t.Errorf("crew list returned %v, want only the real crew", got[0]["slug"])
	}
	for _, c := range got {
		if c["slug"] == setupCrewSlug {
			t.Fatalf("setup crew %q leaked into crew list", setupCrewSlug)
		}
	}
}

// ---- System prompt carries the [LANGUAGE PREFERENCE] block, injected the
//      same way it is for any other agent (agent_config.go), plus this
//      file's own authored text. ----

func TestEnsureOnboardingSetupCrew_SystemPromptCarriesLanguageBlock(t *testing.T) {
	setTestEncryptionKeyParallelSafe(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	if _, err := db.ExecContext(context.Background(),
		"UPDATE workspaces SET preferred_language = ? WHERE id = ?", "Czech", wsID); err != nil {
		t.Fatalf("set preferred_language: %v", err)
	}

	info, err := ensureOnboardingSetupCrew(context.Background(), db, testLogger(), wsID, userID)
	if err != nil {
		t.Fatalf("ensureOnboardingSetupCrew: %v", err)
	}

	handler := NewInternalHandler(db, "test-token", testLogger())
	req := httptest.NewRequest(http.MethodGet, "/api/v1/internal/chats/"+info.ChatID+"/resolve", nil)
	req.SetPathValue("chatId", info.ChatID)
	w := httptest.NewRecorder()
	handler.ResolveChat(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	sysPrompt, _ := resp["system_prompt"].(string)

	if !strings.Contains(sysPrompt, "[LANGUAGE PREFERENCE]") {
		t.Error("system prompt missing [LANGUAGE PREFERENCE] block")
	}
	if !strings.Contains(sysPrompt, "Always respond in: Czech") {
		t.Error("system prompt missing the Czech language directive")
	}
	if !strings.Contains(sysPrompt, "Crewship Guide") {
		t.Error("system prompt missing the guide's own authored text")
	}
	if !strings.Contains(sysPrompt, "Ask for explicit confirmation") {
		t.Error("system prompt missing the confirmation-before-mutation contract")
	}
	if !strings.Contains(sysPrompt, "marker only renders a review card") ||
		!strings.Contains(sysPrompt, "SAME response as the concrete proposal") {
		t.Error("system prompt does not distinguish proposal-card rendering from mutation confirmation")
	}
}

// ---- Setup agent's prompt names at least one builtin crew template. ----

func TestBuildSetupAgentSystemPrompt_ListsBuiltinTemplates(t *testing.T) {
	db := setupTestDB(t)
	prompt, err := buildSetupAgentSystemPrompt(context.Background(), db, testLogger(), "ANTHROPIC", setupAgentModel)
	if err != nil {
		t.Fatalf("buildSetupAgentSystemPrompt: %v", err)
	}
	if !strings.Contains(prompt, "Software Development") {
		t.Errorf("prompt missing a known builtin template name; prompt=%s", prompt)
	}
	if !strings.Contains(prompt, "Security Audit") {
		t.Errorf("prompt missing another known builtin template name; prompt=%s", prompt)
	}
	for _, want := range []string{
		"built-in Crewship product specialist",
		"status.v1",
		"save_routine",
		"apiVersion",
		"CrewTemplate",
		"dedicated Crewship apply tool",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("guide prompt missing product contract %q", want)
		}
	}
}

// ---- Status wiring: creates the setup crew once workspace + credential
//      exist, and a repeat poll does not create a second one. ----

func TestOnboardingStatus_CreatesSetupCrewOnceCredentialExists(t *testing.T) {
	setTestEncryptionKeyParallelSafe(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	h := NewOnboardingHandler(db, nil, testLogger())
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req = req.WithContext(withUser(req.Context(), &AuthUser{ID: userID}))

	// No credential yet: Status must not create a setup crew.
	w := httptest.NewRecorder()
	h.Status(w, req)
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM crews WHERE workspace_id = ? AND kind = ?", wsID, setupCrewKindSetup).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Fatalf("setup crew created before a credential existed")
	}

	// Seed a credential the way onboarding would, then poll again.
	if err := insertOnboardingCredential(context.Background(), db, userID, wsID, "API Key", "ANTHROPIC", "ANTHROPIC_API_KEY", "sk-ant-oat01-fake", isoMillisNow()); err != nil {
		t.Fatalf("insert credential: %v", err)
	}

	w = httptest.NewRecorder()
	h.Status(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	var statusResp map[string]bool
	if err := json.Unmarshal(w.Body.Bytes(), &statusResp); err != nil {
		t.Fatalf("decode first status response: %v", err)
	}
	if statusResp["completed"] {
		t.Fatal("setup crew creation must not itself complete onboarding")
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM crews WHERE workspace_id = ? AND kind = ?", wsID, setupCrewKindSetup).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Fatalf("setup crew count after first poll with credential = %d, want 1", n)
	}

	// A second poll ("a second onboarding") must not create a second one.
	w = httptest.NewRecorder()
	h.Status(w, req)
	if err := json.Unmarshal(w.Body.Bytes(), &statusResp); err != nil {
		t.Fatalf("decode second status response: %v", err)
	}
	if statusResp["completed"] {
		t.Fatal("setup agent was counted as a real crew agent on the next status poll")
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM crews WHERE workspace_id = ? AND kind = ?", wsID, setupCrewKindSetup).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Fatalf("setup crew count after second poll = %d, want 1 (must not duplicate)", n)
	}
}

// ---- Persistence: Complete() keeps the system crew, guide, and chat live. ----

func TestOnboardingComplete_RetainsCrewshipGuide(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	info, err := ensureOnboardingSetupCrew(context.Background(), db, testLogger(), wsID, userID)
	if err != nil {
		t.Fatalf("ensureOnboardingSetupCrew: %v", err)
	}

	h := NewOnboardingHandler(db, nil, testLogger())
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req = req.WithContext(withUser(req.Context(), &AuthUser{ID: userID}))
	w := httptest.NewRecorder()
	h.Complete(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}

	var crewDeletedAt, agentDeletedAt sql.NullString
	if err := db.QueryRow("SELECT deleted_at FROM crews WHERE id = ?", info.CrewID).Scan(&crewDeletedAt); err != nil {
		t.Fatalf("read crew: %v", err)
	}
	if crewDeletedAt.Valid {
		t.Error("Crewship Guide crew was deleted when onboarding completed")
	}
	if err := db.QueryRow("SELECT deleted_at FROM agents WHERE id = ?", info.AgentID).Scan(&agentDeletedAt); err != nil {
		t.Fatalf("read agent: %v", err)
	}
	if agentDeletedAt.Valid {
		t.Error("Crewship Guide agent was deleted when onboarding completed")
	}
	var chatExists int
	if err := db.QueryRow("SELECT COUNT(*) FROM chats WHERE id = ?", info.ChatID).Scan(&chatExists); err != nil {
		t.Fatalf("read chat: %v", err)
	}
	if chatExists != 1 {
		t.Error("Crewship Guide chat row should remain available")
	}

	var name, roleTitle, suggested string
	var memoryEnabled int
	if err := db.QueryRow(`
		SELECT name, role_title, memory_enabled, suggested_prompts
		FROM agents WHERE id = ?`, info.AgentID).
		Scan(&name, &roleTitle, &memoryEnabled, &suggested); err != nil {
		t.Fatalf("read retained guide configuration: %v", err)
	}
	if name != "Crewship Guide" || roleTitle != "Crewship Specialist" {
		t.Fatalf("retained identity = %q / %q", name, roleTitle)
	}
	if memoryEnabled != 1 {
		t.Error("Crewship Guide must retain workspace memory after onboarding")
	}
	for _, want := range []string{"Design a crew", "Create a routine", "Crewship Page", "YAML manifest"} {
		if !strings.Contains(suggested, want) {
			t.Errorf("suggested prompts missing %q: %q", want, suggested)
		}
	}
}

func TestOnboardingStatus_RetainsSetupCrewForAlreadyCompletedUser(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	info, err := ensureOnboardingSetupCrew(context.Background(), db, testLogger(), wsID, userID)
	if err != nil {
		t.Fatalf("ensureOnboardingSetupCrew: %v", err)
	}
	if _, err := db.Exec("UPDATE users SET onboarding_completed = 1, onboarding_skipped_at = ? WHERE id = ?", "2026-08-22T00:00:00Z", userID); err != nil {
		t.Fatalf("mark completed: %v", err)
	}

	h := NewOnboardingHandler(db, nil, testLogger())
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req = req.WithContext(withUser(req.Context(), &AuthUser{ID: userID}))
	w := httptest.NewRecorder()
	h.Status(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}

	var crewDeletedAt, agentDeletedAt sql.NullString
	if err := db.QueryRow("SELECT deleted_at FROM crews WHERE id = ?", info.CrewID).Scan(&crewDeletedAt); err != nil {
		t.Fatalf("read crew: %v", err)
	}
	if err := db.QueryRow("SELECT deleted_at FROM agents WHERE id = ?", info.AgentID).Scan(&agentDeletedAt); err != nil {
		t.Fatalf("read agent: %v", err)
	}
	if crewDeletedAt.Valid || agentDeletedAt.Valid {
		t.Fatalf("completed-user status deleted guide rows: crew=%v agent=%v", crewDeletedAt.Valid, agentDeletedAt.Valid)
	}
}

func TestOnboardingStatus_ReopensCompletedUserWithNoRealAgent(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	info, err := ensureOnboardingSetupCrew(context.Background(), db, testLogger(), wsID, userID)
	if err != nil {
		t.Fatalf("ensure setup crew: %v", err)
	}
	// Simulate a workspace upgraded from a build that reclaimed the temporary
	// setup rows when onboarding completed. Status must revive them as the
	// permanent Crewship Guide.
	if _, err := db.Exec("UPDATE agents SET deleted_at = '2026-08-22T00:00:00Z' WHERE id = ?", info.AgentID); err != nil {
		t.Fatalf("tombstone legacy setup agent: %v", err)
	}
	if _, err := db.Exec("UPDATE crews SET deleted_at = '2026-08-22T00:00:00Z' WHERE id = ?", info.CrewID); err != nil {
		t.Fatalf("tombstone legacy setup crew: %v", err)
	}
	if _, err := db.Exec("UPDATE users SET onboarding_completed = 1 WHERE id = ?", userID); err != nil {
		t.Fatalf("mark completed: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO credentials
			(id, workspace_id, name, encrypted_value, type, provider, status, created_by)
		VALUES ('resume-cred', ?, 'ANTHROPIC_API_KEY', 'encrypted', 'AI_CLI_TOKEN', 'ANTHROPIC', 'ACTIVE', ?)`,
		wsID, userID); err != nil {
		t.Fatalf("insert credential: %v", err)
	}

	h := NewOnboardingHandler(db, nil, testLogger())
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req = req.WithContext(withUser(req.Context(), &AuthUser{ID: userID}))
	w := httptest.NewRecorder()
	h.Status(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	var body map[string]bool
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if body["completed"] {
		t.Fatal("interrupted onboarding with no real agent remained completed")
	}

	var completed int
	if err := db.QueryRow("SELECT onboarding_completed FROM users WHERE id = ?", userID).Scan(&completed); err != nil {
		t.Fatalf("read completed flag: %v", err)
	}
	if completed != 0 {
		t.Fatalf("onboarding_completed = %d, want 0", completed)
	}
	var crewDeletedAt, agentDeletedAt sql.NullString
	if err := db.QueryRow("SELECT deleted_at FROM crews WHERE id = ?", info.CrewID).Scan(&crewDeletedAt); err != nil {
		t.Fatalf("read setup crew: %v", err)
	}
	if err := db.QueryRow("SELECT deleted_at FROM agents WHERE id = ?", info.AgentID).Scan(&agentDeletedAt); err != nil {
		t.Fatalf("read setup agent: %v", err)
	}
	if crewDeletedAt.Valid || agentDeletedAt.Valid {
		t.Fatalf("setup rows were not revived: crew_deleted=%v agent_deleted=%v", crewDeletedAt.Valid, agentDeletedAt.Valid)
	}
}

// ---- Credential-aware model choice: the setup agent's provider follows
//      whatever credential the workspace already has, so autoAssignCredentials
//      can actually wire it up. ----

func TestEnsureOnboardingSetupCrew_FollowsWorkspaceCredentialProvider(t *testing.T) {
	setTestEncryptionKeyParallelSafe(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	if err := insertOnboardingCredential(context.Background(), db, userID, wsID, "API Key", "OPENAI", "OPENAI_API_KEY", "test-value", isoMillisNow()); err != nil {
		t.Fatalf("insert credential: %v", err)
	}

	info, err := ensureOnboardingSetupCrew(context.Background(), db, testLogger(), wsID, userID)
	if err != nil {
		t.Fatalf("ensureOnboardingSetupCrew: %v", err)
	}

	var adapter, provider, model, prompt string
	if err := db.QueryRow("SELECT cli_adapter, llm_provider, llm_model, system_prompt_legacy FROM agents WHERE id = ?", info.AgentID).
		Scan(&adapter, &provider, &model, &prompt); err != nil {
		t.Fatalf("read agent provider: %v", err)
	}
	if adapter != "CODEX_CLI" {
		t.Errorf("agent cli_adapter = %q, want CODEX_CLI", adapter)
	}
	if provider != "OPENAI" {
		t.Errorf("agent llm_provider = %q, want OPENAI (matching the workspace's only credential)", provider)
	}
	if model != "gpt-5.5" {
		t.Errorf("agent llm_model = %q, want gpt-5.5", model)
	}
	// The marker's model is the PROPOSED CREW's, not the Guide's own, and the
	// two are deliberately different: the Guide runs the runtime default
	// asserted above, while the crews it proposes get the middle tier of the
	// provider's curated catalogue. This assertion used to demand the Guide's
	// own gpt-5.5 here, which is not a curated id — so validateCrewModel threw
	// it away and put claude-sonnet-5 on an OpenAI crew.
	wantCrewModel := crewDefaultModelForProvider("OPENAI")
	if !strings.Contains(prompt, `"llm_provider":"OPENAI","llm_model":"`+wantCrewModel+`"`) {
		t.Errorf("proposal marker does not offer the curated OPENAI crew model %q", wantCrewModel)
	}

	var assigned int
	if err := db.QueryRow("SELECT COUNT(*) FROM agent_credentials WHERE agent_id = ?", info.AgentID).Scan(&assigned); err != nil {
		t.Fatalf("count agent_credentials: %v", err)
	}
	if assigned != 1 {
		t.Errorf("agent_credentials rows for setup agent = %d, want 1", assigned)
	}
}

func TestEnsureOnboardingSetupCrew_RefreshesExistingAgentPrompt(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	info, err := ensureOnboardingSetupCrew(context.Background(), db, testLogger(), wsID, userID)
	if err != nil {
		t.Fatalf("first ensure: %v", err)
	}
	if _, err := db.Exec(`
		UPDATE agents
		SET system_prompt_legacy = 'obsolete prompt', cli_adapter = 'CODEX_CLI',
			llm_provider = 'OPENAI', llm_model = 'obsolete-model'
		WHERE id = ?`, info.AgentID); err != nil {
		t.Fatalf("make setup agent stale: %v", err)
	}

	second, err := ensureOnboardingSetupCrew(context.Background(), db, testLogger(), wsID, userID)
	if err != nil {
		t.Fatalf("second ensure: %v", err)
	}
	if second.AgentID != info.AgentID {
		t.Fatalf("refresh replaced the stable setup agent: first=%s second=%s", info.AgentID, second.AgentID)
	}

	var adapter, provider, model, prompt string
	if err := db.QueryRow(`
		SELECT cli_adapter, llm_provider, llm_model, system_prompt_legacy
		FROM agents WHERE id = ?`, info.AgentID).Scan(&adapter, &provider, &model, &prompt); err != nil {
		t.Fatalf("read refreshed setup agent: %v", err)
	}
	if adapter != "CLAUDE_CODE" || provider != "ANTHROPIC" || model != setupAgentModel {
		t.Errorf("refreshed runtime = %s/%s/%s, want CLAUDE_CODE/ANTHROPIC/%s", adapter, provider, model, setupAgentModel)
	}
	if !strings.Contains(prompt, "Crewship Guide") || !strings.Contains(prompt, "crewship:onboarding-proposal") {
		t.Error("existing setup agent did not receive the current authored prompt")
	}
}

func TestWorkspaceHasCredential_RejectsUnusableRows(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewOnboardingHandler(db, nil, testLogger())

	if err := insertOnboardingCredential(context.Background(), db, userID, wsID, "GitHub token", "GITHUB", "GITHUB_TOKEN", "fake", isoMillisNow()); err != nil {
		t.Fatalf("insert unrelated credential: %v", err)
	}
	if h.workspaceHasCredential(context.Background(), wsID) {
		t.Fatal("unrelated credential opened the model setup chat")
	}

	if err := insertOnboardingCredential(context.Background(), db, userID, wsID, "Anthropic", "ANTHROPIC", "ANTHROPIC_API_KEY", "fake", isoMillisNow()); err != nil {
		t.Fatalf("insert model credential: %v", err)
	}
	if !h.workspaceHasCredential(context.Background(), wsID) {
		t.Fatal("active Anthropic model credential was not recognized")
	}
	if _, err := db.Exec(`UPDATE credentials SET status = 'REVOKED' WHERE workspace_id = ? AND provider = 'ANTHROPIC'`, wsID); err != nil {
		t.Fatalf("revoke credential: %v", err)
	}
	if h.workspaceHasCredential(context.Background(), wsID) {
		t.Fatal("revoked model credential opened the setup chat")
	}
}

// The Guide's ordering rule, pinned in the prompt because the server-side
// refusal (internal_delegated_crew.go) is a backstop, not the mechanism. A
// backstop that fires on every attempt means every onboarding burns a
// round-trip on an error the prompt should have prevented.
func TestSetupAgentPrompt_TellsTheGuideItOwnsNothing(t *testing.T) {
	prompt := renderSetupAgentPrompt(setupAgentSystemPromptTemplate, "ANTHROPIC", setupAgentModel)

	// The consequence, not just the rule. A model that knows ownership picks
	// the egress allowlist and the container has a reason to get it right on
	// a routine the prompt never anticipated.
	for _, want := range []string{
		"YOU OWN NOTHING",
		"allowlist",
		`"crew" argument`,
		"discover_capabilities",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt is missing %q — the Guide will not know to name the target crew", want)
		}
	}

	// Ordering: propose, wait for Create, then build. Stated as a numbered
	// sequence rather than left implicit, because "logically first" is
	// exactly the kind of instruction a model satisfies in its own order.
	own := strings.Index(prompt, "YOU OWN NOTHING")
	contract := strings.Index(prompt, "REAL TOOL CONTRACT")
	if own < 0 || contract < 0 || own > contract {
		t.Errorf("the ownership rule must come before the tool contract (own=%d contract=%d)", own, contract)
	}
}

// The model the prompt tells the Guide to emit must be one validateCrewModel
// then ACCEPTS. Those two were derived from different tables — the prompt from
// providerRuntimeDefaults, the check from llm.CuratedModels — and the tables
// disagreed: providerRuntimeDefaults answers "gpt-5.5" for OPENAI and
// "gemini-2.5-pro" for GOOGLE, neither of which is curated. So the Guide
// dutifully emitted the id it was handed, validateCrewModel missed it, and
// substituted crewAgentDefaultModel — a CLAUDE id — onto an OpenAI crew.
// Provider OPENAI, adapter CODEX_CLI, model claude-sonnet-5: every field
// valid, the combination unrunnable, which is the exact failure the
// interpolation was added to prevent.
func TestSetupAgentPrompt_SuggestsAModelTheValidatorAccepts(t *testing.T) {
	for _, provider := range []string{"ANTHROPIC", "OPENAI", "GOOGLE"} {
		t.Run(provider, func(t *testing.T) {
			suggested := crewDefaultModelForProvider(provider)
			if suggested == "" {
				t.Fatalf("no default model offered for %s, which has a curated catalogue", provider)
			}
			resolved, substituted := validateCrewModel(provider, suggested)
			if substituted {
				t.Errorf("the prompt suggests %q for %s but the validator swaps it for %q",
					suggested, provider, resolved)
			}
			// And it has to actually reach the prompt, not merely exist.
			prompt := renderSetupAgentPrompt(setupAgentSystemPromptTemplate, provider, setupAgentModel)
			if !strings.Contains(prompt, `"llm_model":"`+suggested+`"`) {
				t.Errorf("%s prompt does not carry %q in the marker template", provider, suggested)
			}
		})
	}
}

// A provider whose catalogue this binary does not ship (Ollama and anything
// self-hosted — its models live on the daemon) must be told to OMIT the field,
// never handed a Claude id to copy. An empty llm_model is already understood
// as "no override" by validateCrewModel; a wrong one is a crew that fails at
// the adapter on every run.
func TestSetupAgentPrompt_OffersNoModelWhereThereIsNoCatalogue(t *testing.T) {
	for _, provider := range []string{"OLLAMA", "CURSOR", "FACTORY"} {
		t.Run(provider, func(t *testing.T) {
			if got := crewDefaultModelForProvider(provider); got != "" {
				t.Errorf("suggested %q for %s, which has no curated catalogue", got, provider)
			}
			prompt := renderSetupAgentPrompt(setupAgentSystemPromptTemplate, provider, setupAgentModel)
			if strings.Contains(prompt, crewAgentDefaultModel) {
				t.Errorf("%s prompt names the Anthropic default %q", provider, crewAgentDefaultModel)
			}
		})
	}
}
