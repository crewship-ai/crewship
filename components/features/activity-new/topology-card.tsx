"use client"

// The cross-feature topology — how one thing caused another.
//
// This is the picture the whole surface was asked for: an automation fires a
// routine, the routine runs, an agent executes it, an inbox item lands on a
// person. Read left to right as causation.
//
// It is drawn from GET /api/v1/chains/{anchor}, which walks the links the
// schema actually carries and returns the ones it does not as `gaps`. Those
// gaps are rendered, not swallowed: a graph that quietly stops is a graph
// that says "nothing else happened" when it means "we cannot see it".

import * as React from "react"
import dynamic from "next/dynamic"
import { GitBranch, TriangleAlert } from "lucide-react"

import { DashboardCard } from "@/components/features/dashboard/dashboard-card"
import { Button } from "@/components/ui/button"
import { Spinner } from "@/components/ui/spinner"
import { apiFetch } from "@/lib/api-fetch"
import { buildChainGraph, CHAIN_FIT_PADDING, type ChainGraph } from "@/lib/trace/build-chain-graph"

// React Flow (~200 KB+) only loads when a graph actually renders — the same
// call /activity makes, for the same reason.
const ChainCanvas = dynamic(() => import("./chain-canvas").then((m) => m.ChainCanvas), {
  ssr: false,
  loading: () => (
    <div className="flex h-full items-center justify-center text-xs text-muted-foreground">
      Loading topology…
    </div>
  ),
})

/**
 * The frame the graph is drawn in, from the size the graph actually is.
 *
 * Every chain used to get the same 380px box: a two-node chain then sat in a
 * third of it under a band of dot background, and a branching one was still
 * clipped. fitView insets by `CHAIN_FIT_PADDING` per side, so the frame has
 * to be that much bigger than the graph for the graph to come out at its own
 * scale.
 *
 * Clamped at both ends — a single rank should not collapse to a sliver, and
 * a deep chain should not push the rest of the page below the fold; past the
 * ceiling it zooms out, which is the honest response to "this is bigger than
 * the space".
 */
const MIN_CANVAS_HEIGHT = 180
const MAX_CANVAS_HEIGHT = 420

export function canvasHeightFor(graphHeight: number): number {
  if (!Number.isFinite(graphHeight) || graphHeight <= 0) return MIN_CANVAS_HEIGHT
  const framed = Math.round(graphHeight / (1 - 2 * CHAIN_FIT_PADDING))
  return Math.min(MAX_CANVAS_HEIGHT, Math.max(MIN_CANVAS_HEIGHT, framed))
}

export interface TopologyCardProps {
  workspaceId: string
  /** An issue identifier, a run id, a routine slug, an automation id… */
  anchor: string
  /** Shown in the card header so a reader knows what they are looking at. */
  anchorLabel?: string
  onOpenNode?: (kind: string, ref: string) => void
}

export function TopologyCard({ workspaceId, anchor, anchorLabel, onOpenNode }: TopologyCardProps) {
  const [chain, setChain] = React.useState<ChainGraph | null>(null)
  const [loading, setLoading] = React.useState(false)
  const [error, setError] = React.useState<string | null>(null)

  React.useEffect(() => {
    if (!anchor) return
    let cancelled = false
    setLoading(true)
    setError(null)
    apiFetch(
      `/api/v1/chains/${encodeURIComponent(anchor)}?workspace_id=${encodeURIComponent(workspaceId)}`,
    )
      .then(async (r) => {
        if (!r.ok) throw new Error(`HTTP ${r.status}`)
        return (await r.json()) as ChainGraph
      })
      .then((d) => {
        if (!cancelled) setChain(d)
      })
      .catch((e: unknown) => {
        if (!cancelled) setError(e instanceof Error ? e.message : "could not load the chain")
      })
      .finally(() => {
        if (!cancelled) setLoading(false)
      })
    return () => {
      cancelled = true
    }
  }, [anchor, workspaceId])

  const graph = React.useMemo(() => (chain ? buildChainGraph(chain) : null), [chain])

  const hint = chain
    ? `${chain.nodes.length} ${chain.nodes.length === 1 ? "node" : "nodes"}${
        chain.truncated ? " · truncated" : ""
      }`
    : loading
      ? "walking…"
      : ""

  return (
    <DashboardCard
      title={anchorLabel ? `How ${anchorLabel} happened` : "Topology"}
      icon={GitBranch}
      hint={hint}
    >
      {loading && !chain && (
        <div className="flex items-center justify-center gap-2 py-12 text-xs text-muted-foreground">
          <Spinner className="h-3.5 w-3.5" /> Walking the chain…
        </div>
      )}

      {error && (
        <div className="flex flex-col items-center gap-2 py-10 text-center">
          <TriangleAlert className="h-4 w-4 text-destructive" />
          <p className="text-[11px] text-muted-foreground">Could not load the chain: {error}</p>
          <Button size="sm" variant="outline" onClick={() => setChain(null)}>
            Try again
          </Button>
        </div>
      )}

      {chain && chain.nodes.length <= 1 && !loading && (
        <div className="flex flex-col items-center gap-1.5 py-10 text-center">
          <GitBranch className="h-4 w-4 text-muted-foreground-soft" />
          <p className="max-w-[320px] text-[11px] text-muted-foreground-soft">
            Nothing links to this yet. A chain appears once a routine, an agent or a rule touches
            it.
          </p>
        </div>
      )}

      {graph && chain && chain.nodes.length > 1 && (
        <div className="flex flex-col gap-2">
          <div
            className="w-full overflow-hidden rounded-md border border-white/[0.06]"
            style={{ height: canvasHeightFor(graph.bounds.height) }}
          >
            <ChainCanvas nodes={graph.nodes} edges={graph.edges} onOpenNode={onOpenNode} />
          </div>

          {/* The honest edge of the picture. Two links genuinely do not exist
              in the schema — inbox → issue, escalation → run — and a reader
              deciding "nothing else happened" from a graph that simply
              could not look is the one failure this card must not cause. */}
          {/* One line, and the reasons on demand.
              These two gaps are the same two on every chain in the product —
              they are properties of the schema, not of this walk — and printing
              both hundred-word explanations under every graph spent more of the
              card on what could not be drawn than on what was. The claim still
              has to be made, because a reader deciding "nothing else happened"
              from a graph that could not look is the one failure this card must
              not cause. It is made in a sentence, and the sentence opens. */}
          {chain.gaps.length > 0 && (
            <details className="group">
              <summary className="flex cursor-pointer list-none items-center gap-1.5 text-[10.5px] text-muted-foreground-soft hover:text-muted-foreground">
                <TriangleAlert className="h-2.5 w-2.5 shrink-0 text-warn" />
                <span>
                  {chain.gaps.length} {chain.gaps.length === 1 ? "link" : "links"} the schema cannot carry
                  {" "}are not drawn
                </span>
                <span aria-hidden className="text-muted-foreground/50 group-open:hidden">
                  — why?
                </span>
              </summary>
              <ul className="mt-1.5 flex flex-col gap-1 pl-4">
                {chain.gaps.map((g, i) => (
                  <li
                    key={`${g.from}-${g.to}-${i}`}
                    className="text-[10.5px] leading-relaxed text-muted-foreground-soft"
                  >
                    <span className="font-mono">
                      {g.from} → {g.to}
                    </span>{" "}
                    — {g.reason}
                  </li>
                ))}
              </ul>
            </details>
          )}

          {chain.truncated && (
            <p className="text-[10.5px] text-warn">
              Chain truncated{chain.truncated_by ? ` by ${chain.truncated_by}` : ""} — there is more
              than this.
            </p>
          )}
        </div>
      )}
    </DashboardCard>
  )
}
