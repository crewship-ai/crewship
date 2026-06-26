"use client"

import * as React from "react"
import { Wrench, Search } from "lucide-react"

import { ToolkitIcon, EmptyHint, TableSkeleton } from "./shared"
import type { Tool, ToolsResp } from "./types"

// ToolsTab — pick a toolkit (slug), then list the tools it exposes (GitHub has
// 846, Gmail 61, …). The tools endpoint requires a toolkit, so we prompt for
// one before fetching; search narrows within the toolkit. Both inputs are
// debounced (300ms) to match the catalog.
export function ToolsTab({
  workspaceId,
  suggestions,
}: {
  workspaceId: string
  // Connected/known toolkit slugs surfaced as quick-pick chips.
  suggestions: string[]
}) {
  const [toolkit, setToolkit] = React.useState(suggestions[0] ?? "")
  const [search, setSearch] = React.useState("")
  const [tools, setTools] = React.useState<Tool[]>([])
  const [total, setTotal] = React.useState(0)
  const [loading, setLoading] = React.useState(false)
  const [err, setErr] = React.useState<string | null>(null)

  React.useEffect(() => {
    const tk = toolkit.trim()
    if (!tk) {
      setTools([])
      setTotal(0)
      return
    }
    const ctrl = new AbortController()
    const t = setTimeout(async () => {
      setLoading(true)
      setErr(null)
      try {
        const params = new URLSearchParams({ workspace_id: workspaceId, toolkit: tk })
        if (search.trim()) params.set("search", search.trim())
        const r = await fetch(`/api/v1/integrations/composio/tools?${params}`, {
          signal: ctrl.signal,
        })
        if (!r.ok) throw new Error(`Failed (${r.status})`)
        const j = (await r.json()) as ToolsResp
        setTools(j.tools ?? [])
        setTotal(j.total ?? 0)
      } catch (e) {
        if ((e as Error).name !== "AbortError") {
          setErr(e instanceof Error ? e.message : "Failed to load tools")
          setTools([])
        }
      } finally {
        setLoading(false)
      }
    }, 300)
    return () => {
      clearTimeout(t)
      ctrl.abort()
    }
  }, [workspaceId, toolkit, search])

  return (
    <section className="space-y-3">
      <h2 className="flex items-center gap-1.5 text-xs font-semibold uppercase tracking-wider text-muted-foreground">
        <Wrench className="h-3.5 w-3.5" /> Tools
        <span className="font-normal normal-case tracking-normal text-muted-foreground/70">
          · the actions a connector exposes
        </span>
      </h2>

      <div className="flex flex-wrap items-center gap-2">
        <input
          value={toolkit}
          onChange={(e) => setToolkit(e.target.value)}
          placeholder="Toolkit slug (gmail, github…)"
          className="w-52 rounded-lg border border-white/10 bg-card px-3 py-1.5 text-xs text-foreground placeholder:text-muted-foreground focus:border-blue-400/50 focus:outline-none"
        />
        <div className="relative">
          <Search className="pointer-events-none absolute left-2.5 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-muted-foreground" />
          <input
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            placeholder="Search tools…"
            className="w-56 rounded-lg border border-white/10 bg-card py-1.5 pl-8 pr-3 text-xs text-foreground placeholder:text-muted-foreground focus:border-blue-400/50 focus:outline-none"
          />
        </div>
      </div>

      {suggestions.length > 0 && (
        <div className="flex flex-wrap items-center gap-1.5">
          <span className="text-[11px] text-muted-foreground">Connected:</span>
          {suggestions.map((s) => (
            <button
              key={s}
              type="button"
              onClick={() => setToolkit(s)}
              className="inline-flex items-center gap-1 rounded-full border border-white/10 bg-white/[0.03] px-2 py-0.5 text-[11px] text-muted-foreground hover:text-foreground"
            >
              <ToolkitIcon toolkit={{ slug: s }} size={12} />
              <span className="capitalize">{s}</span>
            </button>
          ))}
        </div>
      )}

      {err && <div className="text-[11px] text-red-400">{err}</div>}

      {!toolkit.trim() ? (
        <EmptyHint text="Enter a toolkit slug (or pick a connected one) to list its tools." />
      ) : loading ? (
        <TableSkeleton rows={5} />
      ) : tools.length === 0 ? (
        <EmptyHint text={`No tools found for “${toolkit.trim()}”.`} />
      ) : (
        <div className="overflow-hidden rounded-xl border border-white/10 bg-card">
          <table className="w-full border-collapse">
            <thead>
              <tr>
                {["Tool", "Name", "Description"].map((h) => (
                  <th
                    key={h}
                    className="px-3 py-2.5 text-left text-[10px] font-semibold uppercase tracking-wider text-muted-foreground"
                  >
                    {h}
                  </th>
                ))}
              </tr>
            </thead>
            <tbody>
              {tools.map((t) => (
                <tr key={t.slug} className="border-t border-white/[0.06] align-top">
                  <td className="px-3 py-2.5 font-mono text-[11px] text-foreground/90">{t.slug}</td>
                  <td className="px-3 py-2.5 text-[13px]">{t.name}</td>
                  <td className="px-3 py-2.5 text-[12px] text-muted-foreground">{t.description}</td>
                </tr>
              ))}
            </tbody>
          </table>
          {total > tools.length && (
            <div className="border-t border-white/[0.06] px-3 py-2 text-[11px] text-muted-foreground">
              Showing {tools.length} of {total} tools — narrow with search.
            </div>
          )}
        </div>
      )}
    </section>
  )
}
