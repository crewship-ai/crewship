"use client"

import { useEffect, useMemo, useState } from "react"
import { motion, AnimatePresence } from "motion/react"
import {
  AlertTriangle,
  Archive,
  Bell,
  Bot,
  CheckCheck,
  CheckCircle2,
  ChevronDown,
  ChevronRight,
  CircleDot,
  Clock,
  Cog,
  Eye,
  EyeOff,
  Inbox as InboxIcon,
  Layers,
  MailOpen,
  MessageSquare,
  RotateCcw,
  ScrollText,
  Sparkles,
  Users,
  Workflow,
  XCircle,
} from "lucide-react"
import Link from "next/link"
import { cn } from "@/lib/utils"
import { useInbox, type InboxItem } from "@/hooks/use-inbox"
import { useWorkspace } from "@/hooks/use-workspace"
import { useCrewSummaries } from "@/hooks/use-dashboard-data"
import { Button } from "@/components/ui/button"
import { Checkbox } from "@/components/ui/checkbox"
import { ListRow } from "@/components/ui/list-row"
import { TabBar } from "@/components/ui/tab-bar"
import { ListRowSkeleton } from "@/components/ui/skeletons"
import { EmptyState as PageEmptyState } from "@/components/layout/empty-state"
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog"
import { toast } from "sonner"
import { WaitpointRunDetail } from "./waitpoint-run-detail"
import { MarkdownContent } from "@/components/features/issues/markdown-content"
import { waitpointDecide } from "@/lib/api/waitpoints"
import { inboxBulk } from "@/lib/api/inbox"
import { escalationResolve } from "@/lib/api/escalations"
import { getAgentAvatarUrl } from "@/lib/agent-avatar"

// InboxList — the /inbox page surface. Gmail-style triage: the default
// "Inbox" tab shows everything that isn't archived (unread + read), so
// opening a row marks it read but it STAYS in place — it only leaves the
// list when you Archive it (→ resolved/archived). "Unread" is a filter,
// "Archived" is the resolved bucket. Decision items (waitpoint /
// escalation / blocking) keep their source-of-truth Approve/Deny/Resolve
// actions and can't be blind-archived. List on the left, detail (with a
// human-formatted body, not raw JSON) on the right.

// View tabs — see the component doc above. Mapped to the backend
// state param in InboxList (inbox→all, unread→unread, archived→resolved).
type StateFilter = "inbox" | "unread" | "archived"

interface KindMeta {
  label: string
  icon: React.ComponentType<{ className?: string }>
  accent: string
}

const KIND_META: Record<InboxItem["kind"], KindMeta> = {
  waitpoint: { label: "Waitpoint", icon: Clock, accent: "text-amber-300" },
  escalation: { label: "Escalation", icon: AlertTriangle, accent: "text-rose-300" },
  failed_run: { label: "Failed run", icon: XCircle, accent: "text-rose-400" },
  message: { label: "Notification", icon: MessageSquare, accent: "text-blue-300" },
}

// The kind column can hold values the UI doesn't have a card for yet
// (e.g. memory_consolidation, admitted by migration v90). Render those
// as a generic notification instead of crashing on an undefined meta.
const FALLBACK_META: KindMeta = {
  label: "Notification",
  icon: Sparkles,
  accent: "text-muted-foreground",
}

function metaFor(kind: string): KindMeta {
  return (KIND_META as Record<string, KindMeta>)[kind] ?? FALLBACK_META
}

// Decision items must never be bulk-closed: a waitpoint or escalation is
// an agent waiting on a human to approve / decide, and a blocking row of
// any kind means "needs explicit action". Used to warn before a bulk
// Resolve and to split the selection into safe vs protected.
function isDecisionItem(item: InboxItem): boolean {
  return item.kind === "waitpoint" || item.kind === "escalation" || item.blocking
}

// A keeper-synthetic escalation (Skill review, memory health) carries
// kind=escalation but no escalation_type — real agent escalations always do.
// These have no backing escalations row and thus no /escalations/{id}/resolve
// endpoint, so the inbox row itself is the only handle: they're dismissible
// (the backend allows PATCH→resolved for them) even though other escalations
// aren't. Without this they'd be trapped in "Decisions needed" forever.
function isSourceLessEscalation(item: InboxItem): boolean {
  return item.kind === "escalation" && typeof item.payload?.escalation_type !== "string"
}

// Tree grouping. The list can collapse 100 rows into a handful of
// folders so "47 failures from one routine" reads as one line you can
// expand, bulk-select, and clear — instead of 47 rows to scroll past.
// Every dimension keys off a field already on the row (payload + kind),
// so grouping is pure client-side over the data the list already holds.
type GroupDim = "smart" | "kind" | "sender" | "routine" | "issue" | "crew"

const GROUP_DIMS: { id: GroupDim; label: string }[] = [
  { id: "smart", label: "Smart" },
  { id: "kind", label: "Type" },
  { id: "sender", label: "Sender" },
  { id: "routine", label: "Routine" },
  { id: "issue", label: "Issue" },
  { id: "crew", label: "Crew" },
]

// Smart buckets give the Linear-style "what needs me vs what's FYI" split
// the flat kind list can't. Order is intentional (decisions first); the
// rank drives group sorting so "Decisions needed" always sits on top.
const SMART_ORDER: Record<string, number> = {
  "sm:decisions": 0,
  "sm:review": 1,
  "sm:fyi": 2,
}

function payloadString(item: InboxItem, key: string): string {
  const v = item.payload?.[key]
  return typeof v === "string" ? v : ""
}

// groupOf returns the bucket key + display label for an item under a
// dimension. Items missing the dimension's field land in a stable
// "No …" bucket (key prefixed so it can't collide with a real value).
// crewName resolves a crew_id to its human name so the Crew grouping
// shows "Engineering" instead of a raw "cmqtg…" id (the key stays the id
// so the bucket is stable even before names have loaded).
function groupOf(
  item: InboxItem,
  dim: GroupDim,
  crewName?: (id: string) => string,
): { key: string; label: string } {
  switch (dim) {
    case "smart": {
      // Decisions = something an agent is blocked on (waitpoint /
      // escalation / any blocking row). Review = a non-blocking nudge
      // tied to an issue ("ENG-6 ready for review"). Everything else is
      // FYI (advisories, failed-run notices).
      if (item.kind === "waitpoint" || item.kind === "escalation" || item.blocking)
        return { key: "sm:decisions", label: "Decisions needed" }
      if (item.kind === "message" && payloadString(item, "issue_identifier"))
        return { key: "sm:review", label: "Needs review" }
      return { key: "sm:fyi", label: "FYI / advisories" }
    }
    case "sender": {
      const s = item.sender_name || metaFor(item.kind).label
      return { key: `sn:${s}`, label: s }
    }
    case "routine": {
      const slug = payloadString(item, "pipeline_slug")
      return slug ? { key: `r:${slug}`, label: slug } : { key: "r:_none", label: "No routine" }
    }
    case "issue": {
      const iss = payloadString(item, "issue_identifier")
      return iss ? { key: `i:${iss}`, label: iss } : { key: "i:_none", label: "No issue" }
    }
    case "crew": {
      // Waitpoints carry the crew on invoking_crew_id, other kinds on
      // crew_id — accept either so a pipeline approval still groups under
      // its crew.
      const crew = payloadString(item, "crew_id") || payloadString(item, "invoking_crew_id")
      if (!crew) return { key: "c:_none", label: "No crew" }
      const name = crewName?.(crew)
      return { key: `c:${crew}`, label: name && name !== "" ? name : crew }
    }
    case "kind":
    default:
      return { key: `k:${item.kind}`, label: metaFor(item.kind).label }
  }
}

interface InboxGroup {
  key: string
  label: string
  items: InboxItem[]
}

// ── Sender avatar ───────────────────────────────────────────────────
// Renders a per-sender chip so an agent message reads as "🤖 Atlas", a
// routine notice as a workflow glyph, etc. — instead of the bare kind
// icon every row shared before. Icon keys off sender_type; the tile
// colour is hashed from the sender name so each sender is visually
// stable across rows without needing avatar data fetched per item.
const SENDER_ICONS = {
  agent: Bot,
  crew: Users,
  pipeline: Workflow,
  system: Cog,
} as const

const AVATAR_COLORS = [
  "bg-blue-500/20 text-blue-300",
  "bg-violet-500/20 text-violet-300",
  "bg-amber-500/20 text-amber-300",
  "bg-emerald-500/20 text-emerald-300",
  "bg-cyan-500/20 text-cyan-300",
  "bg-rose-500/20 text-rose-300",
  "bg-fuchsia-500/20 text-fuchsia-300",
]

function avatarColor(seed: string): string {
  let h = 0
  for (let i = 0; i < seed.length; i++) h = (h * 31 + seed.charCodeAt(i)) >>> 0
  return AVATAR_COLORS[h % AVATAR_COLORS.length]
}

function SenderAvatar({ item, className }: { item: InboxItem; className?: string }) {
  // Real agent senders render their actual avatar (same DiceBear seed/style
  // the agent card uses), so "casey escalated…" reads as Casey's face — not a
  // generic bot glyph. The backend only fills avatar_seed for agent senders;
  // system/crew/pipeline keep the kind-keyed glyph below.
  if (item.sender_type === "agent" && (item.avatar_seed || item.sender_name)) {
    return (
      <img
        src={getAgentAvatarUrl(item.avatar_seed || item.sender_name || "agent", item.avatar_style)}
        alt=""
        className={cn("shrink-0 rounded-md object-cover", className ?? "h-6 w-6")}
        aria-hidden
      />
    )
  }
  const Icon =
    (item.sender_type && SENDER_ICONS[item.sender_type as keyof typeof SENDER_ICONS]) ||
    metaFor(item.kind).icon
  const seed = item.sender_name || item.sender_id || item.kind
  return (
    <span
      className={cn(
        "grid shrink-0 place-items-center rounded-md",
        avatarColor(seed),
        className ?? "h-6 w-6",
      )}
      aria-hidden
    >
      <Icon className="h-3.5 w-3.5" />
    </span>
  )
}

// ── Secret redaction (client-side display) ──────────────────────────
// The backend already redacts secrets before they hit body_md, but
// payload values can still carry credential-ish material an agent put
// there. Mask anything that looks like a secret in the Context card and
// reveal it only on explicit click — defense in depth + don't shoulder-
// surf-leak a token sitting in someone's inbox.
// Display-only defense in depth: the backend already redacts real secrets
// out of body_md before they ever reach the client (see inbox.RedactSecrets
// / lookout.Redact — the source of truth). This just hides a credential-
// looking *Context value* behind a reveal toggle. Keep the key vocabulary
// in sync with the backend's kvSecretRe so the two agree on "looks secret"
// (same keys + "credential"). Mask ONLY a credential-named key or a
// connection string with inline creds — a bare skill_id / run_id / crew_id
// is an identifier, not a secret (the thing the user flagged).
const SECRET_KEY_RE =
  /(password|passwd|pwd|secret|token|api[_-]?key|access[_-]?key|auth|bearer|credential)/i
const SECRET_VAL_RE = /:\/\/[^/@\s]+:[^/@\s]+@/

function looksSecret(key: string, value: string): boolean {
  return SECRET_KEY_RE.test(key) || SECRET_VAL_RE.test(value)
}

function RevealableValue({ value }: { value: string }) {
  const [shown, setShown] = useState(false)
  return (
    <span className="inline-flex items-center gap-1.5">
      <span className="font-mono text-[11px] text-foreground/80">
        {shown ? value : "••••••••"}
      </span>
      <button
        type="button"
        onClick={() => setShown((s) => !s)}
        className="text-muted-foreground/60 hover:text-foreground"
        aria-label={shown ? "Hide value" : "Reveal value"}
      >
        {shown ? <EyeOff className="h-3 w-3" /> : <Eye className="h-3 w-3" />}
      </button>
    </span>
  )
}

// Keys that duplicate what's already shown (body/title) or are pure
// plumbing — hidden from the human Context card so it reads like a
// summary, not a JSON dump. The data is still on the wire for anything
// that needs it programmatically.
const CONTEXT_HIDE_KEYS = new Set([
  "reason",
  "raw_reason",
  "source",
  "kind",
  "inputs",
  "step_id",
])

function humanizeKey(k: string): string {
  return k
    .replace(/_/g, " ")
    .replace(/\bid\b/i, "ID")
    .replace(/^\w/, (c) => c.toUpperCase())
}

// ContextDetails renders payload as a clean key/value summary instead of
// a raw <pre>{JSON}</pre> block. Strings that look like secrets are
// masked with a reveal toggle; nested objects fall back to compact JSON.
function ContextDetails({ payload }: { payload: Record<string, unknown> }) {
  const entries = Object.entries(payload).filter(
    ([k, v]) => !CONTEXT_HIDE_KEYS.has(k) && v !== null && v !== undefined && v !== "",
  )
  if (entries.length === 0) return null
  return (
    <dl className="grid grid-cols-[max-content_1fr] gap-x-4 gap-y-1.5 text-[11px]">
      {entries.map(([k, v]) => {
        return (
          <div key={k} className="contents">
            <dt className="text-muted-foreground/70">{humanizeKey(k)}</dt>
            <dd className="min-w-0 break-words text-foreground/80">
              {typeof v === "string" ? (
                looksSecret(k, v) ? (
                  <RevealableValue value={v} />
                ) : (
                  <span>{v}</span>
                )
              ) : (
                <span className="font-mono text-[10px]">{JSON.stringify(v)}</span>
              )}
            </dd>
          </div>
        )
      })}
    </dl>
  )
}

export function InboxList() {
  const { workspaceId } = useWorkspace()
  // Default to "inbox" (every non-resolved item): a blocking item the operator
  // has merely *read* — e.g. a pending CREDENTIAL escalation they clicked
  // through to — stays visible here (only `resolved` is filtered out), so it is
  // never silently hidden. The filter pills still let them narrow to unread.
  const [stateFilter, setStateFilter] = useState<StateFilter>("inbox")
  const [selectedId, setSelectedId] = useState<string | null>(null)
  // Snapshot of the open item. Clicking an *unread* row marks it read,
  // which drops it from the unread-filtered list (see use-inbox
  // onSuccess reconciliation) — so a `selected` derived purely from
  // `items` would go null the instant you open it and the detail pane
  // would snap shut. We keep the last live version here so the detail
  // stays open after the row leaves the current filter (Linear-style:
  // the row greys out of Unread, the detail keeps showing).
  const [selectedSnapshot, setSelectedSnapshot] = useState<InboxItem | null>(null)

  // inbox → state=active (unread+read, resolved excluded SERVER-SIDE so
  // archived rows don't eat the LIMIT window); unread/archived map
  // straight through.
  const filterParam: "active" | "unread" | "resolved" =
    stateFilter === "inbox" ? "active" : stateFilter === "unread" ? "unread" : "resolved"
  const { items, unreadCount, loading, error, patch, refresh } = useInbox(workspaceId, filterParam)

  // Server already excludes resolved on the Inbox tab; this is a harmless
  // belt-and-suspenders so a just-archived row leaves the view instantly
  // (before the refetch) instead of lingering.
  const visibleItems = useMemo(
    () => (stateFilter === "inbox" ? items.filter((it) => it.state !== "resolved") : items),
    [items, stateFilter],
  )

  // Crew id → name, so the Crew grouping reads "Engineering" not a raw id.
  const { data: crews } = useCrewSummaries(workspaceId)
  const crewName = useMemo(() => {
    const m = new Map((crews ?? []).map((c) => [c.id, c.name]))
    return (id: string) => m.get(id) ?? ""
  }, [crews])

  const liveSelected = useMemo(
    () => items.find((it) => it.id === selectedId) ?? null,
    [items, selectedId],
  )
  // Track the freshest live version while the row is still in the list;
  // fall back to the snapshot once it's been filtered out.
  useEffect(() => {
    if (liveSelected) setSelectedSnapshot(liveSelected)
  }, [liveSelected])
  // Scope the snapshot to the active workspace too — switching workspaces
  // must not keep rendering the prior workspace's detail item until fresh
  // list data lands.
  const selected =
    liveSelected ??
    (selectedSnapshot?.id === selectedId && selectedSnapshot.workspace_id === workspaceId
      ? selectedSnapshot
      : null)

  // ── Tree grouping + bulk selection ──────────────────────────────
  const [groupBy, setGroupBy] = useState<GroupDim>("smart")
  const [collapsed, setCollapsed] = useState<Set<string>>(new Set())
  const [checked, setChecked] = useState<Set<string>>(new Set())
  const [bulkBusy, setBulkBusy] = useState(false)
  const [confirmResolve, setConfirmResolve] = useState(false)

  const groups = useMemo<InboxGroup[]>(() => {
    const map = new Map<string, InboxGroup>()
    for (const it of visibleItems) {
      const g = groupOf(it, groupBy, crewName)
      const bucket = map.get(g.key)
      if (bucket) bucket.items.push(it)
      else map.set(g.key, { key: g.key, label: g.label, items: [it] })
    }
    const out = Array.from(map.values())
    // Smart buckets get a fixed priority order (decisions → review →
    // fyi); other dimensions keep newest-first insertion order.
    if (groupBy === "smart") {
      out.sort((a, b) => (SMART_ORDER[a.key] ?? 99) - (SMART_ORDER[b.key] ?? 99))
    }
    return out
  }, [visibleItems, groupBy, crewName])

  // Drop checked ids that are no longer visible (filter switch, refresh,
  // regroup) so the bulk bar count never lies about what it will act on.
  useEffect(() => {
    setChecked((prev) => {
      if (prev.size === 0) return prev
      const live = new Set(visibleItems.map((i) => i.id))
      let changed = false
      const next = new Set<string>()
      for (const id of prev) {
        if (live.has(id)) next.add(id)
        else changed = true
      }
      return changed ? next : prev
    })
  }, [visibleItems])

  const toggleCollapse = (key: string) =>
    setCollapsed((prev) => {
      const next = new Set(prev)
      next.has(key) ? next.delete(key) : next.add(key)
      return next
    })

  const toggleItem = (id: string) =>
    setChecked((prev) => {
      const next = new Set(prev)
      next.has(id) ? next.delete(id) : next.add(id)
      return next
    })

  const toggleGroup = (g: InboxGroup) =>
    setChecked((prev) => {
      const next = new Set(prev)
      const allOn = g.items.every((it) => next.has(it.id))
      for (const it of g.items) allOn ? next.delete(it.id) : next.add(it.id)
      return next
    })

  const clearChecked = () => setChecked(new Set())

  // Split the current selection into items a bulk Resolve will actually
  // close vs. decision items the server will refuse to close. Drives the
  // confirmation warning so nothing important gets mass-closed by mistake.
  const selectionSplit = useMemo(() => {
    const sel = visibleItems.filter((it) => checked.has(it.id))
    const decision = sel.filter(isDecisionItem)
    return { total: sel.length, decision: decision.length, safe: sel.length - decision.length }
  }, [visibleItems, checked])

  // Bulk apply via /inbox/bulk. Chunked to the backend's 500-id cap so a
  // large select-all can't fail the whole action; the server skips
  // decision items it must not close, and we surface every count
  // (resolved / left-open / no-longer-available).
  const runBulk = async (state: "read" | "resolved", action?: string) => {
    if (!workspaceId || checked.size === 0) return
    setBulkBusy(true)
    try {
      const ids = Array.from(checked)
      const CHUNK = 500
      let updated = 0
      let skipped = 0
      let notFound = 0
      for (let i = 0; i < ids.length; i += CHUNK) {
        const res = await inboxBulk(workspaceId, ids.slice(i, i + CHUNK), state, action)
        if (!res.ok) {
          toast.error(res.error)
          return
        }
        updated += res.result.updated
        skipped += res.result.skipped
        notFound += res.result.not_found
      }
      const verb = state === "resolved" ? "resolved" : "marked read"
      const extra = [
        skipped > 0 ? `${skipped} left open (need a decision)` : "",
        notFound > 0 ? `${notFound} no longer available` : "",
      ]
        .filter(Boolean)
        .join(" · ")
      toast.success(extra ? `${updated} ${verb} · ${extra}` : `${updated} ${verb}`)
      clearChecked()
      await refresh()
    } finally {
      setBulkBusy(false)
    }
  }

  // Resolve entry point. If the selection holds any decision item
  // (waitpoint / escalation / blocking), warn first — a bulk Resolve must
  // never quietly close something an agent is waiting on. Otherwise go
  // straight through.
  const requestResolve = () => {
    if (selectionSplit.decision > 0) setConfirmResolve(true)
    else void runBulk("resolved", "dismissed")
  }

  return (
    <div className="flex h-[calc(100vh-48px)] bg-background">
      {/* ── List panel ─────────────────────────────────────────── */}
      <div className="flex w-[420px] shrink-0 flex-col border-r border-white/[0.06] bg-card">
        {/* Header */}
        <div className="flex shrink-0 items-center gap-2 border-b border-white/[0.06] px-3 py-2">
          <InboxIcon className="h-4 w-4 text-muted-foreground/60" />
          <span className="text-sm font-medium">Inbox</span>
          {unreadCount > 0 && (
            <span className="ml-auto rounded-full bg-blue-500/15 px-2 py-0.5 text-[10px] font-semibold text-blue-300">
              {unreadCount} unread
            </span>
          )}
        </div>

        {/* State filter */}
        <TabBar
          value={stateFilter}
          onValueChange={(v) => setStateFilter(v as StateFilter)}
          layoutId="inbox-filter-indicator"
          ariaLabel="Filter inbox by state"
          className="shrink-0 [&>button]:flex-1"
        >
          <TabBar.Item
            value="inbox"
            count={stateFilter === "inbox" ? visibleItems.length : null}
          >
            Inbox
          </TabBar.Item>
          <TabBar.Item
            value="unread"
            count={stateFilter === "unread" ? visibleItems.length : unreadCount || null}
          >
            Unread
          </TabBar.Item>
          <TabBar.Item value="archived" count={stateFilter === "archived" ? visibleItems.length : null}>
            Archived
          </TabBar.Item>
        </TabBar>

        {/* Group-by control */}
        <div className="flex shrink-0 items-center gap-1 border-b border-white/[0.06] px-3 py-1.5">
          <Layers className="mr-1 h-3 w-3 text-muted-foreground/50" />
          <span className="mr-1 text-[10px] uppercase tracking-wider text-muted-foreground/50">
            Group
          </span>
          {GROUP_DIMS.map((d) => (
            <button
              key={d.id}
              onClick={() => setGroupBy(d.id)}
              aria-pressed={groupBy === d.id}
              className={cn(
                "rounded px-1.5 py-0.5 text-[11px] transition-colors",
                groupBy === d.id
                  ? "bg-white/[0.08] text-foreground"
                  : "text-muted-foreground/60 hover:text-foreground",
              )}
            >
              {d.label}
            </button>
          ))}
        </div>

        {/* List — collapsible tree, one folder per group */}
        <div className="flex-1 overflow-y-auto">
          {loading && visibleItems.length === 0 ? (
            <ListRowSkeleton rows={3} className="p-3" />
          ) : error ? (
            <div className="p-6 text-center text-xs text-rose-300">
              Inbox unavailable: {error}
            </div>
          ) : visibleItems.length === 0 ? (
            <InboxEmpty filter={stateFilter} />
          ) : (
            <div>
              {groups.map((g) => {
                const isCollapsed = collapsed.has(g.key)
                const checkedCount = g.items.reduce(
                  (n, it) => n + (checked.has(it.id) ? 1 : 0),
                  0,
                )
                const groupState: boolean | "indeterminate" =
                  checkedCount === 0
                    ? false
                    : checkedCount === g.items.length
                      ? true
                      : "indeterminate"
                return (
                  <div key={g.key}>
                    {/* Group header — checkbox selects the whole folder */}
                    <div className="sticky top-0 z-[1] flex items-center gap-2 border-b border-white/[0.04] bg-card/95 px-3 py-1.5 backdrop-blur">
                      <Checkbox
                        checked={groupState}
                        onCheckedChange={() => toggleGroup(g)}
                        aria-label={`Select all in ${g.label}`}
                      />
                      <button
                        onClick={() => toggleCollapse(g.key)}
                        aria-expanded={!isCollapsed}
                        className="flex min-w-0 flex-1 items-center gap-1.5 text-left"
                      >
                        {isCollapsed ? (
                          <ChevronRight className="h-3 w-3 shrink-0 text-muted-foreground/50" />
                        ) : (
                          <ChevronDown className="h-3 w-3 shrink-0 text-muted-foreground/50" />
                        )}
                        <span className="truncate text-xs font-medium">{g.label}</span>
                        <span className="ml-auto shrink-0 rounded-full bg-white/[0.06] px-1.5 py-0.5 text-[10px] text-muted-foreground">
                          {g.items.length}
                        </span>
                      </button>
                    </div>
                    {/* Group rows */}
                    {!isCollapsed && (
                      <ul className="divide-y divide-white/[0.04]">
                        {g.items.map((item) => (
                          <InboxRow
                            key={item.id}
                            item={item}
                            selected={selectedId === item.id}
                            checked={checked.has(item.id)}
                            onToggleCheck={() => toggleItem(item.id)}
                            onSelect={() => {
                              setSelectedId(item.id)
                              // Snapshot immediately so the detail survives
                              // the read-transition evicting this row.
                              setSelectedSnapshot(item)
                              if (item.state === "unread") {
                                // Fire-and-forget; useInbox surfaces the
                                // error itself, so just swallow the reject
                                // to avoid an unhandled rejection.
                                void patch(item.id, "read").catch(() => {})
                              }
                            }}
                          />
                        ))}
                      </ul>
                    )}
                  </div>
                )
              })}
            </div>
          )}
        </div>

        {/* Bulk action bar — appears once anything is checked */}
        {checked.size > 0 && (
          <div className="flex shrink-0 items-center gap-2 border-t border-white/[0.06] bg-card/40 px-3 py-2">
            <span className="text-xs font-medium">{checked.size} selected</span>
            <div className="ml-auto flex items-center gap-1.5">
              <Button
                size="sm"
                disabled={bulkBusy}
                onClick={requestResolve}
                className="gap-1.5"
              >
                <CheckCheck className="h-3 w-3" />
                {bulkBusy ? "Working…" : "Resolve"}
              </Button>
              <Button
                size="sm"
                variant="ghost"
                disabled={bulkBusy}
                onClick={() => runBulk("read")}
                className="gap-1.5"
              >
                <MailOpen className="h-3 w-3" />
                Mark read
              </Button>
              <Button size="sm" variant="ghost" disabled={bulkBusy} onClick={clearChecked}>
                Clear
              </Button>
            </div>
          </div>
        )}
      </div>

      {/* ── Detail panel ───────────────────────────────────────── */}
      <div className="flex-1 overflow-y-auto bg-background">
        <AnimatePresence mode="wait">
          {selected ? (
            <motion.div
              key={selected.id}
              initial={{ opacity: 0, y: 6 }}
              animate={{ opacity: 1, y: 0 }}
              exit={{ opacity: 0, y: -6 }}
              transition={{ duration: 0.18 }}
              className="h-full"
            >
              <InboxDetail
                item={selected}
                onResolve={async (action) => {
                  await patch(selected.id, "resolved", action)
                  toast.success(`Marked as ${action}`)
                  refresh()
                }}
                onArchive={async () => {
                  // Gmail archive: move out of the inbox into Archived
                  // without a "decision" outcome. Maps to resolved with a
                  // dedicated action so it's distinguishable from an
                  // explicit dismiss/approve in the audit trail. Undo
                  // restores it to read (back in the inbox, not unread).
                  const prevState = selected.state
                  await patch(selected.id, "resolved", "archived")
                  toast.success("Archived", {
                    action: {
                      label: "Undo",
                      onClick: () => {
                        void patch(selected.id, prevState === "unread" ? "unread" : "read")
                          .then(refresh)
                          .catch(() => {})
                      },
                    },
                  })
                }}
                onMarkUnread={() => patch(selected.id, "unread")}
                // onRefresh lets source-managed actions (e.g. PR-D
                // approve-hire, which resolves the inbox row server-
                // side via inbox.ResolveBySource) repaint the list
                // without going through the inbox PATCH (which 409s
                // on kind=waitpoint for any state other than 'read').
                onRefresh={refresh}
              />
            </motion.div>
          ) : (
            <motion.div
              key="empty"
              initial={{ opacity: 0 }}
              animate={{ opacity: 1 }}
              exit={{ opacity: 0 }}
              transition={{ duration: 0.15 }}
              className="h-full"
            >
              <DetailEmpty />
            </motion.div>
          )}
        </AnimatePresence>
      </div>

      {/* Bulk-resolve safety gate. Shown only when the selection contains
          decision items; reassures the user those won't be closed and
          confirms resolving just the safe remainder. */}
      <AlertDialog open={confirmResolve} onOpenChange={setConfirmResolve}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {selectionSplit.safe > 0
                ? `Resolve ${selectionSplit.safe} item${selectionSplit.safe === 1 ? "" : "s"}?`
                : "Nothing to resolve in bulk"}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {selectionSplit.decision} of the {selectionSplit.total} selected{" "}
              {selectionSplit.decision === 1 ? "is an" : "are"} approval / escalation request
              {selectionSplit.decision === 1 ? "" : "s"} an agent is waiting on — these are{" "}
              <span className="font-medium text-foreground">never closed in bulk</span> and stay in
              your inbox to decide one by one.
              {selectionSplit.safe > 0
                ? ` Only the ${selectionSplit.safe} dismissable item${
                    selectionSplit.safe === 1 ? "" : "s"
                  } will be resolved.`
                : " Open each one to approve, deny, or resolve it individually."}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            {selectionSplit.safe > 0 && (
              <AlertDialogAction onClick={() => void runBulk("resolved", "dismissed")}>
                Resolve {selectionSplit.safe}
              </AlertDialogAction>
            )}
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  )
}

function InboxRow({
  item,
  selected,
  checked,
  onToggleCheck,
  onSelect,
}: {
  item: InboxItem
  selected: boolean
  checked: boolean
  onToggleCheck: () => void
  onSelect: () => void
}) {
  const meta = metaFor(item.kind)
  const Icon = meta.icon
  return (
    <ListRow
      selected={selected}
      onSelect={onSelect}
      className={cn(
        "items-start gap-2 px-3 py-2.5",
        item.state === "resolved" && "opacity-60",
      )}
    >
      {/* Per-row checkbox for bulk select. Toggle is wired through
          onCheckedChange so it works for keyboard + screen-reader users;
          the wrapper just stops the click from also opening the detail. */}
      <span className="mt-0.5 shrink-0" onClick={(e) => e.stopPropagation()}>
        <Checkbox
          checked={checked}
          onCheckedChange={onToggleCheck}
          aria-label={`Select ${item.title}`}
        />
      </span>
      {/* unread dot — left of the sender avatar */}
      <span
        className={cn(
          "mt-2 h-1.5 w-1.5 shrink-0 rounded-full",
          item.state === "unread" ? "bg-blue-400" : "bg-transparent",
        )}
      />
      <SenderAvatar item={item} className="mt-0.5 h-6 w-6" />
      <div className="min-w-0 flex-1">
        <div className="flex items-baseline gap-2">
          <span
            className={cn(
              "truncate text-xs",
              item.state === "unread" ? "font-medium text-foreground" : "text-foreground/70",
            )}
          >
            {item.title}
          </span>
        </div>
        <div className="mt-0.5 flex items-center gap-1.5 text-[10px] text-muted-foreground/70">
          <Icon className={cn("h-3 w-3 shrink-0", meta.accent)} />
          <span>{meta.label}</span>
          {item.sender_name && <><span>·</span><span className="truncate">{item.sender_name}</span></>}
          <span>·</span>
          <span className="shrink-0">{relTime(item.created_at)}</span>
        </div>
      </div>
      <ChevronRight className="h-3 w-3 shrink-0 text-muted-foreground/30" />
    </ListRow>
  )
}

function InboxDetail({
  item,
  onResolve,
  onArchive,
  onMarkUnread,
  onRefresh,
}: {
  item: InboxItem
  onResolve: (action: string) => void | Promise<void>
  onArchive: () => void | Promise<void>
  onMarkUnread: () => void
  onRefresh: () => void | Promise<void>
}) {
  const meta = metaFor(item.kind)
  const Icon = meta.icon
  const isResolved = item.state === "resolved"
  // Decision items (waitpoint / escalation / blocking) can't be blind-
  // archived — they're source-managed (inbox PATCH→resolved 409s) and an
  // agent is waiting on a real decision. Everything else (messages,
  // failed-run notices, advisories) archives Gmail-style. Exception:
  // source-less keeper escalations have no resolve endpoint, so the inbox
  // row IS the handle — they archive like advisories.
  const archivable =
    !isResolved &&
    ((item.kind !== "waitpoint" && item.kind !== "escalation" && !item.blocking) ||
      isSourceLessEscalation(item))

  return (
    <div className="flex h-full flex-col">
      <div className="shrink-0 border-b border-white/[0.06] px-6 py-4">
        <div className="flex items-center gap-2 text-[11px] uppercase tracking-wider text-muted-foreground/60">
          <Icon className={cn("h-3.5 w-3.5", meta.accent)} />
          <span>{meta.label}</span>
          {item.priority !== "medium" && (
            <span className={cn(
              "rounded px-1.5 py-0.5 text-[9px] font-semibold",
              item.priority === "urgent" && "bg-rose-500/15 text-rose-300",
              item.priority === "high" && "bg-amber-500/15 text-amber-300",
              item.priority === "low" && "bg-white/[0.06] text-muted-foreground",
            )}>
              {item.priority}
            </span>
          )}
        </div>
        <h1 className="mt-1.5 text-base font-semibold">{item.title}</h1>
        <div className="mt-3 flex items-center gap-2.5">
          <SenderAvatar item={item} className="h-8 w-8" />
          <div className="min-w-0">
            <div className="text-xs font-medium text-foreground">
              {item.sender_name || meta.label}
            </div>
            <div className="flex items-center gap-1.5 text-[11px] text-muted-foreground">
              {item.sender_type && <span>{item.sender_type}</span>}
              {item.sender_type && <span>·</span>}
              <span>{relTime(item.created_at)}</span>
              {isResolved && item.resolved_action && (
                <>
                  <span>·</span>
                  <span className="text-emerald-400">{item.resolved_action}</span>
                </>
              )}
            </div>
          </div>
        </div>
      </div>

      {/* Body — rendered markdown (headings, lists, code) rather than a
          raw pre-wrapped blob, so a change plan reads like a document. */}
      {item.body_md && (
        <div className="border-b border-white/[0.06] px-6 py-4">
          <MarkdownContent compact>{item.body_md}</MarkdownContent>
        </div>
      )}

      {/* Kind-specific actions */}
      <div className="px-6 py-4">
        <KindActions item={item} onResolve={onResolve} onRefresh={onRefresh} disabled={isResolved} />
      </div>

      {/* Rich run progress for waitpoints — fetches the underlying
        * pipeline_run + DSL definition so the user can see exactly
        * which step is paused and what each preceding step produced.
        * Only meaningful when the payload has a pipeline_run_id. */}
      {item.kind === "waitpoint" && (() => {
        const runID = item.payload?.pipeline_run_id
        if (typeof runID !== "string" || runID === "") return null
        return (
          <div className="border-t border-white/[0.06] px-6 py-4">
            <div className="mb-2 text-[10px] font-medium uppercase tracking-wider text-muted-foreground/60">
              Run progress
            </div>
            <WaitpointRunDetail
              workspaceId={item.workspace_id}
              pipelineRunId={runID}
              inboxResolved={item.state === "resolved"}
            />
          </div>
        )
      })()}

      {/* Context — humanised key/value summary (secrets masked) instead
          of a raw JSON dump. */}
      {item.payload && Object.keys(item.payload).length > 0 && (
        <div className="border-t border-white/[0.06] px-6 py-4">
          <div className="mb-2.5 text-[10px] uppercase tracking-wider text-muted-foreground/60">
            Context
          </div>
          <ContextDetails payload={item.payload} />
        </div>
      )}

      <div className="mt-auto flex items-center gap-2 border-t border-white/[0.06] bg-card/20 px-6 py-3">
        {!isResolved ? (
          <>
            <Button size="sm" variant="ghost" onClick={onMarkUnread} className="text-xs">
              <Bell className="mr-1.5 h-3 w-3" />
              Mark unread
            </Button>
            {archivable && (
              <Button
                size="sm"
                variant="ghost"
                onClick={() => void onArchive()}
                className="text-xs"
              >
                <Archive className="mr-1.5 h-3 w-3" />
                Archive
              </Button>
            )}
          </>
        ) : item.resolved_action === "archived" ? (
          <span className="flex items-center gap-1.5 text-[11px] text-muted-foreground">
            <Archive className="h-3 w-3" />
            Archived {relTime(item.resolved_at ?? item.updated_at)}
            <Button
              size="sm"
              variant="ghost"
              onClick={onMarkUnread}
              className="ml-1 h-auto px-1.5 py-0.5 text-[11px]"
            >
              <RotateCcw className="mr-1 h-3 w-3" />
              Restore
            </Button>
          </span>
        ) : (
          <span className="text-[11px] text-muted-foreground">
            Resolved {relTime(item.resolved_at ?? item.updated_at)}
            {item.resolved_action && ` · ${item.resolved_action}`}
          </span>
        )}
      </div>
    </div>
  )
}

function KindActions({
  item,
  onResolve,
  onRefresh,
  disabled,
}: {
  item: InboxItem
  onResolve: (action: string) => void | Promise<void>
  onRefresh: () => void | Promise<void>
  disabled: boolean
}) {
  const [busy, setBusy] = useState<string | null>(null)
  const wrap = async (action: string, fn: () => Promise<void>) => {
    setBusy(action)
    try {
      await fn()
    } finally {
      setBusy(null)
    }
  }

  switch (item.kind) {
    case "waitpoint": {
      // PR-D hire waitpoints share the inbox kind='waitpoint' shape
      // but live on a different source: source_id is an agent_id, not
      // a pipeline_waitpoints token, and the approve endpoint is
      // /agents/{id}/approve-hire (which resolves the inbox row
      // server-side via inbox.ResolveBySource). The generic
      // waitpointDecide() helper would 404 against the pipeline
      // waitpoints route for these. Disambiguated by payload.kind,
      // which writeInboxItem sets to "hire" for both blocking and
      // non-blocking hire surfaces (blocking lands as kind=waitpoint).
      if (item.payload?.kind === "hire") {
        return (
          <div className="flex items-center gap-2">
            <Button
              size="sm"
              disabled={disabled || busy !== null}
              onClick={() =>
                wrap("approved", async () => {
                  // fetch() rejects on network failure (offline, DNS,
                  // CORS preflight). Without try/catch the user sees
                  // no toast and the action looks like silent success.
                  let res: Response
                  try {
                    res = await fetch(
                      `/api/v1/agents/${encodeURIComponent(item.source_id)}/approve-hire`,
                      {
                        method: "POST",
                        headers: { "Content-Type": "application/json" },
                      },
                    )
                  } catch (e) {
                    toast.error(e instanceof Error ? `Approve failed: ${e.message}` : "Approve failed (network error)")
                    return
                  }
                  if (!res.ok) {
                    const body = (await res.json().catch(() => null)) as
                      | { error?: string; reason?: string }
                      | null
                    toast.error(body?.error ?? `Approve failed (${res.status})`)
                    return
                  }
                  toast.success("Hire approved — agent is live")
                  await onRefresh()
                })
              }
              className="gap-1.5 bg-emerald-500/20 text-emerald-300 hover:bg-emerald-500/30"
            >
              <CheckCircle2 className="h-3 w-3" />
              {busy === "approved" ? "Approving…" : "Approve hire"}
            </Button>
            {/* No deny counterpart exists for approve-hire yet — the
                PENDING_REVIEW agent stays put until the operator fires
                it from the crew. Surface that explicitly so the
                missing button doesn't read as broken UI. */}
            <span className="text-[11px] text-muted-foreground">
              To deny, fire the agent from its crew page.
            </span>
          </div>
        )
      }
      // Both Approve and Deny hit the same /approve endpoint —
      // the body's `approved` boolean is what disambiguates. An empty
      // body decoded to approved=false because Go's JSON unmarshal
      // gives bools their zero value when absent, so a "{}" body was
      // silently equivalent to denying. The earlier "already decided
      // or expired" complaint was the second click hitting the
      // already-denied row.
      return (
        <div className="flex items-center gap-2">
          <Button
            size="sm"
            disabled={disabled || busy !== null}
            onClick={() =>
              wrap("approved", async () => {
                const res = await waitpointDecide(item.workspace_id, item.source_id, true)
                if (!res.ok) {
                  toast.error(res.error)
                  return
                }
                // Server-side CompleteApproval already cascades
                // inbox state via inbox.ResolveBySource — the local
                // onResolve mostly ensures the optimistic UI
                // matches before the WS event arrives.
                await onResolve("approved")
              })
            }
            className="gap-1.5 bg-emerald-500/20 text-emerald-300 hover:bg-emerald-500/30"
          >
            <CheckCircle2 className="h-3 w-3" />
            {busy === "approved" ? "Approving…" : "Approve"}
          </Button>
          <Button
            size="sm"
            variant="ghost"
            disabled={disabled || busy !== null}
            onClick={() =>
              wrap("denied", async () => {
                const res = await waitpointDecide(item.workspace_id, item.source_id, false)
                if (!res.ok) {
                  toast.error(res.error)
                  return
                }
                await onResolve("denied")
              })
            }
            className="gap-1.5"
          >
            <XCircle className="h-3 w-3" />
            {busy === "denied" ? "Denying…" : "Deny"}
          </Button>
        </div>
      )
    }
    case "escalation": {
      // An escalation is an agent decision request — resolving it must go
      // through the escalation lifecycle (/escalations/{id}/resolve), NOT
      // a blind inbox flip (that 409s, since escalation is source-managed).
      // Real agent escalations carry escalation_type + a source_id that IS
      // the escalations-row id. Keeper/synthetic escalations don't — for
      // those the inbox can't resolve inline, so we point at the source.
      const escType =
        typeof item.payload?.escalation_type === "string"
          ? (item.payload.escalation_type as string)
          : ""
      const resolveEsc = (action: "approve" | "reject") =>
        wrap(action, async () => {
          const res = await escalationResolve(
            item.source_id,
            action,
            action === "approve" ? "Approved from inbox" : "Rejected from inbox",
            item.workspace_id,
          )
          if (!res.ok) {
            // 404 = no escalations row behind this item (keeper/synthetic):
            // tell the user where to handle it instead of a raw error.
            toast.error(
              res.status === 404
                ? "Resolve this from its source (crew escalations / review panel)."
                : res.error,
            )
            return
          }
          // The lifecycle cascades the inbox row to resolved via
          // ResolveBySource server-side; refresh to pick that up.
          toast.success(`Escalation ${action === "approve" ? "approved" : "rejected"}`)
          await onRefresh()
        })

      // CREDENTIAL escalations: when the agent already proposed a value, it is
      // sitting in the vault as PENDING_APPROVAL, so Approve just activates it
      // (no secret to type here) and Reject discards it — one-click both ways.
      // Legacy CREDENTIAL escalations (no pending credential, the human must
      // supply the secret) keep Reject-only and point at the crew panel.
      if (escType === "CREDENTIAL") {
        if (item.payload?.has_pending_credential === true) {
          return (
            <div className="flex items-center gap-2">
              <Button
                size="sm"
                disabled={disabled || busy !== null}
                onClick={() => resolveEsc("approve")}
                className="gap-1.5 bg-emerald-500/20 text-emerald-300 hover:bg-emerald-500/30"
              >
                <CheckCircle2 className="h-3 w-3" />
                {busy === "approve" ? "Approving…" : "Approve"}
              </Button>
              <Button
                size="sm"
                variant="ghost"
                disabled={disabled || busy !== null}
                onClick={() => resolveEsc("reject")}
                className="gap-1.5"
              >
                <XCircle className="h-3 w-3" />
                {busy === "reject" ? "Rejecting…" : "Reject"}
              </Button>
            </div>
          )
        }
        return (
          <div className="flex items-center gap-2">
            <Button
              size="sm"
              variant="ghost"
              disabled={disabled || busy !== null}
              onClick={() => resolveEsc("reject")}
              className="gap-1.5"
            >
              <XCircle className="h-3 w-3" />
              {busy === "reject" ? "Rejecting…" : "Reject"}
            </Button>
            <span className="text-[11px] text-muted-foreground">
              To grant the credential, resolve from the crew’s escalations panel.
            </span>
          </div>
        )
      }
      // Real agent escalation (non-credential): inline approve / reject.
      if (escType !== "") {
        return (
          <div className="flex items-center gap-2">
            <Button
              size="sm"
              disabled={disabled || busy !== null}
              onClick={() => resolveEsc("approve")}
              className="gap-1.5 bg-emerald-500/20 text-emerald-300 hover:bg-emerald-500/30"
            >
              <CheckCircle2 className="h-3 w-3" />
              {busy === "approve" ? "Approving…" : "Approve"}
            </Button>
            <Button
              size="sm"
              variant="ghost"
              disabled={disabled || busy !== null}
              onClick={() => resolveEsc("reject")}
              className="gap-1.5"
            >
              <XCircle className="h-3 w-3" />
              {busy === "reject" ? "Rejecting…" : "Reject"}
            </Button>
          </div>
        )
      }
      // Keeper / synthetic escalation — no inline decision to make here; the
      // source review tracks the outcome. There's no resolve endpoint behind
      // it, so the inbox row is the only handle: point at Archive (enabled
      // for these) rather than a button that 409s.
      return (
        <span className="text-[11px] text-muted-foreground">
          No decision to make here — its source review tracks the outcome. Use “Archive” to clear it
          from your inbox.
        </span>
      )
    }
    case "failed_run":
      // Retry actually re-fires the routine: POST /pipelines/{slug}/run
      // with the same inputs that produced the failure (replayed from
      // the run's inputs_json so dynamic context is preserved). The
      // payload carries the slug + inputs the writer captured at
      // failure time. If the slug is missing we fall back to just
      // marking the inbox item resolved so the user isn't stuck.
      return (
        <div className="flex items-center gap-2">
          <Button
            size="sm"
            disabled={disabled || busy !== null}
            onClick={() =>
              wrap("retried", async () => {
                const slug = (item.payload?.pipeline_slug ??
                  item.sender_name) as string | undefined
                const inputs = (item.payload?.inputs ?? {}) as Record<string, unknown>
                if (!slug) {
                  toast.error("Cannot retry — pipeline slug missing in payload")
                  await onResolve("cancelled")
                  return
                }
                // Same try/catch pattern as approve-hire above: fetch()
                // rejects on network failure (offline, DNS, CORS); the
                // wrap() helper clears busy state on return, so without
                // explicit error handling the user sees no toast and
                // the retry appears to silently succeed.
                let res: Response
                try {
                  res = await fetch(
                    `/api/v1/workspaces/${encodeURIComponent(item.workspace_id)}/pipelines/${encodeURIComponent(slug)}/run`,
                    {
                      method: "POST",
                      headers: { "Content-Type": "application/json" },
                      body: JSON.stringify({ inputs, triggered_via: "manual" }),
                    },
                  )
                } catch (e) {
                  toast.error(e instanceof Error ? `Retry failed: ${e.message}` : "Retry failed (network error)")
                  return
                }
                if (!res.ok) {
                  const body = await res.json().catch(() => null)
                  toast.error(body?.error ?? "Retry failed")
                  return
                }
                toast.success(`Routine ${slug} re-queued — see /activity`)
                await onResolve("retried")
              })
            }
            className="gap-1.5"
          >
            <ScrollText className="h-3 w-3" />
            {busy === "retried" ? "Retrying…" : "Retry"}
          </Button>
          <Button
            size="sm"
            variant="ghost"
            disabled={disabled || busy !== null}
            onClick={() => wrap("cancelled", async () => onResolve("cancelled"))}
            className="gap-1.5"
          >
            Cancel
          </Button>
        </div>
      )
    case "message":
      // Messages from the orchestrator (e.g. "ENG-1 ready for review")
      // carry the issue identifier in payload so the inbox can offer
      // a one-click jump to the issue. Without this the user reads
      // the title and has nowhere to go.
      return (
        <div className="flex items-center gap-2">
          {typeof item.payload?.issue_identifier === "string" && (
            <Button asChild size="sm" className="gap-1.5">
              <Link
                href={`/issues/${encodeURIComponent(item.payload.issue_identifier as string)}`}
              >
                <CircleDot className="h-3 w-3" />
                Open {item.payload.issue_identifier}
              </Link>
            </Button>
          )}
          <Button
            size="sm"
            variant="ghost"
            disabled={disabled || busy !== null}
            onClick={() => wrap("dismissed", async () => onResolve("dismissed"))}
            className="gap-1.5"
          >
            Dismiss
          </Button>
        </div>
      )
    default:
      return (
        <Button
          size="sm"
          disabled={disabled || busy !== null}
          onClick={() => wrap("dismissed", async () => onResolve("dismissed"))}
          className="gap-1.5"
        >
          Dismiss
        </Button>
      )
  }
}

function InboxEmpty({ filter }: { filter: StateFilter }) {
  return (
    <PageEmptyState
      size="inline"
      icon={InboxIcon}
      title={
        filter === "unread"
          ? "All caught up"
          : filter === "archived"
            ? "Nothing archived yet"
            : "Inbox zero"
      }
      description={
        filter === "archived"
          ? "Archived items live here. Archive a message to clear it from your inbox without losing it."
          : "Agents have nothing waiting on you. New waitpoints, escalations, failed runs, and messages will appear here."
      }
    />
  )
}

function DetailEmpty() {
  return (
    <PageEmptyState
      size="inline"
      icon={InboxIcon}
      title="Select an item"
      description="Pick a waitpoint, escalation, failed run, or message on the left to see details."
    />
  )
}

function relTime(iso?: string) {
  if (!iso) return "—"
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return "—"
  const diff = Date.now() - d.getTime()
  if (Math.abs(diff) < 60_000) return "just now"
  const mins = Math.round(Math.abs(diff) / 60_000)
  if (mins < 60) return `${mins}m ago`
  const hrs = Math.round(mins / 60)
  if (hrs < 24) return `${hrs}h ago`
  const days = Math.round(hrs / 24)
  return `${days}d ago`
}

