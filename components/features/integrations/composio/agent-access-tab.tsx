"use client"

import * as React from "react"
import { Bot } from "lucide-react"

import { Button } from "@/components/ui/button"
import { cn } from "@/lib/utils"
import { ToolkitIcon, AppChip, EmptyHint, TableSkeleton } from "./shared"
import type { AgentLite, AgentBindingsMap, Inventory, Toolkit } from "./types"

// AgentAccessTab — which agent acts as which Composio user, and therefore which
// connected accounts (apps) it can call. Binding maps an agent → a user_id;
// the apps it sees are that user's connected accounts (or the restricted
// subset). Assign/Edit opens AssignModal; Remove access deletes the binding.
export function AgentAccessTab({
  workspaceId,
  agents,
  bindings,
  data,
  loading,
  onChanged,
}: {
  workspaceId: string
  agents: AgentLite[]
  bindings: AgentBindingsMap
  data: Inventory
  loading: boolean
  onChanged: () => void
}) {
  const [assign, setAssign] = React.useState<AgentLite | null>(null)

  // The set of connectable toolkit slugs (auth configs + connected accounts) —
  // the chips an operator can toggle to restrict an agent's access.
  const allToolkits = React.useMemo(() => {
    const seen = new Set<string>()
    const list: Toolkit[] = []
    const add = (t: Toolkit) => {
      if (t.slug && !seen.has(t.slug)) {
        seen.add(t.slug)
        list.push(t)
      }
    }
    data.auth_configs.forEach((ac) => add(ac.toolkit))
    data.users.forEach((u) => u.connected_accounts.forEach((a) => add(a.toolkit)))
    return list
  }, [data])

  // user_id → connected-account toolkits (the apps an agent bound to that user
  // can reach).
  const appsForUser = React.useCallback(
    (userId: string): Toolkit[] => {
      const u = data.users.find((x) => x.user_id === userId)
      return u ? u.connected_accounts.map((a) => a.toolkit) : []
    },
    [data],
  )

  return (
    <section className="space-y-3">
      <h2 className="flex items-center gap-1.5 text-xs font-semibold uppercase tracking-wider text-muted-foreground">
        <Bot className="h-3.5 w-3.5" /> Agent access
        <span className="font-normal normal-case tracking-normal text-muted-foreground/70">
          · which agent acts as which user
        </span>
      </h2>

      {loading ? (
        <TableSkeleton rows={4} />
      ) : agents.length === 0 ? (
        <EmptyHint text="No agents in this workspace yet." />
      ) : (
        <div className="overflow-hidden rounded-xl border border-white/10 bg-card">
          <table className="w-full border-collapse">
            <thead>
              <tr>
                {["Agent", "Crew", "Bound user", "Apps it can use", ""].map((h, i) => (
                  <th
                    key={i}
                    className="px-3 py-2.5 text-left text-[10px] font-semibold uppercase tracking-wider text-muted-foreground"
                  >
                    {h}
                  </th>
                ))}
              </tr>
            </thead>
            <tbody>
              {agents.map((a) => {
                const bound = bindings[a.id]?.[0]
                const apps = bound ? appsForUser(bound.user_id) : []
                return (
                  <tr key={a.id} className="border-t border-white/[0.06]">
                    <td className="px-3 py-2.5 text-sm font-medium">{a.name}</td>
                    <td className="px-3 py-2.5 text-[13px] text-muted-foreground">
                      {a.crew?.name ?? "—"}
                    </td>
                    <td className="px-3 py-2.5">
                      {bound ? (
                        <span className="font-mono text-xs text-foreground/90">
                          {bound.user_id}
                        </span>
                      ) : (
                        <span className="text-[11px] text-muted-foreground">— not bound —</span>
                      )}
                    </td>
                    <td className="px-3 py-2.5">
                      {bound ? (
                        apps.length > 0 ? (
                          <span className="flex flex-wrap gap-1.5">
                            {apps.map((t) => (
                              <AppChip key={t.slug} toolkit={t} />
                            ))}
                          </span>
                        ) : (
                          <span className="text-[11px] text-muted-foreground">
                            all of {bound.user_id}&apos;s accounts
                          </span>
                        )
                      ) : (
                        <span className="text-[11px] text-muted-foreground">no access</span>
                      )}
                    </td>
                    <td className="px-3 py-2.5 text-right">
                      <button
                        type="button"
                        onClick={() => setAssign(a)}
                        className="text-xs text-blue-400 hover:text-blue-300"
                      >
                        {bound ? "Edit" : "Assign"}
                      </button>
                    </td>
                  </tr>
                )
              })}
            </tbody>
          </table>
        </div>
      )}

      {assign && (
        <AssignModal
          workspaceId={workspaceId}
          agent={assign}
          current={bindings[assign.id]?.[0] ?? null}
          users={data.users.map((u) => u.user_id)}
          toolkits={allToolkits}
          onClose={() => setAssign(null)}
          onSaved={() => {
            setAssign(null)
            onChanged()
          }}
        />
      )}
    </section>
  )
}

function AssignModal({
  workspaceId,
  agent,
  current,
  users,
  toolkits,
  onClose,
  onSaved,
}: {
  workspaceId: string
  agent: AgentLite
  current: { user_id: string } | null
  users: string[]
  toolkits: Toolkit[]
  onClose: () => void
  onSaved: () => void
}) {
  const [userId, setUserId] = React.useState(current?.user_id ?? users[0] ?? "")
  const [selected, setSelected] = React.useState<Set<string>>(new Set())
  const [busy, setBusy] = React.useState(false)
  const [err, setErr] = React.useState<string | null>(null)

  const toggle = (slug: string) =>
    setSelected((s) => {
      const next = new Set(s)
      if (next.has(slug)) next.delete(slug)
      else next.add(slug)
      return next
    })

  const save = async () => {
    const uid = userId.trim()
    if (!uid) {
      setErr("Pick or enter a Composio user id.")
      return
    }
    setBusy(true)
    setErr(null)
    try {
      const r = await fetch(
        `/api/v1/integrations/composio/agents/${agent.id}/bind?workspace_id=${workspaceId}`,
        {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ user_id: uid, toolkits: Array.from(selected) }),
        },
      )
      if (!r.ok) {
        const body = await r.json().catch(() => null)
        throw new Error(body?.detail || `Failed (${r.status})`)
      }
      onSaved()
    } catch (e) {
      setErr(e instanceof Error ? e.message : "Failed to save binding")
    } finally {
      setBusy(false)
    }
  }

  const remove = async () => {
    if (!current) return
    setBusy(true)
    setErr(null)
    try {
      const r = await fetch(
        `/api/v1/integrations/composio/agents/${agent.id}/bind?workspace_id=${workspaceId}&user_id=${encodeURIComponent(current.user_id)}`,
        { method: "DELETE" },
      )
      if (!r.ok) {
        const body = await r.json().catch(() => null)
        throw new Error(body?.detail || `Failed (${r.status})`)
      }
      onSaved()
    } catch (e) {
      setErr(e instanceof Error ? e.message : "Failed to remove access")
    } finally {
      setBusy(false)
    }
  }

  return (
    <div
      className="fixed inset-0 z-50 flex items-center justify-center bg-black/60 p-4"
      onClick={onClose}
    >
      <div
        className="w-full max-w-md rounded-xl border border-white/10 bg-card p-6 shadow-2xl"
        onClick={(e) => e.stopPropagation()}
      >
        <h2 className="text-base font-semibold text-foreground">
          {current ? "Edit" : "Assign"} agent access
        </h2>
        <p className="mt-1 text-xs leading-relaxed text-muted-foreground">
          <span className="font-medium text-foreground/90">{agent.name}</span>
          {agent.crew?.name ? ` (${agent.crew.name})` : ""} acts as the chosen Composio user.
          Crewship generates a scoped MCP URL for this agent.
        </p>

        <div className="mt-4 space-y-3">
          {users.length > 0 && (
            <div>
              <label className="mb-1 block text-xs text-muted-foreground">Acts as user</label>
              <select
                value={users.includes(userId) ? userId : ""}
                onChange={(e) => setUserId(e.target.value)}
                className="w-full rounded-lg border border-white/10 bg-background px-3 py-2 font-mono text-xs focus:border-blue-400/50 focus:outline-none"
              >
                <option value="">— enter a user id —</option>
                {users.map((u) => (
                  <option key={u} value={u}>
                    {u}
                  </option>
                ))}
              </select>
            </div>
          )}
          <div>
            <label className="mb-1 block text-xs text-muted-foreground">User id</label>
            <input
              value={userId}
              onChange={(e) => setUserId(e.target.value)}
              placeholder="e.g. alice@acme.com"
              className="w-full rounded-lg border border-white/10 bg-background px-3 py-2 font-mono text-xs focus:border-blue-400/50 focus:outline-none"
            />
          </div>

          {toolkits.length > 0 && (
            <div>
              <label className="mb-1 block text-xs text-muted-foreground">
                Restrict to apps (optional)
              </label>
              <div className="flex flex-wrap gap-2">
                {toolkits.map((t) => {
                  const on = selected.has(t.slug)
                  return (
                    <button
                      key={t.slug}
                      type="button"
                      onClick={() => toggle(t.slug)}
                      className={cn(
                        "inline-flex items-center gap-1.5 rounded-lg border px-2 py-1 text-[11px] transition-colors",
                        on
                          ? "border-blue-400/40 bg-blue-500/10 text-blue-300"
                          : "border-white/10 bg-white/[0.03] text-muted-foreground hover:text-foreground",
                      )}
                    >
                      <ToolkitIcon toolkit={t} size={14} />
                      <span className="capitalize">{t.slug}</span>
                    </button>
                  )
                })}
              </div>
              <p className="mt-1.5 text-[11px] text-muted-foreground">
                Empty = all of the user&apos;s connected accounts.
              </p>
            </div>
          )}
        </div>

        {err && <div className="mt-3 text-xs text-red-400">{err}</div>}

        <div className="mt-5 flex items-center justify-between gap-2">
          <div>
            {current && (
              <Button variant="ghost" size="sm" onClick={remove} disabled={busy}>
                Remove access
              </Button>
            )}
          </div>
          <div className="flex gap-2">
            <Button variant="outline" size="sm" onClick={onClose} disabled={busy}>
              Cancel
            </Button>
            <Button size="sm" onClick={save} disabled={busy || !userId.trim()}>
              {busy ? "Saving…" : "Save binding"}
            </Button>
          </div>
        </div>
      </div>
    </div>
  )
}
