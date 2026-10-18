"use client"

import { useEffect, useMemo, useRef } from "react"
import Link from "next/link"

import {
  AlertCircle, AlertTriangle, Bell, Brain, Check, CircleSlash, Clock3, History,
  Inbox, LayoutDashboard, ListChecks, MessageSquare, ShieldCheck, Workflow, type LucideIcon,
} from "lucide-react"

import { remainingLabel } from "@/components/features/inbox/inbox-derive"
import {
  SidebarActiveChip, SidebarActiveChips, SidebarFilterButton, SidebarRow, SidebarSearch,
  SidebarSection, SidebarToolbar, SidebarViewButton,
} from "@/components/layout/sidebar-kit"
import {
  FACET_VALUE_LIMIT, SidebarFacetBlock, SidebarInlineFacet, useSidebarFacets, type SidebarFacetValue,
} from "@/components/layout/sidebar-facets"
import { AgentAvatar } from "@/components/ui/agent-avatar"
import { Badge } from "@/components/ui/badge"
import { CrewIcon } from "@/components/ui/crew-icon"
import { InlineEmpty } from "@/components/ui/inline-empty"
import { cn } from "@/lib/utils"

import { agentByRef, EntryAvatar, entryFromAgent, entryIdentity } from "./inbox-entry-identity"
import { ATTENTION_LABELS, type InboxAttention } from "./inbox-v2-attention"
import {
  ageLabel, entryKindPill, entryTitle, facetCounts, INBOX_ATTENTION, INBOX_SORTS, INBOX_V2_TYPES,
  isArchivedNotDecided, needsHumanDecision,
  type InboxSort, type InboxV2DeadlineKey, type InboxV2Filters, type InboxV2TypeKey,
} from "./inbox-v2-derive"
import { EMPTY_INBOX_LOOKUP, type InboxAgentRef, type InboxLookup, type InboxV2Entry, type InboxV2View } from "./inbox-v2-types"

/**
 * The Inbox's secondary panel (Harbor 2): search, Filter and Sort in the
 * panel toolbar; the facets open IN the panel under it — the kit's facet
 * block (COM-01), never a popover, so two facets combine in one visit and the
 * list stays in view; each facet lists six values and "All N…"; the three
 * spaces as a segmented control (keys 1–3); an Overview row; then the list in
 * its sections.
 *
 * Every facet answers to a real field (see INBOX_V2_TYPES); counts are over
 * the whole feed of the space, not over what is on screen.
 */

const SPACES: { key: InboxV2View; label: string; icon: LucideIcon }[] = [
  { key: "action", label: "To handle", icon: Inbox },
  { key: "updates", label: "Updates", icon: Bell },
  { key: "history", label: "History", icon: History },
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

const TYPE_LABEL: Record<string, string> = Object.fromEntries(INBOX_V2_TYPES.map((t) => [t.key, t.label]))
const ATTENTION_LABEL: Record<string, string> = Object.fromEntries(INBOX_ATTENTION.map((a) => [a.key, a.label]))


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
  /** The Overview row: shown, and selected while the overview is in the pane. */
  overviewSelected?: boolean
  onOverview?: () => void
  sort?: InboxSort
  onSort?: (sort: InboxSort) => void
  /** Crews and agents by id/slug, so rows can name them. */
  lookup?: InboxLookup
  /** True while the first load is still in flight: skeleton rows, not "empty". */
  loading?: boolean
  /** Select mode (v67): rows toggle a pick instead of opening; the page owns the bulk bar. */
  picking?: boolean
  picked?: ReadonlySet<string>
  onTogglePicking?: () => void
  onPick?: (entry: InboxV2Entry) => void
}

export function InboxV2Explorer({
  view, onView, viewCounts, entries, visible, filters, onFilters, attention, onClearAttention,
  selectedKey, onOpen, overviewSelected = false, onOverview, sort = "urgency", onSort,
  lookup = EMPTY_INBOX_LOOKUP, loading = false, picking = false, picked, onTogglePicking, onPick,
}: Props) {
  // Memoised on the feed, not on the render: the explorer re-renders on every
  // keystroke in search, and the counts do not depend on the filters at all.
  const counts = useMemo(() => facetCounts(entries), [entries])
  const crews = useMemo(() => {
    const byId = new Map<string, number>()
    for (const entry of entries) {
      const id = entryIdentity(entry, lookup).crew?.id
      if (id) byId.set(id, (byId.get(id) ?? 0) + 1)
    }
    return [...byId.entries()]
      .flatMap(([id, count]) => {
        const crew = lookup.crewById.get(id)
        return crew ? [{ crew, count }] : []
      })
      .sort((a, b) => b.count - a.count || a.crew.name.localeCompare(b.crew.name))
  }, [entries, lookup])
  // Agent (INB-01): the agents behind the rows of this space, by id. Counted
  // over the loaded feed — the server has no agent filter yet (#3455).
  const agents = useMemo(() => {
    const byId = new Map<string, { agent: InboxAgentRef; count: number }>()
    for (const entry of entries) {
      const agent = entryIdentity(entry, lookup).agent
      if (!agent) continue
      const seen = byId.get(agent.id)
      if (seen) seen.count += 1
      else byId.set(agent.id, { agent, count: 1 })
    }
    return [...byId.values()].sort((a, b) => b.count - a.count || a.agent.name.localeCompare(b.agent.name))
  }, [entries, lookup])
  const facets = useSidebarFacets()
  const showFilters = facets.open
  const searchRef = useRef<HTMLDivElement>(null)

  // `/` focuses search, as every panel does (v67 toolbar hint).
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== "/" || e.metaKey || e.ctrlKey || e.altKey || isTyping(e.target)) return
      const input = searchRef.current?.querySelector("input")
      if (!input) return
      e.preventDefault()
      input.focus()
    }
    window.addEventListener("keydown", onKey)
    return () => window.removeEventListener("keydown", onKey)
  }, [])

  const activeCount =
    (filters.type ? 1 : 0) + (filters.deadline ? 1 : 0) + (filters.unreadOnly ? 1 : 0) +
    (filters.attention ? 1 : 0) + (filters.crew ? 1 : 0) + (filters.agent ? 1 : 0)
  const crewChip = filters.crew ? lookup.crewById.get(filters.crew)?.name ?? "Crew" : null
  const agentChip = filters.agent ? agentName(filters.agent, lookup, entries) : null
  const narrowed = Boolean(attention) || activeCount > 0 || filters.search.trim() !== ""
  const set = (patch: Partial<InboxV2Filters>) => onFilters({ ...filters, ...patch })
  const clearFacets = () => set({ type: null, deadline: null, unreadOnly: false, attention: null, crew: null, agent: null })

  const stateValues: SidebarFacetValue[] = [
    { value: "unread", label: "Unread only", count: counts.unread, icon: <Bell className="h-3.5 w-3.5 shrink-0" aria-hidden /> },
    ...DEADLINES.map((d) => ({ value: d.key, label: d.label, count: counts.deadline[d.key], icon: <Clock3 className="h-3.5 w-3.5 shrink-0" aria-hidden /> })),
  ]
  const stateSelected = [...(filters.unreadOnly ? ["unread"] : []), ...(filters.deadline ? [filters.deadline] : [])]
  const attentionValues: SidebarFacetValue[] = INBOX_ATTENTION.map((a) => ({ value: a.key, label: a.label, count: counts.attention[a.key] }))
  const typeValues: SidebarFacetValue[] = INBOX_V2_TYPES.filter((t) => counts.type[t.key] > 0 || filters.type === t.key).map((t) => {
    const Icon = TYPE_ICON[t.key]
    return { value: t.key, label: t.label, count: counts.type[t.key], icon: <Icon className="h-3.5 w-3.5 shrink-0" aria-hidden /> }
  })
  // The facet lists the crews that have rows here; "All N…" every crew.
  const crewsWithRows = new Map(crews.map(({ crew, count }) => [crew.id, count]))
  const crewValues: SidebarFacetValue[] = [
    ...crews.map(({ crew }) => crew),
    ...[...lookup.crewById.values()].filter((crew) => !crewsWithRows.has(crew.id)).sort((a, b) => a.name.localeCompare(b.name)),
  ].map((crew) => ({
    value: crew.id, label: crew.name, text: `${crew.name} ${crew.slug}`, count: crewsWithRows.get(crew.id) ?? 0,
    icon: <CrewIcon icon={crew.icon || "users"} color={crew.color} size="sm" className="h-4 w-4 shrink-0 rounded-sm [&_svg]:h-2.5 [&_svg]:w-2.5" />,
  }))
  const agentValues: SidebarFacetValue[] = agents.map(({ agent, count }) => ({
    value: agent.id, label: agent.name, text: `${agent.name} ${agent.slug}`, count,
    icon: <AgentAvatar seed={agent.avatar_seed || agent.slug} style={agent.avatar_style} avatarUrl={agent.avatar_url} alt="" className="h-4 w-4 shrink-0 rounded-sm bg-muted" />,
  }))
  // The facet value of the chosen agent: a link may name it by slug, the
  // facet lists ids. A link can also name an agent this space has no row
  // from, or one nobody knows: it is still the choice, so it stays listed
  // and removable.
  const agentSelected = filters.agent
    ? agents.find(({ agent }) => agent.id === filters.agent || agent.slug === filters.agent)?.agent.id ?? filters.agent
    : null
  if (agentSelected && !agentValues.some((v) => v.value === agentSelected)) {
    agentValues.unshift({ value: agentSelected, label: agentChip ?? agentSelected, count: entries.filter((e) => entryFromAgent(e, agentSelected, lookup)).length })
  }

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
    // Inside SecondaryPanel: its kit toolbar is the panel toolbar, which
    // carries the collapse button; the panel owns width and the phone pattern.
    <div className="flex h-full min-h-0 flex-1 flex-col">
      <SidebarToolbar>
        <div ref={searchRef} data-inbox-search className="relative min-w-0 flex-1">
          <SidebarSearch
            value={filters.search}
            onValueChange={(search) => set({ search })}
            placeholder="Search inbox…"
          />
          {/* The v67 shortcut hint; the clear button takes its place once there is text. */}
          {filters.search === "" && (
            <kbd aria-hidden className="pointer-events-none absolute right-2 top-1/2 hidden h-[18px] min-w-4 -translate-y-1/2 items-center justify-center rounded border border-border bg-muted px-1 text-micro leading-none text-muted-foreground-soft md:inline-flex">
              /
            </kbd>
          )}
        </div>
        <SidebarFilterButton activeCount={activeCount} {...facets.triggerProps}>
          Filter
        </SidebarFilterButton>
        {onTogglePicking && (
          <SidebarViewButton
            aria-label="Select several"
            title="Select several"
            aria-pressed={picking}
            onClick={onTogglePicking}
            className={cn(picking && "border-primary/30 bg-primary/10 text-primary-hover")}
          >
            <ListChecks className="h-3.5 w-3.5" />
          </SidebarViewButton>
        )}
      </SidebarToolbar>

      {!showFilters && (
        <SidebarActiveChips>
          {attention && <SidebarActiveChip onRemove={onClearAttention ?? (() => {})}>{ATTENTION_LABELS[attention]}</SidebarActiveChip>}
          {filters.unreadOnly && <SidebarActiveChip onRemove={() => set({ unreadOnly: false })}>State: Unread only</SidebarActiveChip>}
          {filters.deadline && <Chip onRemove={() => set({ deadline: null })} text={`State: ${DEADLINES.find((d) => d.key === filters.deadline)?.label}`} />}
          {filters.attention && <Chip onRemove={() => set({ attention: null })} text={`Attention: ${ATTENTION_LABEL[filters.attention]}`} />}
          {filters.type && <Chip onRemove={() => set({ type: null })} text={`Source type: ${TYPE_LABEL[filters.type]}`} />}
          {crewChip && <Chip onRemove={() => set({ crew: null })} text={`Crew: ${crewChip}`} />}
          {agentChip && <Chip onRemove={() => set({ agent: null })} text={`Agent: ${agentChip}`} />}
        </SidebarActiveChips>
      )}

      <SidebarFacetBlock {...facets.blockProps} label="Filter inbox" activeCount={activeCount} onClear={clearFacets}>
        <SidebarInlineFacet
          label="State"
          values={stateValues}
          selected={stateSelected}
          onToggle={(value) => value === "unread" ? set({ unreadOnly: !filters.unreadOnly }) : set({ deadline: filters.deadline === value ? null : value as InboxV2DeadlineKey })}
          onClear={() => set({ unreadOnly: false, deadline: null })}
        />
        <SidebarInlineFacet
          label="Attention"
          values={attentionValues}
          selected={filters.attention}
          onToggle={(value) => set({ attention: filters.attention === value ? null : value as InboxV2Filters["attention"] })}
          onClear={() => set({ attention: null })}
        />
        <SidebarInlineFacet
          label="Source type"
          values={typeValues}
          selected={filters.type}
          onToggle={(value) => set({ type: filters.type === value ? null : value as InboxV2TypeKey })}
          onClear={() => set({ type: null })}
        />
        {(crews.length > 0 || filters.crew) && (
          <SidebarInlineFacet
            label="Crew"
            values={crewValues}
            // Up to six of the crews that have rows here; every crew behind All N….
            limit={Math.max(1, Math.min(FACET_VALUE_LIMIT, crews.length))}
            selected={filters.crew}
            onToggle={(value) => set({ crew: filters.crew === value ? null : value })}
            onClear={() => set({ crew: null })}
          />
        )}
        {onSort && (
          // v67 keeps Sort under Display in the Filter block, not in the toolbar.
          <SidebarInlineFacet
            label="Sort"
            values={INBOX_SORTS.map((o) => ({ value: o.key, label: o.label }))}
            selected={sort === "urgency" ? null : sort}
            onToggle={(value) => onSort(value as InboxSort)}
            onClear={() => onSort("urgency")}
          />
        )}
        {agentValues.length > 0 && (
          <SidebarInlineFacet
            label="Agent"
            values={agentValues}
            selected={agentSelected}
            onToggle={(value) => set({ agent: agentSelected === value ? null : value })}
            onClear={() => set({ agent: null })}
          />
        )}
      </SidebarFacetBlock>

      <div className="shrink-0 px-2 pt-2">
        <div role="tablist" aria-label="Inbox spaces" className="grid gap-0.5 rounded-lg bg-muted p-0.5 ring-1 ring-border ring-inset">
          {SPACES.map((s, index) => {
            // The Overview row sits beside the spaces, not instead of one:
            // the list below stays the current space's (v67 keeps it marked).
            const on = !attention && view === s.key
            return (
              <button
                key={s.key}
                type="button"
                role="tab"
                aria-selected={on}
                title={`${s.label} · ${index + 1}`}
                onClick={() => onView(s.key)}
                className={cn(
                  "kit-tap group flex min-w-0 items-center gap-2 rounded-md px-2 py-1.5 text-left text-body transition-colors",
                  on ? "bg-card font-semibold text-foreground shadow-[var(--shadow-sel)]" : "text-muted-foreground hover:text-foreground",
                )}
              >
                <s.icon className={cn("h-3.5 w-3.5 shrink-0", on ? "text-primary-hover" : "text-muted-foreground-soft")} aria-hidden />
                <span className="min-w-0 flex-1 truncate">{s.label}</span>
                {s.key !== "history" && <span className="text-micro tabular-nums text-muted-foreground">{viewCounts[s.key]}</span>}
              </button>
            )
          })}
        </div>
      </div>

      <div className="min-h-0 flex-1 overflow-y-auto pb-1 pt-1">
        {onOverview && (
          <SidebarRow as="div" selected={overviewSelected} onSelect={onOverview} className="mt-1">
            <LayoutDashboard className="h-3.5 w-3.5 shrink-0 text-muted-foreground-soft" aria-hidden />
            <span className="flex-1">Overview</span>
          </SidebarRow>
        )}
        {loading && entries.length === 0 ? (
          <div role="status" aria-label="Loading inbox" className="flex flex-col gap-1 px-2 pt-2">
            {Array.from({ length: 7 }, (_, i) => (
              <div key={i} className="flex items-start gap-2 px-2 py-2">
                <span className="page-sk mt-0.5 h-6 w-6 shrink-0 rounded-md" />
                <span className="flex min-w-0 flex-1 flex-col gap-1.5">
                  <span className="page-sk h-3 rounded" style={{ width: `${55 + ((i * 17) % 35)}%` }} />
                  <span className="page-sk h-2.5 rounded" style={{ width: `${30 + ((i * 11) % 30)}%` }} />
                </span>
              </div>
            ))}
          </div>
        ) : (
          <>
            {sections.filter((s) => s.rows.length > 0).map((section) => (
              <div key={section.label} data-slot="inbox-section">
                <SidebarSection label={section.label} count={section.rows.length} />
                {section.rows.map((entry) => (
                  <EntryRow
                    key={entry.key}
                    entry={entry}
                    selected={picking ? Boolean(picked?.has(entry.key)) : selectedKey === entry.key}
                    picking={picking}
                    onOpen={() => (picking && onPick ? onPick(entry) : onOpen(entry))}
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
                    onFilters({ search: "", type: null, attention: null, deadline: null, unreadOnly: false, crew: null, agent: null })
                    onClearAttention?.()
                  }}
                />
              </div>
            )}
          </>
        )}
      </div>
    </div>
  )
}

function isTyping(target: EventTarget | null): boolean {
  const el = target as HTMLElement | null
  if (!el) return false
  return el.isContentEditable || /^(INPUT|TEXTAREA|SELECT)$/.test(el.tagName)
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
        text="Nothing matches these filters."
        action={
          <button type="button" onClick={onClear} className="text-primary-hover hover:underline">
            Clear filters
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
        action={<Link href="/activity" className="text-primary-hover hover:underline">Open Activity →</Link>}
      />
    )
  }
  if (view === "history") {
    return <InlineEmpty icon={History} text="No decisions recorded yet. Decided and archived items stay here." />
  }
  return <InlineEmpty icon={Inbox} text="Nothing waits for you. Decisions, questions and reviews from agents land here." />
}

/** An active-filter chip whose × names the filter it removes ("Remove filter Agent: Scout"). */
function Chip({ text, onRemove }: { text: string; onRemove: () => void }) {
  return <SidebarActiveChip onRemove={onRemove} label={text}>{text}</SidebarActiveChip>
}

/**
 * The name an agent chip shows: the roster's, else the key the rows carry
 * when some row in the space is from it, else "unknown" once the roster has
 * answered (an old link naming an agent that is gone) — "…" until then.
 */
function agentName(ref: string, lookup: InboxLookup, entries: InboxV2Entry[]): string {
  const known = agentByRef(lookup, ref)
  if (known) return known.name
  if (entries.some((entry) => entryFromAgent(entry, ref, lookup))) return ref
  return lookup.ready ? "unknown" : "…"
}

function EntryRow({ entry, selected, picking = false, onOpen, lookup }: { entry: InboxV2Entry; selected: boolean; picking?: boolean; onOpen: () => void; lookup: InboxLookup }) {
  const deadlineMins = entry.deadlineAt ? Math.round((Date.parse(entry.deadlineAt) - Date.now()) / 60_000) : null
  const { crew } = entryIdentity(entry, lookup)
  const kind = entryKindPill(entry)
  const expiring = deadlineMins != null && entry.actionable
  const merged = entry.groupedItems?.length ?? 0
  return (
    <SidebarRow as="div" selected={selected} onSelect={onOpen} data-testid="inbox-row" className="items-start gap-2 py-2">
      {picking ? (
        <span aria-hidden className={cn("mt-1.5 grid h-4 w-4 shrink-0 place-items-center rounded border", selected ? "border-primary bg-primary text-primary-foreground" : "border-control-border bg-card")}>
          {selected && <Check className="h-3 w-3" />}
        </span>
      ) : (
        <span className={cn("mt-2.5 h-1.5 w-1.5 shrink-0 rounded-full", entry.unread ? "bg-primary" : "bg-transparent")}>
          {entry.unread && <span className="sr-only">Unread</span>}
        </span>
      )}
      <span className="shrink-0">
        <EntryAvatar entry={entry} lookup={lookup} size={26} />
      </span>
      <span className="flex min-w-0 flex-1 flex-col">
        <span title={entryTitle(entry)} className={cn("truncate text-body leading-5 text-foreground", entry.unread ? "font-semibold" : "font-medium")}>{entryTitle(entry)}</span>
        <span className="truncate text-label leading-4 text-muted-foreground">
          {crew ? `${crew.name} · ` : ""}{entry.historical && entry.outcome ? outcomeWord(entry.outcome) : kind.label}
        </span>
      </span>
      <span className="flex shrink-0 flex-col items-end gap-0.5">
        {expiring ? (
          <span className="whitespace-nowrap text-micro font-semibold text-warn">
            {deadlineMins! > 0 ? `in ${remainingLabel(deadlineMins!)}` : "expired"}
          </span>
        ) : (
          <span className="whitespace-nowrap text-micro tabular-nums text-muted-foreground-soft">{ageLabel(entry.createdAt)}</span>
        )}
        {merged > 1 && <Badge variant="outline" className="h-4 px-1 text-micro tabular-nums">×{merged}</Badge>}
      </span>
    </SidebarRow>
  )
}

function outcomeWord(outcome: string): string {
  const o = outcome.toLowerCase()
  if (o === "approve" || o === "approved") return "Approved"
  if (o === "reject" || o === "rejected") return "Rejected"
  if (o === "deny" || o === "denied") return "Denied"
  if (o === "expired" || o === "timeout" || o === "timed_out") return "Expired"
  return o.charAt(0).toUpperCase() + o.slice(1).replaceAll("_", " ")
}
