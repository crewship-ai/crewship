"use client"

import {
  AlertCircle, AlertTriangle, Bell, Brain, CircleSlash, Clock3, History,
  ListChecks, MessageSquare, ShieldCheck, Workflow, type LucideIcon,
} from "lucide-react"

import { ActorAvatar } from "@/components/features/inbox/inbox-actor"
import { remainingLabel, since, subjectOf } from "@/components/features/inbox/inbox-derive"
import {
  SidebarActiveChip, SidebarActiveChips, SidebarCollapseButton, SidebarFacet,
  SidebarFacetOption, SidebarFilterPopover, SidebarRow, SidebarSearch,
  SidebarSection, SidebarToolbar,
} from "@/components/layout/sidebar-kit"
import { cn } from "@/lib/utils"

import {
  deadlineBucket, entryType, facetCounts, INBOX_V2_TYPES, isArchivedNotDecided,
  type InboxV2DeadlineKey, type InboxV2Filters, type InboxV2TypeKey,
} from "./inbox-v2-derive"
import type { InboxV2Entry, InboxV2View } from "./inbox-v2-types"

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
}

export function InboxV2Explorer({
  view, onView, viewCounts, entries, visible, filters, onFilters,
  selectedKey, onOpen, onToggleCollapse, onMarkAllRead,
}: Props) {
  const counts = facetCounts(entries)
  const activeCount = (filters.type ? 1 : 0) + (filters.deadline ? 1 : 0) + (filters.unreadOnly ? 1 : 0)
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
            placeholder="Search inbox, agents…"
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
                />
              ))}
            </div>
          ))}
          {visible.length === 0 && (
            <div className="flex items-center justify-center py-6 text-xs text-muted-foreground-soft">
              {activeCount > 0 || filters.search ? "Nothing matches those filters" : "Nothing here"}
            </div>
          )}
        </div>
      </div>
    </div>
  )
}

/** SidebarFacetOption has no count slot; the kit's Check claims ml-auto, and
 *  the first auto margin wins, so the count carries it and the tick follows. */
function FacetCount({ value }: { value: number }) {
  return <span className="ml-auto shrink-0 tabular-nums text-[10px] opacity-70">{value}</span>
}

function EntryRow({
  entry, selected, onOpen,
}: { entry: InboxV2Entry; selected: boolean; onOpen: () => void }) {
  const deadlineMins = entry.deadlineAt
    ? Math.round((Date.parse(entry.deadlineAt) - Date.now()) / 60_000)
    : null
  const actor = entry.inboxItem ? subjectOf(entry.inboxItem) : null
  const type = entryType(entry)
  const Icon = type ? TYPE_ICON[type] : MessageSquare
  const expiring = deadlineMins != null && entry.actionable && deadlineBucket(entry) === "hour"

  return (
    <SidebarRow as="div" selected={selected} onSelect={onOpen} className="items-start py-1.5">
      <span className="relative mt-0.5 shrink-0">
        {actor ? (
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
      <span className="min-w-0 flex-1">
        <span className={cn("block truncate", entry.unread ? "font-semibold text-foreground" : "text-foreground/80")}>
          {entry.title}
        </span>
        <span className="mt-0.5 block truncate text-[11px] text-muted-foreground">
          {entry.subject}
          {entry.historical && entry.outcome ? ` · ${entry.outcome}` : ""}
        </span>
      </span>
      <span className={cn(
        "shrink-0 self-start text-[10px] tabular-nums",
        expiring ? "font-semibold text-destructive" : "text-muted-foreground-soft",
      )}>
        {deadlineMins != null && entry.actionable
          ? deadlineMins > 0 ? remainingLabel(deadlineMins) : "expired"
          : since(entry.createdAt)}
      </span>
    </SidebarRow>
  )
}
