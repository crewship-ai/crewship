// Build the /activity overview graph: the cross-feature chain a piece
// of work actually travels — an automation fires a routine, an issue
// binds one, the routine runs, an agent executes that run, and what
// still needs a human lands in the inbox.
//
// Rendered when no specific run is selected — the workspace-level view
// of "what is wired to what, and what is currently happening".

import { Graph as DagreGraph, layout as dagreLayout } from "@dagrejs/dagre"
import type { Edge, Node } from "@xyflow/react"
import type { Mission } from "@/lib/types/mission"
import type { Pipeline } from "@/hooks/use-pipelines"
import type { PipelineRun } from "@/hooks/use-pipeline-runs"

/**
 * Every node type this builder emits, and the canvas registers.
 *
 * The list is the contract between the two: `overviewNodeTypes` in
 * `components/features/activity/overview-nodes.tsx` is typed as a
 * `Record<OverviewNodeType, …>`, so a component registered without a
 * type here — or a type here without a width below — is a compile
 * error, and a test asserts the same at runtime.
 */
export const OVERVIEW_NODE_TYPES = [
  "overviewIssue",
  "overviewRoutine",
  "overviewRun",
  "overviewAutomation",
  "overviewAgent",
  "overviewInbox",
] as const

export type OverviewNodeType = (typeof OVERVIEW_NODE_TYPES)[number]

/**
 * Layout width per node type, in the same pixels the node component
 * renders (`w-[200px]` etc.). Dagre is handed these and returns node
 * CENTRES, so the same number has to come back out when the centre is
 * converted to React Flow's top-left `position`.
 *
 * This used to be two parallel ternaries — one for the dagre pass, one
 * for the position pass — that both ended in `: RUN_W`. A node type
 * added to only the canvas therefore ranked correctly and rendered
 * half a card off its column, with nothing to catch it. One map, read
 * by both passes, cannot drift from itself.
 *
 * Keep a value in step with the width class on its component.
 */
export const OVERVIEW_NODE_WIDTH: Record<OverviewNodeType, number> = {
  overviewIssue: 200,
  overviewRoutine: 200,
  overviewRun: 180,
  overviewAutomation: 200,
  overviewAgent: 180,
  overviewInbox: 200,
}

/** Shared layout height. Every overview card is a two-line card. */
export const OVERVIEW_NODE_HEIGHT = 64

/**
 * Edge strokes, by what the edge means.
 *
 * Values are `globals.css` tokens, never literals — the palette is
 * owned there, and a literal here is a colour that stops tracking the
 * theme the moment either changes.
 */
const EDGE_STROKE = {
  /** An issue binds a routine — a declaration, not an execution. */
  issueToRoutine: "var(--info)",
  /** An automation fires a routine on an event. */
  automationToRoutine: "var(--gold)",
  /** The routine actually ran. */
  routineToRun: "var(--muted-foreground)",
  /** That run was executed by an agent. */
  runToAgent: "var(--primary)",
  /** That run left something for a human. */
  runToInbox: "var(--warn)",
} as const

export interface OverviewGraphData {
  nodes: Node[]
  edges: Edge[]
}

export interface OverviewIssueNodeData {
  identifier: string
  title: string
  status: string
  hasRoutine: boolean
  [key: string]: unknown
}

export interface OverviewRoutineNodeData {
  slug: string
  name: string
  authoredVia?: string
  invocationCount?: number
  [key: string]: unknown
}

export interface OverviewRunNodeData {
  runId: string
  status: string
  startedAt: string
  triggeredVia?: string
  pipelineSlug: string
  isWaitpoint?: boolean
  [key: string]: unknown
}

export interface OverviewAutomationNodeData {
  automationId: string
  name: string
  /** The event that fires it, e.g. `issue.created`. */
  eventType: string
  enabled: boolean
  /** Slug of the routine it fires — the edge target. */
  routineSlug: string
  [key: string]: unknown
}

export interface OverviewAgentNodeData {
  agentId: string
  slug: string
  name: string
  crewName?: string
  /** DiceBear seed/style, so the node renders the agent's real face. */
  avatarSeed?: string
  avatarStyle?: string | null
  /** Stored render (#1297) when the agent has one. */
  avatarUrl?: string | null
  [key: string]: unknown
}

export interface OverviewInboxNodeData {
  itemId: string
  /** waitpoint | escalation | message | failed_run | … */
  kind: string
  title: string
  priority?: string
  blocking?: boolean
  [key: string]: unknown
}

// ── caller-supplied inputs ─────────────────────────────────────────
//
// Deliberately structural rather than imported from a hook: the three
// new columns come from three different endpoints, and a builder that
// insisted on the exact wire type of each would be un-callable from a
// surface that has only part of one.

/** A rule that fires a routine when an event happens. */
export interface OverviewAutomation {
  id: string
  name: string
  event_type: string
  enabled: boolean
  /** Slug of the routine it fires. An automation without one is skipped. */
  routine_slug?: string
}

/** An agent that can execute a run. */
export interface OverviewAgent {
  id: string
  slug: string
  name: string
  crew_name?: string
  avatar_seed?: string
  avatar_style?: string | null
  avatar_url?: string | null
}

/** An item parked in the inbox waiting on a human. */
export interface OverviewInboxItem {
  id: string
  kind: string
  title: string
  /** The run that produced it. An item without one is skipped. */
  run_id?: string
  priority?: string
  blocking?: boolean
}

export interface BuildOverviewInput {
  // Missions with routine_id are issues that bind a routine. We only
  // surface these — orphan issues without a routine binding aren't
  // "overview-worthy" because there's no execution chain to show.
  missions: Mission[]
  pipelines: Pipeline[]
  runs: PipelineRun[]
  // Optional: caller-provided "is this run currently in a paused
  // waitpoint state" predicate. Lets the canvas badge those
  // distinctively without re-deriving the rule everywhere.
  runsWithWaitpoint?: ReadonlySet<string>
  /**
   * Rules that fire a routine. Each surfaces its routine even when no
   * issue binds it and it has never run — "this is wired up but has
   * never fired" is exactly what the overview is for.
   */
  automations?: OverviewAutomation[]
  /**
   * The agents a run can be attributed to. Resolved by id or by slug,
   * so a caller holding either can pass it through unchanged.
   */
  agents?: OverviewAgent[]
  /**
   * Which agent executed which run, keyed run id → agent id-or-slug.
   * The run wire shape carries only `invoking_agent_id` (who *asked*),
   * which is the fallback; anything better has to come from the caller
   * rather than be guessed here.
   */
  agentIdByRunId?: ReadonlyMap<string, string>
  /**
   * Items waiting on a human. Only those naming a run that is on the
   * graph are drawn — a floating card with no chain into it says
   * nothing the /inbox page doesn't say better.
   */
  inboxItems?: OverviewInboxItem[]
}

// buildOverviewGraph wires the chain, left to right:
//
//   automation ─dashed─▶ ┐
//                        ├─▶ routine ─solid─▶ run ─solid─▶  agent
//   issue      ─dashed─▶ ┘                        └─dashed─▶ inbox
//
// Node id prefixes are load-bearing — the canvas dispatches clicks off
// them: `iss:` `rt:` `run:` `auto:` `agt:` `ibx:`. Click handling
// itself stays at the canvas level, so the builder is purely
// structural.
export function buildOverviewGraph(input: BuildOverviewInput): OverviewGraphData {
  const {
    missions,
    pipelines,
    runs,
    runsWithWaitpoint,
    automations = [],
    agents = [],
    agentIdByRunId,
    inboxItems = [],
  } = input

  // ── 1. Bound issues ─────────────────────────────────────────────
  const boundIssues = missions.filter(
    (m) => m.routine_id && (m.routine_slug || m.routine_name),
  )

  // ── 2. Automations that name a routine ──────────────────────────
  const boundAutomations = automations.filter((a) => Boolean(a.routine_slug))

  // ── 3. Routines ────────────────────────────────────────────────
  // A routine surfaces in the overview if (a) it's bound to an
  // issue, (b) an automation fires it, (c) there's a recent run for
  // it, OR (d) it's a saved pipeline. We dedupe by slug.
  const routineSlugs = new Set<string>()
  for (const m of boundIssues) if (m.routine_slug) routineSlugs.add(m.routine_slug)
  for (const a of boundAutomations) routineSlugs.add(a.routine_slug!)
  for (const r of runs) routineSlugs.add(r.pipeline_slug)
  // Resolve metadata via pipelines list (preferred) then fall back
  // to anything we know from runs.
  const routineByslug = new Map<string, OverviewRoutineNodeData>()
  for (const slug of routineSlugs) {
    const p = pipelines.find((pp) => pp.slug === slug)
    if (p) {
      routineByslug.set(slug, {
        slug: p.slug,
        name: p.name,
        authoredVia: p.authored_via,
        invocationCount: p.invocation_count,
      })
    } else {
      const r = runs.find((rr) => rr.pipeline_slug === slug)
      routineByslug.set(slug, {
        slug,
        name: r?.pipeline_name || slug,
      })
    }
  }

  // ── 4. Last run per routine ─────────────────────────────────────
  const latestRunBySlug = new Map<string, PipelineRun>()
  for (const r of runs) {
    const existing = latestRunBySlug.get(r.pipeline_slug)
    if (!existing || (r.started_at ?? "") > (existing.started_at ?? "")) {
      latestRunBySlug.set(r.pipeline_slug, r)
    }
  }

  // ── 5. Build nodes ─────────────────────────────────────────────
  const nodes: Node[] = []
  const edges: Edge[] = []

  const push = (id: string, type: OverviewNodeType, data: Record<string, unknown>) => {
    nodes.push({ id, type, data, position: { x: 0, y: 0 } })
  }

  const connect = (
    source: string,
    target: string,
    opts: { stroke: string; dashed?: boolean; animated?: boolean },
  ) => {
    edges.push({
      id: `e:${source}->${target}`,
      source,
      target,
      type: "default",
      animated: opts.animated ?? false,
      style: {
        stroke: opts.stroke,
        strokeWidth: 1.5,
        ...(opts.dashed ? { strokeDasharray: "5 4" } : {}),
      },
    })
  }

  for (const m of boundIssues) {
    const identifier = m.identifier ?? m.id
    const data: OverviewIssueNodeData = {
      identifier,
      title: m.title,
      status: m.status,
      hasRoutine: true,
    }
    push(`iss:${identifier}`, "overviewIssue", data as unknown as Record<string, unknown>)
    if (m.routine_slug) {
      connect(`iss:${identifier}`, `rt:${m.routine_slug}`, {
        stroke: EDGE_STROKE.issueToRoutine,
        dashed: true,
      })
    }
  }

  for (const a of boundAutomations) {
    const routineSlug = a.routine_slug!
    const data: OverviewAutomationNodeData = {
      automationId: a.id,
      name: a.name,
      eventType: a.event_type,
      enabled: a.enabled,
      routineSlug,
    }
    push(`auto:${a.id}`, "overviewAutomation", data as unknown as Record<string, unknown>)
    connect(`auto:${a.id}`, `rt:${routineSlug}`, {
      stroke: EDGE_STROKE.automationToRoutine,
      dashed: true,
    })
  }

  // Runs that made it onto the graph, in draw order — the agent and
  // inbox columns hang off these and nothing else.
  const drawnRuns: PipelineRun[] = []

  for (const [slug, data] of routineByslug) {
    push(`rt:${slug}`, "overviewRoutine", data as unknown as Record<string, unknown>)
    const latest = latestRunBySlug.get(slug)
    if (!latest) continue
    const runData: OverviewRunNodeData = {
      runId: latest.id,
      status: latest.status,
      startedAt: latest.started_at,
      triggeredVia: latest.triggered_via,
      pipelineSlug: slug,
      isWaitpoint: runsWithWaitpoint?.has(latest.id) ?? false,
    }
    push(`run:${latest.id}`, "overviewRun", runData as unknown as Record<string, unknown>)
    drawnRuns.push(latest)
    const sourceStatus = latest.status
    connect(`rt:${slug}`, `run:${latest.id}`, {
      stroke: EDGE_STROKE.routineToRun,
      animated: sourceStatus === "running" || sourceStatus === "queued",
    })
  }

  // ── 6. Agents that executed those runs ─────────────────────────
  const agentByKey = new Map<string, OverviewAgent>()
  for (const a of agents) {
    agentByKey.set(a.id, a)
    if (a.slug) agentByKey.set(a.slug, a)
  }
  const drawnAgents = new Set<string>()
  for (const r of drawnRuns) {
    const key = agentIdByRunId?.get(r.id) || r.invoking_agent_id
    if (!key) continue
    const agent = agentByKey.get(key)
    // An id we cannot resolve to an agent draws nothing. A node
    // labelled with a bare id is worse than no node: it looks like
    // knowledge and is not.
    if (!agent) continue
    if (!drawnAgents.has(agent.id)) {
      drawnAgents.add(agent.id)
      const data: OverviewAgentNodeData = {
        agentId: agent.id,
        slug: agent.slug,
        name: agent.name,
        crewName: agent.crew_name,
        avatarSeed: agent.avatar_seed,
        avatarStyle: agent.avatar_style,
        avatarUrl: agent.avatar_url,
      }
      push(`agt:${agent.id}`, "overviewAgent", data as unknown as Record<string, unknown>)
    }
    connect(`run:${r.id}`, `agt:${agent.id}`, { stroke: EDGE_STROKE.runToAgent })
  }

  // ── 7. What those runs left for a human ────────────────────────
  const drawnRunIds = new Set(drawnRuns.map((r) => r.id))
  for (const item of inboxItems) {
    if (!item.run_id || !drawnRunIds.has(item.run_id)) continue
    const data: OverviewInboxNodeData = {
      itemId: item.id,
      kind: item.kind,
      title: item.title,
      priority: item.priority,
      blocking: item.blocking,
    }
    push(`ibx:${item.id}`, "overviewInbox", data as unknown as Record<string, unknown>)
    connect(`run:${item.run_id}`, `ibx:${item.id}`, {
      stroke: EDGE_STROKE.runToInbox,
      dashed: true,
    })
  }

  // ── 8. Layout via dagre LR ─────────────────────────────────────
  const g = new DagreGraph({ multigraph: false, compound: false })
  g.setGraph({ rankdir: "LR", nodesep: 30, ranksep: 90, marginx: 20, marginy: 20 })
  g.setDefaultEdgeLabel(() => ({}))

  for (const n of nodes) {
    g.setNode(n.id, { width: widthFor(n.type), height: OVERVIEW_NODE_HEIGHT })
  }
  for (const e of edges) g.setEdge(e.source, e.target)
  dagreLayout(g)

  for (const n of nodes) {
    const pos = g.node(n.id)
    if (pos) {
      n.position = {
        x: pos.x - widthFor(n.type) / 2,
        y: pos.y - OVERVIEW_NODE_HEIGHT / 2,
      }
    }
  }

  return { nodes, edges }
}

/**
 * Width for a node type, or a loud failure.
 *
 * Unreachable for anything this builder emits — `push` only accepts an
 * `OverviewNodeType`, and the width map is total over that union — so
 * the throw fires only for a type someone added without registering a
 * width, at the moment they add it, which is the whole point. The
 * alternative was the silent `?? RUN_W` that shipped a node type
 * rendering half a card off its column.
 */
function widthFor(type: string | undefined): number {
  const w = type === undefined ? undefined : OVERVIEW_NODE_WIDTH[type as OverviewNodeType]
  if (typeof w !== "number") {
    throw new Error(`buildOverviewGraph: no width registered for node type "${type}"`)
  }
  return w
}
