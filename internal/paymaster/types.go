// Package paymaster owns cost accounting and budget enforcement for every LLM
// call the platform makes. It is the read/write side of the cost_ledger and
// budget_limits tables introduced in migration 52.
//
// The package is intentionally small and pure-data on the boundary: callers
// hand in a Call (what happened) or a Scope (where they're checking budgets),
// and the package returns either a recorded ledger row or a list of budget
// statuses. All journal emission goes through the journal.Emitter interface
// so tests can substitute a fake without spinning up the writer goroutine.
package paymaster

import (
	"fmt"
	"time"
)

// ScopeKind enumerates the four levels at which a budget can apply. Order
// matters: workspace is broadest, agent is narrowest. Enforcement walks the
// hierarchy and surfaces the most restrictive applicable budget.
type ScopeKind string

const (
	ScopeWorkspace ScopeKind = "workspace"
	ScopeCrew      ScopeKind = "crew"
	ScopeMission   ScopeKind = "mission"
	ScopeAgent     ScopeKind = "agent"
)

// EnforcementMode controls how Enforce reacts when a budget is breached.
//
//   - soft   — never blocks; Check still reports state=warn|exceeded so the UI
//     can paint it red. Used for cost-curious teams that don't want the
//     orchestrator to pull the plug.
//   - hard   — at 100% utilization Enforce returns BudgetExceededError and
//     emits budget.exceeded. No grace period.
//   - tiered — emits budget.warning at 80% (no block) and budget.exceeded at
//     100% (block). The default for newly-provisioned budgets because it
//     gives operators a chance to react before agents stop.
type EnforcementMode string

const (
	ModeSoft   EnforcementMode = "soft"
	ModeHard   EnforcementMode = "hard"
	ModeTiered EnforcementMode = "tiered"
)

// BudgetWindow is the time horizon over which spend is summed for one budget.
// "mission" is window-less and rolls up the whole mission's spend regardless
// of duration; the others are calendar-aligned (UTC) so dashboards line up
// with operator intuition.
type BudgetWindow string

const (
	WindowHour    BudgetWindow = "hour"
	WindowDay     BudgetWindow = "day"
	WindowWeek    BudgetWindow = "week"
	WindowMonth   BudgetWindow = "month"
	WindowMission BudgetWindow = "mission"
)

// BillingMode discriminates how a Call should be costed. Set on the request
// path from credential type — every workspace credential is either a metered
// API key (pay-per-token) or a flat-rate subscription token (Anthropic Max,
// Cursor Pro, ChatGPT+Codex, Google AI Pro, etc). Default is metered so any
// caller that forgets to set it still produces a costed row.
type BillingMode string

const (
	// BillingMetered is the historical path: provider returns usage, we
	// price it via the rate card, write a $ row to the ledger, and enforce
	// $ budgets. Applies to API-key credentials.
	BillingMetered BillingMode = "metered"

	// BillingFlatRate marks a call covered by a flat-fee subscription.
	// CostUSD is forced to 0 and Confidence to Unknown — the user already
	// paid the subscription, the marginal cost per token is structurally
	// zero from our perspective. $ budgets do NOT apply; quota signals
	// (rate-limit headers, 429s) drive enforcement instead.
	BillingFlatRate BillingMode = "flat_rate"
)

// CostConfidence is the provenance of the CostUSD field. Adopted from the
// Helicone observability pattern: never display a number without a badge
// telling the operator how trustworthy it is.
type CostConfidence string

const (
	// ConfidencePrecise means token counts came from the provider's response
	// usage block (Anthropic message_stop, OpenAI usage, Gemini usageMetadata)
	// and the rate card was applied at write time.
	ConfidencePrecise CostConfidence = "precise"

	// ConfidenceEstimate means we approximated tokens from request body
	// length or model defaults — typical for streaming responses where the
	// usage block isn't surfaced before the body is closed.
	ConfidenceEstimate CostConfidence = "estimate"

	// ConfidenceUnknown marks rows where no cost can be assigned at all.
	// Always paired with BillingFlatRate. Quota fields may still carry
	// signal even when cost cannot be computed.
	ConfidenceUnknown CostConfidence = "unknown"
)

// QuotaWindow names the rate-limit dimension reported by the upstream
// provider. Mirrors the four-axis structure Anthropic and OpenAI both use
// (requests + tokens, with tokens further split into input vs output).
// Empty means the response carried no rate-limit headers (Google, OAuth
// CONNECT tunnels, errors before headers were written).
type QuotaWindow string

const (
	QuotaRequestsPerMin     QuotaWindow = "requests_per_min"
	QuotaTokensPerMin       QuotaWindow = "tokens_per_min"
	QuotaInputTokensPerMin  QuotaWindow = "input_tokens_per_min"
	QuotaOutputTokensPerMin QuotaWindow = "output_tokens_per_min"
)

// BudgetState is the coarse traffic-light over a budget. Computed from
// SpentUSD/LimitUSD by deriveState; callers should not invent their own
// thresholds so the warn boundary stays consistent across UI and engine.
type BudgetState string

const (
	StateOK       BudgetState = "ok"
	StateWarn     BudgetState = "warn"
	StateExceeded BudgetState = "exceeded"
)

// warnThreshold is the utilization (fraction) at which tiered budgets emit a
// warning entry. Centralised here so both Check and Enforce stay in sync.
const warnThreshold = 0.80

// Scope is the addressing tuple for "where am I spending right now". Every
// field except WorkspaceID is optional; an empty value means "not scoped to
// this dimension". Used both as the lookup key for budget evaluation and as
// the destination tag on a recorded ledger row.
type Scope struct {
	WorkspaceID string
	CrewID      string
	AgentID     string
	MissionID   string
}

// Call is what the middleware hands to Record after an LLM round-trip. The
// fields mirror cost_ledger columns one-to-one so the writer doesn't need to
// reshape anything. Tags is freeform and persisted as JSON; expected uses are
// {"feature":"summary"} or {"retry":2}.
//
// Subscription fields (BillingMode through Confidence) are populated by the
// sidecar / middleware that knows whether the credential was an API key or
// an OAuth subscription token. Leaving them zero produces a metered row
// with confidence=estimate — the historical default before migration v60.
type Call struct {
	Scope               Scope
	Provider            string
	Model               string
	InputTokens         int64
	OutputTokens        int64
	CachedInputTokens   int64
	CacheCreationTokens int64
	CostUSD             float64
	Tags                map[string]any
	TS                  time.Time // zero ⇒ time.Now()

	// BillingMode discriminates metered vs flat-rate. Empty defaults to
	// BillingMetered for backwards-compat with pre-v60 callers.
	BillingMode BillingMode

	// Confidence labels the trustworthiness of CostUSD. Empty defaults to
	// ConfidenceEstimate. Record forces ConfidenceUnknown when BillingMode
	// is BillingFlatRate and zeroes CostUSD on the way to disk.
	Confidence CostConfidence

	// SubscriptionPlan is a free-form display label persisted on flat-rate
	// rows so the UI can show "Anthropic Max 20×" / "Cursor Ultra" without
	// joining back to the credentials table. Ignored for metered rows.
	SubscriptionPlan string

	// QuotaRemainingPct (0.0–1.0) is the live remaining-quota signal pulled
	// from the upstream's rate-limit headers (anthropic-ratelimit-* /
	// x-ratelimit-*). Zero when no signal was carried. Optional; populated
	// for metered API-key calls when the sidecar saw a header.
	QuotaRemainingPct float64

	// QuotaWindow names which rate-limit axis QuotaRemainingPct refers to
	// (requests-per-min, tokens-per-min, input/output tokens). Empty when
	// no header was returned.
	QuotaWindow QuotaWindow
}

// CostRecord is what Record returns: the assigned ledger ID plus the call
// timestamp the row was written with. Lets callers correlate a ledger row to
// the journal entry that was emitted alongside it.
type CostRecord struct {
	ID   string
	TS   time.Time
	Cost float64
}

// Budget is one row from the budget_limits table, hydrated. Code paths that
// only need to compute statuses don't need this struct directly — they go via
// Check/Enforce — but the rollup queries return it for the management UI.
type Budget struct {
	ID          string
	WorkspaceID string
	ScopeKind   ScopeKind
	ScopeID     string
	Window      BudgetWindow
	LimitUSD    float64
	Mode        EnforcementMode
	Enabled     bool
}

// BudgetStatus is the evaluated state of one budget for the current scope. The
// UI paints these as a stack; Enforce uses them to decide whether to block.
// UtilPct is 0–100+ (we deliberately don't clamp so dashboards can show
// "187%" when an out-of-mode budget is way over).
type BudgetStatus struct {
	Budget   Budget
	SpentUSD float64
	LimitUSD float64
	UtilPct  float64
	State    BudgetState
}

// BudgetExceededError is returned by Enforce when a hard-mode budget (or the
// hard tier of a tiered budget) is at or over its limit. Callers can type-
// assert with errors.As to surface the specific budget back to the operator.
type BudgetExceededError struct {
	Statuses []BudgetStatus
}

func (e *BudgetExceededError) Error() string {
	if len(e.Statuses) == 0 {
		return "paymaster: budget exceeded"
	}
	first := e.Statuses[0]
	return fmt.Sprintf(
		"paymaster: budget exceeded (%s scope=%s window=%s spent=$%.4f limit=$%.4f)",
		first.Budget.Mode, first.Budget.ScopeKind, first.Budget.Window,
		first.SpentUSD, first.Budget.LimitUSD,
	)
}

// deriveState turns a (spent, limit, mode) triple into the traffic-light. Soft
// budgets still report exceeded so the UI can paint red even though Enforce
// will not block; tiered budgets get the warn band. Hard budgets jump from
// ok to exceeded with no warn — by design, since the warn signal is what
// "tiered" exists to provide.
func deriveState(spent, limit float64, mode EnforcementMode) BudgetState {
	if limit <= 0 {
		return StateOK
	}
	util := spent / limit
	if util >= 1.0 {
		return StateExceeded
	}
	if (mode == ModeTiered || mode == ModeSoft) && util >= warnThreshold {
		return StateWarn
	}
	return StateOK
}
