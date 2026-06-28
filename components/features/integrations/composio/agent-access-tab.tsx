"use client"

import * as React from "react"
import { Bot, Search } from "lucide-react"

import { AgentAvatar } from "@/components/ui/agent-avatar"
import { ScopeChip, EmptyHint, TableSkeleton, toolkitLabel } from "./shared"
import { AccessEditor } from "./access-editor"
import type { AgentLite, AgentBindingsMap } from "./types"

// AgentAccessTab — agent-centric vertical list of Composio access. One row per
// agent shows only the apps that agent is granted (as colour-coded scope
// chips), so the view stays compact even with 50+ connectable apps in the
// project — you browse the full catalogue inside the editor, not here. Filter
// by agent name + by app/access status. Edit/Assign opens the shared
// AccessEditor.
type AccessFilter = "any" | "has" | "none" | `app:${string}`

export function AgentAccessTab({
  workspaceId,
  agents,
  bindings,
  loading,
  onChanged,
}: {
  workspaceId: string
  agents: AgentLite[]
  bindings: AgentBindingsMap
  loading: boolean
  onChanged: () => void
}) {
  const [editing, setEditing] = React.useState<AgentLite | null>(null)
  const [query, setQuery] = React.useState("")
  const [filter, setFilter] = React.useState<AccessFilter>("any")

  // Toolkits that appear in at least one agent's grant — power the per-app
  // filter options ("has Gmail", "has GitHub", …).
  const appOptions = React.useMemo(() => {
    const s = new Set<string>()
    Object.values(bindings).forEach((bs) => bs.forEach((b) => s.add(b.toolkit)))
    return Array.from(s).sort()
  }, [bindings])

  const visible = React.useMemo(() => {
    const q = query.trim().toLowerCase()
    return agents.filter((a) => {
      if (q && !a.name.toLowerCase().includes(q)) return false
      const bs = bindings[a.id] ?? []
      if (filter === "has") return bs.length > 0
      if (filter === "none") return bs.length === 0
      if (filter.startsWith("app:")) {
        const slug = filter.slice(4)
        return bs.some((b) => b.toolkit === slug)
      }
      return true
    })
  }, [agents, bindings, query, filter])

  return (
    <section className="space-y-3">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h2 className="flex items-center gap-1.5 text-xs font-semibold uppercase tracking-wider text-muted-foreground">
          <Bot className="h-3.5 w-3.5" /> Agent access
          <span className="font-normal normal-case tracking-normal text-muted-foreground/70">
            · each agent lists only the apps it&apos;s granted
          </span>
        </h2>
        <div className="flex items-center gap-2">
          <div className="relative">
            <Search className="pointer-events-none absolute left-2.5 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-muted-foreground" />
            <input
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              placeholder="Filter agents…"
              className="w-44 rounded-lg border border-white/10 bg-card py-1.5 pl-8 pr-3 text-xs text-foreground placeholder:text-muted-foreground focus:border-blue-400/50 focus:outline-none"
            />
          </div>
          <select
            value={filter}
            onChange={(e) => setFilter(e.target.value as AccessFilter)}
            className="rounded-lg border border-white/10 bg-card px-2.5 py-1.5 text-xs focus:border-blue-400/50 focus:outline-none"
          >
            <option value="any">Any app</option>
            <option value="has">Has access</option>
            <option value="none">No access</option>
            {appOptions.map((slug) => (
              <option key={slug} value={`app:${slug}`}>
                Has {toolkitLabel(slug)}
              </option>
            ))}
          </select>
        </div>
      </div>

      {loading ? (
        <TableSkeleton rows={4} />
      ) : agents.length === 0 ? (
        <EmptyHint text="No agents in this workspace yet." />
      ) : visible.length === 0 ? (
        <EmptyHint text="No agents match the current filter." />
      ) : (
        <div className="overflow-hidden rounded-xl border border-white/10 bg-card">
          {visible.map((a) => {
            const bs = bindings[a.id] ?? []
            const actsAs = bs[0]?.user_id
            return (
              <div
                key={a.id}
                data-testid="agent-row"
                className="flex items-start justify-between gap-3 border-t border-white/[0.06] px-4 py-3 first:border-t-0"
              >
                <div className="flex min-w-0 items-start gap-3">
                  {/* Lead with the agent's DiceBear avatar (same seed/style as
                      everywhere else) so the list is scannable by face. */}
                  <AgentAvatar
                    data-testid="agent-avatar"
                    seed={a.avatar_seed || a.slug || a.name}
                    style={a.avatar_style || a.crew?.avatar_style}
                    className="mt-0.5 h-8 w-8 shrink-0 rounded-lg"
                  />
                  <div className="min-w-0">
                  <div className="text-sm">
                    <span className="font-medium">{a.name}</span>{" "}
                    <span className="text-[11px] text-muted-foreground">
                      {a.crew?.name ?? ""}
                      {actsAs ? (
                        <>
                          {a.crew?.name ? " · " : ""}acts as{" "}
                          <span className="font-mono text-foreground/80">{actsAs}</span>
                        </>
                      ) : null}
                    </span>
                  </div>
                  {bs.length > 0 ? (
                    <div className="mt-1.5 flex flex-wrap gap-1.5">
                      {bs.map((b) => (
                        <ScopeChip
                          key={b.toolkit}
                          toolkit={{ slug: b.toolkit }}
                          mode={b.mode}
                          count={b.tools?.length}
                        />
                      ))}
                    </div>
                  ) : (
                    <div className="mt-1 text-[12px] text-muted-foreground">
                      — no connector access —
                    </div>
                  )}
                  </div>
                </div>
                <button
                  type="button"
                  onClick={() => setEditing(a)}
                  className="shrink-0 text-xs text-blue-400 hover:text-blue-300"
                >
                  {bs.length > 0 ? "Edit" : "Assign"}
                </button>
              </div>
            )
          })}
        </div>
      )}

      <div className="flex flex-wrap items-center gap-x-3 gap-y-1 text-[11px] text-muted-foreground">
        <span>Scope legend —</span>
        <span className="text-emerald-400">● Full</span>
        <span>all tools</span>
        <span className="text-blue-300">● Read-only</span>
        <span>fetch/list/get/search</span>
        <span className="text-amber-300">● Custom</span>
        <span>hand-picked tools.</span>
      </div>

      {editing && (
        <AccessEditor
          workspaceId={workspaceId}
          agentId={editing.id}
          agentName={editing.name}
          agentCrew={editing.crew?.name}
          onClose={() => setEditing(null)}
          onSaved={() => {
            setEditing(null)
            onChanged()
          }}
        />
      )}
    </section>
  )
}
