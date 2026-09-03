"use client"

import { useMemo } from "react"
import Link from "next/link"

import {
  AlertCircle, AlertTriangle, Bell, Brain, CircleSlash, Clock3, History,
  Inbox, ListChecks, MessageSquare, ShieldCheck, Workflow, type LucideIcon,
} from "lucide-react"

import { ActorAvatar } from "@/components/features/inbox/inbox-actor"
import { remainingLabel, since, subjectOf } from "@/components/features/inbox/inbox-derive"
import {
  SidebarActiveChip, SidebarActiveChips, SidebarCollapseButton, SidebarFacet,
  SidebarFacetOption, SidebarFilterPopover, SidebarRow, SidebarSearch,
  SidebarSection, SidebarToolbar,
} from "@/components/layout/sidebar-kit"
import { AgentAvatar } from "@/components/ui/agent-avatar"
import { InlineEmpty } from "@/components/ui/inline-empty"
import { StatusPill } from "@/components/ui/status-pill"
import { entityHref } from "@/lib/entity-links"
import { crewColor } from "@/app/(dashboard)/dashboard-helpers"
import { cn } from "@/lib/utils"

import {
  deadlineBucket, entryAgentRef, entryCrewId, entryKindPill, entryTitle, entryType, entryVerb,
  facetCounts, INBOX_V2_TYPES, isArchivedNotDecided, outcomeStatus,
  type InboxV2DeadlineKey, type InboxV2Filters, type InboxV2TypeKey,
} from "./inbox-v2-derive"
import { EMPTY_INBOX_LOOKUP, type InboxLookup, type InboxV2Entry, type InboxV2View } from "./inbox-v2-types"

/**
 * The inbox column, built on the shared sidebar-kit — the same explorer
 * Routines, Issues, Crews and Pages use.
 *
 * What this replaces: a 190px rail that held three nav rows, and three raw
 * <select>s whose options were not answerable. Two of those three were client
 * fictions (see the note on INBOX_V2_TYPES), which is why the facets here are
 * type / deadline / unread and nothing else.
 *
 * The popover is the KIT's, not a hand-rolled dropdown: it owns Escape, the
 * dismiss layer, the aria wiring, and — the reason it exists — staying OPEN
 * when a facet is picked, so two facets can be combined in one visit.
 *
 * The ROW is a decision, not a log line (docs/ux/audit-conversations.md
 * P1-1): a kind pill, the question without the server's prefix, the crew and
 * the agent by name, the age or the expiry, and a verb.
 */

const VIEWS: { key: InboxV2View; label: string; icon: LucideIcon; tone: string }[] = [
  { key: "action", label: "Needs action", icon: ListChecks, tone: "text-warn" },
  { key: "updates", label: "Updates", icon: Bell, tone: "text-primary" },
  { key: "history", label: "History", icon: History, tone: "text-success" },
]

const TYPE_ICON: Record<InboxV2TypeKey, LucideIcon> = {
  waitpoint: ShieldCheck,
  escalation: AlertCircle,
  failed_run: AlertTriangle,
  message: MessageSquare,
  memory_consolidation: Brain,
  schedule_missed: Clock3,
  schedule_circuit_breaker_tripped: CircleSlash,
  approval: ShieldCheck,
  mission: Workflow,
}

const DEADLINES: { key: InboxV2DeadlineKey; label: string }[] = [
  { key: "hour", label: "Within the hour" },
  { key: "today", label: "Today" },
  { key: "none", label: "No deadline" },
]

const TYPE_LABEL: Record<string, string> = Object.fromEntries(
  INBOX_V2_TYPES.map((t) => [t.key, t.label]),
)

interface Props {
  view: InboxV2View
  onView: (view: InboxV2View) => void
  viewCounts: Record<InboxV2View, number>
  /** The whole feed for this view — facet counts are computed over it. */
  entries: InboxV2Entry[]
  /** Filtered and sorted; what actually renders. */
  visible: InboxV2Entry[]
  filters: InboxV2Filters
  onFilters: (filters: InboxV2Filters) => void
  selectedKey: string | null
  onOpen: (entry: InboxV2Entry) => void
  onToggleCollapse?: () => void
  /** Rendered as a section action, the way SidebarSection takes them. */
  onMarkAllRead?: () => void
  /** Crews and agents by id/slug, so rows can name them. */
  lookup?: InboxLookup
}

export function InboxV2Explorer({
  view, onView, viewCounts, entries, visible, filters, onFilters,
  selectedKey, onOpen, onToggleCollapse, onMarkAllRead, lookup = EMPTY_INBOX_LOOKUP,
}: Props) {
  // Memoised on the feed, not on the render: the explorer re-renders on every
  // keystroke in search, and the counts do not depend on the filters at all.
  // With loadAll the feed is the whole history, so this was an O(n) sweep per
  // character typed.
  const counts = useMemo(() => facetCounts(entries), [entries])
  const activeCount = (filters.type ? 1 : 0) + (filters.deadline ? 1 : 0) + (filters.unreadOnly ? 1 : 0)
  const narrowed = activeCount > 0 || filters.search.trim() !== ""
  const set = (patch: Partial<InboxV2Filters>) => onFilters({ ...filters, ...patch })

  const sections = view === "updates"
    ? [
        { label: "Agent replies", rows: visible.filter((e) => e.category === "chat.replies") },
        { label: "Important updates", rows: visible.filter((e) => e.category !== "chat.replies") },
      ]
    : view === "history"
      ? [
          { label: "Decisions", rows: visible.filter((e) => !isArchivedNotDecided(e)) },
          { label: "Archived", rows: visible.filter(isArchivedNotDecided) },
        ]
      : [{ label: "Waiting for you", rows: visible }]

  return (
    <div className="flex h-full flex-col">
      <SidebarToolbar>
        <div data-inbox-search className="min-w-0 flex-1">
          <SidebarSearch
            value={filters.search}
            onValueChange={(search) => set({ search })}
            placeholder="Search inbox, agents, crews…"
          />
        </div>
        <SidebarFilterPopover
          label="Filter inbox"
          activeCount={activeCount}
          onClear={() => set({ type: null, deadline: null, unreadOnly: false })}
        >
          <SidebarFacet
            label="Type"
            resetLabel="Any type"
            resetActive={!filters.type}
            onReset={() => set({ type: null })}
            first
          >
            {INBOX_V2_TYPES.map((t) => {
              const Icon = TYPE_ICON[t.key]
              return (
                <SidebarFacetOption
                  key={t.key}
                  active={filters.type === t.key}
                  onToggle={() => set({ type: filters.type === t.key ? null : t.key })}
                >
                  <Icon className="h-3.5 w-3.5 shrink-0" />
                  {t.label}
                  <FacetCount value={counts.type[t.key]} />
                </SidebarFacetOption>
              )
            })}
          </SidebarFacet>

          <SidebarFacet
            label="Deadline"
            resetLabel="Any time"
            resetActive={!filters.deadline}
            onReset={() => set({ deadline: null })}
          >
            {DEADLINES.map((d) => (
              <SidebarFacetOption
                key={d.key}
                active={filters.deadline === d.key}
                onToggle={() => set({ deadline: filters.deadline === d.key ? null : d.key })}
              >
                <Clock3 className="h-3.5 w-3.5 shrink-0" />
                {d.label}
                <FacetCount value={counts.deadline[d.key]} />
              </SidebarFacetOption>
            ))}
          </SidebarFacet>

          <SidebarFacet
            label="State"
            resetLabel="Read and unread"
            resetActive={!filters.unreadOnly}
            onReset={() => set({ unreadOnly: false })}
          >
            <SidebarFacetOption
              active={filters.unreadOnly}
              onToggle={() => set({ unreadOnly: !filters.unreadOnly })}
            >
              <Bell className="h-3.5 w-3.5 shrink-0" />
              Unread only
              <FacetCount value={counts.unread} />
            </SidebarFacetOption>
          </SidebarFacet>
        </SidebarFilterPopover>
        {onToggleCollapse && <SidebarCollapseButton collapsed={false} onToggle={onToggleCollapse} />}
      </SidebarToolbar>

      <SidebarActiveChips className="border-b border-white/[0.06] pt-2">
        {filters.type && (
          <SidebarActiveChip onRemove={() => set({ type: null })}>{TYPE_LABEL[filters.type]}</SidebarActiveChip>
        )}
        {filters.deadline && (
          <SidebarActiveChip onRemove={() => set({ deadline: null })}>
            {DEADLINES.find((d) => d.key === filters.deadline)?.label}
          </SidebarActiveChip>
        )}
        {filters.unreadOnly && (
          <SidebarActiveChip onRemove={() => set({ unreadOnly: false })}>Unread only</SidebarActiveChip>
        )}
      </SidebarActiveChips>

      <SidebarSection label="View" count={VIEWS.length} className="border-b border-white/[0.06]">
        {VIEWS.map((v) => {
          const Icon = v.icon
          const count = viewCounts[v.key]
          const selected = view === v.key
          return (
            <SidebarRow as="div" key={v.key} selected={selected} onSelect={() => onView(v.key)}>
              <Icon className={cn("h-3.5 w-3.5 shrink-0", v.tone, count === 0 && !selected && "opacity-40")} />
              <span className={cn("flex-1 truncate", count === 0 && !selected ? "text-foreground/40" : "text-foreground/80")}>
                {v.label}
              </span>
              <span className={cn(
                "rounded-full px-1.5 py-px text-[10px] tabular-nums",
                count === 0
                  ? "text-muted-foreground-soft/50"
                  : selected
                    ? "bg-primary/15 text-primary"
                    : "bg-white/[0.05] text-muted-foreground",
              )}>
                {count}
              </span>
            </SidebarRow>
          )
        })}
      </SidebarSection>

      <div className="flex min-h-0 flex-1 flex-col">
        <div className="min-h-0 flex-1 overflow-y-auto pb-1">
          {sections.filter((s) => s.rows.length > 0).map((section, index) => (
            <div key={section.label}>
              <SidebarSection
                label={section.label}
                count={section.rows.length}
                actions={index === 0 && onMarkAllRead ? (
                  <button
                    type="button"
                    onClick={onMarkAllRead}
                    className="text-[10px] text-muted-foreground/80 transition-colors hover:text-foreground"
                  >
                    Mark all read
                  </button>
                ) : undefined}
              />
              {section.rows.map((entry) => (
                <EntryRow
                  key={entry.key}
                  entry={entry}
                  selected={selectedKey === entry.key}
                  onOpen={() => onOpen(entry)}
                  lookup={lookup}
                />
              ))}
            </div>
          ))}
          {visible.length === 0 && (
            <div className="p-2">
              <ExplorerEmpty
                view={view}
                narrowed={narrowed}
                onClear={() => onFilters({ search: "", type: null, deadline: null, unreadOnly: false })}
              />
            </div>
          )}
        </div>
      </div>
    </div>
  )
}

/**
 * The column's empty state says what lands here and one way to make it
 * appear (README §6) — "Nothing here" said neither.
 */
function ExplorerEmpty({ view, narrowed, onClear }: { view: InboxV2View; narrowed: boolean; onClear: () => void }) {
  if (narrowed) {
    return (
      <InlineEmpty
        icon={Inbox}
        text="Nothing matches those filters."
        action={
          <button type="button" onClick={onClear} className="text-primary-hover hover:underline">
            Clear
          </button>
        }
      />
    )
  }
  if (view === "updates") {
    return (
      <InlineEmpty
        icon={Bell}
        text="No updates. Agent replies, routine progress and issue reviews land here."
        action={<Link href={entityHref({ kind: "routines" })} className="text-primary-hover hover:underline">Routines →</Link>}
      />
    )
  }
  if (view === "history") {
    return <InlineEmpty icon={History} text="No decisions recorded yet. Decided and archived items stay here." />
  }
  return (
    <InlineEmpty
      icon={ListChecks}
      text="Nothing is waiting on you. Approvals, questions from agents, failed runs and missed schedules land here."
      action={<Link href={entityHref({ kind: "crew", slug: "" }).replace(/\?.*$/, "")} className="text-primary-hover hover:underline">Crews →</Link>}
    />
  )
}

/** SidebarFacetOption has no count slot; the kit's Check claims ml-auto, and
 *  the first auto margin wins, so the count carries it and the tick follows. */
function FacetCount({ value }: { value: number }) {
  return <span className="ml-auto shrink-0 tabular-nums text-[10px] opacity-70">{value}</span>
}

function EntryRow({
  entry, selected, onOpen, lookup,
}: { entry: InboxV2Entry; selected: boolean; onOpen: () => void; lookup: InboxLookup }) {
  const deadlineMins = entry.deadlineAt
    ? Math.round((Date.parse(entry.deadlineAt) - Date.now()) / 60_000)
    : null
  const pill = entryKindPill(entry)
  const title = entryTitle(entry)
  const verb = entryVerb(entry)
  const crewId = entryCrewId(entry)
  const crew = crewId ? lookup.crewById.get(crewId) ?? null : null
  const ref = entryAgentRef(entry)
  const agent = ref.slug ? lookup.agentBySlug.get(ref.slug) ?? null : null
  const crewName = crew?.name ?? agent?.crew?.name ?? null
  const crewTint = crewColor(crew?.color ?? agent?.crew?.color ?? null)
  const agentLabel = agent?.name ?? ref.label
  const actor = entry.inboxItem ? subjectOf(entry.inboxItem) : null
  const type = entryType(entry)
  const Icon = type ? TYPE_ICON[type] : MessageSquare
  const expiring = deadlineMins != null && entry.actionable && deadlineBucket(entry) === "hour"
  const outcome = entry.historical ? outcomeStatus(entry.outcome) : null

  return (
    <SidebarRow as="div" selected={selected} onSelect={onOpen} className="items-start py-1.5">
      <span className="relative mt-0.5 shrink-0">
        {agent ? (
          <AgentAvatar
            seed={agent.avatar_seed || agent.slug}
            style={agent.avatar_style}
            agentId={agent.id}
            avatarUrl={agent.avatar_url}
            alt=""
            className="h-5 w-5 rounded-md"
          />
        ) : actor ? (
          <ActorAvatar actor={actor} size={20} />
        ) : (
          <span className={cn(
            "flex h-5 w-5 items-center justify-center rounded-md",
            entry.actionable ? "bg-warn/10 text-warn" : "bg-white/[0.05] text-muted-foreground",
          )}>
            <Icon className="h-3 w-3" />
          </span>
        )}
        {entry.unread && (
          <span aria-hidden className="absolute -right-0.5 -top-0.5 h-1.5 w-1.5 rounded-full bg-info ring-2 ring-card" />
        )}
      </span>
      <span className="flex min-w-0 flex-1 flex-col gap-0.5">
        <span className="flex min-w-0 items-center gap-1.5">
          {outcome ? <StatusPill status={outcome} /> : <StatusPill tone={pill.tone} label={pill.label} />}
          <span className={cn("min-w-0 truncate", entry.unread ? "font-semibold text-foreground" : "text-foreground/80")}>
            {title}
          </span>
        </span>
        <span className="flex min-w-0 items-center gap-1 truncate text-[11px] text-muted-foreground">
          {crewName && (
            <>
              <span className="h-[7px] w-[7px] shrink-0 rounded-full" style={{ background: crewTint }} aria-hidden />
              <span className="truncate">{crewName}</span>
              <span className="text-muted-foreground-soft">·</span>
            </>
          )}
          <span className="truncate">{agentLabel}</span>
          <span className="text-muted-foreground-soft">·</span>
          <span className={cn("shrink-0 tabular-nums", expiring && "font-semibold text-destructive")}>
            {deadlineMins != null && entry.actionable
              ? deadlineMins > 0 ? `expires in ${remainingLabel(deadlineMins)}` : "expired"
              : since(entry.createdAt)}
          </span>
        </span>
      </span>
      <span
        className={cn(
          "kit-tap mt-0.5 inline-flex h-6 shrink-0 items-center rounded-md border px-2 text-[11px] font-medium",
          entry.actionable
            ? "border-border bg-card text-foreground group-hover:border-primary/40"
            : "border-transparent text-muted-foreground",
        )}
        aria-hidden
      >
        {verb}
      </span>
    </SidebarRow>
  )
}
