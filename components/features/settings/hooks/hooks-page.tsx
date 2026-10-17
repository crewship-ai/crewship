"use client"

import * as React from "react"
import Link from "next/link"
import { useReducedMotion } from "motion/react"
import { useSearchParams } from "next/navigation"
import { ArrowRight, Bot, Globe, List, Map as MapIcon, Pencil, Plus, Search, Settings as SettingsIcon, ShieldAlert, Terminal, Trash2, Webhook, X } from "lucide-react"
import { toast } from "@/lib/toast"

import { cn } from "@/lib/utils"
import { apiFetch } from "@/lib/api-fetch"
import { useWorkspace } from "@/hooks/use-workspace"
import { useIsMobile } from "@/hooks/use-mobile"
import { isAdminTier } from "@/lib/permissions/tiers"
import { Button } from "@/components/ui/button"
import { Switch } from "@/components/ui/switch"
import { Skeleton } from "@/components/ui/skeleton"
import { Sheet, SheetContent, SheetDescription, SheetTitle } from "@/components/ui/sheet"
import {
  AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent,
  AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle, AlertDialogTrigger,
} from "@/components/ui/alert-dialog"
import { DrillPage } from "@/components/layout/drill-page"
import { SettingsCard } from "@/components/features/settings/shared"

import {
  DEFAULT_HOOK_FILTERS,
  LIFECYCLE_STEPS,
  POLICY_EVENTS,
  RETIRED_EVENTS,
  ago,
  eventInfo,
  hookMatches,
  outcomeTone,
  runsByHook,
  shortTarget,
  slotOf,
  targetOf,
  type HookFilters,
  type HookRow,
  type HookRun,
} from "./hooks-model"
import { HookEditor } from "./hook-editor"

const KIND: Record<string, { label: string; tag: string; icon: React.ElementType; tone: string }> = {
  http: { label: "Webhook", tag: "HTTP", icon: Globe, tone: "bg-chip-info-bg text-chip-info-fg" },
  shell: { label: "Command on the server", tag: "SHELL", icon: Terminal, tone: "bg-chip-danger-bg text-chip-danger-fg" },
  subagent: { label: "Agent", tag: "AGENT", icon: Bot, tone: "bg-chip-violet-bg text-chip-violet-fg" },
}
const TONE_DOT = { ok: "bg-success", warn: "bg-warn", bad: "bg-destructive", none: "bg-muted-foreground/40" } as const
const TONE_TEXT = { ok: "text-success", warn: "text-warn", bad: "text-destructive", none: "text-muted-foreground" } as const
/** `.eyebrow` without its colour, so a heading can be amber or red. */
const EYEBROW = "font-mono text-[11px] font-medium uppercase leading-4 tracking-[0.08em]"

const STATES: { key: HookFilters["state"]; label: string }[] = [
  { key: "", label: "All" },
  { key: "enabled", label: "On" },
  { key: "disabled", label: "Off" },
  { key: "blocking", label: "Can stop" },
  { key: "failing", label: "Failed last time" },
]

type View = "map" | "list"

/**
 * Settings › Lifecycle hooks: a map of where in an agent run your own code
 * can run, and what is hooked in there now.
 *
 * Fourteen events read as a flat list hid the two things that matter: they
 * are BEFORE / AFTER pairs around seven steps of a run, and only the BEFORE
 * side can stop anything. So the page is the run itself — rows are the steps
 * in order, columns are "before · can stop it" and "after · gets told", and
 * policy alerts (approval, budget, guardrail) sit in their own row because
 * they can come at any point. Every slot shows its hooks as cards; an empty
 * one offers "+ Add" with the event already chosen. With nothing registered
 * the map still explains what hooks can do, which a blank list never did.
 *
 * A hook opens in a side panel (full screen on a phone): what it runs, its
 * scope and mode, its recent runs from the journal, on/off, edit, delete.
 * List view is the same hooks as rows, for searching.
 *
 * Reading is open to every member (GET /hooks is authed + workspace); every
 * write is ADMIN+ (roleManage), and a shell handler OWNER only.
 */
export function HooksPage() {
  const { workspaceId, role, loading: wsLoading } = useWorkspace()
  const isMobile = useIsMobile()
  const reduce = useReducedMotion()
  const canWrite = isAdminTier(role)
  const isOwner = role === "OWNER"
  const search = useSearchParams().toString()

  const [hooks, setHooks] = React.useState<HookRow[] | null>(null)
  const [loadError, setLoadError] = React.useState(false)
  const [runs, setRuns] = React.useState<Record<string, HookRun[]>>({})
  const [runsCapped, setRunsCapped] = React.useState(false)
  const [crews, setCrews] = React.useState<{ id: string; name: string; slug: string }[]>([])
  const [filters, setFiltersState] = React.useState<HookFilters>(DEFAULT_HOOK_FILTERS)
  const [view, setView] = React.useState<View>("map")
  const [openId, setOpenId] = React.useState("")
  const [editorOpen, setEditorOpen] = React.useState(false)
  const [editing, setEditing] = React.useState<HookRow | null>(null)
  const [presetEvent, setPresetEvent] = React.useState<string | undefined>()
  const [pending, setPending] = React.useState<Set<string>>(new Set())
  const [reload, setReload] = React.useState(0)

  // ?hook= (a link to one hook) is read on arrival, then dropped: the side
  // panel keeps its own history entry for the back button, and a URL written
  // under it would come back when that entry unwinds.
  React.useEffect(() => {
    const id = new URLSearchParams(search).get("hook")
    if (!id) return
    setOpenId(id)
    const url = new URL(window.location.href)
    url.searchParams.delete("hook")
    window.history.replaceState(window.history.state, "", url.toString())
  }, [search])

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
    // journal leaves the hooks on the map, just without "last fired".
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
  const all = React.useMemo(() => hooks ?? [], [hooks])
  const shown = all.filter((h) => hookMatches(h, filters, runs))
  const filtering = filters.q.trim() !== "" || filters.state !== ""
  const opened = all.find((h) => h.id === openId) ?? null
  const crewName = (id?: string) => (id ? crews.find((c) => c.id === id)?.name ?? "A crew" : "Whole workspace")
  const inSlot = (event: string) => shown.filter((h) => h.event === event || (event === "pre_tool_call" && RETIRED_EVENTS.has(h.event)))

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
    if (openId === hook.id) setOpenId("")
    toast.success("Hook deleted")
  }

  const openNew = (event?: string) => { setEditing(null); setPresetEvent(event); setEditorOpen(true) }
  const openEdit = (h: HookRow) => { setOpenId(""); setEditing(h); setPresetEvent(undefined); setEditorOpen(true) }

  const enabled = all.filter((h) => h.enabled).length
  const blocking = all.filter((h) => h.enabled && h.blocking && eventInfo(h.event).canBlock).length
  const failing = all.filter((h) => outcomeTone(runs[h.id]?.[0]?.outcome) === "bad").length

  const newButton = canWrite && (
    <Button size="sm" className="h-7 gap-1.5 px-3 text-xs" onClick={() => openNew()}><Plus className="h-3.5 w-3.5" />New hook</Button>
  )
  const slotProps = { inSlot, runs, canWrite, filtering, onOpen: (h: HookRow) => setOpenId(h.id), onAdd: openNew }

  return (
    <>
      <DrillPage
        parent={{ href: "/settings", label: "Settings", icon: SettingsIcon }}
        title="Lifecycle hooks"
        icon={Webhook}
        description="Your code, run at fixed points of every agent run"
        actions={newButton || undefined}
      >
        {wsLoading || hooks === null ? (
          <div className="space-y-3 p-4 md:p-6"><Skeleton className="h-10 rounded-lg" /><Skeleton className="h-[420px] rounded-card" /></div>
        ) : (
          <div className="space-y-4 p-4 md:p-6">
            <div className="flex flex-wrap items-center gap-x-5 gap-y-2 text-xs text-muted-foreground" data-slot="hooks-summary">
              <Stat n={all.length} one="hook" many="hooks" />
              <Stat n={enabled} one="on" many="on" />
              <button type="button" onClick={() => setFilters({ state: filters.state === "blocking" ? "" : "blocking" })} className="hover:text-foreground">
                <Stat n={blocking} one="can stop an action" many="can stop an action" className={cn(blocking > 0 && "text-warn")} />
              </button>
              <button type="button" onClick={() => setFilters({ state: filters.state === "failing" ? "" : "failing" })} className="hover:text-foreground">
                <Stat n={failing} one="failed last time" many="failed last time" className={cn(failing > 0 && "text-destructive")} />
              </button>
              {!canWrite && <span>Read-only — admins add and change hooks.</span>}
            </div>

            {loadError && (
              <p role="alert" className="rounded-lg border border-destructive/30 bg-destructive/5 px-3 py-2 text-xs text-destructive">
                The hooks could not be loaded. <button type="button" className="underline" onClick={() => setReload((n) => n + 1)}>Retry</button>
              </p>
            )}

            <Toolbar filters={filters} setFilters={setFilters} view={view} setView={setView} hasHooks={all.length > 0} />

            <FlowStrip reduce={Boolean(reduce)} />

            {all.length === 0 && !loadError && (
              <p className="rounded-lg border border-primary/25 bg-primary/[0.06] px-3 py-2 text-xs text-foreground/85" data-slot="hooks-intro">
                No hooks yet. Each slot below is a point where your code can run — <span className="text-warn">before</span> a step it can stop it, <span className="text-primary-hover">after</span> it hears what happened.{canWrite ? " “+ Add” starts a hook right there." : " An admin can add hooks."}
              </p>
            )}

            {view === "list" && all.length > 0 ? (
              <HookList hooks={shown} runs={runs} crewName={crewName} onOpen={(h) => setOpenId(h.id)} />
            ) : isMobile ? (
              <MapStack {...slotProps} />
            ) : (
              <MapGrid {...slotProps} />
            )}
          </div>
        )}
      </DrillPage>

      <Sheet open={opened !== null} onOpenChange={(o) => { if (!o) setOpenId("") }}>
        <SheetContent side="right" className="w-full gap-0 overflow-y-auto p-5 sm:max-w-[440px]" data-slot="hook-sheet">
          <SheetTitle className="sr-only">{opened ? eventInfo(opened.event).label : "Hook"}</SheetTitle>
          <SheetDescription className="sr-only">What the hook runs, where, and its recent runs</SheetDescription>
          {opened && (
            <HookDetail hook={opened} runs={runs[opened.id] ?? []} runsCapped={runsCapped} crewName={crewName(opened.crew_id)}
              canWrite={canWrite} isOwner={isOwner} pending={pending.has(opened.id)}
              onToggle={() => void toggle(opened)} onEdit={() => openEdit(opened)} onDelete={() => remove(opened)} />
          )}
        </SheetContent>
      </Sheet>

      {workspaceId && (
        <HookEditor open={editorOpen} onOpenChange={setEditorOpen} workspaceId={workspaceId} role={role} crews={crews}
          editing={editing} presetEvent={presetEvent}
          onSaved={(saved) => {
            toast.success(editing ? "Hook saved" : "Hook created")
            if (saved?.id) setOpenId(saved.id)
            setReload((n) => n + 1)
          }} />
      )}
    </>
  )
}

function Stat({ n, one, many, className }: { n: number; one: string; many: string; className?: string }) {
  return <span className={className}><span className="font-mono tabular-nums text-foreground">{n}</span> {n === 1 ? one : many}</span>
}

function Toolbar({ filters, setFilters, view, setView, hasHooks }: {
  filters: HookFilters
  setFilters: (f: Partial<HookFilters>) => void
  view: View
  setView: (v: View) => void
  hasHooks: boolean
}) {
  if (!hasHooks) return null
  return (
    <div className="flex flex-wrap items-center gap-2" data-slot="hooks-toolbar">
      <label className="relative min-w-[200px] flex-1 sm:max-w-[280px]">
        <span className="sr-only">Search hooks</span>
        <Search className="pointer-events-none absolute left-2.5 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-muted-foreground" aria-hidden />
        <input value={filters.q} onChange={(e) => setFilters({ q: e.target.value })} placeholder="Search URL, command or event"
          className="h-8 w-full rounded-md border border-control-border bg-background pl-8 pr-7 text-xs outline-none focus-visible:ring-2 focus-visible:ring-ring/40" />
        {filters.q && (
          <button type="button" aria-label="Clear search" onClick={() => setFilters({ q: "" })} className="absolute right-1.5 top-1/2 grid h-5 w-5 -translate-y-1/2 place-items-center rounded text-muted-foreground hover:text-foreground">
            <X className="h-3 w-3" />
          </button>
        )}
      </label>
      <div role="group" aria-label="Show" className="flex flex-wrap gap-1">
        {STATES.map((s) => (
          <button key={s.key || "all"} type="button" aria-pressed={filters.state === s.key} onClick={() => setFilters({ state: s.key })}
            className={cn("h-7 rounded-full border px-2.5 text-xs transition-colors",
              filters.state === s.key ? "border-primary/40 bg-primary/10 text-primary-hover" : "border-border text-muted-foreground hover:text-foreground")}>
            {s.label}
          </button>
        ))}
      </div>
      <div role="group" aria-label="View" className="ml-auto inline-flex gap-0.5 rounded-lg border border-border bg-surface-subtle p-0.5">
        {([["map", "Map", MapIcon], ["list", "List", List]] as const).map(([k, label, Icon]) => (
          <button key={k} type="button" aria-pressed={view === k} onClick={() => setView(k)}
            className={cn("inline-flex h-6 items-center gap-1 rounded-md px-2.5 text-xs font-medium transition-colors", view === k ? "bg-card text-foreground shadow-xs" : "text-muted-foreground hover:text-foreground")}>
            <Icon className="h-3 w-3" aria-hidden />{label}
          </button>
        ))}
      </div>
    </div>
  )
}

/** One agent run in a line, so the rows below read as its steps. */
function FlowStrip({ reduce }: { reduce: boolean }) {
  const go = (id: string) => document.getElementById(`hook-step-${id}`)?.scrollIntoView({ behavior: reduce ? "auto" : "smooth", block: "center" })
  return (
    <div className="rounded-card border border-border bg-card px-4 py-3" data-slot="hooks-flow">
      <p className="eyebrow mb-2 text-primary-hover">One agent run</p>
      <ol className="flex flex-wrap items-center gap-x-1.5 gap-y-2">
        {LIFECYCLE_STEPS.map((s, i) => (
          <li key={s.id} className="flex items-center gap-1.5">
            <button type="button" onClick={() => go(s.id)}
              className="inline-flex h-7 items-center gap-1.5 rounded-md border border-border bg-surface-subtle px-2.5 text-xs hover:border-primary/40 hover:text-foreground">
              <span className="font-mono text-micro text-primary-hover">{i + 1}</span>{s.label}
            </button>
            {i < LIFECYCLE_STEPS.length - 1 && <ArrowRight className="h-3 w-3 text-muted-foreground-soft" aria-hidden />}
          </li>
        ))}
        <li className="ml-auto">
          <button type="button" onClick={() => go("policy")}
            className="inline-flex h-7 items-center gap-1.5 rounded-md border border-destructive/25 bg-destructive/5 px-2.5 text-xs text-destructive hover:border-destructive/50">
            <ShieldAlert className="h-3 w-3" aria-hidden />Policy alerts — any time
          </button>
        </li>
      </ol>
    </div>
  )
}

interface SlotProps {
  inSlot: (event: string) => HookRow[]
  runs: Record<string, HookRun[]>
  canWrite: boolean
  filtering: boolean
  onOpen: (h: HookRow) => void
  onAdd: (event: string) => void
}

/** The map on a wide screen: steps down, before / after across. */
function MapGrid(p: SlotProps) {
  const head = cn(EYEBROW, "px-4 py-2")
  return (
    <SettingsCard icon={Webhook} title="Lifecycle map" description="Before a step your hook can stop it · after a step it gets told" bodyClassName="overflow-x-auto">
      <div className="min-w-[760px]" data-slot="hooks-map" role="table" aria-label="Where hooks can run">
        <div role="row" className="grid grid-cols-[minmax(0,15rem)_minmax(0,1fr)_minmax(0,1fr)] border-b border-border/60 bg-surface-subtle">
          <span role="columnheader" className={cn(head, "text-muted-foreground")}>Step</span>
          <span role="columnheader" className={cn(head, "border-l border-border/40 text-warn")}>Before · can stop it</span>
          <span role="columnheader" className={cn(head, "border-l border-border/40 text-primary-hover")}>After · gets told</span>
        </div>
        {LIFECYCLE_STEPS.map((s, i) => (
          <div key={s.id} id={`hook-step-${s.id}`} role="row" aria-label={s.label} className="grid scroll-mt-4 grid-cols-[minmax(0,15rem)_minmax(0,1fr)_minmax(0,1fr)] border-b border-border/40">
            <StepCell n={i + 1} label={s.label} sub={s.sub} />
            <Slot {...p} event={s.before ?? (s.id === "tools" ? "pre_tool_call" : undefined)} note={s.beforeNote} side="before" className="border-l border-border/40" />
            <Slot {...p} event={s.after} note={s.afterNote} side="after" className="border-l border-border/40" />
          </div>
        ))}
        <div id="hook-step-policy" className="scroll-mt-4">
          <p className={cn(EYEBROW, "border-b border-border/40 bg-destructive/[0.04] px-4 py-2 text-destructive")}>Policy alerts · whenever they happen · gets told</p>
          <div role="row" aria-label="Policy alerts" className="grid grid-cols-3">
            {POLICY_EVENTS.map((e, i) => (
              <Slot key={e} {...p} event={e} side="on" title={eventInfo(e).label} className={cn(i > 0 && "border-l border-border/40")} />
            ))}
          </div>
        </div>
      </div>
    </SettingsCard>
  )
}

/** The map on a phone: each step a card with its two sides stacked. Steps
 *  with nothing hooked in fold behind "Show all" once there is anything to
 *  show, so the hooks that exist are what the screen leads with. */
function MapStack(p: SlotProps) {
  const [all, setAll] = React.useState(false)
  const steps = LIFECYCLE_STEPS.map((s, i) => ({ ...s, n: i + 1 }))
  const has = (s: (typeof steps)[number]) => [s.before, s.after, s.id === "tools" ? "pre_tool_call" : undefined].some((e) => e && p.inSlot(e).length > 0)
  const anyHook = steps.some(has) || POLICY_EVENTS.some((e) => p.inSlot(e).length > 0)
  const visible = all || !anyHook ? steps : steps.filter(has)
  const hidden = steps.length - visible.length
  return (
    <div className="space-y-3" data-slot="hooks-map">
      {visible.map((s) => (
        <section key={s.id} id={`hook-step-${s.id}`} aria-label={s.label} className="overflow-hidden rounded-card border border-border bg-card">
          <StepCell n={s.n} label={s.label} sub={s.sub} className="border-b border-border/60" />
          <Slot {...p} event={s.before ?? (s.id === "tools" ? "pre_tool_call" : undefined)} note={s.beforeNote} side="before" heading />
          <Slot {...p} event={s.after} note={s.afterNote} side="after" heading className="border-t border-border/40" />
        </section>
      ))}
      {hidden > 0 && (
        <button type="button" onClick={() => setAll(true)} className="w-full rounded-lg border border-dashed border-border py-2.5 text-xs text-muted-foreground">
          Show the {hidden} steps with no hooks
        </button>
      )}
      <section id="hook-step-policy" aria-label="Policy alerts" className="overflow-hidden rounded-card border border-destructive/25 bg-card">
        <p className="flex items-center gap-2 border-b border-border/60 px-4 py-2.5 text-sm font-medium text-destructive"><ShieldAlert className="h-4 w-4" aria-hidden />Policy alerts</p>
        {POLICY_EVENTS.map((e, i) => <Slot key={e} {...p} event={e} side="on" title={eventInfo(e).label} className={cn(i > 0 && "border-t border-border/40")} />)}
      </section>
    </div>
  )
}

function StepCell({ n, label, sub, className }: { n: number; label: string; sub: string; className?: string }) {
  return (
    <div role="rowheader" className={cn("flex items-start gap-3 px-4 py-3", className)}>
      <span className="grid h-7 w-7 shrink-0 place-items-center rounded-lg border border-primary/25 bg-primary/10 font-mono text-micro text-primary-hover">{n}</span>
      <span className="min-w-0">
        <span className="block text-control font-medium">{label}</span>
        <span className="block text-label text-muted-foreground">{sub}</span>
      </span>
    </div>
  )
}

function Slot({ event, note, side, title, heading, className, inSlot, runs, canWrite, filtering, onOpen, onAdd }: SlotProps & {
  event?: string
  note?: string
  side: "before" | "after" | "on"
  title?: string
  heading?: boolean
  className?: string
}) {
  const list = event ? inSlot(event) : []
  const retiredSlot = event === "pre_tool_call"
  const sideLabel = side === "before" ? "Before · can stop it" : side === "after" ? "After · gets told" : null
  return (
    <div role="cell" className={cn("flex min-h-[64px] flex-col justify-center gap-1.5 px-4 py-2.5", className)} data-event={event}>
      {heading && sideLabel && <span className={cn(EYEBROW, side === "before" ? "text-warn" : "text-primary-hover")}>{sideLabel}</span>}
      {title && <span className="text-control">{title}</span>}
      {!event || (retiredSlot && list.length === 0) ? (
        <span className="text-label italic text-muted-foreground-soft">{note ?? "—"}</span>
      ) : (
        <>
          {!retiredSlot && <span className="font-mono text-[10.5px] text-muted-foreground">{event}{eventInfo(event).hasSeverity ? " · by severity" : eventInfo(event).hasTool ? " · by tool" : ""}</span>}
          {list.map((h) => <HookCard key={h.id} hook={h} last={runs[h.id]?.[0]} onOpen={() => onOpen(h)} />)}
          {canWrite && !retiredSlot && !(filtering && list.length === 0) && (
            <button type="button" onClick={() => onAdd(event)} aria-label={`Add a hook: ${eventInfo(event).label}`}
              className="inline-flex h-7 w-fit items-center gap-1.5 rounded-md border border-dashed border-border px-2.5 text-xs text-muted-foreground transition-colors hover:border-primary/50 hover:text-foreground">
              <Plus className="h-3 w-3" aria-hidden />Add
            </button>
          )}
          {!canWrite && list.length === 0 && <span className="text-label text-muted-foreground-soft">Nothing hooked in</span>}
        </>
      )}
    </div>
  )
}

function HookCard({ hook, last, onOpen }: { hook: HookRow; last?: HookRun; onOpen: () => void }) {
  const kind = KIND[hook.handler_kind] ?? KIND.http
  const info = eventInfo(hook.event)
  const tone = outcomeTone(last?.outcome)
  const stops = info.canBlock && hook.blocking
  const retired = RETIRED_EVENTS.has(hook.event)
  return (
    <button type="button" onClick={onOpen} title={`${info.label} · ${targetOf(hook)}`} data-hook={hook.id}
      className={cn("flex h-8 w-full min-w-0 items-center gap-2 rounded-lg border bg-surface-subtle px-2.5 text-left text-xs transition-colors hover:border-primary/50",
        retired ? "border-warn/40" : "border-border", !hook.enabled && "opacity-55")}>
      <span className={cn("rounded px-1.5 py-px font-mono text-[9.5px] font-semibold tracking-wide", kind.tone)}>{kind.tag}</span>
      <span className="min-w-0 flex-1 truncate font-mono text-[11.5px]">{shortTarget(hook)}</span>
      {retired && <span className="font-mono text-[9.5px] text-warn">RETIRED</span>}
      {stops && hook.enabled && <span className="rounded bg-warn/15 px-1.5 py-px font-mono text-[9.5px] font-semibold text-warn">STOPS</span>}
      {!hook.enabled ? (
        <span className="text-micro text-muted-foreground">off</span>
      ) : (
        <span className={cn("inline-flex shrink-0 items-center gap-1 text-micro", TONE_TEXT[tone])}>
          <span className={cn("h-1.5 w-1.5 rounded-full", TONE_DOT[tone])} aria-hidden />{last ? ago(last.ts) : "never"}
        </span>
      )}
    </button>
  )
}

/** The same hooks as rows, in run order, for searching. */
function HookList({ hooks, runs, crewName, onOpen }: {
  hooks: HookRow[]
  runs: Record<string, HookRun[]>
  crewName: (id?: string) => string
  onOpen: (h: HookRow) => void
}) {
  const order = (h: HookRow) => {
    const s = slotOf(h.event)
    const i = s.step === "policy" ? 99 : LIFECYCLE_STEPS.findIndex((x) => x.id === s.step)
    return i * 2 + (s.side === "after" ? 1 : 0)
  }
  const sorted = [...hooks].sort((a, b) => order(a) - order(b) || a.created_at.localeCompare(b.created_at))
  return (
    <SettingsCard icon={List} title="Hooks" description={`${hooks.length} in run order`}>
      {sorted.length === 0 ? (
        <p className="px-4 py-8 text-center text-xs text-muted-foreground">No hook matches.</p>
      ) : (
        <ul data-slot="hooks-list">
          {sorted.map((h) => {
            const last = runs[h.id]?.[0]
            const tone = outcomeTone(last?.outcome)
            const s = slotOf(h.event)
            return (
              <li key={h.id}>
                <button type="button" onClick={() => onOpen(h)} title={`${eventInfo(h.event).label} · ${targetOf(h)}`}
                  className={cn("grid w-full grid-cols-[minmax(0,1fr)_auto] items-center gap-x-4 gap-y-0.5 border-b border-border/40 px-4 py-2.5 text-left hover:bg-[var(--row-hover-bg)] sm:grid-cols-[minmax(0,14rem)_minmax(0,1fr)_8rem_7rem]", !h.enabled && "opacity-60")}>
                  <span className="min-w-0">
                    <span className="block truncate text-control">{eventInfo(h.event).label}</span>
                    <span className={cn("block font-mono text-[10.5px]", s.side === "before" ? "text-warn" : "text-muted-foreground")}>{s.side === "on" ? "policy alert" : s.side}</span>
                  </span>
                  <span className="hidden min-w-0 truncate font-mono text-[11.5px] sm:block">{targetOf(h)}</span>
                  <span className="hidden truncate text-xs text-muted-foreground sm:block">{crewName(h.crew_id)}</span>
                  <span className={cn("inline-flex items-center gap-1.5 justify-self-end text-xs", h.enabled ? TONE_TEXT[tone] : "text-muted-foreground")}>
                    <span className={cn("h-1.5 w-1.5 rounded-full", h.enabled ? TONE_DOT[tone] : "bg-border")} aria-hidden />
                    {!h.enabled ? "off" : last ? `${last.outcome} · ${ago(last.ts)}` : "never fired"}
                  </span>
                </button>
              </li>
            )
          })}
        </ul>
      )}
    </SettingsCard>
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
  const matcher = hook.matcher ?? {}
  const s = slotOf(hook.event)
  const step = LIFECYCLE_STEPS.findIndex((x) => x.id === s.step)
  const where = s.step === "policy" ? "Policy alert" : `Step ${step + 1} · ${LIFECYCLE_STEPS[step].label} · ${s.side}`
  return (
    <div className="space-y-5" data-slot="hook-detail">
      <section aria-label={`${info.label} hook`} className="space-y-3">
        <div className="flex items-start gap-3 pr-6">
          <span aria-hidden className={cn("grid h-10 w-10 shrink-0 place-items-center rounded-xl", kind.tone)}><kind.icon className="h-5 w-5" /></span>
          <div className="min-w-0 flex-1">
            <h2 className="text-base font-semibold tracking-[-0.01em]">{info.label}</h2>
            <p className="font-mono text-micro text-muted-foreground">{hook.event} · {where}</p>
          </div>
        </div>
        <p className="text-xs text-muted-foreground">{info.when}</p>
        <div className="flex flex-wrap items-center gap-3">
          <label className="flex items-center gap-2 text-xs">
            <Switch checked={hook.enabled} onCheckedChange={onToggle} disabled={!canWrite || pending} aria-label={`${hook.enabled ? "Disable" : "Enable"} the ${hook.event} hook`} />
            {hook.enabled ? "On" : "Off"}
          </label>
          {blockingLive && <span className="rounded bg-warn/15 px-1.5 py-0.5 font-mono text-[10px] font-semibold text-warn">STOPS THE ACTION</span>}
          {canChange && <Button variant="outline" size="sm" className="ml-auto h-7 gap-1.5 px-2.5 text-xs" onClick={onEdit}><Pencil className="h-3 w-3" />Edit</Button>}
        </div>
      </section>

      {retired && (
        <p role="alert" className="rounded-lg border border-warn/30 bg-warn/5 px-3 py-2 text-xs text-warn">
          This event is no longer dispatched, so the hook can never run. Delete it, or re-create it on an event that fires (e.g. After a tool runs).
        </p>
      )}

      <section aria-label="What it runs" className="space-y-2">
        <h3 className="eyebrow text-primary-hover">What it runs</h3>
        <dl className="grid grid-cols-[6rem_minmax(0,1fr)] gap-x-3 gap-y-2 text-xs">
          <dt className="text-muted-foreground">Does</dt><dd>{kind.label}{hook.handler_kind === "http" ? " — POSTs the event as JSON" : ""}</dd>
          <dt className="text-muted-foreground">{hook.handler_kind === "http" ? "URL" : hook.handler_kind === "shell" ? "Command" : "Agent"}</dt>
          <dd className="break-all font-mono">{targetOf(hook)}</dd>
          {hook.handler_kind === "http" && <><dt className="text-muted-foreground">Signed</dt><dd>{hook.handler_config?.secret ? "Yes — X-Crewship-Signature" : "No"}</dd></>}
          <dt className="text-muted-foreground">Scope</dt><dd>{crewName}</dd>
          {(matcher.severities?.length ?? 0) > 0 && <><dt className="text-muted-foreground">Severities</dt><dd>{matcher.severities!.join(", ")}</dd></>}
          {(matcher.tools?.length ?? 0) > 0 && <><dt className="text-muted-foreground">Tools</dt><dd className="font-mono">{matcher.tools!.join(", ")}</dd></>}
          <dt className="text-muted-foreground">Mode</dt>
          <dd>{blockingLive ? "Waits for the hook; a refusal stops the action" : info.canBlock ? "Gets told — could stop it, blocking is off" : "Gets told — cannot stop anything"}</dd>
        </dl>
        {hook.handler_kind === "shell" && (
          <p className="flex items-start gap-2 rounded-lg border border-destructive/20 bg-destructive/5 px-3 py-2 text-label text-destructive">
            <ShieldAlert className="mt-px h-3.5 w-3.5 shrink-0" />Runs as the Crewship service on the host, outside any container. Only the owner can change it.
          </p>
        )}
      </section>

      <section aria-label="Recent runs" className="space-y-2">
        <h3 className="eyebrow flex items-center text-primary-hover">Recent runs<span className="ml-2 font-mono normal-case tracking-normal text-muted-foreground">{runs.length}</span></h3>
        {runs.length === 0 ? (
          <p className="text-xs text-muted-foreground">{runsCapped ? "No run in the recent journal window." : "Has not fired yet — it will, the next time this event happens."}</p>
        ) : (
          <ul className="overflow-hidden rounded-lg border border-border">
            {runs.slice(0, 8).map((r, i) => {
              const tone = outcomeTone(r.outcome)
              return (
                <li key={`${r.ts}-${i}`} className="flex items-center gap-2.5 border-b border-border/40 px-3 py-2 text-xs last:border-b-0">
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
        <Link href="/journal?q=hook.fired" className="inline-block text-xs text-primary-hover hover:underline">Open in Journal →</Link>
      </section>

      {canChange && (
        <section aria-label="Delete hook" className="flex flex-wrap items-center gap-3 border-t border-border/60 pt-4">
          <p className="min-w-0 flex-1 basis-48 text-label text-muted-foreground">The hook stops running at once. Its past runs stay in the journal.</p>
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
        </section>
      )}
    </div>
  )
}
