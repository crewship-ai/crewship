"use client"

/**
 * The /pages filter rail — zone 2 of PRD §9b.1.
 *
 *   [icon rail]  [filter rail 280px]              [main]
 *   AppSidebar   SidebarToolbar / Search /        Overview · or a single page
 *                FilterButton / Collapse
 *                ── STATUS ─────── All · Fresh · Stale · Failed · Never produced
 *                ── OWNER  ─────── per crew · Shared with me
 *                ── MINE ───────── the list, grouped by owner (#2523):
 *                ── <crew> ─────── Mine · crews I belong to · other crews ·
 *                ── OWNED BY OTHERS  Owned by others — each a collapsible
 *                                    section, the owner said once in its header
 *
 * Every control here comes from `components/layout/sidebar-kit.tsx`. That is a
 * hard requirement, not a preference: #1776 is open on five surfaces that each
 * hand-rolled this popover and drifted — Credentials' `set({category});
 * setFilterOpen(false)` makes combining two facets impossible — and #1777
 * lifted the panel into the kit with Issues as the parity proof. Pages is the
 * SECOND surface on the shared panel, never the sixth hand-rolled one.
 *
 * The two behaviours that buys, and which nothing in this file re-implements:
 * a pick never closes the panel, and a pick never touches a sibling facet.
 * Both facets are multi-select.
 */

import * as React from "react"
import { AnimatePresence, motion } from "motion/react"
import { Share2 } from "lucide-react"

import { listRow } from "@/lib/motion"

import {
  SidebarActiveChip,
  SidebarActiveChips,
  SidebarCollapseButton,
  SidebarFacet,
  SidebarFacetOption,
  SidebarFilterPopover,
  SidebarRow,
  SidebarSearch,
  SidebarSection,
  SidebarToolbar,
} from "@/components/layout/sidebar-kit"
import { CONCEPT_ICON } from "@/lib/concept-icons"
import { cn } from "@/lib/utils"
import {
  EMPTY_PAGE_FILTERS,
  groupPagesByOwner,
  hasReach,
  matchesPageFilters,
  ownerFacets,
  pageFilterCount,
  stateFacetCounts,
  togglePageFilter,
  type PageFilters,
  type PageGroup,
  type PageView,
} from "@/hooks/use-pages"
import type { PanelState } from "@/components/features/pages/panels/types"
import { PAGE_STATE_META, PAGE_STATE_ORDER } from "@/components/features/pages/page-state"

export interface PagesRailProps {
  pages: PageView[]
  /** With `currentUserId`, the key the collapse state is saved under. */
  workspaceId: string
  /** `user/<id>` owner of the "Mine" group. Null while the session loads, or
   *  in a context with no auth provider — then nothing is "mine". */
  currentUserId?: string | null
  search: string
  onSearchChange: (value: string) => void
  filters: PageFilters
  onFiltersChange: (next: PageFilters) => void
  selectedSlug: string | null
  onSelectPage: (slug: string) => void
  /** Opens the YAML editor on a new document (§10b.1). Optional so the rail
   *  still renders in a context that has no authoring affordance. */
  onCreatePage?: () => void
  onToggleCollapse?: () => void
}

// ── Collapse state (#2523) ──────────────────────────────────────────────────
//
// Saved per user AND per workspace: the same person folds different crews in
// different workspaces, and two people on one browser must not share a fold.
// Storage is treated as optional at every step — a private window, a full
// quota, a policy that throws on access, or a value someone else wrote there
// all have to leave the rail working with the default, which is "everything
// open". Nothing here is worth an error boundary.

export const collapseStorageKey = (workspaceId: string, userId: string | null | undefined) =>
  `pages-rail:${workspaceId}:${userId ?? "anonymous"}`

const NONE_COLLAPSED: ReadonlySet<string> = new Set()

function readCollapsed(key: string): ReadonlySet<string> {
  try {
    const raw = globalThis.localStorage?.getItem(key)
    if (!raw) return NONE_COLLAPSED
    const parsed: unknown = JSON.parse(raw)
    if (!Array.isArray(parsed)) return NONE_COLLAPSED
    return new Set(parsed.filter((k): k is string => typeof k === "string"))
  } catch {
    return NONE_COLLAPSED
  }
}

function writeCollapsed(key: string, collapsed: ReadonlySet<string>) {
  try {
    globalThis.localStorage?.setItem(key, JSON.stringify(Array.from(collapsed)))
  } catch {
    // Unsaveable is not unusable: the fold still holds for this mount.
  }
}

/**
 * Every element the arrow keys may land on, in document order: group headers
 * (the kit's `aria-expanded` buttons) and the rows currently on screen. Read
 * from the DOM at the keystroke rather than kept in state, because what is
 * visible is exactly what is rendered — a folded group's rows are not there.
 */
const NAV_SELECTOR = "[data-rail-header], [data-rail-row]"

function navigables(root: HTMLElement): HTMLElement[] {
  return Array.from(root.querySelectorAll<HTMLElement>(NAV_SELECTOR))
}

function focusHeader(root: HTMLElement, key: string) {
  navigables(root).find((el) => el.dataset.railHeader === key)?.focus()
}

/**
 * Everything this file adds to the rail is sized in the NAVIGATION register
 * (`.type-nav-sub`, `app/globals.css`), not in the Pages one.
 *
 * That is the register's own rule, applied rather than re-argued: *"A detail
 * card is read; a rail is scanned… sized down so the row belongs to the face
 * rather than competing with it."* `SidebarRow` from the kit is already written
 * in `.type-nav`, so a count or a badge Pages hangs off that row has to be its
 * sub-size or the row reads as two competing type systems in one line. The
 * Pages register governs the main pane; the rail was never its territory.
 *
 * It also settles the sub-floor: these were 10px, under the 11px `--typo-micro`
 * documents as the smallest allowed, and `.type-nav-sub` is exactly 11.
 */
function Count({ n, dim = false }: { n: number; dim?: boolean }) {
  return (
    <span
      className={cn(
        "type-nav-sub ml-auto shrink-0 tabular-nums",
        dim ? "text-muted-foreground-soft/50" : "text-muted-foreground-soft",
      )}
    >
      {n}
    </span>
  )
}

export function PagesRail({
  pages,
  workspaceId,
  currentUserId,
  search,
  onSearchChange,
  filters,
  onFiltersChange,
  selectedSlug,
  onSelectPage,
  onCreatePage,
  onToggleCollapse,
}: PagesRailProps) {
  // Facet counts are computed against the WHOLE list, never the filtered view.
  // Counting the filtered view makes every unpicked option read 0 the moment
  // one is picked, which is a menu that argues with itself.
  const stateCounts = React.useMemo(() => stateFacetCounts(pages), [pages])
  const owners = React.useMemo(() => ownerFacets(pages), [pages])

  const displayed = React.useMemo(
    () => pages.filter((p) => matchesPageFilters(p, filters, search)),
    [pages, filters, search],
  )
  // The "Shared with me" switch exists only when the server can answer it.
  // A toggle that can never match anything is a dead switch, and today's
  // servers do not send `reach` yet.
  const reachKnown = React.useMemo(() => hasReach(pages), [pages])

  const activeCount = pageFilterCount(filters)
  const ownerLabel = (ref: string) => owners.find((o) => o.ref === ref)?.label ?? ref

  // ── Groups ────────────────────────────────────────────────────────────────
  // Grouped from the DISPLAYED list, so a facet or a search narrows every
  // group and a group it empties disappears rather than standing there with
  // a zero. The header count is therefore "what is in it now".
  const groups = React.useMemo(
    () => groupPagesByOwner(displayed, currentUserId),
    [displayed, currentUserId],
  )

  // ── Collapse state ────────────────────────────────────────────────────────
  // State is kept WITH the key it was read under. When the user or workspace
  // changes the stored value belongs to someone else, so it is dropped in the
  // same render rather than shown for a frame and then written back under
  // the new key — a persist effect keyed on the state alone would do exactly
  // that write.
  const storageKey = collapseStorageKey(workspaceId, currentUserId)
  const [store, setStore] = React.useState(() => ({
    key: storageKey,
    collapsed: readCollapsed(storageKey),
  }))
  React.useEffect(() => {
    if (store.key !== storageKey) setStore({ key: storageKey, collapsed: readCollapsed(storageKey) })
  }, [storageKey, store.key])
  const collapsed = store.key === storageKey ? store.collapsed : NONE_COLLAPSED

  const setCollapsed = React.useCallback(
    (key: string, fold: boolean) => {
      setStore((prev) => {
        const base = prev.key === storageKey ? prev.collapsed : readCollapsed(storageKey)
        if (base.has(key) === fold) return prev.key === storageKey ? prev : { key: storageKey, collapsed: base }
        const next = new Set(base)
        if (fold) next.add(key)
        else next.delete(key)
        writeCollapsed(storageKey, next)
        return { key: storageKey, collapsed: next }
      })
    },
    [storageKey],
  )

  // A search opens every group that has a match and hides the rest; the
  // saved fold is untouched and comes back the moment the query is cleared.
  const searching = search.trim() !== ""
  const isOpen = (g: PageGroup) => searching || !collapsed.has(g.key)

  // The active page is always visible: selecting one inside a folded group
  // unfolds it. Keyed on the selection, not on the fold — the person may fold
  // the active group again afterwards and that has to stick.
  const activeGroupKey = React.useMemo(
    () => (selectedSlug ? groups.find((g) => g.pages.some((p) => p.slug === selectedSlug))?.key ?? null : null),
    [groups, selectedSlug],
  )
  React.useEffect(() => {
    if (activeGroupKey) setCollapsed(activeGroupKey, false)
  }, [activeGroupKey, setCollapsed])

  // ── Keyboard ──────────────────────────────────────────────────────────────
  // One handler on the list, not one per row: the rows already answer Enter
  // and Space through `ListRow`, and a header is a native button. What the
  // list adds is movement — Up/Down/Home/End between whatever is on screen,
  // Left to fold the group the focus is in (landing on its header, so the
  // focus never falls off a row that just vanished) and Right to unfold it.
  const listRef = React.useRef<HTMLDivElement>(null)
  const onListKeyDown = (e: React.KeyboardEvent<HTMLDivElement>) => {
    const root = listRef.current
    const target = e.target as HTMLElement | null
    if (!root || !target) return
    const groupKey = target.dataset.railHeader ?? target.dataset.railRow
    if (groupKey == null) return

    if (e.key === "ArrowDown" || e.key === "ArrowUp" || e.key === "Home" || e.key === "End") {
      const items = navigables(root)
      const i = items.indexOf(target)
      if (i < 0) return
      const last = items.length - 1
      const step = e.key === "ArrowDown" ? 1 : -1
      const next =
        e.key === "Home" ? 0 : e.key === "End" ? last : Math.min(last, Math.max(0, i + step))
      e.preventDefault()
      items[next]?.focus()
      return
    }
    if (e.key === "ArrowLeft") {
      e.preventDefault()
      setCollapsed(groupKey, true)
      if (target.dataset.railRow != null) focusHeader(root, groupKey)
      return
    }
    if (e.key === "ArrowRight") {
      e.preventDefault()
      setCollapsed(groupKey, false)
    }
  }

  return (
    // Tagged so a test can hold the node across a selection: the rail must not
    // be torn down and rebuilt when a different page is opened.
    <div data-slot="pages-rail" className="flex h-full flex-col">
      <SidebarToolbar>
        <div data-pages-search className="min-w-0 flex-1">
          <SidebarSearch
            value={search}
            onValueChange={onSearchChange}
            placeholder="Search pages, owners…"
            aria-label="Search pages"
          />
        </div>
        <SidebarFilterPopover
          label="Filter pages"
          activeCount={activeCount}
          // Anchored to the trigger's right edge inside a 280px rail that
          // clips its overflow, so the panel is kept narrower than the
          // trigger's distance from the rail's left edge.
          panelClassName="w-[228px]"
          onClear={() => onFiltersChange(EMPTY_PAGE_FILTERS)}
        >
          <SidebarFacet
            first
            label="Status"
            resetLabel="All"
            resetActive={filters.states.length === 0}
            onReset={() => onFiltersChange({ ...filters, states: [] })}
          >
            {PAGE_STATE_ORDER.map((state: PanelState) => {
              const meta = PAGE_STATE_META[state]
              const n = stateCounts[state]
              const Icon = meta.icon
              return (
                <SidebarFacetOption
                  key={state}
                  active={filters.states.includes(state)}
                  onToggle={() =>
                    onFiltersChange({
                      ...filters,
                      states: togglePageFilter(filters.states, state),
                    })
                  }
                >
                  <Icon className={cn("h-3.5 w-3.5 shrink-0", meta.tone, n === 0 && "opacity-40")} />
                  <span className="truncate">{meta.label}</span>
                  <Count n={n} dim={n === 0} />
                </SidebarFacetOption>
              )
            })}
          </SidebarFacet>

          {owners.length > 0 && (
            <SidebarFacet
              label="Owner"
              resetLabel="All crews"
              resetActive={filters.owners.length === 0}
              onReset={() => onFiltersChange({ ...filters, owners: [] })}
            >
              {owners.map((o) => (
                <SidebarFacetOption
                  key={o.ref}
                  active={filters.owners.includes(o.ref)}
                  onToggle={() =>
                    onFiltersChange({
                      ...filters,
                      owners: togglePageFilter(filters.owners, o.ref),
                    })
                  }
                >
                  <CONCEPT_ICON.crews className="h-3.5 w-3.5 shrink-0 text-muted-foreground-soft" />
                  <span className="truncate">{o.label}</span>
                  <Count n={o.count} />
                </SidebarFacetOption>
              ))}
            </SidebarFacet>
          )}

          {reachKnown && (
            <SidebarFacet
              label="Reach"
              resetLabel="Everything I reach"
              resetActive={!filters.shared}
              onReset={() => onFiltersChange({ ...filters, shared: false })}
            >
              <SidebarFacetOption
                active={filters.shared}
                onToggle={() => onFiltersChange({ ...filters, shared: !filters.shared })}
              >
                <Share2 className="h-3.5 w-3.5 shrink-0 text-muted-foreground-soft" />
                <span className="truncate">Shared with me</span>
              </SidebarFacetOption>
            </SidebarFacet>
          )}
        </SidebarFilterPopover>
        {onToggleCollapse && <SidebarCollapseButton collapsed={false} onToggle={onToggleCollapse} />}
      </SidebarToolbar>

      {/* What is currently narrowing the list, removable one at a time. With
          two multi-select facets the count badge alone cannot say WHICH picks
          are active. */}
      <SidebarActiveChips>
        {filters.states.map((s) => (
          <SidebarActiveChip
            key={`state-${s}`}
            onRemove={() =>
              onFiltersChange({ ...filters, states: filters.states.filter((v) => v !== s) })
            }
          >
            {PAGE_STATE_META[s].label}
          </SidebarActiveChip>
        ))}
        {filters.owners.map((ref) => (
          <SidebarActiveChip
            key={`owner-${ref}`}
            onRemove={() =>
              onFiltersChange({ ...filters, owners: filters.owners.filter((v) => v !== ref) })
            }
          >
            {ownerLabel(ref)}
          </SidebarActiveChip>
        ))}
        {filters.shared && (
          <SidebarActiveChip onRemove={() => onFiltersChange({ ...filters, shared: false })}>
            Shared with me
          </SidebarActiveChip>
        )}
      </SidebarActiveChips>

      <div className="flex min-h-0 flex-1 flex-col">
        <div
          ref={listRef}
          onKeyDown={onListKeyDown}
          className="min-h-0 flex-1 overflow-y-auto pb-1"
        >
          {groups.map((group) => (
            <SidebarSection
              key={group.key}
              collapsible
              collapsed={!isOpen(group)}
              onToggle={() => setCollapsed(group.key, !collapsed.has(group.key))}
              label={group.label}
              count={group.pages.length}
              // The header is the only place the owner is written, and a crew
              // name has no upper bound; the kit truncates it so the count
              // stays on the line. `title` carries the whole of it.
              headerClassName="min-w-0"
              headerProps={{ "data-rail-header": group.key, title: group.label }}
            >
              {/* The rail is a FILTERED list: picking a facet above removes
                  rows, clearing it brings them back. Those are the two moments
                  worth drawing, so each row owns its own entry and exit rather
                  than the whole list being swapped out — and `listRow.layout`
                  closes the gap a removed row leaves instead of snapping the
                  rest upward. */}
              <AnimatePresence initial={false}>
                {group.pages.map((page) => {
                  const meta = page.state ? PAGE_STATE_META[page.state] : null
                  const Icon = meta?.icon ?? CONCEPT_ICON.pages
                  return (
                    <motion.div
                      key={page.id || page.slug}
                      {...listRow}
                      initial={{ opacity: 0 }}
                      animate={{ opacity: 1 }}
                      exit={{ opacity: 0, height: 0 }}
                      className="overflow-hidden"
                    >
                      <SidebarRow
                        selected={selectedSlug === page.slug}
                        onSelect={() => onSelectPage(page.slug)}
                        data-rail-row={group.key}
                      >
                        <Icon
                          className={cn("h-3.5 w-3.5 shrink-0", meta?.tone ?? "text-muted-foreground-soft")}
                          aria-hidden
                        />
                        {/* One line, whatever the name's length; the full
                            name is in `title`, and the owner is in the header
                            above — never repeated here. */}
                        <span className="min-w-0 flex-1 truncate text-foreground/80" title={page.name}>
                          {page.name}
                        </span>
                        {/* The badge is the panel count and only ever the
                            panel count. A badge that means one thing on some
                            rows and another on the rest is unreadable at a
                            glance. */}
                        {page.tally.total > 0 && (
                          <span className="type-nav-sub shrink-0 rounded-full bg-white/[0.05] px-1.5 py-px tabular-nums text-foreground">
                            {page.tally.total}
                          </span>
                        )}
                      </SidebarRow>
                    </motion.div>
                  )
                })}
              </AnimatePresence>
            </SidebarSection>
          ))}
          {displayed.length === 0 && (
            <div className="type-nav-sub px-3 py-6 text-center text-muted-foreground-soft">
              {pages.length === 0 ? (
                <>
                  {/* Authoring is three doors onto one document (§10b.1). The
                      empty state used to name only the CLI, which is the door
                      you cannot open from here. */}
                  <p>No pages yet.</p>
                  {onCreatePage && (
                    <button
                      type="button"
                      onClick={onCreatePage}
                      className="type-nav-sub mt-2 rounded-md border border-border/60 px-2.5 py-1 text-foreground/80 transition-colors hover:border-primary/40 hover:text-primary"
                    >
                      New page
                    </button>
                  )}
                  <p className="mt-2">
                    Or author one as YAML: crewship page create --file page.yaml, then push a
                    payload to a panel.
                  </p>
                </>
              ) : (
                <p>No page matches. Clear a facet above, or search for another name.</p>
              )}
            </div>
          )}
        </div>
      </div>
    </div>
  )
}
