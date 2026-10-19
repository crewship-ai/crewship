/** Server-reported state of one piece of agent work. The client never infers it from streaming or tool success. */
export type WorkRunState = "queued" | "running" | "waiting" | "proposal_ready" | "applied" | "conflicted" | "failed" | "canceled"

/** Durable content, kept separate from the run state: a run can fail after a revision was saved. */
export type SavedContent =
  | { kind: "none" }
  | { kind: "saved"; revision: number }

/** Whether what the card shows is known to be current. */
export type Freshness = "current" | "stale" | "unknown"

/** Distinct problem states (CX-06); each has its own next action. */
export type WorkProblem =
  | "revoked"
  | "disconnected"
  | "unknown_result"
  | "conflict"
  | "stopped"
  | "missing_runtime"
  | "broken_preview"

/** Whether the card's own data is loaded. "denied" is an auth refusal and drops cached content; "unavailable" is a temporary failure and does not. */
export type WorkCardLoad = "ready" | "loading" | "unavailable" | "denied"

export type WorkCardAccess =
  /** The title is a projection of the current reader's authorization. */
  | { status: "readable"; title: string; typeLabel: string }
  /** A linked item the reader may not open: no name, type, owner or thumbnail. */
  | { status: "restricted" }

export type WorkCardActionId =
  | "open" | "compare" | "cancel" | "retry"
  | "reconnect" | "refresh" | "check_result" | "change_executor" | "retry_preview" | "dismiss"

export interface WorkCardModel {
  /** Stable server id; Retry and Check result reuse it, never a new request id. */
  id: string
  runState: WorkRunState
  saved: SavedContent
  freshness: Freshness
  problem: WorkProblem | null
  load: WorkCardLoad
  access: WorkCardAccess
  executor: string
  requester: "you" | "collaborator"
  /** Server-granted capabilities; the card offers nothing the server did not grant. */
  can: { cancel: boolean; retry: boolean; compare: boolean; open: boolean }
}

export interface WorkCardAction { id: WorkCardActionId; label: string }
export interface WorkCardView {
  title: string
  /** True when no name, type, owner or state detail may be shown. Decided from the model, never from the title text. */
  restricted: boolean
  /** True while the state can still change; only then is the card a live region. */
  live: boolean
  busy: boolean
  headline: string
  runLabel: string
  contentLabel: string
  freshnessLabel: string
  detail: string | null
  actions: WorkCardAction[]
}
