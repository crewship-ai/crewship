"use client"

// Pieces shared by the run page and the agent-work page (#2979, #2989): the
// tree of what a piece of work caused, the "linked to" chips, and the walk
// they are read from. Kept apart so a page can use them without pulling in
// the run page's hooks.

import * as React from "react"
import Link from "next/link"
import { Bot, ChevronRight, CircleDot, GitBranch, Inbox, ScrollText, type LucideIcon } from "lucide-react"

import { routineViewHref } from "@/components/features/routines/routine-navigation"
import {
  RUN_TONE_CLASS,
  RUN_TONE_DOT,
  runStatusLabel,
  runTone,
  type BranchGraph,
  type LinkedEntity,
  type RunTreeNode,
} from "@/lib/activity-run"
import { formatDurationMs } from "@/lib/activity-stream"
import { apiFetch } from "@/lib/api-fetch"
import { cn } from "@/lib/utils"

/* ------------------------------------------------------------------ *
 *  Pieces
 * ------------------------------------------------------------------ */

const BRANCH_KIND: Record<string, { label: string; icon: LucideIcon }> = {
  run: { label: "Sub-run", icon: ScrollText },
  assignment: { label: "Agent", icon: Bot },
  inbox: { label: "Ask", icon: Inbox },
  automation: { label: "Rule", icon: GitBranch },
}

export function countTree(nodes: RunTreeNode[]): number {
  return nodes.reduce((n, c) => n + 1 + countTree(c.children), 0)
}

export function BranchRow({
  node,
  depth,
  onOpenNode,
}: {
  node: RunTreeNode
  depth: number
  onOpenNode: (kind: string, ref: string) => void
}) {
  const [open, setOpen] = React.useState(true)
  const kind = BRANCH_KIND[node.kind] ?? { label: node.kind, icon: CircleDot }
  const Icon = kind.icon
  const tone = runTone(node.status)
  return (
    <li>
      <div className="group flex items-center gap-2 rounded-md py-1.5 pr-1.5 transition-colors hover:bg-foreground/[0.03]" style={{ paddingLeft: 6 + depth * 18 }}>
        {node.children.length > 0 ? (
          <button
            type="button"
            aria-label={open ? "Collapse" : "Expand"}
            aria-expanded={open}
            onClick={() => setOpen(!open)}
            className="text-muted-foreground hover:text-foreground"
          >
            <ChevronRight className={cn("h-3.5 w-3.5 transition-transform duration-150", open && "rotate-90")} />
          </button>
        ) : (
          <span className="w-3.5" />
        )}
        <span aria-hidden className={cn("h-1.5 w-1.5 shrink-0 rounded-full", node.status ? RUN_TONE_DOT[tone] : "bg-muted-foreground/40")} />
        <span className="w-14 shrink-0 font-mono text-[10px] uppercase tracking-wide text-muted-foreground-soft">{kind.label}</span>
        <button
          type="button"
          onClick={() => onOpenNode(node.kind, node.ref)}
          className="flex min-w-0 flex-1 items-center gap-1.5 text-left text-xs"
        >
          <Icon className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
          <span className="truncate group-hover:underline">{node.label}</span>
        </button>
        {node.status && <span className={cn("text-[10.5px]", RUN_TONE_CLASS[tone])}>{runStatusLabel(node.status)}</span>}
        <span className="w-14 text-right font-mono text-[10.5px] tabular-nums text-muted-foreground">
          {node.durationMs != null ? formatDurationMs(node.durationMs) : ""}
        </span>
      </div>
      {open && node.children.length > 0 && (
        <ul>
          {node.children.map((c) => (
            <BranchRow key={c.id} node={c} depth={depth + 1} onOpenNode={onOpenNode} />
          ))}
        </ul>
      )}
    </li>
  )
}

const LINK_STYLE: Record<LinkedEntity["kind"], { label: string; icon: LucideIcon; tone: string }> = {
  routine: { label: "Routine", icon: ScrollText, tone: "text-info" },
  issue: { label: "Issue", icon: CircleDot, tone: "text-purple" },
  agent: { label: "Agent", icon: Bot, tone: "text-primary" },
}

export function LinkChip({ link, onOpenNode }: { link: LinkedEntity; onOpenNode: (kind: string, ref: string) => void }) {
  const style = LINK_STYLE[link.kind]
  const Icon = style.icon
  const body = (
    <>
      <Icon className={cn("h-3 w-3 shrink-0", style.tone)} />
      <span className="text-[10px] uppercase tracking-wide text-muted-foreground-soft">{style.label}</span>
      <span className="truncate">{link.label}</span>
    </>
  )
  const cls =
    "inline-flex max-w-full items-center gap-1.5 rounded-md border border-border bg-foreground/[0.02] px-2 py-1 text-xs transition-colors hover:bg-foreground/[0.06]"
  if (link.kind === "routine") {
    return (
      <Link href={routineViewHref(link.ref, "history")} className={cls}>
        {body}
      </Link>
    )
  }
  return (
    <button type="button" onClick={() => onOpenNode(link.kind, link.ref)} className={cls}>
      {body}
    </button>
  )
}

/* ------------------------------------------------------------------ *
 *  Data
 * ------------------------------------------------------------------ */

/** The causal walk around this run — what it caused, and what it belongs to. */
export function useChainGraph(workspaceId: string, runId: string): BranchGraph | null {
  const [graph, setGraph] = React.useState<BranchGraph | null>(null)
  React.useEffect(() => {
    const ctrl = new AbortController()
    setGraph(null)
    apiFetch(`/api/v1/chains/${encodeURIComponent(runId)}?workspace_id=${encodeURIComponent(workspaceId)}`, {
      signal: ctrl.signal,
    })
      .then(async (r) => (r.ok ? ((await r.json()) as BranchGraph) : null))
      .then((g) => {
        if (!ctrl.signal.aborted && g) setGraph({ nodes: g.nodes ?? [], edges: g.edges ?? [] })
      })
      .catch(() => {
        /* The page stands without the walk; only "caused" and links thin out. */
      })
    return () => ctrl.abort()
  }, [workspaceId, runId])
  return graph
}

/** How many journal events carry this run — the size of the technical record. */
export function useRunEventCount(workspaceId: string, runId: string): number | null {
  const [count, setCount] = React.useState<number | null>(null)
  React.useEffect(() => {
    const ctrl = new AbortController()
    setCount(null)
    apiFetch(
      `/api/v1/journal/count?workspace_id=${encodeURIComponent(workspaceId)}&run_id=${encodeURIComponent(runId)}`,
      { signal: ctrl.signal },
    )
      .then(async (r) => (r.ok ? ((await r.json()) as { total?: number }) : null))
      .then((b) => {
        if (!ctrl.signal.aborted && typeof b?.total === "number") setCount(b.total)
      })
      .catch(() => {})
    return () => ctrl.abort()
  }, [workspaceId, runId])
  return count
}
