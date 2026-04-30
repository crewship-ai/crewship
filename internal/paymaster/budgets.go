package paymaster

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
)

// Check resolves every budget that applies to scope and returns the current
// status of each. "Applies" = workspace budgets always apply; crew/mission/
// agent budgets apply only when the scope identifies that level. The list is
// returned in scope-specificity order (workspace first, agent last) so the UI
// can render them as a hierarchy without resorting.
//
// An empty result is not an error — it just means the operator hasn't set
// any budgets for this scope yet, and Enforce will let the call through.
func Check(ctx context.Context, db *sql.DB, scope Scope) ([]BudgetStatus, error) {
	if db == nil {
		return nil, fmt.Errorf("paymaster: nil db")
	}
	if scope.WorkspaceID == "" {
		return nil, fmt.Errorf("paymaster: workspace_id required")
	}

	budgets, err := loadApplicableBudgets(ctx, db, scope)
	if err != nil {
		return nil, err
	}

	statuses := make([]BudgetStatus, 0, len(budgets))
	now := time.Now().UTC()
	for _, b := range budgets {
		spent, err := sumSpend(ctx, db, b, scope, now)
		if err != nil {
			return nil, fmt.Errorf("paymaster: sum spend for budget %s: %w", b.ID, err)
		}
		util := 0.0
		if b.LimitUSD > 0 {
			util = (spent / b.LimitUSD) * 100.0
		}
		statuses = append(statuses, BudgetStatus{
			Budget:   b,
			SpentUSD: spent,
			LimitUSD: b.LimitUSD,
			UtilPct:  util,
			State:    deriveState(spent, b.LimitUSD, b.Mode),
		})
	}
	return statuses, nil
}

// Enforce calls Check, emits journal entries for any warn/exceeded states,
// and returns a BudgetExceededError when at least one exceeded budget is in
// hard or tiered mode. Soft-mode budgets never block; they only emit warning
// entries so dashboards light up.
//
// The journal emit is best-effort. If the journal writer is down we still
// block on hard-mode breaches — the budget call is a control-plane decision,
// not an audit-plane decision, so it can't be gated on observability.
func Enforce(ctx context.Context, db *sql.DB, j journal.Emitter, scope Scope) error {
	statuses, err := Check(ctx, db, scope)
	if err != nil {
		return err
	}

	var blocking []BudgetStatus
	for _, s := range statuses {
		switch s.State {
		case StateWarn:
			if j != nil {
				_, _ = j.Emit(ctx, journal.Entry{
					WorkspaceID: scope.WorkspaceID,
					CrewID:      scope.CrewID,
					AgentID:     scope.AgentID,
					MissionID:   scope.MissionID,
					Type:        journal.EntryBudgetWarning,
					Severity:    journal.SeverityWarn,
					ActorType:   journal.ActorSystem,
					Summary: fmt.Sprintf("budget warning: %s/%s at %.1f%% ($%.2f / $%.2f)",
						s.Budget.ScopeKind, s.Budget.Window, s.UtilPct, s.SpentUSD, s.Budget.LimitUSD),
					Payload: budgetPayload(s),
				})
			}
		case StateExceeded:
			// Soft budgets surface as exceeded but never block; emit a warning
			// (not an error) so operators see something but the agent runs on.
			if s.Budget.Mode == ModeSoft {
				if j != nil {
					_, _ = j.Emit(ctx, journal.Entry{
						WorkspaceID: scope.WorkspaceID,
						CrewID:      scope.CrewID,
						AgentID:     scope.AgentID,
						MissionID:   scope.MissionID,
						Type:        journal.EntryBudgetWarning,
						Severity:    journal.SeverityWarn,
						ActorType:   journal.ActorSystem,
						Summary: fmt.Sprintf("soft budget over limit: %s/%s at %.1f%% ($%.2f / $%.2f)",
							s.Budget.ScopeKind, s.Budget.Window, s.UtilPct, s.SpentUSD, s.Budget.LimitUSD),
						Payload: budgetPayload(s),
					})
				}
				continue
			}
			blocking = append(blocking, s)
			if j != nil {
				_, _ = j.Emit(ctx, journal.Entry{
					WorkspaceID: scope.WorkspaceID,
					CrewID:      scope.CrewID,
					AgentID:     scope.AgentID,
					MissionID:   scope.MissionID,
					Type:        journal.EntryBudgetExceed,
					Severity:    journal.SeverityError,
					ActorType:   journal.ActorSystem,
					Summary: fmt.Sprintf("budget exceeded: %s/%s at %.1f%% ($%.2f / $%.2f)",
						s.Budget.ScopeKind, s.Budget.Window, s.UtilPct, s.SpentUSD, s.Budget.LimitUSD),
					Payload: budgetPayload(s),
				})
			}
		}
	}

	if len(blocking) > 0 {
		return &BudgetExceededError{Statuses: blocking}
	}
	return nil
}

// quotaWarnThreshold is the remaining-quota fraction at which we emit a
// budget.warning entry. Mirror of warnThreshold for the $-side budgets.
// 0.20 means "warn when ≤20% of the rate-limit window is left".
const quotaWarnThreshold = 0.20

// EnforceQuota reacts to a provider rate-limit signal that arrived with the
// upstream response. It is the quota-side analogue of Enforce: where Enforce
// looks at $ spent vs budget, this looks at "remaining quota" vs warn /
// exhausted thresholds derived from the rate-limit headers (Anthropic
// anthropic-ratelimit-* / OpenAI x-ratelimit-*).
//
// hadStatus429 indicates the upstream returned a 429 — that's the
// authoritative "you're out". remainingPct (0.0–1.0) is the smaller of the
// "tokens remaining" / "requests remaining" fractions reported in headers.
// window names which axis we sampled (display only).
//
// Effects:
//   - 429: emit budget.exceeded with reason='quota_exhausted', return a
//     *BudgetExceededError so callers fail closed (same shape as the $-side
//     so existing error handling keeps working). The Statuses slice is
//     synthetic (no row in budget_limits) but carries the window + util
//     fields so the UI shows something meaningful.
//   - remainingPct < 0.20: emit budget.warning, return nil (don't block).
//   - otherwise: no-op.
//
// Emitter j may be nil — same convention as Enforce. This function does NOT
// touch the database; the signal is per-call and ephemeral, so persisting
// it would just inflate the journal.
func EnforceQuota(ctx context.Context, j journal.Emitter, scope Scope, remainingPct float64, window QuotaWindow, hadStatus429 bool) error {
	if hadStatus429 {
		if j != nil {
			_, _ = j.Emit(ctx, journal.Entry{
				WorkspaceID: scope.WorkspaceID,
				CrewID:      scope.CrewID,
				AgentID:     scope.AgentID,
				MissionID:   scope.MissionID,
				Type:        journal.EntryBudgetExceed,
				Severity:    journal.SeverityError,
				ActorType:   journal.ActorSystem,
				Summary:     fmt.Sprintf("provider quota exhausted (window=%s) — back off and retry", windowOrUnknown(window)),
				Payload: map[string]any{
					"reason":              "quota_exhausted",
					"quota_window":        string(window),
					"quota_remaining_pct": 0.0,
					"http_status":         429,
				},
			})
		}
		return &BudgetExceededError{Statuses: []BudgetStatus{{
			Budget: Budget{
				ScopeKind: ScopeWorkspace,
				ScopeID:   scope.WorkspaceID,
				Window:    BudgetWindow(window),
				Mode:      ModeHard,
			},
			UtilPct: 100.0,
			State:   StateExceeded,
		}}}
	}

	// Header missing or full quota — nothing to say.
	if remainingPct <= 0 || remainingPct >= 1 {
		return nil
	}

	if remainingPct >= quotaWarnThreshold {
		return nil
	}

	if j != nil {
		_, _ = j.Emit(ctx, journal.Entry{
			WorkspaceID: scope.WorkspaceID,
			CrewID:      scope.CrewID,
			AgentID:     scope.AgentID,
			MissionID:   scope.MissionID,
			Type:        journal.EntryBudgetWarning,
			Severity:    journal.SeverityWarn,
			ActorType:   journal.ActorSystem,
			Summary: fmt.Sprintf("provider quota low: %.1f%% remaining (window=%s)",
				remainingPct*100.0, windowOrUnknown(window)),
			Payload: map[string]any{
				"reason":              "quota_low",
				"quota_window":        string(window),
				"quota_remaining_pct": remainingPct,
			},
		})
	}
	return nil
}

// windowOrUnknown formats a QuotaWindow for display, substituting "unknown"
// when the upstream didn't tell us which axis the signal came from. Avoids
// rendering the empty string into a user-facing summary.
func windowOrUnknown(w QuotaWindow) string {
	if w == "" {
		return "unknown"
	}
	return string(w)
}

// budgetPayload is the shared shape we put under journal.Entry.Payload for
// budget events. Kept as a helper so warn / exceeded entries stay consistent
// and the UI can rely on stable keys.
func budgetPayload(s BudgetStatus) map[string]any {
	return map[string]any{
		"budget_id":  s.Budget.ID,
		"scope_kind": string(s.Budget.ScopeKind),
		"scope_id":   s.Budget.ScopeID,
		"window":     string(s.Budget.Window),
		"mode":       string(s.Budget.Mode),
		"spent_usd":  s.SpentUSD,
		"limit_usd":  s.Budget.LimitUSD,
		"util_pct":   s.UtilPct,
		"state":      string(s.State),
	}
}

// loadApplicableBudgets returns enabled budgets that match the scope. The
// query union-ifies the four scope kinds into a single round-trip; we'd
// rather one slightly bigger SQL than four serial queries on a path that
// runs before every LLM call. ORDER BY scope_kind keeps the result in the
// hierarchy order Check documents (workspace → agent).
func loadApplicableBudgets(ctx context.Context, db *sql.DB, scope Scope) ([]Budget, error) {
	// scopeKindOrder gives the SQL CASE its sort key — workspace=0 first.
	const q = `
SELECT id, workspace_id, scope_kind, scope_id, window, limit_usd, mode, enabled
FROM budget_limits
WHERE enabled = 1
  AND workspace_id = ?
  AND (
        (scope_kind = 'workspace' AND scope_id = ?)
     OR (scope_kind = 'crew'      AND scope_id = ? AND ? != '')
     OR (scope_kind = 'mission'   AND scope_id = ? AND ? != '')
     OR (scope_kind = 'agent'     AND scope_id = ? AND ? != '')
  )
ORDER BY CASE scope_kind
    WHEN 'workspace' THEN 0
    WHEN 'crew'      THEN 1
    WHEN 'mission'   THEN 2
    WHEN 'agent'     THEN 3
    ELSE 4
  END, window`

	rows, err := db.QueryContext(ctx, q,
		scope.WorkspaceID,
		scope.WorkspaceID,
		scope.CrewID, scope.CrewID,
		scope.MissionID, scope.MissionID,
		scope.AgentID, scope.AgentID,
	)
	if err != nil {
		return nil, fmt.Errorf("paymaster: query budgets: %w", err)
	}
	defer rows.Close()

	var out []Budget
	for rows.Next() {
		var (
			b       Budget
			enabled int
		)
		if err := rows.Scan(&b.ID, &b.WorkspaceID, &b.ScopeKind, &b.ScopeID,
			&b.Window, &b.LimitUSD, &b.Mode, &enabled); err != nil {
			return nil, fmt.Errorf("paymaster: scan budget: %w", err)
		}
		b.Enabled = enabled != 0
		out = append(out, b)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("paymaster: iterate budgets: %w", err)
	}
	return out, nil
}

// sumSpend totals cost_ledger rows that count against budget b under the
// given scope. The ledger filter mirrors the budget's scope (a crew budget
// sums everything that crew spent, agent budget only that agent's rows, and
// so on) and the time window narrows by ts. Mission window is window-less:
// it sums every row for that mission regardless of time.
func sumSpend(ctx context.Context, db *sql.DB, b Budget, scope Scope, now time.Time) (float64, error) {
	conds := []string{"workspace_id = ?"}
	args := []any{b.WorkspaceID}

	switch b.ScopeKind {
	case ScopeWorkspace:
		// no extra narrowing — workspace budget covers all spend in the workspace
	case ScopeCrew:
		conds = append(conds, "crew_id = ?")
		args = append(args, b.ScopeID)
	case ScopeMission:
		conds = append(conds, "mission_id = ?")
		args = append(args, b.ScopeID)
	case ScopeAgent:
		conds = append(conds, "agent_id = ?")
		args = append(args, b.ScopeID)
	default:
		return 0, fmt.Errorf("paymaster: unknown scope_kind %q", b.ScopeKind)
	}

	if b.Window != WindowMission {
		since, ok := windowStart(b.Window, now)
		if !ok {
			return 0, fmt.Errorf("paymaster: unknown window %q", b.Window)
		}
		conds = append(conds, "ts >= ?")
		args = append(args, since.UTC().Format(tsLayout))
	}

	q := "SELECT COALESCE(SUM(cost_usd), 0) FROM cost_ledger WHERE " + joinAnd(conds)
	var total float64
	if err := db.QueryRowContext(ctx, q, args...).Scan(&total); err != nil {
		return 0, err
	}
	return total, nil
}

// windowStart turns a budget window into the inclusive lower bound of the
// current period. Hour/day are calendar-aligned (truncated to the unit) so
// dashboards reset on the hour/midnight UTC instead of "65 minutes ago".
// Week is ISO-style starting Monday 00:00 UTC. Month starts on the 1st.
func windowStart(w BudgetWindow, now time.Time) (time.Time, bool) {
	now = now.UTC()
	switch w {
	case WindowHour:
		return now.Truncate(time.Hour), true
	case WindowDay:
		return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC), true
	case WindowWeek:
		// Go's Weekday: Sunday=0. Shift so Monday=0.
		offset := (int(now.Weekday()) + 6) % 7
		monday := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC).
			AddDate(0, 0, -offset)
		return monday, true
	case WindowMonth:
		return time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC), true
	case WindowMission:
		// Sentinel — caller should not invoke for mission window. Return zero
		// time so an accidental call still produces a deterministic SQL value.
		return time.Time{}, true
	default:
		return time.Time{}, false
	}
}

// joinAnd concatenates SQL WHERE conditions. We don't import strings just
// for this one call site — the package is otherwise heavy on strings ops in
// pricing.go but ledger/budgets stay free of it for clarity.
func joinAnd(conds []string) string {
	out := ""
	for i, c := range conds {
		if i > 0 {
			out += " AND "
		}
		out += c
	}
	return out
}
