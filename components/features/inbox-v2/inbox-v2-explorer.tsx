"use client"

import { useMemo } from "react"
import Link from "next/link"

import {
  AlertCircle, AlertTriangle, Bell, Brain, CircleSlash, Clock3, History,
  Inbox, ListChecks, MessageSquare, ShieldCheck, Workflow, type LucideIcon,
} from "lucide-react"

import { remainingLabel, since } from "@/components/features/inbox/inbox-derive"
import {
  SidebarActiveChip, SidebarActiveChips, SidebarCollapseButton, SidebarFacet,
  SidebarFacetOption, SidebarFilterPopover, SidebarRow, SidebarSearch,
  SidebarSection, SidebarToolbar,
} from "@/components/layout/sidebar-kit"
import { CrewIcon } from "@/components/ui/crew-icon"
import { InboxCrewPicker } from "./inbox-crew-picker"
import { EntryAvatar, entryIdentity } from "./inbox-entry-identity"
import { ATTENTION_LABELS, type InboxAttention } from "./inbox-v2-attention"
import { InlineEmpty } from "@/components/ui/inline-empty"
import { StatusPill } from "@/components/ui/status-pill"
import { entityHref } from "@/lib/entity-links"
import { cn } from "@/lib/utils"

import {
  deadlineBucket, entryKindPill, entryTitle,
  facetCounts, INBOX_V2_TYPES, isArchivedNotDecided, needsHumanDecision, outcomeStatus,
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
 * Rows use the compact Routines explorer density: title, identity and age.
 * Decisions additionally retain their kind/outcome and expiry.
 */

const VIEWS: { key: InboxV2View; label: string; icon: LucideIcon; tone: string }[] = [
  { key: "action", label: "To handle", icon: ListChecks, tone: "text-warn" },
  { key: "updates", label: "Updates", icon: Bell, tone: "text-primary" },
  { key: "history", label: "History", icon: History, tone: "text-muted-foreground" },
]

const TYPE_ICON: Record<InboxV2TypeKey, LucideIcon> = {
  waitpoint: ShieldCheck,
  escalation: AlertCircle,
  failed_run: AlertTriangle,
  message: MessageSquare,
  memory_consolidation: Brain,
  schedule_missed: Clock3,
  schedule_circuit_breaker_tripped: CircleSlash,
  run_needs_human: AlertCircle,
  webhook_fire_failed: AlertTriangle,
  automation_enqueue_failed: AlertTriangle,
  approval: ShieldCheck,
  mission: Workflow,
}

const DEADLINES: { key: InboxV2DeadlineKey; label: string }[] = [
  { key: "soon", label: "Due within 24 hours" },
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
  attention?: InboxAttention | null
  onClearAttention?: () => void
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
  view, onView, viewCounts, entries, visible, filters, onFilters, attention, onClearAttention,
  selectedKey, onOpen, onToggleCollapse, onMarkAllRead, lookup = EMPTY_INBOX_LOOKUP,
}: Props) {
  // Memoised on the feed, not on the render: the explorer re-renders on every
  // keystroke in search, and the counts do not depend on the filters at all.
  // With loadAll the feed is the whole history, so this was an O(n) sweep per
  // character typed.
  const counts = useMemo(() => facetCounts(entries), [entries])
  const activeCount = (filters.type ? 1 : 0) + (filters.deadline ? 1 : 0) + (filters.unreadOnly ? 1 : 0)
  const crewChip = filters.crew ? lookup.crewById.get(filters.crew)?.name ?? "Crew" : null
  const narrowed = Boolean(attention) || activeCount > 0 || Boolean(filters.crew) || filters.search.trim() !== ""
  const set = (patch: Partial<InboxV2Filters>) => onFilters({ ...filters, ...patch })

  const sections = attention
    ? [{ label: ATTENTION_LABELS[attention], rows: visible }]
    : view === "updates"
    ? [
        { label: "Replies & results", rows: visible.filter((e) => e.category === "chat.replies") },
        { label: "Important updates", rows: visible.filter((e) => e.category !== "chat.replies") },
      ]
    : view === "history"
      ? [
          { label: "Decisions", rows: visible.filter((e) => !isArchivedNotDecided(e)) },
          { label: "Archived", rows: visible.filter(isArchivedNotDecided) },
        ]
      : [
          { label: "Decision required", rows: visible.filter(needsHumanDecision) },
          { label: "Operational alerts", rows: visible.filter((entry) => !needsHumanDecision(entry)) },
        ]

  return (
    <div className="flex h-full flex-col">
      <SidebarToolbar>
        <div data-inbox-search className="min-w-0 flex-1">
          <SidebarSearch
            value={filters.search}
            onValueChange={(search) => set({ search })}
            placeholder="Search inbox…"
          />
        </div>
        <SidebarFilterPopover
          label="Filter inbox"
          className="[&>button]:text-muted-foreground"
          activeCount={activeCount}
          onClear={() => set({ type: null, deadline: null, unreadOnly: false })}
        >
          <SidebarFacet
            label="State"
            resetLabel="Read and unread"
            resetActive={!filters.unreadOnly}
            onReset={() => set({ unreadOnly: false })}
            first
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

          {(counts.deadline.soon > 0 || filters.deadline) && <SidebarFacet
            label="Deadline"
            resetLabel="Any time"
            resetActive={!filters.deadline}
            onReset={() => set({ deadline: null })}
          >
            {DEADLINES.filter((d) => counts.deadline[d.key] > 0 || filters.deadline === d.key).map((d) => (
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
          </SidebarFacet>}

          <details className="border-t border-border/50 px-2 py-2" key={filters.type ? "selected-type" : "all-types"} open={filters.type ? true : undefined}>
            <summary className="cursor-pointer text-[11px] text-muted-foreground">Advanced · source type</summary>
            <SidebarFacet label="Source type" resetLabel="Any type" resetActive={!filters.type} onReset={() => set({ type: null })}>
              {INBOX_V2_TYPES.filter((t) => counts.type[t.key] > 0 || filters.type === t.key).map((t) => {
                const Icon = TYPE_ICON[t.key]
                return <SidebarFacetOption
                  key={t.key}
                  active={filters.type === t.key}
                  onToggle={() => set({ type: filters.type === t.key ? null : t.key })}
                >
                  <Icon className="h-3.5 w-3.5 shrink-0" />
                  {t.label}
                  <FacetCount value={counts.type[t.key]} />
                </SidebarFacetOption>
              })}
            </SidebarFacet>
          </details>
        </SidebarFilterPopover>
        {onToggleCollapse && <span className="hidden lg:inline-flex"><SidebarCollapseButton collapsed={false} onToggle={onToggleCollapse} /></span>}
      </SidebarToolbar>

      <div className="shrink-0 border-b border-border/60 pb-1" aria-label="Inbox views">
        {VIEWS.map((v) => <SidebarRow as="div" key={v.key} selected={!attention && view === v.key} onSelect={() => onView(v.key)} aria-pressed={!attention && view === v.key}>
          <v.icon className={cn("h-3.5 w-3.5 shrink-0", v.tone)} aria-hidden />
          <span className="flex-1">{v.label}</span>
          <span className="text-micro tabular-nums text-muted-foreground">{viewCounts[v.key]}</span>
        </SidebarRow>)}
      </div>
      <div className="shrink-0 px-2 pb-2 pt-1">
        <InboxCrewPicker lookup={lookup} entries={entries} value={filters.crew} onChange={(crew) => set({ crew })} />
      </div>
      <SidebarActiveChips className="border-b border-white/[0.06] pt-2">
        {attention && <SidebarActiveChip onRemove={onClearAttention ?? (() => {})}>{ATTENTION_LABELS[attention]}</SidebarActiveChip>}
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
        {crewChip && (
          <SidebarActiveChip onRemove={() => set({ crew: null })}>{crewChip}</SidebarActiveChip>
        )}
      </SidebarActiveChips>

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
                    className="text-label text-muted-foreground transition-colors hover:text-foreground"
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
                onClear={() => {
                  onFilters({ search: "", type: null, deadline: null, unreadOnly: false, crew: null })
                  onClearAttention?.()
                }}
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

function EntryRow({ entry, selected, onOpen, lookup }: { entry: InboxV2Entry; selected: boolean; onOpen: () => void; lookup: InboxLookup }) {
  const deadlineMins = entry.deadlineAt ? Math.round((Date.parse(entry.deadlineAt) - Date.now()) / 60_000) : null
  const pill = entryKindPill(entry)
  const { crew, name } = entryIdentity(entry, lookup)
  const expiring = deadlineMins != null && entry.actionable && deadlineBucket(entry) === "hour"
  const outcome = entry.historical ? outcomeStatus(entry.outcome) : null
  return (
    <SidebarRow as="div" selected={selected} onSelect={onOpen} className="items-start gap-2 py-1.5 transition-colors duration-150">
      <span className="relative mt-0.5 shrink-0">
        <EntryAvatar entry={entry} lookup={lookup} compact />
        {entry.unread && <span className="absolute -right-0.5 -top-0.5 h-1.5 w-1.5 rounded-full bg-primary ring-1 ring-card"><span className="sr-only">Unread</span></span>}
      </span>
      <span className="flex min-w-0 flex-1 flex-col">
        <span title={entryTitle(entry)} className={cn("truncate text-body leading-5", entry.unread ? "font-semibold text-foreground" : "text-foreground/85")}>{entryTitle(entry)}</span>
        <span className="flex min-w-0 items-center gap-1 text-micro leading-4 text-muted-foreground">
          {crew && <CrewIcon icon={crew.icon || "users"} color={crew.color} size="sm" className="h-3.5 w-3.5 rounded-sm [&_svg]:h-2.5 [&_svg]:w-2.5" />}
          <span className="truncate">{crew ? `${crew.name} · ${name}` : name}</span>
          <span className={cn("ml-auto shrink-0 tabular-nums", expiring && "font-semibold text-destructive")}>
            {deadlineMins != null && entry.actionable ? deadlineMins > 0 ? `expires in ${remainingLabel(deadlineMins)}` : "expired" : since(entry.createdAt)}
          </span>
        </span>
        {(outcome || entry.actionable) && <span className="mt-0.5">{outcome ? <StatusPill status={outcome} /> : <StatusPill tone={pill.tone} label={pill.label} />}</span>}
      </span>
    </SidebarRow>
  )
}
