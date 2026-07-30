"use client"

import { useMemo, useState } from "react"
import { AnimatePresence, motion } from "motion/react"
import {
  Check, CheckCheck, ChevronDown, ChevronRight, CircleDot, Inbox as InboxIcon, ListChecks,
  MailOpen, ScrollText, Share2, SlidersHorizontal, Sparkles,
} from "lucide-react"
import type { LucideIcon } from "lucide-react"

import { Button } from "@/components/ui/button"
import { Checkbox } from "@/components/ui/checkbox"
import { ListRow } from "@/components/ui/list-row"
import { SidebarActiveChip, SidebarActiveChips, SidebarFilterButton, SidebarSearch } from "@/components/layout/sidebar-kit"
import { TabBar } from "@/components/ui/tab-bar"
import { cn } from "@/lib/utils"

import { ActorAvatar } from "./actor"
import { SubjectPicker } from "./subject-picker"
import { PREVIEW_DIRECTORY } from "./mock-data"
import { canRole, type PreviewInboxItem, type WorkspaceRole } from "./mock-data"
import { bucketOf, categoryOf, decisionFor, expiresIn, since, subjectOf } from "./logic"
import type { Bucket, GroupBy, InboxView, SubjectFacet } from "./types"

// =============================================================================
// The list panel — the inbox's own column, the way the shipped page has it.
//
// A sidebar-kit rail was tried here and taken back out. The rail is right for
// /issues and /routines, where the left column navigates a different object
// than the one on the right; in an inbox both columns are the same list, so a
// rail meant three columns to cross to reach one message.
//
// So the column carries its own chrome, as it does today: search + select on
// one row, view tabs + Filter + Display on the next, collapsible groups below.
// What is new is that selection actually works — Select turns on checkboxes,
// shift-click takes a range, and the bulk bar says what it will refuse to touch.
// =============================================================================

const dropdownAnim = {
  initial: { opacity: 0, scale: 0.95, y: -4 },
  animate: { opacity: 1, scale: 1, y: 0, transition: { duration: 0.12 } },
  exit: { opacity: 0, scale: 0.95, y: -4, transition: { duration: 0.1 } },
}

const BUCKETS: { id: Bucket; label: string; icon: LucideIcon; tone: string; testId: string }[] = [
  { id: "decisions", label: "Decisions needed", icon: Sparkles, tone: "text-warn", testId: "facet-bucket-decisions" },
  { id: "replies", label: "Agent replies", icon: Share2, tone: "text-primary", testId: "facet-bucket-replies" },
  { id: "review", label: "Ready for review", icon: CircleDot, tone: "text-purple", testId: "facet-bucket-review" },
  { id: "routines", label: "Routine progress", icon: ScrollText, tone: "text-notice", testId: "facet-bucket-routines" },
  { id: "other", label: "Everything else", icon: InboxIcon, tone: "text-muted-foreground", testId: "facet-bucket-other" },
]

const BUCKET_ORDER: Bucket[] = ["decisions", "replies", "review", "routines", "other"]

const BUCKET_LABEL: Record<Bucket, string> = {
  decisions: "Decisions needed",
  replies: "Agent replies",
  review: "Ready for review",
  routines: "Routine progress",
  other: "Everything else",
}

const GROUP_BYS: { id: GroupBy; label: string }[] = [
  { id: "smart", label: "Smart buckets" },
  { id: "category", label: "Category" },
  { id: "subject", label: "Subject" },
  { id: "none", label: "Nothing" },
]

const SORTS: { id: string; label: string }[] = [
  { id: "newest", label: "Newest first" },
  { id: "oldest", label: "Oldest first" },
  { id: "expiring", label: "Expiring first" },
]

const PERIODS = [
  { id: "7", label: "Last 7 days" },
  { id: "30", label: "Last 30 days" },
  { id: "all", label: "All time" },
]

const OUTCOME_DOT: Record<string, string> = {
  approved: "bg-success",
  rejected: "bg-destructive",
  archived: "bg-muted-foreground/40",
  retried: "bg-primary",
  expired: "bg-warn",
}

export interface InboxListPanelProps {
  rows: PreviewInboxItem[]
  total: number
  role: WorkspaceRole

  view: InboxView
  onViewChange: (v: InboxView) => void
  viewCounts: Record<InboxView, number>

  selectedId: string | null
  onSelect: (id: string) => void

  bucket: Bucket | null
  onBucketChange: (b: Bucket | null) => void
  bucketCounts: Record<Bucket, number>
  subjects: SubjectFacet[]
  selectedSubject: string | null
  onSubjectChange: (id: string | null) => void

  outcome: string | null
  onOutcomeChange: (o: string | null) => void
  outcomeCounts: { id: string; label: string; count: number }[]
  actor: string | null
  onActorChange: (a: string | null) => void
  actorCounts: { id: string; count: number }[]
  period: string
  onPeriodChange: (p: string) => void

  groupBy: GroupBy
  onGroupByChange: (g: GroupBy) => void
  sort: string
  onSortChange: (s: string) => void

  search: string
  onSearchChange: (v: string) => void
}

export function InboxListPanel(props: InboxListPanelProps) {
  const {
    rows, total, role, view, onViewChange, viewCounts, selectedId, onSelect,
    bucket, onBucketChange, bucketCounts, subjects, selectedSubject, onSubjectChange,
    outcome, onOutcomeChange, outcomeCounts, actor, onActorChange, actorCounts,
    period, onPeriodChange, groupBy, onGroupByChange, sort, onSortChange,
    search, onSearchChange,
  } = props

  const [filterOpen, setFilterOpen] = useState(false)
  const [displayOpen, setDisplayOpen] = useState(false)
  const [selectMode, setSelectMode] = useState(false)
  const [checked, setChecked] = useState<Set<string>>(new Set())
  const [collapsed, setCollapsed] = useState<Set<string>>(new Set())
  /** Index of the last row clicked, so shift-click has a range to span. */
  const [anchor, setAnchor] = useState<number | null>(null)

  const archive = view === "archived"

  const groups = useMemo(() => {
    if (groupBy === "none") return [{ key: "all", label: "All", items: rows }]
    const map = new Map<string, { key: string; label: string; items: PreviewInboxItem[] }>()
    for (const item of rows) {
      const [key, label] =
        groupBy === "smart" ? [bucketOf(item), BUCKET_LABEL[bucketOf(item)]]
          : groupBy === "category" ? [categoryOf(item), categoryOf(item)]
            : [subjectOf(item).id, subjectOf(item).label]
      const found = map.get(key)
      if (found) found.items.push(item)
      else map.set(key, { key, label, items: [item] })
    }
    const out = [...map.values()]
    // Smart buckets have a fixed order — decisions first, always. Insertion
    // order would put whichever bucket the newest row happened to land in at
    // the top, so the one thing that blocks an agent could sit below routine
    // chatter purely because nothing had been decided in a while.
    if (groupBy === "smart") {
      out.sort((a, b) => BUCKET_ORDER.indexOf(a.key as Bucket) - BUCKET_ORDER.indexOf(b.key as Bucket))
    }
    return out
  }, [rows, groupBy])

  /**
   * Flattened visible order. Shift-click has to span what the eye sees, so the
   * range walks this array rather than the ungrouped list — otherwise picking
   * two rows in one group would sweep in every row of the groups between them.
   */
  const flat = useMemo(
    () => groups.filter((g) => !collapsed.has(g.key)).flatMap((g) => g.items),
    [groups, collapsed],
  )

  function toggleRow(item: PreviewInboxItem, index: number, shiftKey: boolean) {
    setChecked((prev) => {
      const next = new Set(prev)
      if (shiftKey && anchor != null) {
        const [from, to] = anchor < index ? [anchor, index] : [index, anchor]
        // A shift range ADDS; it never clears what is already ticked, which is
        // what makes "pick a block, then another block" work.
        for (let i = from; i <= to; i++) next.add(flat[i].id)
        return next
      }
      if (next.has(item.id)) next.delete(item.id)
      else next.add(item.id)
      return next
    })
    if (!shiftKey) setAnchor(index)
  }

  function toggleGroup(key: string, items: PreviewInboxItem[]) {
    setChecked((prev) => {
      const next = new Set(prev)
      const allOn = items.every((i) => next.has(i.id))
      for (const i of items) {
        if (allOn) next.delete(i.id)
        else next.add(i.id)
      }
      return next
    })
  }

  function leaveSelectMode() {
    setSelectMode(false)
    setChecked(new Set())
    setAnchor(null)
  }

  // A waitpoint or an escalation is an agent standing still until a human
  // answers. The server refuses to close those in bulk, so the bar says so
  // before the click rather than after it.
  const protectedCount = rows.filter((r) => checked.has(r.id) && decisionFor(r) != null).length

  const activeFilters = archive
    ? (outcome ? 1 : 0) + (actor ? 1 : 0) + (period !== "30" ? 1 : 0) + (selectedSubject ? 1 : 0)
    : (bucket ? 1 : 0) + (selectedSubject ? 1 : 0)

  const bucketLabel = BUCKETS.find((b) => b.id === bucket)?.label
  const subjectLabel = subjects.find((s) => s.id === selectedSubject)?.label
  const outcomeLabel = outcomeCounts.find((o) => o.id === outcome)?.label
  const periodLabel = PERIODS.find((p) => p.id === period)?.label

  let flatIndex = -1

  return (
    <div className="flex w-[460px] shrink-0 flex-col overflow-hidden border-r border-white/[0.06] bg-card">
      {/* ── Search + Select ── */}
      <div className="flex shrink-0 items-center gap-2 border-b border-white/[0.06] px-3 py-2">
        <SidebarSearch
          value={search}
          onValueChange={onSearchChange}
          placeholder={archive ? "Search the archive…" : "Search inbox…"}
        />
        <Button
          variant={selectMode ? "secondary" : "ghost"}
          size="icon-sm"
          aria-pressed={selectMode}
          aria-label={selectMode ? "Done selecting" : "Select items"}
          title={selectMode ? "Done" : "Select items"}
          onClick={() => (selectMode ? leaveSelectMode() : setSelectMode(true))}
        >
          <ListChecks className="h-4 w-4" />
        </Button>
      </div>

      {/* ── Views + Filter + Display ── */}
      <div className="flex shrink-0 items-center border-b border-white/[0.06] pr-2">
        <TabBar
          value={view}
          onValueChange={(v) => onViewChange(v as InboxView)}
          layoutId="inbox-preview-view"
          ariaLabel="Inbox views"
          className="flex-1 border-b-0"
        >
          <TabBar.Item value="inbox" count={viewCounts.inbox}>Inbox</TabBar.Item>
          <TabBar.Item value="unread" count={viewCounts.unread || null}>Unread</TabBar.Item>
          <TabBar.Item value="archived" count={viewCounts.archived}>Archived</TabBar.Item>
        </TabBar>

        <div className="relative shrink-0">
          <SidebarFilterButton
            activeCount={activeFilters}
            aria-expanded={filterOpen}
            onClick={() => { setFilterOpen(!filterOpen); setDisplayOpen(false) }}
          />
          <AnimatePresence>
            {filterOpen && (
              <>
                <div className="fixed inset-0 z-40" onClick={() => setFilterOpen(false)} />
                <motion.div
                  {...dropdownAnim}
                  className="absolute right-0 top-9 z-50 max-h-[380px] min-w-[230px] overflow-y-auto rounded-lg border border-white/[0.1] bg-card py-1 shadow-xl"
                >
                  {archive ? (
                    <>
                      <MenuHeading>Outcome</MenuHeading>
                      <MenuOption active={outcome === null} onClick={() => onOutcomeChange(null)}>
                        <span className="h-1.5 w-1.5 shrink-0 rounded-full bg-muted-foreground/40" />
                        Any outcome
                      </MenuOption>
                      {outcomeCounts.map((o) => (
                        <MenuOption
                          key={o.id}
                          testId={`outcome-${o.id}`}
                          active={outcome === o.id}
                          count={o.count}
                          onClick={() => onOutcomeChange(outcome === o.id ? null : o.id)}
                        >
                          <span className={cn("h-1.5 w-1.5 shrink-0 rounded-full", OUTCOME_DOT[o.id] ?? "bg-muted-foreground/40")} />
                          {o.label}
                        </MenuOption>
                      ))}
                      <MenuDivider />
                      <MenuHeading>Decided by</MenuHeading>
                      {actorCounts.map((a) => (
                        <MenuOption
                          key={a.id}
                          active={actor === a.id}
                          count={a.count}
                          onClick={() => onActorChange(actor === a.id ? null : a.id)}
                        >
                          <ActorAvatar actor={{ kind: "user", id: a.id, label: a.id }} size={20} />
                          {a.id}
                        </MenuOption>
                      ))}
                      <MenuDivider />
                      <MenuHeading>Period</MenuHeading>
                      {PERIODS.map((p) => (
                        <MenuOption key={p.id} active={period === p.id} onClick={() => onPeriodChange(p.id)}>
                          <span className="h-1.5 w-1.5 shrink-0 rounded-full bg-muted-foreground/40" />
                          {p.label}
                        </MenuOption>
                      ))}
                    </>
                  ) : (
                    <>
                      <MenuHeading>Buckets</MenuHeading>
                      <MenuOption active={bucket === null} onClick={() => onBucketChange(null)}>
                        <InboxIcon className="h-3.5 w-3.5 shrink-0 text-muted-foreground/70" />
                        All buckets
                      </MenuOption>
                      {BUCKETS.map((b) => (
                        <MenuOption
                          key={b.id}
                          testId={b.testId}
                          active={bucket === b.id}
                          count={bucketCounts[b.id]}
                          onClick={() => onBucketChange(bucket === b.id ? null : b.id)}
                        >
                          <b.icon className={cn("h-3.5 w-3.5 shrink-0", b.tone)} />
                          {b.label}
                        </MenuOption>
                      ))}
                    </>
                  )}

                  <MenuDivider />
                  {/* Who the rows are ABOUT — the subject from the payload, not
                      the sender, so a Keeper request files under casey. The
                      picker searches the workspace roster rather than the loaded
                      rows; see subject-picker for why that distinction matters. */}
                  <SubjectPicker
                    subjects={subjects}
                    directory={PREVIEW_DIRECTORY}
                    selected={selectedSubject}
                    onChange={onSubjectChange}
                  />
                </motion.div>
              </>
            )}
          </AnimatePresence>
        </div>

        <div className="relative shrink-0">
          <button
            type="button"
            aria-label="Display options"
            aria-expanded={displayOpen}
            onClick={() => { setDisplayOpen(!displayOpen); setFilterOpen(false) }}
            className={cn(
              "ml-1.5 inline-flex h-8 items-center gap-1.5 whitespace-nowrap rounded-md border px-2.5 text-[11px] transition-colors",
              displayOpen
                ? "border-primary/30 bg-primary/10 text-primary-hover"
                : "border-white/[0.08] bg-white/[0.04] text-muted-foreground/70 hover:text-foreground",
            )}
          >
            <SlidersHorizontal className="h-3 w-3" />
            Display
            <ChevronDown className="h-3 w-3 opacity-60" />
          </button>
          <AnimatePresence>
            {displayOpen && (
              <>
                <div className="fixed inset-0 z-40" onClick={() => setDisplayOpen(false)} />
                <motion.div
                  {...dropdownAnim}
                  className="absolute right-0 top-9 z-50 min-w-[190px] rounded-lg border border-white/[0.1] bg-card py-1 shadow-xl"
                >
                  <MenuHeading>Group by</MenuHeading>
                  {GROUP_BYS.map((g) => (
                    <MenuOption key={g.id} active={groupBy === g.id} onClick={() => onGroupByChange(g.id)}>
                      {g.label}
                    </MenuOption>
                  ))}
                  <MenuDivider />
                  <MenuHeading>Sort</MenuHeading>
                  {SORTS.map((s) => (
                    <MenuOption key={s.id} active={sort === s.id} onClick={() => onSortChange(s.id)}>
                      {s.label}
                    </MenuOption>
                  ))}
                </motion.div>
              </>
            )}
          </AnimatePresence>
        </div>
      </div>

      <SidebarActiveChips className="border-b border-white/[0.06] pt-2">
        {bucketLabel && <SidebarActiveChip onRemove={() => onBucketChange(null)}>{bucketLabel}</SidebarActiveChip>}
        {subjectLabel && <SidebarActiveChip onRemove={() => onSubjectChange(null)}>{subjectLabel}</SidebarActiveChip>}
        {archive && outcomeLabel && <SidebarActiveChip onRemove={() => onOutcomeChange(null)}>{outcomeLabel}</SidebarActiveChip>}
        {archive && actor && <SidebarActiveChip onRemove={() => onActorChange(null)}>{actor}</SidebarActiveChip>}
        {archive && period !== "30" && <SidebarActiveChip onRemove={() => onPeriodChange("30")}>{periodLabel}</SidebarActiveChip>}
      </SidebarActiveChips>

      {/* ── Rows ── */}
      <div className="min-h-0 flex-1 overflow-y-auto">
        {rows.length === 0 && (
          <p className="type-row px-4 py-10 text-center text-muted-foreground-soft">Nothing here.</p>
        )}
        {groups.map((group) => {
          const isCollapsed = collapsed.has(group.key)
          const checkedInGroup = group.items.filter((i) => checked.has(i.id)).length
          const groupState: boolean | "indeterminate" =
            checkedInGroup === 0 ? false : checkedInGroup === group.items.length ? true : "indeterminate"
          return (
            <div key={group.key}>
              {groupBy !== "none" && (
                <div className="sticky top-0 z-[1] flex items-center gap-2 border-b border-white/[0.04] bg-card/95 px-3 py-1.5 backdrop-blur">
                  {selectMode && (
                    <Checkbox
                      checked={groupState}
                      onCheckedChange={() => toggleGroup(group.key, group.items)}
                      aria-label={`Select all in ${group.label}`}
                    />
                  )}
                  <button
                    type="button"
                    onClick={() => setCollapsed((prev) => {
                      const next = new Set(prev)
                      if (next.has(group.key)) next.delete(group.key)
                      else next.add(group.key)
                      return next
                    })}
                    aria-expanded={!isCollapsed}
                    className="flex min-w-0 flex-1 items-center gap-1.5 text-left"
                  >
                    {isCollapsed
                      ? <ChevronRight className="h-3 w-3 shrink-0 text-muted-foreground/50" />
                      : <ChevronDown className="h-3 w-3 shrink-0 text-muted-foreground/50" />}
                    <span className="type-row truncate font-medium">{group.label}</span>
                    <span className="type-meta ml-auto shrink-0 tabular-nums text-muted-foreground-soft">
                      {group.items.length}
                    </span>
                  </button>
                </div>
              )}
              {!isCollapsed && (
                <ul>
                  {group.items.map((item) => {
                    flatIndex += 1
                    const index = flatIndex
                    return (
                      <MailRow
                        key={item.id}
                        item={item}
                        role={role}
                        index={index}
                        selectMode={selectMode}
                        checked={checked.has(item.id)}
                        selected={selectedId === item.id}
                        onToggle={(shiftKey) => toggleRow(item, index, shiftKey)}
                        onSelect={() => onSelect(item.id)}
                      />
                    )
                  })}
                </ul>
              )}
            </div>
          )
        })}
      </div>

      {/* ── Bulk bar ── */}
      {checked.size > 0 && (
        <div className="flex shrink-0 flex-col gap-1.5 border-t border-white/[0.06] bg-surface-subtle/60 px-3 py-2">
          <div className="flex items-center gap-2">
            <span className="type-row font-medium">{checked.size} selected</span>
            <div className="ml-auto flex items-center gap-1.5">
              <Button size="sm" className="gap-1.5" disabled={!canRole(role, "create")}>
                <CheckCheck className="h-3 w-3" />
                Resolve
              </Button>
              <Button size="sm" variant="ghost" className="gap-1.5">
                <MailOpen className="h-3 w-3" />
                Mark read
              </Button>
              <Button size="sm" variant="ghost" onClick={() => setChecked(new Set())}>Clear</Button>
            </div>
          </div>
          {protectedCount > 0 && (
            <p className="type-meta text-muted-foreground">
              {protectedCount} of them {protectedCount === 1 ? "is a decision an agent is" : "are decisions agents are"}{" "}
              waiting on — those stay open and are decided one at a time.
            </p>
          )}
        </div>
      )}

      <div className="type-meta flex shrink-0 items-center gap-2 border-t border-white/[0.06] px-3 py-1.5 text-muted-foreground-soft">
        <span>{rows.length === total ? `${total} items` : `${rows.length} of ${total}`}</span>
        {selectMode && <span className="ml-auto">shift-click takes a range</span>}
      </div>
    </div>
  )
}

function MenuHeading({ children }: { children: React.ReactNode }) {
  return (
    <div className="px-3 py-1 text-[9px] font-semibold uppercase tracking-wider text-foreground/40">{children}</div>
  )
}

function MenuDivider() {
  return <div className="mt-1 border-t border-white/[0.06]" />
}

function MenuOption({
  active, onClick, children, count, testId,
}: {
  active: boolean
  onClick: () => void
  children: React.ReactNode
  count?: number
  testId?: string
}) {
  return (
    <button
      type="button"
      data-testid={testId}
      onClick={onClick}
      className={cn(
        "flex w-full items-center gap-2 px-3 py-1.5 text-left text-xs transition-colors hover:bg-white/[0.06]",
        active ? "text-primary" : "text-muted-foreground/80",
      )}
    >
      {children}
      {count != null && <span className="ml-auto tabular-nums text-[10px] opacity-70">{count}</span>}
      {active && <Check className={cn("h-3 w-3 shrink-0", count != null && "ml-1")} />}
    </button>
  )
}

/**
 * One line of title, one line of everything else — the shape the shipped rows
 * have. Giving each row its own band of pills is what made ten items read as
 * ten cards.
 */
function MailRow({
  item, role, index, selectMode, checked, selected, onToggle, onSelect,
}: {
  item: PreviewInboxItem
  role: WorkspaceRole
  index: number
  selectMode: boolean
  checked: boolean
  selected: boolean
  onToggle: (shiftKey: boolean) => void
  onSelect: () => void
}) {
  const spec = decisionFor(item)
  const blocked = spec != null && !canRole(role, spec.requires)
  const mins = expiresIn(item)
  const subject = subjectOf(item)

  return (
    <ListRow
      selected={selected}
      onSelect={selectMode ? undefined : onSelect}
      data-testid={`row-${item.id}`}
      className={cn("items-start gap-2.5 px-3 py-2", selectMode && "cursor-default")}
    >
      {selectMode ? (
        <span
          className="mt-0.5 shrink-0"
          // Shift-click has to be caught on the wrapper: Radix's Checkbox
          // reports the next checked STATE, not the event, so the modifier is
          // gone by the time onCheckedChange fires.
          onClick={(e) => { e.stopPropagation(); onToggle(e.shiftKey) }}
        >
          <Checkbox checked={checked} aria-label={`Select ${item.title}`} data-testid={`check-${index}`} />
        </span>
      ) : (
        <span
          className={cn(
            "mt-2 h-1.5 w-1.5 shrink-0 rounded-full",
            item.state === "unread" ? "bg-primary" : "bg-transparent",
          )}
        />
      )}
      <ActorAvatar actor={subject} size={24} />
      <span className="min-w-0 flex-1">
        <span className="flex items-baseline gap-2">
          <span
            className={cn(
              "type-row min-w-0 flex-1 truncate",
              item.state === "unread" ? "font-medium text-foreground" : "text-muted-foreground",
            )}
          >
            {item.title}
          </span>
          <span className="type-meta shrink-0 text-muted-foreground-soft">{since(item.created_at)}</span>
        </span>
        <span className="type-meta flex min-w-0 items-center gap-1.5 text-muted-foreground-soft">
          <span className="truncate">{subject.label}</span>
          <span>·</span>
          <span className="truncate font-mono">{categoryOf(item)}</span>
          {mins != null && mins > 0 && (
            <span className="shrink-0 font-medium text-destructive">· expires in {mins}m</span>
          )}
          {blocked && <span className="shrink-0">· admin decides</span>}
        </span>
      </span>
    </ListRow>
  )
}
