package orchestrator

// outcome.go — the §9.6 outcome vocabulary and its routing table
// (PRD-ISSUES-AND-ROUTINES-2026, work package B6, #2349), implemented
// ONCE here and used by every consumer: finishAssignment (assignments,
// internal/api/assignments_run.go), the pipeline executor's terminal write
// (pipeline_runs, internal/pipeline/executor.go), session-state settlement
// (internal/api/issue_session_state.go), and inbox item creation
// (internal/api/issue_outcome_inbox.go).
//
// Outcome is distinct from BOTH existing "how did it go" fields:
//   - status is technical (COMPLETED/FAILED/CANCELLED, or pipeline_runs'
//     lowercase twin) — this file never writes it and never reads it to
//     decide anything other than the CANCELLED/FAILED shortcuts below.
//   - runverdict (internal/runverdict) is an advisory LLM judgment,
//     generated best-effort after the fact. Outcome is authoritative and
//     deterministic: the runner sets it, a consumer never infers it.
//
// This is a single, closed vocabulary shared by both run tables (§9.4,
// §9.6) — putting it in internal/orchestrator (rather than internal/api or
// internal/pipeline) is what lets both sides use the identical routing
// table without either importing the other: internal/api and
// internal/pipeline both already import internal/orchestrator (ParseHandoff
// predates this file), and internal/orchestrator imports neither.
import "strings"

// The §9.6 outcome vocabulary. A run's outcome is exactly one of these —
// enforced in Go here AND by a CHECK constraint on both run tables
// (20260904233804_outcome_contract.sql), so a value that reaches the
// database has already passed both gates.
const (
	OutcomeNoChange    = "NO_CHANGE"    // Ran, nothing to do.
	OutcomeSucceeded   = "SUCCEEDED"    // Did the work, nothing further needed.
	OutcomeWorkCreated = "WORK_CREATED" // Produced or updated an issue.
	OutcomePartial     = "PARTIAL"      // Some done, some failed, no human needed yet.
	OutcomeNeedsHuman  = "NEEDS_HUMAN"  // Blocked on a decision, input or credential.
	OutcomeFailed      = "FAILED"       // Ran and failed (after retries, or reported no outcome).
	OutcomeCancelled   = "CANCELLED"    // Stopped by a human or superseded.
)

// AllOutcomes is the ordered, closed vocabulary — the single source of
// truth the CHECK constraint mirrors and ValidOutcome validates against.
var AllOutcomes = []string{
	OutcomeNoChange, OutcomeSucceeded, OutcomeWorkCreated, OutcomePartial,
	OutcomeNeedsHuman, OutcomeFailed, OutcomeCancelled,
}

var validOutcomes = func() map[string]bool {
	m := make(map[string]bool, len(AllOutcomes))
	for _, o := range AllOutcomes {
		m[o] = true
	}
	return m
}()

// ValidOutcome reports whether v is one of the seven §9.6 values, exactly
// as spelled (upper snake case) — no case-folding here, because storing
// what the model actually wrote (once normalized) is what NormalizeOutcome
// is for; this is the final gate before a value is trusted as authoritative.
func ValidOutcome(v string) bool { return validOutcomes[v] }

// NormalizeOutcome trims and upper-cases a raw hand-off value and reports
// whether the result is one of the seven valid outcomes. A model that
// writes "needs_human" or " Needs_Human " still routes correctly; anything
// else (empty, a typo, prose) is reported invalid so the caller falls back
// to DeriveOutcome's default rather than storing garbage into a CHECK'd
// column.
func NormalizeOutcome(raw string) (string, bool) {
	v := strings.ToUpper(strings.TrimSpace(raw))
	return v, ValidOutcome(v)
}

// ReportedOutcome extracts whatever outcome value the agent's structured
// hand-off reported for this run's result text, checking BOTH existing
// hand-off shapes — CHECKPOINT (session-bearing runs, §11.3/B5) and HANDOFF
// (mission-task runs, §9.5's precedent) — rather than inventing a third
// block. finishAssignment services both run shapes through one function, so
// this does too. Returns "" if neither block was present or neither
// carried an outcome line; the caller (DeriveOutcome) treats that as "not
// reported".
//
// CHECKPOINT is checked first: it is the block session-bearing runs (the
// mention → wake → session loop this whole PRD is about) are instructed to
// emit, and it is a strict superset of what a HANDOFF-shaped result could
// also parse as (both end in a "---END ...---" marker; a false-positive
// HANDOFF match inside checkpoint prose is not a concern because ParseHandoff
// requires its OWN literal markers).
func ReportedOutcome(resultText string) string {
	if cp := ParseCheckpoint(resultText); cp.Outcome != "" {
		return cp.Outcome
	}
	if hd := parseHandoff(resultText); hd.Outcome != "" {
		return hd.Outcome
	}
	return ""
}

// ReasonNoOutcomeReported is the stated reason §9.6 requires when a run
// ends without a recognised outcome ("A run ending without one is FAILED
// with outcome_reason='no outcome reported'"). Rev 3 dropped the dedicated
// outcome_reason column (§9.6, §9.4) in favor of reusing the existing
// error_message column for exactly this string — see DeriveOutcome.
const ReasonNoOutcomeReported = "no outcome reported"

// DeriveOutcome computes the §9.6 outcome for a terminal run from its
// TECHNICAL status and whatever the agent's structured hand-off reported.
// This is the ONE place outcome is decided — finishAssignment and the
// pipeline executor both call it rather than each growing their own
// judgment call.
//
// status is the run's own technical status, case-insensitively one of
// "completed" | "failed" | "cancelled" (assignments spells these upper
// case; pipeline_runs spells them lower case — both normalize the same
// way here). reported is whatever ReportedOutcome extracted from the
// result text ("" if nothing was found).
//
//   - CANCELLED is never something a model self-reports (Tier 1 stop wins
//     over anything the run said, exactly as finishAssignment's own
//     cancel_requested_at check does) — a cancelled run's outcome is
//     CANCELLED, full stop.
//   - FAILED (the execution itself errored, timed out, or crashed) is
//     likewise not overridable by the model's own hand-off: a run that
//     crashed cannot be trusted to self-report NEEDS_HUMAN or PARTIAL, and
//     its error_message already carries the real reason.
//   - Anything else (a clean, technical completion) defers to the
//     hand-off: a recognised value is trusted verbatim; nothing
//     recognised defaults to FAILED with ReasonNoOutcomeReported — an
//     absent outcome is a bug, not a silent success (§9.6).
//
// Returns the outcome to store, and — only when it defaulted because
// nothing valid was reported — the reason string to write into the run's
// error_message column IF that column is otherwise empty (see callers: a
// technically-failed run already has a real error_message and must not
// have it overwritten with this generic reason).
func DeriveOutcome(status, reported string) (outcome string, defaultedReason string) {
	switch strings.ToUpper(strings.TrimSpace(status)) {
	case "CANCELLED":
		return OutcomeCancelled, ""
	case "FAILED":
		return OutcomeFailed, ""
	default:
		if v, ok := NormalizeOutcome(reported); ok {
			return v, ""
		}
		return OutcomeFailed, ReasonNoOutcomeReported
	}
}

// OutcomeRoute is one row of the §9.6 routing table — what happens next
// for a run that ended with a given outcome. A single lookup, not a
// decision each consumer re-derives, is the point of implementing this
// "once" (§17 B6's own wording).
type OutcomeRoute struct {
	// CreatesInboxItem is true only for NEEDS_HUMAN (§12: "NO_CHANGE and
	// SUCCEEDED never create an item"). FAILED's "inbox once retries are
	// exhausted" routing needs retry-count tracking this package does not
	// yet have (deferred like B4 deferred awaiting_input — see
	// issue_session_state.go) and is intentionally NOT set here; wiring it
	// is a follow-up once retries are counted anywhere.
	CreatesInboxItem bool
	// SessionState is the issue_agent_sessions state a session-bearing run
	// with this outcome settles to (§10.1). Every outcome maps to exactly
	// one, so settleSessionForAssignment (B4) can replace its own
	// status-only switch with this table without changing the two cases
	// it already got right (FAILED -> error, everything else -> idle) and
	// ADDING the one B4 could not reach (NEEDS_HUMAN -> awaiting_input).
	SessionState string
	// DigestEligible marks outcomes the B10 digest scheduler should be
	// able to roll up (§9.6: "History only. Digest-eligible."). Not wired
	// to a scheduler here (B10) — recorded so the table is a complete
	// statement of §9.6, not a partial one that silently drops a column.
	DigestEligible bool
	// IssueComment marks outcomes that get a comment on the issue in
	// addition to history (§9.6: WORK_CREATED, PARTIAL). This is
	// DOCUMENTATION of existing behaviour (finishAssignment already posts
	// a mission comment for any non-empty, non-error result — see the
	// HANDOFF-parsing block there) rather than a new effect B6 adds.
	IssueComment bool
}

// outcomeRoutes is the §9.6 table, spelled out once.
var outcomeRoutes = map[string]OutcomeRoute{
	OutcomeNoChange:    {SessionState: "idle", DigestEligible: true},
	OutcomeSucceeded:   {SessionState: "idle", DigestEligible: true},
	OutcomeWorkCreated: {SessionState: "idle", IssueComment: true},
	OutcomePartial:     {SessionState: "idle", IssueComment: true},
	OutcomeNeedsHuman:  {SessionState: "awaiting_input", CreatesInboxItem: true},
	OutcomeFailed:      {SessionState: "error"},
	OutcomeCancelled:   {SessionState: "idle"},
}

// RouteForOutcome resolves outcome's routing row. An unrecognised outcome
// (should be unreachable past the CHECK constraint and ValidOutcome) routes
// as FAILED's row — the same fail-closed posture DeriveOutcome takes for a
// missing outcome, rather than panicking or silently creating no session
// transition at all.
func RouteForOutcome(outcome string) OutcomeRoute {
	if r, ok := outcomeRoutes[outcome]; ok {
		return r
	}
	return outcomeRoutes[OutcomeFailed]
}
