"use client"

/**
 * The durable work ledger's data layer
 * (docs/prd/WEBHOOKS-AGENT-PARALLELISM-IMPLEMENTATION-1-0.md §9).
 *
 * Three things in here are load-bearing rather than decorative, and each of
 * them is a §9 sentence made structural:
 *
 *  1. A run stream is keyed on `run_id`, and on nothing else. §9: "two run
 *     streams must never merge on agent slug or ChatID." The existing log
 *     panels (components/features/orchestration/task-live-logs.tsx,
 *     orchestration-drawer-panels.tsx) filter by `agentSlug`, which is exactly
 *     that merge: two concurrent runs of one agent land in one stream and the
 *     reader cannot tell whose line is whose. `workRunStreams` below partitions
 *     on run_id, which is the same namespace as agent_runs.id, so two attempts
 *     of the same work — let alone two different work items on one agent —
 *     stay apart.
 *
 *  2. Push is not the only evidence of completion. Every read here has a poll
 *     backstop while anything is non-terminal, and a reconnect refetches the
 *     authoritative snapshot rather than trusting that it missed nothing.
 *
 *  3. `mergeWorkDetail` tolerates duplicate and out-of-order arrivals. It has
 *     to: internal/api/work_items.go reads the item row and its history in
 *     SEPARATE statements, on purpose, so a snapshot can legitimately carry an
 *     item row older than the events beside it, or older than what we already
 *     hold. Taking the newest of each rather than the newest response is what
 *     stops a late-arriving snapshot from walking a finished item backwards.
 */

import { useCallback, useMemo } from "react"
import { useQuery, useQueryClient } from "@tanstack/react-query"

import { apiFetch } from "@/lib/api-fetch"
import { useApiMutation } from "@/hooks/use-api-mutation"
import { useRealtimeEventSafe } from "@/hooks/use-realtime"
import type { WebhookDelivery } from "@/hooks/use-webhook-deliveries"

// ── Wire types — mirror internal/api/work_items.go exactly ──────────────────

export type WorkState =
  | "queued"
  | "starting"
  | "running"
  | "waiting"
  | "retry_wait"
  | "succeeded"
  | "failed"
  | "expired"
  | "cancelled"
  | "needs_reconciliation"

export type WorkClass = "chat" | "background"

export type WorkSource =
  | "webhook"
  | "chat"
  | "assignment"
  | "schedule"
  | "pipeline_step"
  | "manual"

/** The order internal/work/work.go declares them in. */
export const WORK_STATES: readonly WorkState[] = [
  "queued", "starting", "running", "waiting", "retry_wait",
  "succeeded", "failed", "expired", "cancelled", "needs_reconciliation",
]

export const WORK_CLASSES: readonly WorkClass[] = ["chat", "background"]

export const WORK_SOURCES: readonly WorkSource[] = [
  "webhook", "chat", "assignment", "schedule", "pipeline_step", "manual",
]

export interface WorkItem {
  id: string
  workspace_id: string
  source: WorkSource
  /** For a webhook, the delivery id — which is how the UI finds out whether
   *  the payload behind a replay still exists. */
  source_ref: string
  domain_kind: string
  domain_id: string
  agent_id: string
  crew_id: string
  session_id: string
  class: WorkClass
  authorized_by_user_id: string
  /** The input's fingerprint. The input itself is never returned. */
  input_sha256: string
  target_revision: string
  state: WorkState
  state_reason: string
  /** Bumped on every claim; the fencing term. */
  generation: number
  attempt_count: number
  priority: number
  eligible_at: string
  deadline_at: string | null
  replay_of: string | null
  replay_reason: string
  created_at: string
  updated_at: string
  terminal_at: string | null
}

export interface WorkAttempt {
  /** Same namespace as agent_runs.id — this is what joins an attempt to its
   *  journal, and the only safe key for a run stream. */
  run_id: string
  attempt: number
  generation: number
  lease_owner: string
  lease_expires_at: string
  heartbeat_at: string
  /** An operational address, not a credential. An item parked in
   *  needs_reconciliation cannot be diagnosed without it. */
  runtime_locator: string
  started_at: string
  start_reason: string
  ended_at: string | null
  end_reason: string
  exit_evidence: string
  cost_usd: number
}

export interface WorkEvent {
  seq: number
  at: string
  from_state: WorkState | ""
  to_state: WorkState | ""
  /** "" for an item-level event that belongs to no single attempt. */
  run_id: string
  generation: number
  reason: string
}

/**
 * The detail adds the attempt and event arrays to the summary.
 *
 * The summary's count is `attempt_count` and the detail's array is `attempts`,
 * deliberately two names: an earlier shape used one key for both, and
 * encoding/json resolved the collision in favour of the shallower field, so a
 * client that expected a number got an array with no warning.
 */
export type WorkItemDetail = WorkItem & {
  attempts: WorkAttempt[]
  events: WorkEvent[]
}

export interface WorkItemPage {
  items: WorkItem[]
  next_cursor: string | null
}

/** A cancel REQUEST is not a cancelled state, and the three answers are
 *  genuinely different (internal/api/work_items.go). */
export type WorkCancelOutcome = "cancelled" | "requested" | "already_terminal"

export interface WorkCancelResponse {
  id: string
  state: WorkState
  outcome: WorkCancelOutcome
  detail: string
}

// ── State semantics (internal/work/work.go) ────────────────────────────────

const TERMINAL: ReadonlySet<WorkState> = new Set<WorkState>([
  "succeeded", "failed", "expired", "cancelled",
])

/** Terminal history is never rewritten; a rerun is a new work item. */
export function isTerminalWorkState(state: WorkState): boolean {
  return TERMINAL.has(state)
}

/**
 * Whether the state occupies one of the server's execution slots.
 *
 * `needs_reconciliation` is in this set ON PURPOSE, and that is the whole
 * point of the state: a runtime MAY still be alive under a locator nobody has
 * verified. Counting it as free is how two live runtimes happen.
 */
export function holdsExecutionSlot(state: WorkState): boolean {
  return state === "starting" || state === "running" || state === "needs_reconciliation"
}

/**
 * How a state should READ, which is not the same as what it is.
 *
 * `unresolved` exists so that §9's rule has somewhere to land:
 * needs_reconciliation is neither a success nor a failure. It means a runtime
 * may still be alive and somebody has to look. Colouring it as failed loses
 * the "still burning capacity" half; colouring it as finished loses all of it.
 */
export type WorkOutcome = "waiting" | "live" | "succeeded" | "failed" | "cancelled" | "unresolved"

export function workOutcome(state: WorkState): WorkOutcome {
  switch (state) {
    case "succeeded": return "succeeded"
    case "failed":
    case "expired": return "failed"
    case "cancelled": return "cancelled"
    case "needs_reconciliation": return "unresolved"
    case "starting":
    case "running": return "live"
    default: return "waiting"
  }
}

/** The label the product uses for each state. `retry_wait` and
 *  `needs_reconciliation` are the two nobody reads correctly as raw column
 *  values. */
export const WORK_STATE_LABEL: Record<WorkState, string> = {
  queued: "Queued",
  starting: "Starting",
  running: "Running",
  waiting: "Waiting",
  retry_wait: "Retry scheduled",
  succeeded: "Succeeded",
  failed: "Failed",
  expired: "Expired",
  cancelled: "Cancelled",
  needs_reconciliation: "Needs reconciliation",
}

/** One sentence per state, for the reader who has never met this vocabulary. */
export const WORK_STATE_MEANING: Record<WorkState, string> = {
  queued: "Accepted and durable, waiting for capacity.",
  starting: "Claimed — capacity reserved, runtime not confirmed yet.",
  running: "A confirmed live runtime.",
  waiting: "Parked on a durable waitpoint or on a child. The session stays occupied, so a later turn cannot overtake it.",
  retry_wait: "A safely repeatable failure. It becomes eligible again at the time below.",
  succeeded: "Finished successfully.",
  failed: "Finished unsuccessfully.",
  expired: "An explicit deadline passed before it started.",
  cancelled: "A confirmed stop. Requesting a cancel does not reach this state on its own.",
  needs_reconciliation:
    "Not a success and not a failure. A runtime may still be alive under a locator nobody has verified, and it holds its capacity until a person or a reconciler resolves it.",
}

/** The reason a queued item is not running yet, said in words. §9 asks for the
 *  "queued reason" and the ledger's own word for it is `state_reason`; this
 *  fills the gap when the producer left it empty. */
export function queuedReason(item: Pick<WorkItem, "state" | "state_reason" | "eligible_at">, now = Date.now()): string {
  if (item.state_reason) return item.state_reason
  const eligible = new Date(item.eligible_at).getTime()
  if (Number.isFinite(eligible) && eligible > now) {
    return item.state === "retry_wait"
      ? "Backing off after a repeatable failure."
      : "Not eligible yet."
  }
  if (item.state === "queued") return "Waiting for capacity."
  return ""
}

/** The attempt count.
 *
 *  Prefer the server's own counter over the length of the attempts array: the
 *  array is the attempt ROWS, which can lag `work_items.attempts` while a claim
 *  is in flight. The length is the fallback for a shape that carries the array
 *  and no counter. */
export function attemptCount(item: WorkItem | WorkItemDetail): number {
  if (typeof item.attempt_count === "number") return item.attempt_count
  const attempts = (item as WorkItemDetail).attempts
  return Array.isArray(attempts) ? attempts.length : 0
}

/** Total cost across every attempt. A retry costs real money and the sum is
 *  what an operator is actually asking about. */
export function totalCostUSD(attempts: readonly WorkAttempt[]): number {
  return attempts.reduce((sum, a) => sum + (Number.isFinite(a.cost_usd) ? a.cost_usd : 0), 0)
}

// ── Run streams: the §9 rule, made structural ──────────────────────────────

export interface WorkRunStream {
  /** The ONLY key. Never the agent slug, never the session id. */
  runId: string
  attempt: WorkAttempt | null
  events: WorkEvent[]
}

/**
 * One stream per run_id, ordered by attempt number and then by first event.
 *
 * Two attempts of the same work carry the same agent_id and the same
 * session_id and differ only in run_id, so any grouping that reaches for the
 * agent or the conversation collapses them into one — which is the merge §9
 * forbids, and the bug the existing agentSlug-filtered log panels have.
 */
export function workRunStreams(detail: Pick<WorkItemDetail, "attempts" | "events">): WorkRunStream[] {
  const byRun = new Map<string, WorkRunStream>()
  for (const attempt of detail.attempts) {
    if (!attempt.run_id) continue
    byRun.set(attempt.run_id, { runId: attempt.run_id, attempt, events: [] })
  }
  for (const event of detail.events) {
    if (!event.run_id) continue
    const stream = byRun.get(event.run_id)
    if (stream) stream.events.push(event)
    else byRun.set(event.run_id, { runId: event.run_id, attempt: null, events: [event] })
  }
  const streams = Array.from(byRun.values())
  for (const stream of streams) stream.events.sort((a, b) => a.seq - b.seq)
  return streams.sort((a, b) => {
    const an = a.attempt?.attempt ?? Number.MAX_SAFE_INTEGER
    const bn = b.attempt?.attempt ?? Number.MAX_SAFE_INTEGER
    if (an !== bn) return an - bn
    return (a.events[0]?.seq ?? 0) - (b.events[0]?.seq ?? 0)
  })
}

/** Events that belong to the work item rather than to any one attempt —
 *  acceptance, a cancel request, an expiry. */
export function ledgerEvents(detail: Pick<WorkItemDetail, "events">): WorkEvent[] {
  return detail.events.filter((e) => !e.run_id).sort((a, b) => a.seq - b.seq)
}

// ── Duplicate / out-of-order tolerance ─────────────────────────────────────

/** Events deduped by seq and ordered by it. The append-only sequence is the
 *  ledger's own ordering, so a push that arrives twice, or late, is not new
 *  information — it is the same information again. */
export function mergeWorkEvents(
  previous: readonly WorkEvent[] = [],
  incoming: readonly WorkEvent[] = [],
): WorkEvent[] {
  const bySeq = new Map<number, WorkEvent>()
  for (const event of previous) bySeq.set(event.seq, event)
  // The later read wins for a seq we already hold: same seq, same row, and
  // taking the newer copy costs nothing while taking the older one could
  // resurrect a value the server has since corrected.
  for (const event of incoming) bySeq.set(event.seq, event)
  return Array.from(bySeq.values()).sort((a, b) => a.seq - b.seq)
}

/** Attempts merged by run_id, higher generation winning, an ended attempt
 *  beating an open one at equal generation. */
export function mergeWorkAttempts(
  previous: readonly WorkAttempt[] = [],
  incoming: readonly WorkAttempt[] = [],
): WorkAttempt[] {
  const byRun = new Map<string, WorkAttempt>()
  for (const attempt of previous) byRun.set(attempt.run_id, attempt)
  for (const attempt of incoming) {
    const held = byRun.get(attempt.run_id)
    if (!held || isNewerAttempt(attempt, held)) byRun.set(attempt.run_id, attempt)
  }
  return Array.from(byRun.values()).sort((a, b) => a.attempt - b.attempt)
}

function isNewerAttempt(candidate: WorkAttempt, held: WorkAttempt): boolean {
  if (candidate.generation !== held.generation) return candidate.generation > held.generation
  if (Boolean(candidate.ended_at) !== Boolean(held.ended_at)) return Boolean(candidate.ended_at)
  return (candidate.heartbeat_at ?? "") >= (held.heartbeat_at ?? "")
}

/**
 * Whether `candidate` is a later reading of the same work item than `held`.
 *
 * generation first — it is the fencing term and it only ever increases — then
 * updated_at, and a terminal row beats a non-terminal one at equal generation,
 * because terminal history is never rewritten.
 */
export function isNewerWorkRow(
  candidate: Pick<WorkItem, "generation" | "updated_at" | "state">,
  held: Pick<WorkItem, "generation" | "updated_at" | "state">,
): boolean {
  if (candidate.generation !== held.generation) return candidate.generation > held.generation
  const candidateTerminal = isTerminalWorkState(candidate.state)
  const heldTerminal = isTerminalWorkState(held.state)
  if (candidateTerminal !== heldTerminal) return candidateTerminal
  return (candidate.updated_at ?? "") >= (held.updated_at ?? "")
}

/**
 * Fold a freshly-read snapshot into what we already hold.
 *
 * The three §9 tolerances live here. A duplicate response changes nothing; an
 * out-of-order one contributes its events without dragging the item row
 * backwards; and a snapshot whose item row is older than its own history —
 * which internal/api/work_items.go can genuinely produce, because it reads the
 * row and the events in separate statements — keeps the newer row.
 */
export function mergeWorkDetail(
  previous: WorkItemDetail | undefined,
  incoming: WorkItemDetail,
): WorkItemDetail {
  // A DIFFERENT work item is not a later reading of this one. Merging across
  // ids is the same conflation §9 forbids one level up, and it is how a
  // replay ends up wearing the original's history: the panel is reused, the
  // cache entry is not, and the incoming snapshot is simply the truth.
  if (!previous || previous.id !== incoming.id) {
    return {
      ...incoming,
      attempts: mergeWorkAttempts([], incoming.attempts),
      events: mergeWorkEvents([], incoming.events),
    }
  }
  const events = mergeWorkEvents(previous.events, incoming.events)
  const attempts = mergeWorkAttempts(previous.attempts, incoming.attempts)
  const row = isNewerWorkRow(incoming, previous) ? incoming : previous
  return { ...row, attempts, events }
}

// ── Query keys ─────────────────────────────────────────────────────────────

export interface WorkItemFilters {
  state?: WorkState | null
  class?: WorkClass | null
  source?: WorkSource | null
  agentId?: string | null
  after?: string | null
}

/** The filter set as the server names it, absent filters ABSENT — so `{}` and
 *  `{state: null}` share one cache entry. */
export function workQueryParams(filters: WorkItemFilters): Record<string, string> {
  const params: Record<string, string> = {}
  if (filters.state) params.state = filters.state
  if (filters.class) params.class = filters.class
  if (filters.source) params.source = filters.source
  if (filters.agentId) params.agent_id = filters.agentId
  if (filters.after) params.after = filters.after
  return params
}

/** `[resource, workspaceId, params?]` — CONTRIBUTING.md, and the same shape
 *  `pagesKeys` / `inboxKeys` / `poolKeys` use. The workspace id is IN the key,
 *  which is what keeps two workspaces from sharing a cache entry. */
export const workItemKeys = {
  all: (workspaceId: string) => ["work-items", workspaceId] as const,
  list: (workspaceId: string, filters: WorkItemFilters = {}) =>
    ["work-items", workspaceId, { view: "list", ...workQueryParams(filters) }] as const,
  detail: (workspaceId: string, workItemId: string) =>
    ["work-items", workspaceId, { id: workItemId }] as const,
}

// ── Transport ──────────────────────────────────────────────────────────────

export class WorkRequestError extends Error {
  readonly status: number
  constructor(status: number, message: string) {
    super(message)
    this.name = "WorkRequestError"
    this.status = status
  }
}

async function fetchWork<T>(url: string, signal: AbortSignal | undefined, what: string): Promise<T> {
  const res = await apiFetch(url, { signal })
  if (!res.ok) {
    const body = (await res.json().catch(() => null)) as { error?: string } | null
    throw new WorkRequestError(res.status, body?.error ?? `${what} (HTTP ${res.status})`)
  }
  return (await res.json()) as T
}

function base(workspaceId: string): string {
  return `/api/v1/workspaces/${encodeURIComponent(workspaceId)}/work-items`
}

/** How often a live ledger is re-read when nothing has pushed.
 *
 *  §9: push is not the only evidence of completion. Nothing in internal/
 *  broadcasts a `work.*` event today, so without this the screen would only
 *  ever move when an unrelated `run.*` happened to fire. */
export const WORK_POLL_MS = 10_000

// ── Realtime ───────────────────────────────────────────────────────────────

/**
 * Invalidation is the consumer's job (hooks/use-realtime.tsx:545-573).
 *
 * `realtime.reconnected` is the §9 requirement: the provider dispatches it
 * after the socket comes BACK, every push during the gap is gone, and the only
 * correct response is to re-read the authoritative snapshot rather than to
 * assume nothing was missed.
 *
 * `run.*` carries a `run_id` in the same namespace as an attempt's, so it is a
 * real signal about this ledger — but it is only ever a hint to re-read. The
 * server row is the evidence, never the push.
 */
function useWorkRealtime(workspaceId: string | null | undefined): void {
  const qc = useQueryClient()
  const invalidate = useCallback(() => {
    if (!workspaceId) return
    qc.invalidateQueries({ queryKey: workItemKeys.all(workspaceId) })
  }, [qc, workspaceId])

  useRealtimeEventSafe("realtime.reconnected", invalidate)
  useRealtimeEventSafe("run.started", invalidate)
  useRealtimeEventSafe("run.completed", invalidate)
  useRealtimeEventSafe("run.failed", invalidate)
}

// ── Hooks ──────────────────────────────────────────────────────────────────

export function useWorkItems(
  workspaceId: string | null | undefined,
  filters: WorkItemFilters = {},
) {
  useWorkRealtime(workspaceId)
  const params = workQueryParams(filters)
  const query = useQuery({
    queryKey: workItemKeys.list(workspaceId ?? "", filters),
    enabled: Boolean(workspaceId),
    queryFn: ({ signal }) => {
      const qs = new URLSearchParams(params).toString()
      return fetchWork<WorkItemPage>(
        `${base(workspaceId as string)}${qs ? `?${qs}` : ""}`,
        signal,
        "Could not read the work ledger",
      )
    },
    refetchInterval: (query) =>
      query.state.data?.items.some((item) => !isTerminalWorkState(item.state)) ? WORK_POLL_MS : false,
  })
  return {
    items: query.data?.items ?? [],
    nextCursor: query.data?.next_cursor ?? null,
    loading: query.isPending && Boolean(workspaceId),
    error: query.error as WorkRequestError | null,
    refetch: query.refetch,
  }
}

export function useWorkItem(
  workspaceId: string | null | undefined,
  workItemId: string | null | undefined,
) {
  useWorkRealtime(workspaceId)
  const qc = useQueryClient()
  const key = useMemo(
    () => workItemKeys.detail(workspaceId ?? "", workItemId ?? ""),
    [workspaceId, workItemId],
  )
  const query = useQuery({
    queryKey: key,
    enabled: Boolean(workspaceId) && Boolean(workItemId),
    retry: false,
    queryFn: async ({ signal }) => {
      const incoming = await fetchWork<WorkItemDetail>(
        `${base(workspaceId as string)}/${encodeURIComponent(workItemId as string)}`,
        signal,
        "Could not read the work item",
      )
      // Merge rather than replace. A snapshot that lost a race — to another
      // tab's refetch, to a poll already in flight — must not walk the view
      // backwards, and its events are still worth having.
      return mergeWorkDetail(qc.getQueryData<WorkItemDetail>(key), incoming)
    },
    refetchInterval: (query) =>
      query.state.data && !isTerminalWorkState(query.state.data.state) ? WORK_POLL_MS : false,
  })
  const error = query.error as WorkRequestError | null
  return {
    item: query.data ?? null,
    loading: query.isPending && Boolean(workspaceId) && Boolean(workItemId),
    notFound: error?.status === 404,
    error: error?.status === 404 ? null : error,
    refetch: query.refetch,
  }
}

/**
 * Cancel targets a run/work — never an agent slug. §4's three outcomes come
 * back verbatim in `outcome` and the caller must not flatten them: `requested`
 * is not `cancelled`, and a UI that says "cancelled" over a container still
 * burning tokens is the failure this distinction exists to prevent.
 */
export function useCancelWorkItem(
  workspaceId: string | null | undefined,
  options: { onSettled?: (response: WorkCancelResponse) => void; onError?: (error: unknown) => void } = {},
) {
  return useApiMutation<{ workItemId: string }, WorkCancelResponse>({
    request: ({ workItemId }) => ({
      input: `${base(workspaceId ?? "")}/${encodeURIComponent(workItemId)}/cancel`,
      init: { method: "POST" },
    }),
    invalidateKeys: workspaceId ? [workItemKeys.all(workspaceId)] : [],
    onOk: (data) => options.onSettled?.(data),
    onAccepted: (data) => options.onSettled?.(data),
    onError: (error) => options.onError?.(error),
  })
}

/**
 * Replay mints NEW work. It is not a retry: a retry keeps the work id and the
 * queue does it on its own, while a replay carries the CURRENT caller's
 * authorization and says so in the ledger via replay_of.
 *
 * `target_revision` is omitted unless the caller set it, because §4 requires
 * running against a different revision to be explicit — an absent field
 * inherits the original and never guesses "latest".
 */
export function useReplayWorkItem(
  workspaceId: string | null | undefined,
  options: { onCreated?: (item: WorkItem) => void; onError?: (error: unknown) => void } = {},
) {
  return useApiMutation<{ workItemId: string; reason: string; targetRevision?: string }, WorkItem>({
    request: ({ workItemId, reason, targetRevision }) => ({
      input: `${base(workspaceId ?? "")}/${encodeURIComponent(workItemId)}/replay`,
      init: {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(
          targetRevision && targetRevision.trim()
            ? { reason, target_revision: targetRevision.trim() }
            : { reason },
        ),
      },
    }),
    invalidateKeys: workspaceId ? [workItemKeys.all(workspaceId)] : [],
    onOk: (data) => options.onCreated?.(data),
    onAccepted: (data) => options.onCreated?.(data),
    onError: (error) => options.onError?.(error),
  })
}

// ── Replay availability ────────────────────────────────────────────────────

export type ReplayAvailabilityState = "available" | "unavailable" | "checking"

export interface ReplayAvailability {
  state: ReplayAvailabilityState
  /** Always a sentence for the person reading it, never a status code. */
  reason: string
}

/**
 * Whether the replay button should exist at all, decided BEFORE it is offered.
 *
 * §9: "if the payload has passed retention, replay is unavailable and the UI
 * must say why rather than offering a dead button." The server answers 409
 * with the same reasoning (internal/api/work_items.go replayAvailability), but
 * a 409 arrives after the click — too late to be the answer to "may I".
 *
 * The delivery row outlives the payload, which is what makes "we received it,
 * we can no longer replay it" a distinguishable answer from "we never saw it".
 */
export function replayAvailability(
  item: Pick<WorkItem, "state" | "source" | "source_ref">,
  delivery: { delivery: WebhookDelivery | null; loading: boolean; notFound: boolean },
): ReplayAvailability {
  if (!isTerminalWorkState(item.state)) {
    return {
      state: "unavailable",
      reason: `This work is still ${WORK_STATE_LABEL[item.state].toLowerCase()}. Replay creates a second run of the same input and is only available once the original has finished. Cancel it first if it is stuck.`,
    }
  }
  if (item.source !== "webhook" || !item.source_ref.trim()) {
    return { state: "available", reason: "" }
  }
  if (delivery.notFound) {
    return {
      state: "unavailable",
      reason: "The delivery that produced this work is no longer in the ledger, so its payload cannot be replayed. Ask the sender to deliver it again.",
    }
  }
  if (delivery.loading || !delivery.delivery) {
    return { state: "checking", reason: "Checking whether the original payload is still retained." }
  }
  if (!delivery.delivery.raw_body_available) {
    const expired = delivery.delivery.raw_body_expires_at
    return {
      state: "unavailable",
      reason:
        "The raw payload for this delivery has passed its retention window and was dropped, so a replay cannot reproduce the original input." +
        (expired ? ` It expired at ${expired}.` : "") +
        " Ask the sender to deliver it again.",
    }
  }
  return { state: "available", reason: "" }
}
