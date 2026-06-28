"use client"

import * as React from "react"
import { Plug, Plus, Search, X } from "lucide-react"

import { useWorkspace } from "@/hooks/use-workspace"
import { Button } from "@/components/ui/button"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from "@/components/ui/popover"
import {
  Command,
  CommandEmpty,
  CommandGroup,
  CommandInput,
  CommandItem,
  CommandList,
} from "@/components/ui/command"
import { cn } from "@/lib/utils"
import { apiFetch } from "@/lib/api-fetch"
import {
  ToolkitIcon,
  ScopeChip,
  EmptyHint,
  TableSkeleton,
  toolkitLabel,
  isReadTool,
} from "./shared"
import type {
  AgentBinding,
  BindingMode,
  Inventory,
  Tool,
  Toolkit,
  ToolsResp,
} from "./types"

// ──────────────────────────────────────────────────────────────────────────
// AccessEditor — the single shared editor for an agent's Composio access,
// reused by the Integrations → Agent access tab and the per-agent Connectors
// card on the agent settings page. It loads the agent's current per-app
// bindings + the workspace inventory, lets an operator pick the "acts as" user,
// set a scope (Full / Read-only / Custom / Off) per granted app, hand-pick
// tools for custom apps, and add more of the user's connected apps. Save POSTs
// the full grant (apps omitted = removed).
// ──────────────────────────────────────────────────────────────────────────

type Scope = BindingMode | "off"

type EditorApp = {
  toolkit: Toolkit
  mode: Scope
  // Selected tool slugs (only meaningful for custom mode).
  tools: Set<string>
}

const SCOPE_OPTIONS: { value: Scope; label: string }[] = [
  { value: "full", label: "Full access" },
  { value: "read", label: "Read-only" },
  { value: "custom", label: "Custom…" },
  { value: "off", label: "Off" },
]

export function AccessEditor({
  workspaceId,
  agentId,
  agentName,
  agentCrew,
  onClose,
  onSaved,
}: {
  workspaceId: string
  agentId: string
  agentName: string
  agentCrew?: string | null
  onClose: () => void
  onSaved: () => void
}) {
  const [loading, setLoading] = React.useState(true)
  const [loadErr, setLoadErr] = React.useState<string | null>(null)
  const [inventory, setInventory] = React.useState<Inventory | null>(null)
  const [userId, setUserId] = React.useState("")
  const [apps, setApps] = React.useState<EditorApp[]>([])
  const [addOpen, setAddOpen] = React.useState(false)
  const [busy, setBusy] = React.useState(false)
  const [err, setErr] = React.useState<string | null>(null)

  // Load current bindings + inventory once on open.
  React.useEffect(() => {
    let alive = true
    setLoading(true)
    setLoadErr(null)
    ;(async () => {
      try {
        const [invR, bindR] = await Promise.all([
          apiFetch(`/api/v1/integrations/composio/inventory?workspace_id=${workspaceId}`),
          apiFetch(
            `/api/v1/integrations/composio/agents/${agentId}/bind?workspace_id=${workspaceId}`,
          ),
        ])
        if (!invR.ok) throw new Error(`Inventory failed (${invR.status})`)
        const inv = (await invR.json()) as Inventory
        const bindings: AgentBinding[] = bindR.ok
          ? ((await bindR.json()) as { bindings?: AgentBinding[] }).bindings ?? []
          : []
        if (!alive) return

        // slug → logo, harvested from connected accounts so chips/icons render
        // brand marks even though bindings only carry a slug.
        const logos = new Map<string, string | undefined>()
        inv.users.forEach((u) =>
          u.connected_accounts.forEach((a) => {
            if (a.toolkit?.slug) logos.set(a.toolkit.slug, a.toolkit.logo)
          }),
        )

        setInventory(inv)
        const firstUser = bindings[0]?.user_id || inv.users[0]?.user_id || ""
        setUserId(firstUser)
        setApps(
          bindings.map((b) => ({
            toolkit: { slug: b.toolkit, logo: logos.get(b.toolkit) },
            mode: b.mode,
            tools: new Set(b.tools ?? []),
          })),
        )
      } catch (e) {
        if (alive) setLoadErr(e instanceof Error ? e.message : "Failed to load access")
      } finally {
        if (alive) setLoading(false)
      }
    })()
    return () => {
      alive = false
    }
  }, [workspaceId, agentId])

  const users = React.useMemo(
    () => (inventory?.users ?? []).map((u) => u.user_id),
    [inventory],
  )

  // Connected toolkits for the selected user — the pool the "+ Add app" menu
  // draws from (you can only grant apps that user has actually connected).
  const userToolkits = React.useMemo<Toolkit[]>(() => {
    const u = inventory?.users.find((x) => x.user_id === userId)
    if (!u) return []
    const seen = new Set<string>()
    const out: Toolkit[] = []
    u.connected_accounts.forEach((a) => {
      if (a.toolkit?.slug && !seen.has(a.toolkit.slug)) {
        seen.add(a.toolkit.slug)
        out.push(a.toolkit)
      }
    })
    return out
  }, [inventory, userId])

  const grantedSlugs = React.useMemo(
    () => new Set(apps.map((a) => a.toolkit.slug)),
    [apps],
  )
  const addable = userToolkits.filter((t) => !grantedSlugs.has(t.slug))

  const setMode = (slug: string, mode: Scope) =>
    setApps((prev) => prev.map((a) => (a.toolkit.slug === slug ? { ...a, mode } : a)))

  const removeApp = (slug: string) =>
    setApps((prev) => prev.filter((a) => a.toolkit.slug !== slug))

  const addApp = (t: Toolkit) =>
    setApps((prev) =>
      prev.some((a) => a.toolkit.slug === t.slug)
        ? prev
        : [...prev, { toolkit: t, mode: "full", tools: new Set<string>() }],
    )

  const setTools = (slug: string, next: Set<string>) =>
    setApps((prev) =>
      prev.map((a) => (a.toolkit.slug === slug ? { ...a, tools: next } : a)),
    )

  const save = async () => {
    const uid = userId.trim()
    if (!uid) {
      setErr("Pick the user this agent acts as.")
      return
    }
    setBusy(true)
    setErr(null)
    try {
      const payload = {
        user_id: uid,
        apps: apps
          .filter((a) => a.mode !== "off")
          .map((a) => ({
            toolkit: a.toolkit.slug,
            mode: a.mode as BindingMode,
            ...(a.mode === "custom" ? { tools: Array.from(a.tools) } : {}),
          })),
      }
      const r = await apiFetch(
        `/api/v1/integrations/composio/agents/${agentId}/bind?workspace_id=${workspaceId}`,
        {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify(payload),
        },
      )
      if (!r.ok) {
        const body = await r.json().catch(() => null)
        throw new Error(body?.detail || `Failed (${r.status})`)
      }
      onSaved()
    } catch (e) {
      setErr(e instanceof Error ? e.message : "Failed to save access")
    } finally {
      setBusy(false)
    }
  }

  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="block max-h-[88vh] max-w-lg overflow-y-auto rounded-xl border-white/10 bg-card shadow-2xl sm:max-w-lg">
        <DialogHeader>
          <DialogTitle className="text-base">Edit access — {agentName}</DialogTitle>
          <DialogDescription className="text-xs leading-relaxed">
            {agentCrew ? `${agentCrew} · ` : ""}Crewship provisions one scoped MCP server
            per grant — {agentName} sees only the apps and tools you allow here.
          </DialogDescription>
        </DialogHeader>

        {loading ? (
          <div className="mt-4">
            <TableSkeleton rows={4} />
          </div>
        ) : loadErr ? (
          <div className="mt-4 text-xs text-red-400">{loadErr}</div>
        ) : (
          <div className="mt-4 space-y-4">
            {/* Acts-as user */}
            <div className="flex flex-wrap items-center gap-2">
              <span className="text-xs text-muted-foreground">Acts as</span>
              <select
                value={users.includes(userId) ? userId : ""}
                onChange={(e) => setUserId(e.target.value)}
                className="rounded-lg border border-white/10 bg-background px-2.5 py-1.5 font-mono text-xs focus:border-blue-400/50 focus:outline-none"
              >
                {!users.includes(userId) && <option value="">— pick a user —</option>}
                {users.map((u) => (
                  <option key={u} value={u}>
                    {u}
                  </option>
                ))}
              </select>
              {users.length === 0 && (
                <span className="text-[11px] text-muted-foreground">
                  No connected users yet — connect an account first.
                </span>
              )}
            </div>

            {/* Granted apps */}
            <div>
              <div className="mb-1.5 text-[11px] text-muted-foreground">
                Granted apps — one scope each:
              </div>
              {apps.length === 0 ? (
                <EmptyHint text="No apps granted yet. Use “+ Add app” to grant one of this user's connected apps." />
              ) : (
                <div className="overflow-hidden rounded-xl border border-white/10">
                  {apps.map((app) => (
                    <div key={app.toolkit.slug} className="border-t border-white/[0.06] first:border-t-0">
                      <div className="flex items-center justify-between gap-2 px-3.5 py-2.5">
                        <span className="flex min-w-0 items-center gap-2 text-sm">
                          <ToolkitIcon toolkit={app.toolkit} size={16} />
                          <span className="truncate font-medium">
                            {toolkitLabel(app.toolkit.slug)}
                          </span>
                        </span>
                        <span className="flex shrink-0 items-center gap-2">
                          <select
                            value={app.mode}
                            onChange={(e) => setMode(app.toolkit.slug, e.target.value as Scope)}
                            className={cn(
                              "rounded-lg border border-white/10 bg-background px-2 py-1 text-xs focus:border-blue-400/50 focus:outline-none",
                              app.mode === "off" && "text-muted-foreground",
                            )}
                          >
                            {SCOPE_OPTIONS.map((o) => (
                              <option key={o.value} value={o.value}>
                                {o.label}
                              </option>
                            ))}
                          </select>
                          <button
                            type="button"
                            onClick={() => removeApp(app.toolkit.slug)}
                            className="text-muted-foreground transition-colors hover:text-foreground"
                            aria-label={`Remove ${app.toolkit.slug}`}
                          >
                            <X className="h-3.5 w-3.5" />
                          </button>
                        </span>
                      </div>
                      {app.mode === "custom" && (
                        <ToolPicker
                          workspaceId={workspaceId}
                          toolkit={app.toolkit.slug}
                          selected={app.tools}
                          onChange={(next) => setTools(app.toolkit.slug, next)}
                        />
                      )}
                    </div>
                  ))}
                </div>
              )}

              {/* Add app — a Popover combobox that portals out of this
                  scrollable dialog, so the menu is never clipped by the
                  dialog's overflow (the old absolute dropdown forced a
                  full-modal scroll to reach its options). */}
              <div className="mt-2 flex flex-wrap items-center gap-2">
                <AddAppMenu
                  toolkits={addable}
                  onPick={addApp}
                  open={addOpen}
                  onOpenChange={setAddOpen}
                  disabled={addable.length === 0}
                />
                {addable.length === 0 ? (
                  <span className="text-[11px] text-muted-foreground">
                    {userToolkits.length === 0
                      ? "this user has no connected apps"
                      : "all connected apps already granted"}
                  </span>
                ) : (
                  <span className="text-[11px] text-muted-foreground">
                    only {userId || "the user"}&apos;s connected apps
                  </span>
                )}
              </div>
            </div>
          </div>
        )}

        {err && <div className="mt-3 text-xs text-red-400">{err}</div>}

        <div className="mt-5 flex items-center gap-2">
          <Button size="sm" onClick={save} disabled={busy || loading || !!loadErr}>
            {busy ? "Saving…" : "Save access"}
          </Button>
          <Button variant="ghost" size="sm" onClick={onClose} disabled={busy}>
            Cancel
          </Button>
          <span className="ml-auto hidden text-[11px] text-muted-foreground sm:block">
            Apps set to Off are removed on save.
          </span>
        </div>
      </DialogContent>
    </Dialog>
  )
}

// A searchable combobox of toolkits to grant, rendered in a Popover that
// portals out of the (scrollable) dialog so the menu is never clipped by the
// dialog's overflow. cmdk's Command gives keyboard search + arrow-key nav for
// free. Open state is controlled by the parent so it can be closed on pick.
export function AddAppMenu({
  toolkits,
  onPick,
  open,
  onOpenChange,
  disabled,
}: {
  toolkits: Toolkit[]
  onPick: (t: Toolkit) => void
  open: boolean
  onOpenChange: (open: boolean) => void
  disabled?: boolean
}) {
  return (
    <Popover open={open} onOpenChange={onOpenChange}>
      <PopoverTrigger asChild>
        <Button variant="outline" size="sm" disabled={disabled}>
          <Plus className="h-3.5 w-3.5" />
          Add app
        </Button>
      </PopoverTrigger>
      <PopoverContent align="start" className="w-64 p-0">
        <Command>
          <CommandInput
            aria-label="Search apps"
            placeholder="Search apps…"
            className="text-xs"
          />
          <CommandList>
            <CommandEmpty className="px-2 py-3 text-center text-[11px] text-muted-foreground">
              No matching apps.
            </CommandEmpty>
            <CommandGroup>
              {toolkits.map((t) => (
                <CommandItem
                  key={t.slug}
                  value={toolkitLabel(t.slug)}
                  onSelect={() => {
                    onPick(t)
                    onOpenChange(false)
                  }}
                  className="gap-2 text-xs"
                >
                  <ToolkitIcon toolkit={t} size={14} />
                  <span className="capitalize">{toolkitLabel(t.slug)}</span>
                </CommandItem>
              ))}
            </CommandGroup>
          </CommandList>
        </Command>
      </PopoverContent>
    </Popover>
  )
}

// Inline tool picker for a custom-scoped app. Fetches the toolkit's tools
// (debounced search), with Read-only / All / None quick selects operating on
// the currently-loaded rows, and a scrollable checkbox list with a read/write
// hint per tool.
function ToolPicker({
  workspaceId,
  toolkit,
  selected,
  onChange,
}: {
  workspaceId: string
  toolkit: string
  selected: Set<string>
  onChange: (next: Set<string>) => void
}) {
  const [search, setSearch] = React.useState("")
  const [tools, setTools] = React.useState<Tool[]>([])
  const [total, setTotal] = React.useState(0)
  const [loading, setLoading] = React.useState(true)
  const [err, setErr] = React.useState<string | null>(null)

  React.useEffect(() => {
    const ctrl = new AbortController()
    const t = setTimeout(async () => {
      setLoading(true)
      setErr(null)
      try {
        const params = new URLSearchParams({ workspace_id: workspaceId, toolkit })
        if (search.trim()) params.set("search", search.trim())
        const r = await apiFetch(`/api/v1/integrations/composio/tools?${params}`, {
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

  const toggle = (slug: string) => {
    const next = new Set(selected)
    if (next.has(slug)) next.delete(slug)
    else next.add(slug)
    onChange(next)
  }

  // Quick selects operate on the loaded rows so they stay predictable when a
  // big toolkit is narrowed by search.
  const selectAll = () => {
    const next = new Set(selected)
    tools.forEach((t) => next.add(t.slug))
    onChange(next)
  }
  const selectReadOnly = () => {
    const next = new Set(selected)
    tools.forEach((t) => (isReadTool(t.slug) ? next.add(t.slug) : next.delete(t.slug)))
    onChange(next)
  }
  const selectNone = () => {
    const next = new Set(selected)
    tools.forEach((t) => next.delete(t.slug))
    onChange(next)
  }

  return (
    <div className="mx-3.5 mb-3 rounded-lg border border-dashed border-white/10 bg-white/[0.02] p-2.5">
      <div className="mb-2 flex flex-wrap items-center justify-between gap-2">
        <div className="relative">
          <Search className="pointer-events-none absolute left-2.5 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-muted-foreground" />
          <input
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            placeholder={`Search ${total || ""} tools…`}
            className="w-44 rounded-lg border border-white/10 bg-background py-1.5 pl-8 pr-2 text-xs focus:border-blue-400/50 focus:outline-none"
          />
        </div>
        <div className="flex items-center gap-1.5">
          <button
            type="button"
            onClick={selectReadOnly}
            className="rounded-md border border-white/10 px-2 py-0.5 text-[11px] text-muted-foreground hover:text-foreground"
          >
            Read-only
          </button>
          <button
            type="button"
            onClick={selectAll}
            className="rounded-md border border-white/10 px-2 py-0.5 text-[11px] text-muted-foreground hover:text-foreground"
          >
            All
          </button>
          <button
            type="button"
            onClick={selectNone}
            className="rounded-md border border-white/10 px-2 py-0.5 text-[11px] text-muted-foreground hover:text-foreground"
          >
            None
          </button>
          <span className="text-[10px] text-muted-foreground">{selected.size} selected</span>
        </div>
      </div>

      {err ? (
        <div className="py-2 text-[11px] text-red-400">{err}</div>
      ) : loading ? (
        <TableSkeleton rows={4} />
      ) : tools.length === 0 ? (
        <div className="py-3 text-center text-[11px] text-muted-foreground">No tools found.</div>
      ) : (
        <div className="max-h-56 overflow-y-auto">
          {tools.map((t) => {
            const read = isReadTool(t.slug)
            const on = selected.has(t.slug)
            return (
              <label
                key={t.slug}
                className="flex cursor-pointer items-center justify-between gap-2 border-t border-white/[0.05] py-1.5 first:border-t-0"
              >
                <span className="flex min-w-0 items-center gap-2">
                  <input
                    type="checkbox"
                    checked={on}
                    onChange={() => toggle(t.slug)}
                    className="h-3.5 w-3.5 shrink-0 accent-blue-500"
                  />
                  <span className="truncate font-mono text-[11px] text-foreground/90">
                    {t.slug}
                  </span>
                </span>
                <span
                  className={cn(
                    "shrink-0 rounded-full border border-white/10 px-1.5 py-px text-[9px]",
                    read ? "text-muted-foreground" : "text-amber-300",
                  )}
                >
                  {read ? "read" : "write"}
                </span>
              </label>
            )
          })}
          {total > tools.length && (
            <div className="pt-2 text-[10px] text-muted-foreground">
              Showing {tools.length} of {total} — search to narrow.
            </div>
          )}
        </div>
      )}
    </div>
  )
}

// ──────────────────────────────────────────────────────────────────────────
// AgentConnectorsCard — a compact summary of an agent's Composio access for
// the agent settings page. Shows "acts as <user>" + scope chips and a "Manage
// access" button that opens the SAME AccessEditor. Self-contained: it resolves
// the workspace and fetches the agent's bindings itself, so it drops into any
// agent surface with just agentId + agentName.
// ──────────────────────────────────────────────────────────────────────────
export function AgentConnectorsCard({
  agentId,
  agentName,
  agentCrew,
  workspaceId: workspaceIdProp,
  className,
}: {
  agentId: string
  agentName: string
  agentCrew?: string | null
  workspaceId?: string
  className?: string
}) {
  const ws = useWorkspace()
  const workspaceId = workspaceIdProp ?? ws.workspaceId
  const [bindings, setBindings] = React.useState<AgentBinding[]>([])
  const [loading, setLoading] = React.useState(true)
  const [editing, setEditing] = React.useState(false)

  const load = React.useCallback(async () => {
    if (!workspaceId) return
    setLoading(true)
    try {
      const r = await apiFetch(
        `/api/v1/integrations/composio/agents/${agentId}/bind?workspace_id=${workspaceId}`,
      )
      if (!r.ok) {
        setBindings([])
        return
      }
      const j = (await r.json()) as { bindings?: AgentBinding[] }
      setBindings(j.bindings ?? [])
    } catch {
      setBindings([])
    } finally {
      setLoading(false)
    }
  }, [workspaceId, agentId])

  React.useEffect(() => {
    void load()
  }, [load])

  const actsAs = bindings[0]?.user_id

  return (
    <div className={cn("rounded-xl border border-white/10 bg-card p-4", className)}>
      <div className="flex items-center justify-between gap-3">
        <span className="flex items-center gap-1.5 text-sm font-medium">
          <Plug className="h-3.5 w-3.5 text-foreground/60" />
          Connectors
          <span className="text-xs font-normal text-muted-foreground">· Composio</span>
        </span>
        <Button
          variant="outline"
          size="sm"
          onClick={() => setEditing(true)}
          disabled={!workspaceId}
        >
          Manage access
        </Button>
      </div>

      <div className="mt-3">
        {loading ? (
          <TableSkeleton rows={1} />
        ) : bindings.length === 0 ? (
          <div className="text-[12px] text-muted-foreground">— no connector access —</div>
        ) : (
          <div className="flex flex-wrap items-center gap-1.5">
            {actsAs && (
              <span className="text-[12px] text-muted-foreground">
                acts as <span className="font-mono text-foreground/80">{actsAs}</span> ·
              </span>
            )}
            {bindings.map((b) => (
              <ScopeChip
                key={b.toolkit}
                toolkit={{ slug: b.toolkit }}
                mode={b.mode}
                count={b.tools?.length}
              />
            ))}
          </div>
        )}
      </div>

      {editing && workspaceId && (
        <AccessEditor
          workspaceId={workspaceId}
          agentId={agentId}
          agentName={agentName}
          agentCrew={agentCrew}
          onClose={() => setEditing(false)}
          onSaved={() => {
            setEditing(false)
            void load()
          }}
        />
      )}
    </div>
  )
}
