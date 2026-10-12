"use client"

import * as React from "react"
import Link from "next/link"
import { motion, useReducedMotion } from "motion/react"
import { useSearchParams } from "next/navigation"
import { Bot, Globe, Pencil, Plus, Settings as SettingsIcon, ShieldAlert, Terminal, Trash2, Webhook, X, Zap } from "lucide-react"
import { toast } from "sonner"

import { cn } from "@/lib/utils"
import { apiFetch } from "@/lib/api-fetch"
import { useWorkspace } from "@/hooks/use-workspace"
import { useIsMobile } from "@/hooks/use-mobile"
import { isAdminTier } from "@/lib/permissions/tiers"
import { Button } from "@/components/ui/button"
import { Switch } from "@/components/ui/switch"
import { Skeleton } from "@/components/ui/skeleton"
import {
  AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent,
  AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle, AlertDialogTrigger,
} from "@/components/ui/alert-dialog"
import { DrillNavItem, DrillNavSection, DrillPage } from "@/components/layout/drill-page"
import { SidebarFacet, SidebarFacetOption, SidebarFilterPopover, SidebarSearch } from "@/components/layout/sidebar-kit"

import {
  DEFAULT_HOOK_FILTERS,
  HOOK_GROUPS,
  RETIRED_EVENTS,
  ago,
  eventInfo,
  hookMatches,
  outcomeTone,
  runsByHook,
  shortTarget,
  targetOf,
  type HookFilters,
  type HookRow,
  type HookRun,
} from "./hooks-model"
import { HookEditor } from "./hook-editor"

const KIND: Record<string, { label: string; icon: React.ElementType; tone: string }> = {
  http: { label: "Webhook", icon: Globe, tone: "bg-chip-info-bg text-chip-info-fg" },
  shell: { label: "Shell", icon: Terminal, tone: "bg-chip-danger-bg text-chip-danger-fg" },
  subagent: { label: "Agent", icon: Bot, tone: "bg-chip-violet-bg text-chip-violet-fg" },
}
const TONE_DOT = { ok: "bg-success", warn: "bg-warn", bad: "bg-destructive", none: "bg-muted-foreground/40" } as const
const TONE_TEXT = { ok: "text-success", warn: "text-warn", bad: "text-destructive", none: "text-muted-foreground" } as const

/** The hook's kind as a tile, the way Crew links shows a crew's icon. */
function KindTile({ kind, size = "sm" }: { kind: string; size?: "sm" | "lg" }) {
  const k = KIND[kind] ?? KIND.http
  return (
    <span aria-hidden className={cn("inline-grid shrink-0 place-items-center", k.tone, size === "lg" ? "h-12 w-12 rounded-xl" : "h-7 w-7 rounded-lg")}>
      <k.icon className={size === "lg" ? "h-6 w-6" : "h-3.5 w-3.5"} />
    </span>
  )
}

function Fact({ children, className }: { children: React.ReactNode; className?: string }) {
  return <span className={cn("inline-flex h-7 items-center gap-1.5 whitespace-nowrap rounded-lg border border-border bg-surface-subtle px-2.5 text-xs text-muted-foreground", className)}>{children}</span>
}

function Block({ title, count, hint, tone, children }: { title: string; count?: React.ReactNode; hint?: string; tone?: "danger"; children: React.ReactNode }) {
  return (
    <section className={cn("min-w-0 overflow-hidden rounded-card border bg-card", tone === "danger" ? "border-destructive/30" : "border-border")} aria-label={title}>
      <header className={cn("flex flex-wrap items-center gap-x-2 gap-y-0.5 border-b px-4 py-2.5", tone === "danger" ? "border-destructive/20 bg-destructive/5" : "border-border/60 bg-surface-subtle")}>
        <h3 className={cn("text-sm font-semibold", tone === "danger" && "text-destructive")}>{title}</h3>
        {count != null && <span className="font-mono text-micro text-muted-foreground">{count}</span>}
        {hint && <span className="ml-auto text-xs text-muted-foreground">{hint}</span>}
      </header>
      {children}
    </section>
  )
}

function Stat({ n, one, many, className }: { n: number; one: string; many: string; className?: string }) {
  return <span className={className}><span className="font-mono tabular-nums text-foreground">{n}</span> {n === 1 ? one : many}</span>
}

/**
 * Settings › Lifecycle hooks, as a nested page like Crew links and Members.
 *
 * It was a read-only list in the Settings column: the registry, the last
 * outcome, an enable switch — and no way to add or change a hook except the
 * CLI, so an empty list read as "nothing here" rather than "nothing set up
 * yet". Now the side panel lists hooks by what they react to, the main area
 * shows the open hook — what fires it, what it runs, where, whether it can
 * block — with its recent runs from the journal (hook.fired), and a hook is
 * created or edited in three plain steps (HookEditor).
 *
 * Reading is open to every member (GET /hooks is authed + workspace); every
 * write is ADMIN+ (roleManage), and a shell handler OWNER only — the editor
 * and the controls here follow the same lines.
 */
export function HooksPage() {
  const { workspaceId, role, loading: wsLoading } = useWorkspace()
  const isMobile = useIsMobile()
  const reduce = useReducedMotion()
  const canWrite = isAdminTier(role)
  const search = useSearchParams().toString()

  const [hooks, setHooks] = React.useState<HookRow[] | null>(null)
  const [loadError, setLoadError] = React.useState(false)
  const [runs, setRuns] = React.useState<Record<string, HookRun[]>>({})
  const [runsCapped, setRunsCapped] = React.useState(false)
  const [crews, setCrews] = React.useState<{ id: string; name: string; slug: string }[]>([])
  const [filters, setFiltersState] = React.useState<HookFilters>(DEFAULT_HOOK_FILTERS)
  const [selectedId, setSelectedId] = React.useState(() => new URLSearchParams(search).get("hook") ?? "")
  const [editorOpen, setEditorOpen] = React.useState(false)
  const [editing, setEditing] = React.useState<HookRow | null>(null)
  const [pending, setPending] = React.useState<Set<string>>(new Set())
  const [reload, setReload] = React.useState(0)

  React.useEffect(() => {
    if (!workspaceId) return
    let cancelled = false
    const ws = encodeURIComponent(workspaceId)
    apiFetch(`/api/v1/hooks?workspace_id=${ws}`)
      .then(async (res) => { if (!res.ok) throw new Error(String(res.status)); return res.json() })
      .then((body: unknown) => {
        if (cancelled) return
        // GET /hooks answers {count, rows}.
        const list = Array.isArray(body) ? body : Array.isArray((body as { rows?: unknown })?.rows) ? (body as { rows: unknown[] }).rows : []
        setHooks(list as HookRow[])
        setLoadError(false)
      })
      .catch(() => { if (!cancelled) { setHooks([]); setLoadError(true) } })
    // The run history is a bonus on top of the registry: an unreachable
    // journal leaves the hooks listed, just without "last fired".
    apiFetch(`/api/v1/journal?workspace_id=${ws}&entry_type=hook.fired,hook.blocked&limit=200`)
      .then(async (res) => { if (!res.ok) throw new Error(String(res.status)); return res.json() })
      .then((body: { entries?: unknown[]; next_cursor?: string }) => {
        if (cancelled) return
        setRuns(runsByHook(Array.isArray(body?.entries) ? (body.entries as Parameters<typeof runsByHook>[0]) : []))
        setRunsCapped(Boolean(body?.next_cursor))
      })
      .catch(() => { if (!cancelled) { setRuns({}); setRunsCapped(true) } })
    apiFetch(`/api/v1/crews?workspace_id=${ws}`)
      .then(async (res) => (res.ok ? res.json() : []))
      .then((body: unknown) => {
        if (cancelled) return
        const list = (Array.isArray(body) ? body : Array.isArray((body as { data?: unknown })?.data) ? (body as { data: unknown[] }).data : []) as { id: string; name: string; slug: string }[]
        setCrews(list)
      })
      .catch(() => {})
    return () => { cancelled = true }
  }, [workspaceId, reload])

  const setFilters = (next: Partial<HookFilters>) => setFiltersState((prev) => ({ ...prev, ...next }))
  const select = (id: string) => {
    setSelectedId(id)
    const url = new URL(window.location.href)
    if (id) url.searchParams.set("hook", id)
    else url.searchParams.delete("hook")
    window.history.replaceState(window.history.state, "", url.toString())
  }

  const all = hooks ?? []
  const order = (h: HookRow) => HOOK_GROUPS.findIndex((g) => g.id === eventInfo(h.event).group)
  const sorted = [...all].sort((a, b) => order(a) - order(b) || a.event.localeCompare(b.event) || a.created_at.localeCompare(b.created_at))
  const shown = sorted.filter((h) => hookMatches(h, filters, runs))
  const current = all.find((h) => h.id === selectedId) ?? shown[0] ?? null
  const crewName = (id?: string) => (id ? crews.find((c) => c.id === id)?.name ?? "A crew" : "Whole workspace")
  const countWhere = (f: Partial<HookFilters>) => sorted.filter((h) => hookMatches(h, { ...filters, ...f }, runs)).length

  const toggle = async (hook: HookRow) => {
    const next = !hook.enabled
    setPending((p) => new Set(p).add(hook.id))
    setHooks((prev) => prev?.map((h) => (h.id === hook.id ? { ...h, enabled: next } : h)) ?? prev)
    try {
      const res = await apiFetch(`/api/v1/hooks/${encodeURIComponent(hook.id)}/${next ? "enable" : "disable"}?workspace_id=${encodeURIComponent(workspaceId ?? "")}`, { method: "POST" })
      if (!res.ok) throw new Error(String(res.status))
    } catch {
      setHooks((prev) => prev?.map((h) => (h.id === hook.id ? { ...h, enabled: hook.enabled } : h)) ?? prev)
      toast.error(next ? "Enabling the hook was refused." : "Disabling the hook was refused.")
    } finally {
      setPending((p) => { const n = new Set(p); n.delete(hook.id); return n })
    }
  }

  const remove = async (hook: HookRow) => {
    const res = await apiFetch(`/api/v1/hooks/${encodeURIComponent(hook.id)}?workspace_id=${encodeURIComponent(workspaceId ?? "")}`, { method: "DELETE" }).catch(() => null)
    if (!res?.ok) { toast.error(res?.status === 403 ? "Your role cannot delete this hook." : "Deleting the hook failed."); return }
    setHooks((prev) => prev?.filter((h) => h.id !== hook.id) ?? prev)
    if (selectedId === hook.id) select("")
    toast.success("Hook deleted")
  }

  const openNew = () => { setEditing(null); setEditorOpen(true) }
  const openEdit = (h: HookRow) => { setEditing(h); setEditorOpen(true) }

  // ── Panel ──────────────────────────────────────────────────────────
  const popoverCount = Number(Boolean(filters.kind)) + Number(Boolean(filters.state))
  const toolbar = (
    <>
      <SidebarSearch value={filters.q} onValueChange={(q) => setFilters({ q })} placeholder="Search event, URL, command…" aria-label="Search hooks" />
      <SidebarFilterPopover label="Filter hooks" activeCount={popoverCount} onClear={() => setFilters({ kind: "", state: "" })} panelClassName="min-w-[220px]">
        <SidebarFacet label="Runs" resetLabel="Any handler" resetActive={!filters.kind} onReset={() => setFilters({ kind: "" })} first>
          {(["http", "shell", "subagent"] as const).map((k) => (
            <SidebarFacetOption key={k} active={filters.kind === k} onToggle={() => setFilters({ kind: filters.kind === k ? "" : k })}>
              {KIND[k].label}<span className="ml-auto font-mono text-micro text-muted-foreground">{countWhere({ kind: k })}</span>
            </SidebarFacetOption>
          ))}
        </SidebarFacet>
        <SidebarFacet label="State" resetLabel="Any state" resetActive={!filters.state} onReset={() => setFilters({ state: "" })}>
          {([["enabled", "Enabled"], ["disabled", "Disabled"], ["blocking", "Can block"], ["failing", "Failed last time"]] as const).map(([k, label]) => (
            <SidebarFacetOption key={k} active={filters.state === k} onToggle={() => setFilters({ state: filters.state === k ? "" : k })}>
              <span className={cn(k === "failing" && "text-destructive", k === "blocking" && "text-warn")}>{label}</span>
              <span className="ml-auto font-mono text-micro text-muted-foreground">{countWhere({ state: k })}</span>
            </SidebarFacetOption>
          ))}
        </SidebarFacet>
      </SidebarFilterPopover>
    </>
  )

  const hookItem = (h: HookRow, i: number) => {
    const last = runs[h.id]?.[0]
    const tone = outcomeTone(last?.outcome)
    return (
      <DrillNavItem key={h.id} index={i} selected={current?.id === h.id} onSelect={() => select(h.id)} muted={!h.enabled}
        title={`${eventInfo(h.event).label} · ${targetOf(h)}`}
        icon={<KindTile kind={h.handler_kind} />}
        label={eventInfo(h.event).label}
        sub={shortTarget(h)}
        meta={<span className={cn("inline-block h-2 w-2 rounded-full", h.enabled ? TONE_DOT[tone] : "bg-border")} title={h.enabled ? (last ? `${last.outcome} · ${ago(last.ts)}` : "Never fired") : "Disabled"} />} />
    )
  }
  const groups = HOOK_GROUPS.map((g) => ({ ...g, hooks: shown.filter((h) => eventInfo(h.event).group === g.id) })).filter((g) => g.hooks.length)

  const nav = (
    <>
      <DrillNavSection label="React to" count={all.length}>
        <DrillNavItem index={0} selected={!filters.group} pressed={!filters.group} onSelect={() => setFilters({ group: "" })}
          icon={<span className="h-1.5 w-1.5 shrink-0 rounded-full bg-border" aria-hidden />} label="Every event" meta={countWhere({ group: "" })} />
        {HOOK_GROUPS.map((g, i) => {
          const n = countWhere({ group: g.id })
          return (
            <DrillNavItem key={g.id} index={i + 1} selected={filters.group === g.id} pressed={filters.group === g.id} muted={!n}
              onSelect={() => setFilters({ group: filters.group === g.id ? "" : g.id })}
              icon={<span className={cn("h-1.5 w-1.5 shrink-0 rounded-full", n ? "bg-primary" : "bg-muted-foreground/40")} aria-hidden />} label={g.label} meta={n} />
          )
        })}
      </DrillNavSection>
      {groups.map((g, gi) => (
        <DrillNavSection key={g.id} label={g.label} count={g.hooks.length}>
          {g.hooks.map((h, i) => hookItem(h, gi * 10 + i))}
        </DrillNavSection>
      ))}
    </>
  )

  const mobileNav = all.length > 0 && current ? (
    <select aria-label="Hook" value={current.id} onChange={(e) => select(e.target.value)}
      className="h-10 w-full rounded-md border border-control-border bg-surface-subtle px-3 text-control">
      {groups.map((g) => (
        <optgroup key={g.id} label={g.label}>
          {g.hooks.map((h) => <option key={h.id} value={h.id}>{eventInfo(h.event).label} · {shortTarget(h)}</option>)}
        </optgroup>
      ))}
    </select>
  ) : undefined

  const newButton = canWrite && (
    <Button size="sm" className="h-7 gap-1.5 px-3 text-xs" onClick={openNew}><Plus className="h-3.5 w-3.5" />New hook</Button>
  )

  // ── Summary and chips ──────────────────────────────────────────────
  const enabled = all.filter((h) => h.enabled).length
  const blocking = all.filter((h) => h.enabled && h.blocking && eventInfo(h.event).canBlock).length
  const failing = all.filter((h) => outcomeTone(runs[h.id]?.[0]?.outcome) === "bad").length
  const chips: { key: string; label: string; clear: Partial<HookFilters> }[] = []
  if (filters.q.trim()) chips.push({ key: "q", label: `“${filters.q.trim()}”`, clear: { q: "" } })
  if (filters.group) chips.push({ key: "group", label: HOOK_GROUPS.find((g) => g.id === filters.group)!.label, clear: { group: "" } })
  if (filters.kind) chips.push({ key: "kind", label: KIND[filters.kind].label, clear: { kind: "" } })
  if (filters.state) chips.push({ key: "state", label: { enabled: "Enabled", disabled: "Disabled", blocking: "Can block", failing: "Failed last time" }[filters.state], clear: { state: "" } })

  return (
    <>
      <DrillPage
        parent={{ href: "/settings", label: "Settings", icon: SettingsIcon }}
        title="Lifecycle hooks"
        icon={Webhook}
        description="Code this workspace runs when something happens on the platform"
        toolbar={toolbar}
        nav={nav}
        mobileNav={mobileNav}
        filterCount={chips.length}
        actions={isMobile ? undefined : newButton || undefined}
      >
        {wsLoading || hooks === null ? (
          <div className="space-y-3 p-4 md:p-6"><Skeleton className="h-10 rounded-lg" /><Skeleton className="h-[320px] rounded-card" /></div>
        ) : (
          <div className="space-y-4 p-4 md:p-6">
            {isMobile && newButton && <div data-slot="hooks-actions">{newButton}</div>}
            {chips.length > 0 && (
              <div className="flex flex-wrap items-center gap-1.5">
                {chips.map((c) => (
                  <motion.span key={c.key} initial={reduce ? false : { scale: 0.85, opacity: 0 }} animate={{ scale: 1, opacity: 1 }} transition={{ duration: 0.18 }}
                    className="inline-flex h-6 items-center gap-1 rounded-full bg-primary/10 pl-2.5 pr-1 text-micro font-medium text-primary-hover">
                    {c.label}
                    <button type="button" onClick={() => setFilters(c.clear)} aria-label={`Remove filter ${c.label}`} className="grid h-4 w-4 place-items-center rounded-full hover:bg-primary/20"><X className="h-3 w-3" /></button>
                  </motion.span>
                ))}
                <button type="button" onClick={() => setFiltersState(DEFAULT_HOOK_FILTERS)} className="px-1.5 text-label text-muted-foreground hover:text-foreground">Clear all</button>
              </div>
            )}
            <div className="flex flex-wrap items-center gap-x-5 gap-y-1.5 text-xs text-muted-foreground" data-slot="hooks-summary">
              <Stat n={all.length} one="hook" many="hooks" />
              <Stat n={enabled} one="enabled" many="enabled" />
              <Stat n={blocking} one="can block an action" many="can block an action" className={cn(blocking > 0 && "text-warn")} />
              <Stat n={failing} one="failed last time" many="failed last time" className={cn(failing > 0 && "text-destructive")} />
              {!canWrite && <span className="ml-auto">Read-only — admins add and change hooks.</span>}
            </div>
            {loadError && (
              <p role="alert" className="rounded-lg border border-destructive/30 bg-destructive/5 px-3 py-2 text-xs text-destructive">
                The hooks could not be loaded. <button type="button" className="underline" onClick={() => setReload((n) => n + 1)}>Retry</button>
              </p>
            )}

            {all.length === 0 && !loadError ? (
              <EmptyState canWrite={canWrite} onNew={openNew} />
            ) : current ? (
              <motion.div key={current.id} initial={reduce ? false : { opacity: 0, y: 6 }} animate={{ opacity: 1, y: 0 }} transition={{ duration: 0.22, ease: [0.2, 0.7, 0.2, 1] }}>
                <HookDetail hook={current} runs={runs[current.id] ?? []} runsCapped={runsCapped} crewName={crewName(current.crew_id)}
                  canWrite={canWrite} isOwner={role === "OWNER"} pending={pending.has(current.id)}
                  onToggle={() => void toggle(current)} onEdit={() => openEdit(current)} onDelete={() => remove(current)} />
              </motion.div>
            ) : (
              <p className="rounded-card border border-border bg-card px-4 py-8 text-center text-xs text-muted-foreground">No hook matches these filters.</p>
            )}
          </div>
        )}
      </DrillPage>
      {workspaceId && (
        <HookEditor open={editorOpen} onOpenChange={setEditorOpen} workspaceId={workspaceId} role={role} crews={crews} editing={editing}
          onSaved={(saved) => {
            toast.success(editing ? "Hook saved" : "Hook created")
            if (saved?.id) select(saved.id)
            setReload((n) => n + 1)
          }} />
      )}
    </>
  )
}

function HookDetail({ hook, runs, runsCapped, crewName, canWrite, isOwner, pending, onToggle, onEdit, onDelete }: {
  hook: HookRow; runs: HookRun[]; runsCapped: boolean; crewName: string; canWrite: boolean; isOwner: boolean; pending: boolean
  onToggle: () => void; onEdit: () => void; onDelete: () => Promise<void>
}) {
  const info = eventInfo(hook.event)
  const kind = KIND[hook.handler_kind] ?? KIND.http
  const retired = RETIRED_EVENTS.has(hook.event)
  const blockingLive = info.canBlock && hook.blocking
  // An ADMIN can toggle any hook but edit or delete a shell one only as OWNER.
  const canChange = canWrite && (hook.handler_kind !== "shell" || isOwner)
  const last = runs[0]
  const matcher = hook.matcher ?? {}
  return (
    <div className="space-y-4" data-slot="hook-detail">
      <section className="grid grid-cols-[auto_minmax(0,1fr)] gap-x-4 gap-y-3 rounded-card border border-border bg-card p-4 sm:grid-cols-[auto_minmax(0,1fr)_auto]" aria-label={`${info.label} hook`}>
        <KindTile kind={hook.handler_kind} size="lg" />
        <div className="min-w-0">
          <h2 className="text-lg font-semibold tracking-[-0.02em]">{info.label}</h2>
          <p className="font-mono text-micro text-muted-foreground">{hook.event}</p>
          <p className="mt-1 max-w-[72ch] text-control text-muted-foreground">{info.when}</p>
          <div className="mt-2.5 flex flex-wrap gap-1.5">
            <Fact><kind.icon className="h-3.5 w-3.5" aria-hidden />{kind.label}</Fact>
            <Fact>{crewName}</Fact>
            <Fact className={cn(blockingLive && "border-warn/30 bg-warn/5 text-warn")}>
              {blockingLive ? <><ShieldAlert className="h-3.5 w-3.5" aria-hidden />Can block</> : <><Zap className="h-3.5 w-3.5" aria-hidden />Observes</>}
            </Fact>
            <Fact className={cn(last && TONE_TEXT[outcomeTone(last.outcome)])}>
              <span className={cn("h-2 w-2 rounded-full", TONE_DOT[outcomeTone(last?.outcome)])} aria-hidden />
              {last ? `${last.outcome} · ${ago(last.ts)}` : "Never fired"}
            </Fact>
          </div>
        </div>
        <div className="col-span-2 flex items-center gap-3 sm:col-span-1 sm:flex-col sm:items-end">
          <label className="flex items-center gap-2 text-xs text-muted-foreground">
            {hook.enabled ? "Enabled" : "Disabled"}
            <Switch checked={hook.enabled} onCheckedChange={onToggle} disabled={!canWrite || pending} aria-label={`${hook.enabled ? "Disable" : "Enable"} the ${hook.event} hook`} />
          </label>
          {canChange && <Button variant="outline" size="sm" className="h-7 gap-1.5 px-2.5 text-xs" onClick={onEdit}><Pencil className="h-3 w-3" />Edit</Button>}
        </div>
      </section>

      {retired && (
        <p role="alert" className="rounded-lg border border-warn/30 bg-warn/5 px-3 py-2 text-xs text-warn">
          This event is no longer dispatched, so the hook can never run. Delete it, or re-create it on an event that fires (e.g. After a tool runs).
        </p>
      )}

      <div className="grid items-start gap-4 xl:grid-cols-2">
        <Block title="What it runs" hint={hook.handler_kind === "shell" ? "on the Crewship host" : undefined}>
          <dl className="grid grid-cols-[7rem_minmax(0,1fr)] gap-x-3 gap-y-2 px-4 py-3 text-xs">
            <dt className="text-muted-foreground">{hook.handler_kind === "http" ? "URL" : hook.handler_kind === "shell" ? "Command" : "Agent"}</dt>
            <dd className="break-all font-mono">{targetOf(hook)}</dd>
            {hook.handler_kind === "http" && <><dt className="text-muted-foreground">Signed</dt><dd>{hook.handler_config?.secret ? "Yes — X-Crewship-Signature" : "No"}</dd></>}
            <dt className="text-muted-foreground">Scope</dt><dd>{crewName}</dd>
            {(matcher.severities?.length ?? 0) > 0 && <><dt className="text-muted-foreground">Severities</dt><dd>{matcher.severities!.join(", ")}</dd></>}
            {(matcher.tools?.length ?? 0) > 0 && <><dt className="text-muted-foreground">Tools</dt><dd className="font-mono">{matcher.tools!.join(", ")}</dd></>}
            <dt className="text-muted-foreground">Mode</dt>
            <dd>{blockingLive ? "Waits for the hook; a refusal cancels the action" : info.canBlock ? "Observes — could block, blocking is off" : "Observes — runs after, cannot cancel"}</dd>
          </dl>
          {hook.handler_kind === "shell" && (
            <p className="flex items-start gap-2 border-t border-destructive/20 bg-destructive/5 px-4 py-2 text-label text-destructive">
              <ShieldAlert className="mt-px h-3.5 w-3.5 shrink-0" />Runs as the Crewship service on the host, outside any container. Only the owner can change it.
            </p>
          )}
        </Block>

        <Block title="Recent runs" count={runs.length} hint="from the journal">
          {runs.length === 0 ? (
            <p className="px-4 py-3 text-xs text-muted-foreground">{runsCapped ? "No run in the recent journal window." : "Has not fired yet — it will, the next time this event happens."}</p>
          ) : (
            <ul>
              {runs.slice(0, 6).map((r, i) => {
                const tone = outcomeTone(r.outcome)
                return (
                  <li key={`${r.ts}-${i}`} className="flex items-center gap-2.5 border-b border-border/40 px-4 py-2 text-xs last:border-b-0">
                    <span className={cn("h-2 w-2 shrink-0 rounded-full", TONE_DOT[tone])} aria-hidden />
                    <span className={cn("font-medium", TONE_TEXT[tone])}>{r.outcome}</span>
                    {r.latencyMs != null && <span className="font-mono text-muted-foreground">{r.latencyMs} ms</span>}
                    {r.message && <span className="min-w-0 truncate text-muted-foreground" title={r.message}>{r.message}</span>}
                    <span className="ml-auto shrink-0 text-muted-foreground">{ago(r.ts)}</span>
                  </li>
                )
              })}
            </ul>
          )}
          <Link href="/journal?q=hook.fired" className="block border-t border-border/40 px-4 py-2 text-xs text-primary-hover hover:bg-[var(--row-hover-bg)]">Open in Journal →</Link>
        </Block>
      </div>

      {canChange && (
        <Block title="Delete hook" tone="danger">
          <div className="flex flex-wrap items-center gap-3 px-4 py-3">
            <p className="min-w-0 flex-1 basis-56 text-xs text-muted-foreground">The hook stops running at once. Its past runs stay in the journal.</p>
            <AlertDialog>
              <AlertDialogTrigger asChild>
                <Button variant="outline" size="sm" className="h-8 gap-1.5 px-3 text-xs text-destructive hover:bg-destructive/10 hover:text-destructive"><Trash2 className="h-3.5 w-3.5" />Delete hook</Button>
              </AlertDialogTrigger>
              <AlertDialogContent>
                <AlertDialogHeader>
                  <AlertDialogTitle className="text-sm">Delete hook</AlertDialogTitle>
                  <AlertDialogDescription className="text-xs">Delete the <span className="font-mono">{hook.event}</span> hook? This cannot be undone.</AlertDialogDescription>
                </AlertDialogHeader>
                <AlertDialogFooter>
                  <AlertDialogCancel className="h-7 text-xs">Cancel</AlertDialogCancel>
                  <AlertDialogAction className="h-7 bg-destructive text-xs text-destructive-foreground hover:bg-destructive/90" onClick={() => void onDelete()}>Delete</AlertDialogAction>
                </AlertDialogFooter>
              </AlertDialogContent>
            </AlertDialog>
          </div>
        </Block>
      )}
    </div>
  )
}

/** Nothing registered yet: what hooks are for, with the three most common
 *  first hooks, rather than an empty frame. */
function EmptyState({ canWrite, onNew }: { canWrite: boolean; onNew: () => void }) {
  const ideas = [
    { icon: Globe, title: "Tell Slack when an agent waits for approval", event: "on_approval_requested" },
    { icon: ShieldAlert, title: "Page someone when a guardrail trips", event: "on_guardrail_triggered" },
    { icon: Zap, title: "Log every finished agent run to your system", event: "post_agent_stop" },
  ]
  return (
    <section className="rounded-card border border-border bg-card p-6 text-center" aria-label="No hooks yet">
      <span className="mx-auto mb-3 grid h-12 w-12 place-items-center rounded-xl bg-primary/10 text-primary-hover"><Webhook className="h-6 w-6" /></span>
      <h2 className="text-base font-semibold">No hooks yet</h2>
      <p className="mx-auto mt-1 max-w-md text-xs text-muted-foreground">
        A hook runs your code when something happens — an agent starts, a model is called, a budget is exceeded. Hooks that run before an action can also stop it.
      </p>
      <ul className="mx-auto mt-4 grid max-w-2xl gap-2 text-left sm:grid-cols-3">
        {ideas.map((i) => (
          <li key={i.event} className="rounded-lg border border-border bg-surface-subtle p-3">
            <i.icon className="mb-1.5 h-4 w-4 text-muted-foreground" aria-hidden />
            <div className="text-xs font-medium">{i.title}</div>
            <div className="mt-0.5 font-mono text-micro text-muted-foreground">{i.event}</div>
          </li>
        ))}
      </ul>
      {canWrite ? (
        <Button size="sm" className="mt-5 gap-1.5" onClick={onNew}><Plus className="h-3.5 w-3.5" />New hook</Button>
      ) : (
        <p className="mt-4 text-label text-muted-foreground">An admin can add hooks here.</p>
      )}
    </section>
  )
}
