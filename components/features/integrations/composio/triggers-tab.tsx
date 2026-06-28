"use client"

import * as React from "react"
import { Zap, Search } from "lucide-react"

import { Button } from "@/components/ui/button"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { apiFetch } from "@/lib/api-fetch"
import { ToolkitIcon, EmptyHint, TableSkeleton } from "./shared"
import type { TriggerType, TriggerTypesResp, TriggerInstance, ActiveTriggersResp } from "./types"

// TriggersTab — event subscriptions (e.g. a new Gmail message wakes an agent).
// Lists the live trigger instances plus the available trigger types (filterable
// by toolkit / search); "Enable" opens a small modal to subscribe a user.
export function TriggersTab({
  workspaceId,
  users,
}: {
  workspaceId: string
  users: string[]
}) {
  const [active, setActive] = React.useState<TriggerInstance[]>([])
  const [activeLoading, setActiveLoading] = React.useState(true)

  const [types, setTypes] = React.useState<TriggerType[]>([])
  const [typesLoading, setTypesLoading] = React.useState(true)
  const [toolkit, setToolkit] = React.useState("")
  const [search, setSearch] = React.useState("")
  const [err, setErr] = React.useState<string | null>(null)

  const [enable, setEnable] = React.useState<TriggerType | null>(null)

  const loadActive = React.useCallback(async () => {
    setActiveLoading(true)
    try {
      const r = await apiFetch(
        `/api/v1/integrations/composio/triggers/active?workspace_id=${workspaceId}`,
      )
      if (!r.ok) throw new Error(String(r.status))
      const j = (await r.json()) as ActiveTriggersResp
      setActive(j.triggers ?? [])
    } catch {
      setActive([])
    } finally {
      setActiveLoading(false)
    }
  }, [workspaceId])

  React.useEffect(() => {
    void loadActive()
  }, [loadActive])

  React.useEffect(() => {
    const ctrl = new AbortController()
    const t = setTimeout(async () => {
      setTypesLoading(true)
      setErr(null)
      try {
        const params = new URLSearchParams({ workspace_id: workspaceId })
        if (toolkit.trim()) params.set("toolkit", toolkit.trim())
        if (search.trim()) params.set("search", search.trim())
        const r = await apiFetch(`/api/v1/integrations/composio/triggers?${params}`, {
          signal: ctrl.signal,
        })
        if (!r.ok) throw new Error(`Failed (${r.status})`)
        const j = (await r.json()) as TriggerTypesResp
        setTypes(j.triggers ?? [])
      } catch (e) {
        if ((e as Error).name !== "AbortError") {
          setErr(e instanceof Error ? e.message : "Failed to load triggers")
          setTypes([])
        }
      } finally {
        setTypesLoading(false)
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
        <Zap className="h-3.5 w-3.5" /> Triggers
        <span className="font-normal normal-case tracking-normal text-muted-foreground/70">
          · event subscriptions
        </span>
      </h2>

      {/* Active instances */}
      <div className="rounded-xl border border-white/10 bg-card p-3">
        <div className="text-[11px] font-semibold uppercase tracking-wider text-muted-foreground">
          Active triggers
        </div>
        {activeLoading ? (
          <div className="mt-2">
            <TableSkeleton rows={2} />
          </div>
        ) : active.length === 0 ? (
          <p className="mt-2 text-[11px] text-muted-foreground">
            No active triggers yet. Enable one below to wake an agent on events like{" "}
            <span className="font-mono">GMAIL_NEW_MESSAGE</span>.
          </p>
        ) : (
          <div className="mt-2 space-y-1.5">
            {active.map((t) => (
              <div
                key={t.id}
                className="flex items-center justify-between gap-3 rounded-lg border border-white/[0.06] bg-white/[0.02] px-2.5 py-1.5 text-[12px]"
              >
                <span className="font-mono">{t.trigger_name}</span>
                <span className="font-mono text-[11px] text-muted-foreground">{t.user_id}</span>
                <span
                  className={
                    t.disabled_at
                      ? "text-[11px] text-amber-400"
                      : "text-[11px] text-emerald-400"
                  }
                >
                  ● {t.disabled_at ? "disabled" : "live"}
                </span>
              </div>
            ))}
          </div>
        )}
      </div>

      {/* Available types */}
      <div className="flex flex-wrap items-center gap-2">
        <input
          value={toolkit}
          onChange={(e) => setToolkit(e.target.value)}
          placeholder="Filter by toolkit (gmail…)"
          className="w-48 rounded-lg border border-white/10 bg-card px-3 py-1.5 text-xs text-foreground placeholder:text-muted-foreground focus:border-blue-400/50 focus:outline-none"
        />
        <div className="relative">
          <Search className="pointer-events-none absolute left-2.5 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-muted-foreground" />
          <input
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            placeholder="Search trigger types…"
            className="w-56 rounded-lg border border-white/10 bg-card py-1.5 pl-8 pr-3 text-xs text-foreground placeholder:text-muted-foreground focus:border-blue-400/50 focus:outline-none"
          />
        </div>
      </div>

      {err && <div className="text-[11px] text-red-400">{err}</div>}

      {typesLoading ? (
        <TableSkeleton rows={5} />
      ) : types.length === 0 ? (
        <EmptyHint text="No trigger types match." />
      ) : (
        <div className="overflow-hidden rounded-xl border border-white/10 bg-card">
          <table className="w-full border-collapse">
            <thead>
              <tr>
                {["Trigger", "Name", "Type", ""].map((h, i) => (
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
              {types.map((t) => (
                <tr key={t.slug} className="border-t border-white/[0.06]">
                  <td className="px-3 py-2.5">
                    <span className="flex items-center gap-2">
                      <ToolkitIcon toolkit={t.toolkit} size={16} />
                      <span className="font-mono text-[11px] text-foreground/90">{t.slug}</span>
                    </span>
                  </td>
                  <td className="px-3 py-2.5 text-[13px]">{t.name}</td>
                  <td className="px-3 py-2.5 text-[11px] text-muted-foreground">{t.type}</td>
                  <td className="px-3 py-2.5 text-right">
                    <button
                      type="button"
                      onClick={() => setEnable(t)}
                      className="text-xs text-blue-400 hover:text-blue-300"
                    >
                      Enable
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      {enable && (
        <TriggerModal
          workspaceId={workspaceId}
          trigger={enable}
          users={users}
          onClose={() => setEnable(null)}
          onCreated={() => {
            setEnable(null)
            void loadActive()
          }}
        />
      )}
    </section>
  )
}

function TriggerModal({
  workspaceId,
  trigger,
  users,
  onClose,
  onCreated,
}: {
  workspaceId: string
  trigger: TriggerType
  users: string[]
  onClose: () => void
  onCreated: () => void
}) {
  const [userId, setUserId] = React.useState(users[0] ?? "")
  const [busy, setBusy] = React.useState(false)
  const [err, setErr] = React.useState<string | null>(null)

  const create = async () => {
    const uid = userId.trim()
    if (!uid) {
      setErr("Pick or enter a user id.")
      return
    }
    setBusy(true)
    setErr(null)
    try {
      const r = await apiFetch(`/api/v1/integrations/composio/triggers?workspace_id=${workspaceId}`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ slug: trigger.slug, user_id: uid }),
      })
      if (!r.ok) {
        const body = await r.json().catch(() => null)
        throw new Error(body?.detail || `Failed (${r.status})`)
      }
      onCreated()
    } catch (e) {
      setErr(e instanceof Error ? e.message : "Failed to create trigger")
    } finally {
      setBusy(false)
    }
  }

  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="block max-w-md rounded-xl border-white/10 bg-card shadow-2xl sm:max-w-md">
        <DialogHeader>
          <DialogTitle className="text-base">Enable trigger</DialogTitle>
          <DialogDescription className="text-xs leading-relaxed">
            Subscribe a Composio user to{" "}
            <span className="font-mono text-foreground/90">{trigger.slug}</span>. Events for that
            user&apos;s connected account will fire this trigger.
          </DialogDescription>
        </DialogHeader>

        <div className="mt-4 space-y-3">
          {users.length > 0 && (
            <div>
              <label className="mb-1 block text-xs text-muted-foreground">For user</label>
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
        </div>

        {err && <div className="mt-3 text-xs text-red-400">{err}</div>}

        <div className="mt-5 flex justify-end gap-2">
          <Button variant="outline" size="sm" onClick={onClose} disabled={busy}>
            Cancel
          </Button>
          <Button size="sm" onClick={create} disabled={busy || !userId.trim()}>
            {busy ? "Creating…" : "Create trigger"}
          </Button>
        </div>
      </DialogContent>
    </Dialog>
  )
}
