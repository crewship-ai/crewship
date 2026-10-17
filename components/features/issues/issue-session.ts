import { hasIssueAgentDelegate } from "@/lib/issue-execution"
import type { Mission } from "@/lib/types/mission"
import type { StatusTone } from "@/lib/format-status"

/**
 * Who is working on an issue, as one of the states the session block draws.
 *
 *   none            nobody: no agent delegated, nobody holds it
 *   delegated       an agent is delegated, no run has started
 *   active          the agent is working (or revising after changes)
 *   awaiting_input  the agent's last run stopped to ask a person
 *   complete        the result is in: Lead review or your acceptance
 *   error           a person must decide (Failed, or Lead review gave up)
 *   closed          Done, or cancelled / duplicate
 *   human           a person holds the work; agents cannot run
 *
 * "Stale" (an agent that stopped reporting) is not here: the issue carries no
 * heartbeat to tell a quiet agent from a stuck one, and guessing would be
 * worse than saying Working.
 */
export type SessionState = "none" | "delegated" | "active" | "awaiting_input" | "complete" | "error" | "closed" | "human"

export const SESSION_STATES: readonly SessionState[] = ["none", "delegated", "active", "awaiting_input", "complete", "error", "closed", "human"]

const CLOSED = new Set(["DONE", "COMPLETED", "CANCELLED", "DUPLICATE"])

export function isClosedStatus(status: string): boolean {
  return CLOSED.has(status)
}

export function sessionState(issue: Mission, opts: { latestOutcome?: string } = {}): SessionState {
  if (CLOSED.has(issue.status)) return "closed"
  if (issue.work_mode === "human") return "human"
  const stage = issue.execution?.stage
  if (issue.status === "FAILED" || stage === "needs_human" || stage === "failed") return "error"
  if (opts.latestOutcome === "NEEDS_HUMAN") return "awaiting_input"
  if (issue.status === "REVIEW" || stage === "reviewing" || stage === "accepted") return "complete"
  if (issue.status === "IN_PROGRESS" || stage === "working" || stage === "changes_requested") return "active"
  if (hasIssueAgentDelegate(issue)) return "delegated"
  return "none"
}

/** The chip a state wears — the same words the row and the board would use. */
export const SESSION_CHIP: Record<SessionState, { label: string; tone: StatusTone } | null> = {
  none: null,
  delegated: null,
  active: { label: "Working", tone: "blue" },
  awaiting_input: { label: "Awaiting input", tone: "warn" },
  complete: { label: "Ready for review", tone: "purple" },
  error: { label: "Needs a person", tone: "danger" },
  closed: { label: "Accepted", tone: "success" },
  human: { label: "With a person", tone: "muted" },
}

/** How many correction rounds the Lead gets before a person must decide (orchestrator/issue_execution.go). */
export const MAX_REVIEW_ROUNDS = 2
