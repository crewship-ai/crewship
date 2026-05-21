package consolidate

// New scenario tests for the consolidator's scoring + Skills-promotion
// bridges. These complement the existing scoring_test.go / skill_promote_test.go
// suites by pinning a handful of contracts the older tests don't exercise:
//
//   - empty-input safety (no panic, empty map return)
//   - end-to-end signal clamping when LearnedRule.Confidence is absurd
//     (the scorer is the last line of defense; parseRules clamps too but
//     a caller that bypasses parseRules — e.g. tests, future programmatic
//     consumers — still gets correct output)
//   - explicit recall=0 / recall=10 + composite>=0.85 boundaries for the
//     Skill-promotion gate (the existing test mixes both gates; these
//     pin them independently)
//   - reject/explain not-found sentinel returns (existing tests cover
//     approve not-found and reject non-pending but not these specific
//     surfaces)
//
// Test names use the TestConsolidate_<Scenario>_<Expected> shape so
// `go test -run '^TestConsolidate_'` selects exactly these tests.

import (
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
)

// TestConsolidate_ComputeProposalScores_EmptyRulesReturnsEmptyMap pins
// the empty-input contract: the scorer is called from writeProposal on
// every consolidator tick, and a tick that produces zero rules
// (post-filter) must not crash on the empty slice. Returning a
// non-nil, empty map preserves the JSON-marshal contract for the
// score_json column (an empty map serialises to "{}" rather than
// "null").
func TestConsolidate_ComputeProposalScores_EmptyRulesReturnsEmptyMap(t *testing.T) {
	// computeProposalScoresWithRecall is the post-PR-#391 entry
	// point (the unused-wrapper computeProposalScores was removed
	// in that PR). Both nil and []LearnedRule{} must return a
	// non-nil, zero-length map so the score_json column always
	// serialises to "{}" rather than "null".
	cases := []struct {
		name  string
		rules []LearnedRule
	}{
		{name: "nil rules", rules: nil},
		{name: "empty rules", rules: []LearnedRule{}},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			scores := computeProposalScoresWithRecall(context.Background(), nil, "", tc.rules, 0, time.Now())
			if scores == nil {
				t.Fatalf("computeProposalScoresWithRecall(%s) returned nil map; want non-nil empty map for JSON-marshal compatibility", tc.name)
			}
			if len(scores) != 0 {
				t.Fatalf("len = %d, want 0 for empty input", len(scores))
			}
		})
	}
}

// TestConsolidate_ComputeScore_AbsurdHighConfidence_ClampedToOne
// verifies the scorer clamps an out-of-spec Confidence ≥ 1.0 to the
// signal's [0,1] range. parseRules already clamps at LLM-extract
// time, but ComputeScore is the load-bearing pure function — direct
// callers (tests, future programmatic emitters) must also see the
// clamp applied so the composite never drifts above 1.0 through a
// single mis-scaled signal.
func TestConsolidate_ComputeScore_AbsurdHighConfidence_ClampedToOne(t *testing.T) {
	now := time.Now()
	m := CandidateMetrics{
		RawRelevance:       5.0, // absurd, must clamp
		RecallCount:        10,
		UniqueQueries:      5,
		ConsolidationCount: 3,
		LastSeenAt:         now,
		EvidenceCount:      4,
		DistinctEntryTypes: 2,
	}
	res := ComputeScore(m, DefaultSignalWeights(), DefaultThresholds(), now)
	if res.Signals.Relevance != 1.0 {
		t.Errorf("Signals.Relevance = %v, want 1.0 (clamped from 5.0)", res.Signals.Relevance)
	}
	if res.Composite > 1.0+1e-9 {
		t.Errorf("Composite = %v, want <= 1.0 (clamping must protect dot-product)", res.Composite)
	}
}

// TestConsolidate_ComputeScore_NegativeConfidence_ClampedToZero
// is the symmetric guard: a negative confidence (sometimes emitted
// by an LLM that meant "no signal") must round up to 0, not become
// a negative contribution that drags the composite below where any
// other signal pushed it.
func TestConsolidate_ComputeScore_NegativeConfidence_ClampedToZero(t *testing.T) {
	now := time.Now()
	m := CandidateMetrics{
		RawRelevance: -1.0,
		RecallCount:  10,
		LastSeenAt:   now,
	}
	res := ComputeScore(m, DefaultSignalWeights(), DefaultThresholds(), now)
	if res.Signals.Relevance != 0.0 {
		t.Errorf("Signals.Relevance = %v, want 0 (clamped from -1.0)", res.Signals.Relevance)
	}
	if res.Composite < 0 {
		t.Errorf("Composite = %v, want >= 0 (negative inputs must not produce negative composite)", res.Composite)
	}
}

// TestConsolidate_ComputeScore_NaNConfidence_NormalisesToZero is
// the NaN guard. LLM-derived floats can occasionally arrive as NaN
// when the prompt fields surface JSON.parse oddities; the scorer
// must not propagate the NaN into Composite where it would poison
// every downstream gate (any NaN comparison is false, so Promoted
// would be silently false but the cause unclear).
func TestConsolidate_ComputeScore_NaNConfidence_NormalisesToZero(t *testing.T) {
	now := time.Now()
	m := CandidateMetrics{
		RawRelevance: math.NaN(),
		RecallCount:  10,
		LastSeenAt:   now,
	}
	res := ComputeScore(m, DefaultSignalWeights(), DefaultThresholds(), now)
	if res.Signals.Relevance != 0 {
		t.Errorf("Signals.Relevance = %v, want 0 (NaN input must zero out)", res.Signals.Relevance)
	}
	if math.IsNaN(res.Composite) {
		t.Errorf("Composite is NaN; the scorer must not propagate NaN through the dot-product")
	}
}

// TestConsolidate_ComputeScore_ZeroValueMetrics_AllSignalsZero
// pins the all-zero baseline. The "no signal at all" candidate
// (everything zero, LastSeenAt unset) must produce all-zero signals
// and Composite=0, NOT promotion. Useful as the minimum-viable input
// the runner might construct when the journal returns nothing
// interesting.
func TestConsolidate_ComputeScore_ZeroValueMetrics_AllSignalsZero(t *testing.T) {
	now := time.Now()
	res := ComputeScore(CandidateMetrics{}, DefaultSignalWeights(), DefaultThresholds(), now)
	if res.Signals.Relevance != 0 {
		t.Errorf("Relevance = %v, want 0", res.Signals.Relevance)
	}
	if res.Signals.Frequency != 0 {
		t.Errorf("Frequency = %v, want 0", res.Signals.Frequency)
	}
	if res.Signals.QueryDiversity != 0 {
		t.Errorf("QueryDiversity = %v, want 0", res.Signals.QueryDiversity)
	}
	if res.Signals.Recency != 0 {
		t.Errorf("Recency = %v, want 0 (zero-time clamp)", res.Signals.Recency)
	}
	if res.Signals.Consolidation != 0 {
		t.Errorf("Consolidation = %v, want 0", res.Signals.Consolidation)
	}
	if res.Signals.ConceptualRichness != 0 {
		t.Errorf("ConceptualRichness = %v, want 0", res.Signals.ConceptualRichness)
	}
	if res.Composite != 0 {
		t.Errorf("Composite = %v, want 0 for all-zero metrics", res.Composite)
	}
	if res.Promoted {
		t.Errorf("Promoted = true on all-zero metrics; the empty candidate must never auto-promote")
	}
}

// TestConsolidate_DefaultThresholds pins the documented baseline
// constants in place. If a future PR retunes these silently the
// promotion gate's behaviour drifts; the test forces an explicit
// "yes, we are intentionally changing the baseline" decision.
func TestConsolidate_DefaultThresholds(t *testing.T) {
	got := DefaultThresholds()
	if got.MinScore != 0.80 {
		t.Errorf("MinScore = %v, want 0.80 (baseline)", got.MinScore)
	}
	if got.MinRecallCount != 3 {
		t.Errorf("MinRecallCount = %d, want 3 (baseline)", got.MinRecallCount)
	}
	if got.MinUniqueQueries != 3 {
		t.Errorf("MinUniqueQueries = %d, want 3 (baseline)", got.MinUniqueQueries)
	}
}

// TestConsolidate_PromoteEligibleRules_RecallZero_NoSkillStaged
// pins the very-first-proposal case: a brand-new rule has
// RecallCount=0 at score time (the scorer fills 0 when the journal
// has no memory.searched hits), and the Skills bridge must NOT
// stage a SKILL.md for it. Only rules that have repeatedly been
// recalled cross the gate — the gate's whole point.
func TestConsolidate_PromoteEligibleRules_RecallZero_NoSkillStaged(t *testing.T) {
	tmp := t.TempDir()
	rules := []LearnedRule{
		{Pattern: "fresh rule never recalled", Action: "act", Evidence: []string{"e1", "e2"}, Confidence: 0.95},
	}
	scores := map[string]ScoreResult{
		"fresh rule never recalled": {
			Composite:   0.92,
			RecallCount: 0, // the gate condition
			Promoted:    false,
		},
	}
	written, err := PromoteEligibleRules(rules, scores, SkillPromoteOptions{
		OutputDir: tmp,
		Now:       time.Now(),
		// Explicit thresholds (not the defaults) so the test pins
		// behaviour against the gate values, not against future
		// const churn.
		MinRecall:    10,
		MinComposite: 0.85,
	})
	if err != nil {
		t.Fatalf("PromoteEligibleRules: %v", err)
	}
	if len(written) != 0 {
		t.Errorf("recall=0 rule was staged anyway: %v", written)
	}
	// And nothing should have landed on disk.
	if entries, _ := os.ReadDir(filepath.Join(tmp, ".proposed")); len(entries) != 0 {
		t.Errorf(".proposed/ should be empty for recall=0 batch, got %v", entries)
	}
}

// TestConsolidate_PromoteEligibleRules_HighRecallHighScore_StagesSkill
// pins the positive case: a rule that has been recalled 10+ times
// AND has a composite score >= 0.85 IS staged under .proposed/ as
// a SKILL.md. The on-disk artefact must be present and contain the
// pattern in its body so a reviewing operator can identify it.
func TestConsolidate_PromoteEligibleRules_HighRecallHighScore_StagesSkill(t *testing.T) {
	tmp := t.TempDir()
	rules := []LearnedRule{
		{
			Pattern:    "deploy database migrations on Sunday",
			Action:     "always run a staging dry-run first",
			Evidence:   []string{"e_a", "e_b", "e_c"},
			Confidence: 0.92,
		},
	}
	scores := map[string]ScoreResult{
		"deploy database migrations on Sunday": {
			Composite:     0.88, // >= 0.85
			RecallCount:   12,   // >= 10
			UniqueQueries: 6,
			Promoted:      true,
		},
	}
	written, err := PromoteEligibleRules(rules, scores, SkillPromoteOptions{
		OutputDir:    tmp,
		Now:          time.Date(2026, 5, 17, 12, 0, 0, 0, time.UTC),
		MinRecall:    10,
		MinComposite: 0.85,
	})
	if err != nil {
		t.Fatalf("PromoteEligibleRules: %v", err)
	}
	if len(written) != 1 {
		t.Fatalf("want exactly 1 staged skill, got %d: %v", len(written), written)
	}
	// File exists, is in .proposed/, has the slugified pattern in
	// the filename, and contains the pattern verbatim in its body.
	got := written[0]
	if !strings.Contains(got, string(filepath.Separator)+".proposed"+string(filepath.Separator)) {
		t.Errorf("staged skill not in .proposed/: %q", got)
	}
	body, err := os.ReadFile(got)
	if err != nil {
		t.Fatalf("read staged skill: %v", err)
	}
	if !strings.Contains(string(body), "deploy database migrations on Sunday") {
		t.Errorf("staged skill body missing pattern; got=\n%s", body)
	}
}

// TestConsolidate_PromoteEligibleRules_EmptyInput_NoError guards
// the no-rules-this-tick path. A consolidator run that produces
// zero rules calls promoteProposalSkills which calls
// PromoteEligibleRules with []; this must NOT error and must NOT
// create any disk artefacts.
func TestConsolidate_PromoteEligibleRules_EmptyInput_NoError(t *testing.T) {
	tmp := t.TempDir()
	written, err := PromoteEligibleRules(nil, map[string]ScoreResult{}, SkillPromoteOptions{
		OutputDir: tmp,
		Now:       time.Now(),
	})
	if err != nil {
		t.Errorf("empty input should not error; got %v", err)
	}
	if len(written) != 0 {
		t.Errorf("empty input produced output: %v", written)
	}
	// .proposed/ must not even be created — there's nothing to stage
	// so the directory is irrelevant.
	if _, err := os.Stat(filepath.Join(tmp, ".proposed")); !os.IsNotExist(err) {
		t.Errorf(".proposed/ should not exist for empty batch, stat err = %v", err)
	}
}

// TestConsolidate_PromoteEligibleRules_MissingScoreEntry_Skipped
// pins the lookup-miss branch. PromoteEligibleRules keys scores
// off rule.Pattern; a rule with no matching score map entry is
// silently skipped (the scorer guarantees coverage in production,
// but defensive code paths must survive a mismatched input set).
func TestConsolidate_PromoteEligibleRules_MissingScoreEntry_Skipped(t *testing.T) {
	tmp := t.TempDir()
	rules := []LearnedRule{
		{Pattern: "scored rule", Action: "a", Evidence: []string{"e1"}, Confidence: 0.9},
		{Pattern: "unscored rule", Action: "b", Evidence: []string{"e2"}, Confidence: 0.9},
	}
	scores := map[string]ScoreResult{
		// Only one of the two rules has a score entry.
		"scored rule": {Composite: 0.90, RecallCount: 12, Promoted: true},
	}
	written, err := PromoteEligibleRules(rules, scores, SkillPromoteOptions{
		OutputDir: tmp, Now: time.Now(), MinRecall: 10, MinComposite: 0.85,
	})
	if err != nil {
		t.Fatalf("PromoteEligibleRules: %v", err)
	}
	if len(written) != 1 {
		t.Fatalf("want 1 staged skill (only the scored rule); got %d: %v", len(written), written)
	}
	if !strings.Contains(written[0], "scored-rule") {
		t.Errorf("wrong rule promoted: %q (expected slug for 'scored rule')", written[0])
	}
}

// TestConsolidate_ExplainProposal_NotFound_ReturnsSentinel pins the
// 404-mapping contract for ExplainProposal. The HTTP layer maps
// ErrProposalNotFound -> 404; the sentinel-error contract is what
// makes that routing reliable across binaries that may have
// different schema versions on the same row id space.
func TestConsolidate_ExplainProposal_NotFound_ReturnsSentinel(t *testing.T) {
	db := openDB(t)
	defer db.Close()
	applyV89Schema(t, db)

	_, err := ExplainProposal(context.Background(), db, "mp_no_such_proposal")
	if !errors.Is(err, ErrProposalNotFound) {
		t.Errorf("err = %v, want ErrProposalNotFound", err)
	}
}

// TestConsolidate_RejectProposal_NotFound_ReturnsSentinel pins the
// matching reject-side contract. Both approve and reject share
// loadProposalForDecision; this test ensures the wrapper returns the
// sentinel rather than wrapping it into a generic "query proposal"
// error string the HTTP layer can't recognise.
func TestConsolidate_RejectProposal_NotFound_ReturnsSentinel(t *testing.T) {
	db := openDB(t)
	defer db.Close()
	w := journal.NewWriter(db, quietLogger(), journal.WriterOptions{FlushSize: 1})
	defer w.Close()
	applyV89Schema(t, db)

	err := RejectProposal(context.Background(), db, w, quietLogger(), "mp_no_such_proposal", "u1", "reason")
	if !errors.Is(err, ErrProposalNotFound) {
		t.Errorf("err = %v, want ErrProposalNotFound", err)
	}
}

// TestConsolidate_ApproveProposal_CanonicalContainsExactRuleBody
// pins the body-merge contract: the .proposed/ proposal's rules
// section (everything after the first "---" divider) lands in
// learned-YYYY-MM-DD.md verbatim. The existing happy-path test
// asserts substring presence; this one asserts every load-bearing
// rule field (Pattern, Action, Confidence, Evidence) appears, so a
// future change to extractRulesBody that silently drops a field
// fails loudly.
func TestConsolidate_ApproveProposal_CanonicalContainsExactRuleBody(t *testing.T) {
	db := openDB(t)
	defer db.Close()
	w := journal.NewWriter(db, quietLogger(), journal.WriterOptions{FlushSize: 1})
	defer w.Close()
	applyV89Schema(t, db)

	ids := seedEntries(t, db, w, "ws_test", "crew_test", 12, journal.EntryPeerEscalation)
	reply := `[{"pattern":"merge me verbatim","action":"copy across the divider","evidence":["` + ids[0] + `","` + ids[1] + `"],"confidence":0.77}]`
	c := &Consolidator{DB: db, Journal: w, Summarizer: &stubSummarizer{Reply: reply}, Logger: quietLogger()}
	outputDir := t.TempDir()
	cfg := Config{
		WorkspaceID:  "ws_test",
		CrewID:       "crew_test",
		Since:        time.Hour,
		MinEntries:   10,
		OutputDir:    outputDir,
		ProposalMode: true,
	}
	if _, err := c.Run(context.Background(), cfg); err != nil {
		t.Fatalf("seed Run: %v", err)
	}
	var proposalID, proposalPath string
	if err := db.QueryRow(`SELECT id, proposal_path FROM memory_proposals WHERE workspace_id='ws_test' LIMIT 1`).
		Scan(&proposalID, &proposalPath); err != nil {
		t.Fatalf("read proposal: %v", err)
	}

	// Snapshot the proposal body BEFORE approving so we can compare
	// the rules-block extraction against what lands in canonical.
	proposalBody, err := os.ReadFile(proposalPath)
	if err != nil {
		t.Fatalf("read proposal body: %v", err)
	}
	dividerIdx := strings.Index(string(proposalBody), "\n---\n")
	if dividerIdx < 0 {
		t.Fatalf("proposal lacks divider, body=\n%s", proposalBody)
	}
	rulesBlock := strings.TrimSpace(string(proposalBody)[dividerIdx+len("\n---\n"):])
	// Sanity-check our local extraction matches the production
	// extractRulesBody helper. If these ever diverge, the test
	// signals which side moved.
	if got := extractRulesBody(string(proposalBody)); got != rulesBlock {
		t.Fatalf("local extract != production extractRulesBody:\nlocal=\n%s\nprod=\n%s", rulesBlock, got)
	}

	appr, err := ApproveProposal(context.Background(), db, w, quietLogger(), proposalID, "operator_x", ApprovalOptions{})
	if err != nil {
		t.Fatalf("ApproveProposal: %v", err)
	}

	canonical, err := os.ReadFile(appr.CanonicalPath)
	if err != nil {
		t.Fatalf("read canonical: %v", err)
	}
	canonicalStr := string(canonical)

	// Every individual rule field must be present in the canonical
	// body — pattern, action, confidence, and each evidence id.
	wants := []string{
		"merge me verbatim",
		"copy across the divider",
		"0.77",
		ids[0],
		ids[1],
	}
	for _, w := range wants {
		if !strings.Contains(canonicalStr, w) {
			t.Errorf("canonical missing %q; got=\n%s", w, canonicalStr)
		}
	}
	// And the canonical must include the full extracted rules block
	// (not a truncated subset).
	if !strings.Contains(canonicalStr, rulesBlock) {
		t.Errorf("canonical does not contain the proposal's full rules block verbatim\nrulesBlock=\n%s\ncanonical=\n%s", rulesBlock, canonicalStr)
	}
}
