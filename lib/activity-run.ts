// One run, as the Activity page tells it (#2979).
//
// The page a rail row opens is the same record /routines shows — the step
// spine, the approval, the result — with what Activity adds around it: what
// started the run, what it caused further down the chain, what it is linked
// to, and how its routine's previous runs went. The judgements behind those
// pieces live here, where they can be wrong in a test instead of on a screen.

import { readableOriginSource, routineRunOrigin } from "@/lib/routine-run-provenance"
import { roleAtLeast } from "@/lib/routine-governance"

/** The rail's five words, so a run reads the same in the list and on its page. */
export type RunTone = "running" | "waiting" | "failed" | "done" | "stopped"

export const RUN_TONE_LABEL: Record<RunTone, string> = {
  running: "Running",
  waiting: "Waiting for you",
  failed: "Could not finish",
  done: "Completed",
  stopped: "Stopped",
}

/** Text colour token per tone, matching the rail's status rows. */
export const RUN_TONE_CLASS: Record<RunTone, string> = {
  running: "text-primary",
  waiting: "text-warn",
  failed: "text-destructive",
  done: "text-success",
  stopped: "text-muted-foreground",
}

export const RUN_TONE_DOT: Record<RunTone, string> = {
  running: "bg-primary",
  waiting: "bg-warn",
  failed: "bg-destructive",
  done: "bg-success",
  stopped: "bg-muted-foreground",
}

export function runTone(status: string | null | undefined): RunTone {
  const s = (status ?? "").toLowerCase()
  if (s === "running" || s === "queued" || s === "pending") return "running"
  if (s === "waiting" || s === "paused") return "waiting"
  if (s === "failed" || s === "error") return "failed"
  if (s === "completed" || s === "succeeded" || s === "done") return "done"
  return "stopped"
}

/**
 * The status word for ONE run. The rail folds cancelled and interrupted into
 * "Stopped" because a chain may hold both; a single run's page says which:
 * somebody stopped it, or the process running it died (#2981).
 */
export function runStatusLabel(status: string | null | undefined): string {
  const s = (status ?? "").toLowerCase()
  if (s === "cancelled" || s === "canceled") return "Cancelled"
  if (s === "interrupted") return "Interrupted"
  return RUN_TONE_LABEL[runTone(s)]
}

const ACTIVE = new Set(["running", "queued", "waiting", "paused"])

export function isActiveRun(status: string | null | undefined): boolean {
  return ACTIVE.has((status ?? "").toLowerCase())
}

const TRIGGER_WORDS: Record<string, string> = {
  schedule: "Schedule",
  webhook: "Webhook",
  manual: "Started by hand",
  call_pipeline: "Called by another routine",
  issue: "From an issue",
  automation: "Automation",
}

/**
 * What started the run, in words. The automation's name is worth printing; a
 * schedule or webhook id is not — it says nothing to a reader and leaks a key.
 */
export function triggerPhrase(run: { triggered_via?: string; triggered_by_id?: string; metadata?: unknown }): string {
  if (!run.triggered_via) return "Unknown trigger"
  const origin = routineRunOrigin(run)
  const word =
    TRIGGER_WORDS[origin.label] ??
    origin.label.charAt(0).toUpperCase() + origin.label.slice(1).replace(/_/g, " ")
  const source = origin.label === "automation" ? readableOriginSource(origin.source) : undefined
  return source ? `${word} · ${source}` : word
}

const FINISHED_STEP = new Set(["completed", "succeeded", "skipped"])

/** "2 of 4" — finished steps out of the recipe's; null when the recipe is unknown. */
export function stepProgress(
  steps: { id: string }[] | undefined,
  byStep: Map<string, { latest: { status: string } }>,
): { done: number; total: number } | null {
  if (!steps || steps.length === 0) return null
  const done = steps.filter((s) => FINISHED_STEP.has((byStep.get(s.id)?.latest.status ?? "").toLowerCase())).length
  return { done, total: steps.length }
}

/**
 * Which actions the header offers. They mirror the server's gates — replay is
 * MANAGER+, cancel is OWNER/ADMIN — so a button never promises what the API
 * will refuse.
 */
export function runActions(status: string | null | undefined, role: string | null | undefined) {
  const active = isActiveRun(status)
  return {
    retry: !active && roleAtLeast(role, "MANAGER"),
    stop: active && roleAtLeast(role, "ADMIN"),
  }
}

/* ------------------------------------------------------------------ *
 *  What the run caused
 * ------------------------------------------------------------------ */

export interface BranchGraphNode {
  id: string
  kind: string
  ref: string
  label: string
  status?: string
  duration_ms?: number
  chain_origin?: string
}

export interface BranchGraph {
  nodes: BranchGraphNode[]
  edges: { from: string; to: string; kind: string }[]
}

export interface RunTreeNode {
  id: string
  kind: string
  ref: string
  label: string
  status?: string
  durationMs?: number
  children: RunTreeNode[]
}

// Edges that point from a cause to what it caused. "runs" is the routine
// definition owning a run and "relates" an author's link — neither is work this
// run did, and following "runs" upward would list the routine's whole history.
const CAUSAL = new Set(["triggers", "produces", "executes"])

/**
 * The work beneath one run: agent assignments, inbox asks, and the runs of the
 * routines it called — nested the way they caused each other.
 *
 * A called routine appears in the walk as its definition node with ALL its runs
 * hanging off it, so the child is the run of it that belongs to this chain
 * (chain_origin), named after the routine rather than its slug.
 */
export function chainBranches(graph: BranchGraph, originRunRef: string): RunTreeNode[] {
  const byId = new Map(graph.nodes.map((n) => [n.id, n]))
  const out = new Map<string, { to: string; kind: string }[]>()
  for (const e of graph.edges) {
    const list = out.get(e.from) ?? []
    list.push({ to: e.to, kind: e.kind })
    out.set(e.from, list)
  }
  const rootId = `run:${originRunRef}`
  const seen = new Set<string>([rootId])

  const childrenOf = (id: string): RunTreeNode[] => {
    const result: RunTreeNode[] = []
    for (const e of out.get(id) ?? []) {
      if (!CAUSAL.has(e.kind) || seen.has(e.to)) continue
      const n = byId.get(e.to)
      if (!n) continue
      if (n.kind === "routine") {
        seen.add(n.id)
        for (const r of out.get(n.id) ?? []) {
          const run = byId.get(r.to)
          if (r.kind !== "runs" || !run || seen.has(run.id)) continue
          if (run.chain_origin && run.chain_origin !== originRunRef) continue
          seen.add(run.id)
          result.push(toTree(run, n.label))
        }
        continue
      }
      if (n.kind === "agent" || n.kind === "issue") continue
      seen.add(n.id)
      result.push(toTree(n))
    }
    return result
  }

  const toTree = (n: BranchGraphNode, label?: string): RunTreeNode => ({
    id: n.id,
    kind: n.kind,
    ref: n.ref,
    label: label || n.label || n.ref,
    status: n.status,
    durationMs: typeof n.duration_ms === "number" ? n.duration_ms : undefined,
    children: childrenOf(n.id),
  })

  return childrenOf(rootId)
}

/**
 * The issues a run created: the `produces` edges from the run to an issue
 * (missions.author_run_id, #2986). Read off the walk, so no five-ref cap
 * applies, and an issue that was only the run's input is never counted.
 */
export function issuesCreatedBy(graph: BranchGraph, runRef: string): { id: string; label: string }[] {
  const from = `run:${runRef}`
  const byId = new Map(graph.nodes.map((n) => [n.id, n]))
  const out: { id: string; label: string }[] = []
  for (const e of graph.edges) {
    if (e.from !== from || e.kind !== "produces") continue
    const n = byId.get(e.to)
    if (n?.kind === "issue") out.push({ id: n.ref, label: n.label || n.ref })
  }
  return out
}

/* ------------------------------------------------------------------ *
 *  Linked to
 * ------------------------------------------------------------------ */

export interface LinkedEntity {
  kind: "routine" | "issue" | "agent"
  ref: string
  label: string
}

/** The routine, issues and agents a run belongs to — each once, routine first. */
export function linkedEntities(input: {
  routine?: { slug: string; name?: string } | null
  issues?: { id: string; identifier?: string; title?: string }[]
  graphIssues?: { ref: string; label: string }[]
  agents?: { id: string; name?: string; slug?: string }[]
}): LinkedEntity[] {
  const links: LinkedEntity[] = []
  if (input.routine?.slug) {
    links.push({ kind: "routine", ref: input.routine.slug, label: input.routine.name || input.routine.slug })
  }
  const issueSeen = new Set<string>()
  for (const i of input.issues ?? []) {
    if (issueSeen.has(i.id)) continue
    issueSeen.add(i.id)
    links.push({ kind: "issue", ref: i.id, label: i.identifier || i.title || i.id })
  }
  for (const i of input.graphIssues ?? []) {
    if (issueSeen.has(i.ref)) continue
    issueSeen.add(i.ref)
    links.push({ kind: "issue", ref: i.ref, label: i.label || i.ref })
  }
  const agentSeen = new Set<string>()
  for (const a of input.agents ?? []) {
    if (agentSeen.has(a.id)) continue
    agentSeen.add(a.id)
    links.push({ kind: "agent", ref: a.id, label: a.name || a.slug || a.id })
  }
  return links
}

/* ------------------------------------------------------------------ *
 *  Previous runs
 * ------------------------------------------------------------------ */

export interface HistoryDot {
  id: string
  tone: RunTone
  startedAt: string
  current: boolean
}

/** The routine's newest runs as dots, oldest on the left, this one marked. */
export function historyStrip(
  records: { id: string; status: string; started_at: string }[],
  currentId: string,
  max = 20,
): HistoryDot[] {
  return [...records]
    .sort((a, b) => a.started_at.localeCompare(b.started_at) || a.id.localeCompare(b.id))
    .slice(-max)
    .map((r) => ({ id: r.id, tone: runTone(r.status), startedAt: r.started_at, current: r.id === currentId }))
}
