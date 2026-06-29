"use client"

import { useEffect, useState } from "react"
import { cn } from "@/lib/utils"
import { apiFetch } from "@/lib/api-fetch"

import type { BottomPanelContext } from "./types"
import { EmptyState } from "./shared"

// Mirror of internal/api diffResponse (changes endpoint).
interface ChangedFile {
  path: string
  status: string // "modified" | "added" | "deleted" | "renamed"
  additions?: number
  deletions?: number
}
interface DiffResponse {
  is_repo: boolean
  workdir?: string
  files?: ChangedFile[]
  diff?: string // unified diff text
  truncated?: boolean
}

function lineClass(line: string): string {
  if (line.startsWith("+") && !line.startsWith("+++")) return "bg-emerald-500/10 text-emerald-200"
  if (line.startsWith("-") && !line.startsWith("---")) return "bg-red-500/10 text-red-200"
  if (line.startsWith("@@")) return "text-cyan-300"
  if (line.startsWith("diff ") || line.startsWith("+++") || line.startsWith("---") || line.startsWith("index "))
    return "text-muted-foreground"
  return "text-foreground/70"
}

/**
 * Changes — what the work on this entity actually produced. Computed
 * on-demand by running `git diff` inside the relevant container (no stored
 * snapshots). Degrades to a friendly note when the workspace isn't a git
 * repo or has no changes.
 *  • issue/mission → /api/v1/crews/{crewId}/issues/{identifier}/changes
 *  • run          → /api/v1/workspaces/{ws}/pipeline-runs/{runId}/changes
 */
export function ChangesTab({ workspaceId, context }: { workspaceId: string; context: BottomPanelContext }) {
  const [data, setData] = useState<DiffResponse | null>(null)
  const [error, setError] = useState<string | null>(null)
  // The diff endpoint is gated on a product decision (working-tree vs
  // base-branch diff). Until it lands the handler returns 501; we render a
  // calm "not wired yet" state rather than a red error.
  const [unavailable, setUnavailable] = useState(false)

  // Diff is computed at the crew-container level (base-branch diff of the
  // crew workspace). An issue maps to its owning crew directly; a run maps
  // via its invoking/author crew (resolved server-side). A run with no crew
  // degrades to the idle state below.
  let url: string | null = null
  if (context?.kind === "mission") {
    url = `/api/v1/crews/${context.crewId}/git-diff?workspace_id=${workspaceId}`
  } else if (context?.kind === "run") {
    url = `/api/v1/workspaces/${workspaceId}/pipeline-runs/${encodeURIComponent(context.runId)}/changes`
  }

  useEffect(() => {
    if (!url) return
    let cancelled = false
    setData(null)
    setError(null)
    setUnavailable(false)
    apiFetch(url)
      .then((r) => {
        if (r.status === 404 || r.status === 501) { if (!cancelled) setUnavailable(true); return null }
        return r.ok ? r.json() : Promise.reject(new Error(`HTTP ${r.status}`))
      })
      .then((d) => { if (!cancelled && d) setData(d) })
      .catch((err) => { if (!cancelled) setError(err instanceof Error ? err.message : String(err)) })
    return () => { cancelled = true }
  }, [url])

  if (!context) return <EmptyState>Select an issue or run to see its changes.</EmptyState>
  if (context.kind !== "mission" && context.kind !== "run") {
    return <EmptyState>Changes are shown per issue or run.</EmptyState>
  }
  if (unavailable) return <EmptyState>Change diffs aren&apos;t wired up for this workspace yet.</EmptyState>
  if (error) return <EmptyState><span className="text-red-300">Failed to load: {error}</span></EmptyState>
  if (data === null) return <EmptyState>Computing diff…</EmptyState>
  if (!data.is_repo) return <EmptyState>This workspace isn&apos;t a git repository — no tracked changes.</EmptyState>
  const files = data.files ?? []
  if (files.length === 0 && !data.diff) return <EmptyState>No changes in the working tree.</EmptyState>

  const totals = files.reduce(
    (acc, f) => ({ add: acc.add + (f.additions ?? 0), del: acc.del + (f.deletions ?? 0) }),
    { add: 0, del: 0 },
  )

  return (
    <div className="h-full overflow-y-auto p-3 text-xs">
      <div className="flex items-center gap-2 text-[11px] text-muted-foreground mb-3">
        <span className="text-foreground">{files.length} file{files.length === 1 ? "" : "s"}</span>
        <span>·</span>
        <span className="text-emerald-300">+{totals.add}</span>
        <span className="text-red-300">−{totals.del}</span>
        {data.truncated && <><span>·</span><span className="text-amber-300">diff truncated</span></>}
      </div>

      {files.length > 0 && (
        <div className="mb-3 space-y-0.5">
          {files.map((f) => (
            <div key={f.path} className="flex items-center justify-between gap-3 font-mono">
              <span className="truncate">
                <span className={cn(
                  "inline-block w-4",
                  f.status === "added" ? "text-emerald-300" :
                  f.status === "deleted" ? "text-red-300" :
                  f.status === "renamed" ? "text-blue-300" : "text-amber-300",
                )}>
                  {f.status === "added" ? "A" : f.status === "deleted" ? "D" : f.status === "renamed" ? "R" : "M"}
                </span>
                {f.path}
              </span>
              <span className="shrink-0 text-muted-foreground-soft">
                <span className="text-emerald-300">+{f.additions ?? 0}</span>{" "}
                <span className="text-red-300">−{f.deletions ?? 0}</span>
              </span>
            </div>
          ))}
        </div>
      )}

      {data.diff && (() => {
        // Cap rendered DOM nodes — a big diff split line-by-line is thousands
        // of nodes that jank scroll. Show the first slice; the full patch is
        // available via the CLI (`issue changes <id> --patch`).
        const MAX_LINES = 2000
        const lines = data.diff.split("\n")
        const shown = lines.slice(0, MAX_LINES)
        return (
          <pre className="font-mono text-[11px] leading-relaxed border border-white/8 rounded-md overflow-x-auto">
            {shown.map((line, i) => (
              <div key={i} className={cn("px-3", lineClass(line))}>{line || " "}</div>
            ))}
            {lines.length > MAX_LINES && (
              <div className="px-3 py-1 text-amber-300">… {lines.length - MAX_LINES} more lines — open the full diff in the CLI</div>
            )}
          </pre>
        )
      })()}
    </div>
  )
}
