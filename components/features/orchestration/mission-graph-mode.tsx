"use client"

import { useMemo } from "react"
import { Check, Clock, Lock, Pause, X } from "lucide-react"
import { cn } from "@/lib/utils"
import type { MissionTask } from "@/lib/types/mission"

interface MissionGraphModeProps {
  tasks: MissionTask[]
}

/**
 * MissionGraphMode renders task dependencies as a left-to-right SVG flow.
 * Layout is layered: column N contains tasks whose deepest dependency
 * lives in column N-1. Tasks with no dependencies sit in column 0.
 * Within a column, rows are stacked vertically by task_order. Edges are
 * straight lines from each task to every task that names it as a
 * dependency, coloured by the upstream task's run state so the operator
 * can see at a glance which dependency chains are actually progressing.
 *
 * The wireframe reference uses a hand-laid graph with three columns;
 * this implementation derives the layers algorithmically so the view
 * scales beyond the four-task demo. For the simple chain mission shape
 * (dep on a single prior) the visual result matches the wireframe.
 */
export function MissionGraphMode({ tasks }: MissionGraphModeProps) {
  const layout = useMemo(() => layoutTasks(tasks), [tasks])

  if (tasks.length === 0) {
    return (
      <div className="rounded-md bg-muted/40 px-6 py-12 text-center text-sm text-muted-foreground">
        No tasks to graph. Once tasks land here, dependencies render as a
        layered DAG with status-coloured edges.
      </div>
    )
  }

  return (
    <div>
      <Legend />
      <div className="relative w-full" style={{ height: layout.height }}>
        <svg
          viewBox={`0 0 ${layout.width} ${layout.height}`}
          preserveAspectRatio="xMidYMid meet"
          className="absolute inset-0 w-full h-full pointer-events-none"
        >
          <defs>
            <marker
              id="mg-arrow-blue"
              viewBox="0 -5 10 10"
              refX="9"
              refY="0"
              markerWidth="6"
              markerHeight="6"
              orient="auto"
            >
              <path d="M0,-5L10,0L0,5" fill="rgb(59 130 246)" />
            </marker>
            <marker
              id="mg-arrow-gray"
              viewBox="0 -5 10 10"
              refX="9"
              refY="0"
              markerWidth="6"
              markerHeight="6"
              orient="auto"
            >
              <path d="M0,-5L10,0L0,5" fill="rgb(156 163 175)" />
            </marker>
          </defs>
          {layout.edges.map((e, i) => (
            <line
              key={i}
              x1={e.x1}
              y1={e.y1}
              x2={e.x2}
              y2={e.y2}
              stroke={e.upstreamRunning ? "rgb(59 130 246)" : "rgb(156 163 175)"}
              strokeWidth={2}
              strokeDasharray={e.upstreamRunning ? undefined : "4 4"}
              markerEnd={`url(#mg-arrow-${e.upstreamRunning ? "blue" : "gray"})`}
            />
          ))}
        </svg>

        {layout.nodes.map((n) => {
          const StatusIcon = statusIcon(n.task.status)
          return (
            <div
              key={n.task.id}
              className={cn(
                "absolute rounded-md border-2 bg-card text-xs shadow-sm px-3 py-2",
                statusBorder(n.task.status),
              )}
              style={{
                left: n.x,
                top: n.y,
                width: NODE_WIDTH,
              }}
            >
              <div className="font-semibold text-foreground line-clamp-2 flex items-center gap-1.5">
                <StatusIcon
                  className={cn("h-3.5 w-3.5 flex-shrink-0", statusIconClass(n.task.status))}
                  aria-hidden="true"
                />
                <span>#{n.task.task_order} {n.task.title}</span>
              </div>
              <div className="mt-1 font-mono text-[10px] text-muted-foreground">
                {n.task.agent_slug ? `@${n.task.agent_slug}` : "unassigned"}
                {n.task.depends_on ? ` · dep: ${n.task.depends_on}` : ""}
              </div>
            </div>
          )
        })}
      </div>

      <p className="mt-4 text-xs italic text-muted-foreground text-center">
        Same data as Spec Mode and Document — visualised as the dependency
        graph.
      </p>
    </div>
  )
}

const NODE_WIDTH = 200
const NODE_HEIGHT = 64
const COL_GAP = 80
const ROW_GAP = 24

interface LayoutNode {
  task: MissionTask
  col: number
  row: number
  x: number
  y: number
}

interface LayoutEdge {
  x1: number
  y1: number
  x2: number
  y2: number
  upstreamRunning: boolean
}

interface Layout {
  nodes: LayoutNode[]
  edges: LayoutEdge[]
  width: number
  height: number
}

/**
 * layoutTasks runs a simple longest-path layering:
 *   col(t) = 1 + max(col(d) for d in deps(t)), 0 if no deps
 * Within a column, rows are stacked by task_order so the visual order
 * matches the Spec view. Coordinates are computed from the layer +
 * row indices using fixed node dimensions; the SVG/HTML overlay is
 * sized to the resulting bounding box so callers don't have to
 * pre-size the container.
 */
function layoutTasks(tasks: MissionTask[]): Layout {
  if (tasks.length === 0) {
    return { nodes: [], edges: [], width: 0, height: 0 }
  }
  const taskById = new Map<string, MissionTask>()
  for (const t of tasks) taskById.set(t.id, t)

  const depsOf = (t: MissionTask): string[] => {
    if (!t.depends_on) return []
    return t.depends_on
      .split(/[,\s]+/)
      .map((d) => d.trim())
      .filter(Boolean)
  }

  const colCache = new Map<string, number>()
  const colOf = (t: MissionTask, stack: Set<string> = new Set()): number => {
    if (colCache.has(t.id)) return colCache.get(t.id)!
    if (stack.has(t.id)) {
      // Cycle defence — treat any task that loops back as a root so we
      // never recurse forever. In practice mission tasks should be acyclic.
      colCache.set(t.id, 0)
      return 0
    }
    stack.add(t.id)
    let max = -1
    for (const d of depsOf(t)) {
      const upstream = taskById.get(d)
      if (!upstream) continue
      max = Math.max(max, colOf(upstream, stack))
    }
    stack.delete(t.id)
    const col = max + 1
    colCache.set(t.id, col)
    return col
  }

  // Bucket by column.
  const columns: MissionTask[][] = []
  for (const t of tasks) {
    const c = colOf(t)
    while (columns.length <= c) columns.push([])
    columns[c].push(t)
  }
  for (const col of columns) col.sort((a, b) => a.task_order - b.task_order)

  // Layout coordinates.
  const nodes: LayoutNode[] = []
  const nodeById = new Map<string, LayoutNode>()
  for (let c = 0; c < columns.length; c++) {
    for (let r = 0; r < columns[c].length; r++) {
      const x = c * (NODE_WIDTH + COL_GAP)
      const y = r * (NODE_HEIGHT + ROW_GAP)
      const node: LayoutNode = { task: columns[c][r], col: c, row: r, x, y }
      nodes.push(node)
      nodeById.set(node.task.id, node)
    }
  }

  // Edges from each task to every task that names it as a dependency.
  const edges: LayoutEdge[] = []
  for (const node of nodes) {
    for (const dep of depsOf(node.task)) {
      const from = nodeById.get(dep)
      if (!from) continue
      edges.push({
        x1: from.x + NODE_WIDTH,
        y1: from.y + NODE_HEIGHT / 2,
        x2: node.x,
        y2: node.y + NODE_HEIGHT / 2,
        upstreamRunning: from.task.status === "IN_PROGRESS" || from.task.status === "COMPLETED",
      })
    }
  }

  const maxRow = Math.max(...columns.map((c) => c.length), 1)
  const width = columns.length * (NODE_WIDTH + COL_GAP)
  const height = maxRow * (NODE_HEIGHT + ROW_GAP)
  return { nodes, edges, width, height }
}

function Legend() {
  return (
    <div className="flex flex-wrap gap-4 text-[11px] text-muted-foreground mb-5">
      <LegendItem cls="border-blue-500 bg-blue-500/10" label="running" />
      <LegendItem cls="border-emerald-500 bg-emerald-500/10" label="done" />
      <LegendItem cls="border-amber-500 bg-amber-500/10" label="awaiting approval" />
      <LegendItem cls="border-border bg-muted/40" label="blocked / pending" />
    </div>
  )
}

function LegendItem({ cls, label }: { cls: string; label: string }) {
  return (
    <span className="inline-flex items-center gap-1.5">
      <span className={cn("h-2.5 w-2.5 rounded-sm border-2", cls)} />
      {label}
    </span>
  )
}

function statusBorder(status: MissionTask["status"]): string {
  switch (status) {
    case "IN_PROGRESS":
      return "border-blue-500 bg-blue-500/5"
    case "COMPLETED":
    case "SKIPPED":
      return "border-emerald-500 bg-emerald-500/5"
    case "AWAITING_APPROVAL":
      return "border-amber-500 bg-amber-500/5"
    case "FAILED":
      return "border-rose-500 bg-rose-500/5"
    default:
      return "border-border bg-muted/40"
  }
}

// Project rule (components/**/*.tsx): "ONLY lucide-react for icons".
// Status → glyph mapping mirrors the wireframe semantics: hourglass for
// in-flight, check for done, pause for awaiting approval, X for failed,
// lock for blocked/queued.
function statusIcon(status: MissionTask["status"]) {
  switch (status) {
    case "IN_PROGRESS":
      return Clock
    case "COMPLETED":
    case "SKIPPED":
      return Check
    case "AWAITING_APPROVAL":
      return Pause
    case "FAILED":
      return X
    default:
      return Lock
  }
}

function statusIconClass(status: MissionTask["status"]): string {
  switch (status) {
    case "IN_PROGRESS":
      return "text-blue-500"
    case "COMPLETED":
    case "SKIPPED":
      return "text-emerald-500"
    case "AWAITING_APPROVAL":
      return "text-amber-500"
    case "FAILED":
      return "text-rose-500"
    default:
      return "text-muted-foreground"
  }
}
