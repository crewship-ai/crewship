"use client"

/**
 * The /pages filter rail — zone 2 of PRD §9b.1.
 *
 *   [icon rail]  [filter rail 280px]              [main]
 *   AppSidebar   SidebarToolbar / Search /        Overview · or a single page
 *                FilterButton / New folder / Collapse
 *                ── STATUS ─────── All · Fresh · Stale · Failed · Never produced
 *                ── OWNER  ─────── per crew · Shared with me
 *                ── GROUP BY ───── Folder · Owner
 *                ── <folder> ───── the list, grouped by FOLDER (#2527): one
 *                ── <folder> ───── section per folder the caller may read,
 *                ── UNFILED ────── icon and colour in the header, then Unfiled
 *                                  — or by OWNER (#2523): Mine · crews I
 *                                  belong to · other crews · Owned by others
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
import { FolderPlus, Globe, ListChecks, Share2 } from "lucide-react"

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
  groupPagesByFolder,
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
import type { PageFolderView } from "@/hooks/use-page-folders"
import { SHARED_WITH_WORKSPACE_TITLE } from "@/lib/pages/folder-sharing"
import { Checkbox } from "@/components/ui/checkbox"
import type { PanelState } from "@/components/features/pages/panels/types"
import { PAGE_STATE_META, PAGE_STATE_ORDER } from "@/components/features/pages/page-state"
import { FolderGlyph } from "@/components/features/pages/folder-glyph"
import { FolderHeaderMenu, PageRowMenu } from "@/components/features/pages/pages-rail-menus"

export type PagesGroupBy = "folder" | "owner"

export interface PagesRailProps {
  pages: PageView[]
  /**
   * Every folder the caller may read, empty ones included (#2527). `null` or
   * absent means this server has no folders: the rail then groups by owner
   * and offers no folder control at all, rather than one empty "Unfiled".
   */
  folders?: PageFolderView[] | null
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
  /** The row menu's two verbs. Absent, the menu is not drawn. */
  onMovePage?: (page: PageView) => void
  onRemoveFromFolder?: (page: PageView) => void
  /** The folder verbs: the toolbar button and the header menu. */
  onCreateFolder?: () => void
  onEditFolder?: (slug: string) => void
  /** Folder → Sharing (#2533). Absent, the menu has no such item. */
  onShareFolder?: (slug: string) => void
  onDeleteFolder?: (slug: string) => void
  /**
   * Several pages at once (#2533). Present, the toolbar gains a Select mode:
   * checkboxes on the rows, Space toggles the focused row, Shift+click takes
   * a range, and Move to folder… hands the chosen pages over. The rail leaves
   * Select mode as it does so; the dialog owns the rest.
   */
  onMovePages?: (pages: PageView[]) => void
}

// ── Saved view state (#2523, #2527) ─────────────────────────────────────────
//
// Saved per user AND per workspace: the same person folds different crews in
// different workspaces, and two people on one browser must not share a fold.
// Storage is treated as optional at every step — a private window, a full
// quota, a policy that throws on access, or a value someone else wrote there
// all have to leave the rail working with the default, which is "everything
// open, grouped by folder". Nothing here is worth an error boundary.
//
// One record holds both the folds and the grouping. It began as a bare array
// of folded keys (#2523) and is read that way still; it is written as an
// object now that there is a second thing to remember.

export const collapseStorageKey = (workspaceId: string, userId: string | null | undefined) =>
  `pages-rail:${workspaceId}:${userId ?? "anonymous"}`

const NONE_COLLAPSED: ReadonlySet<string> = new Set()

interface RailStore {
  collapsed: ReadonlySet<string>
  groupBy: PagesGroupBy
}

const DEFAULT_STORE: RailStore = { collapsed: NONE_COLLAPSED, groupBy: "folder" }

function keysOf(value: unknown): ReadonlySet<string> {
  if (!Array.isArray(value)) return NONE_COLLAPSED
  return new Set(value.filter((k): k is string => typeof k === "string"))
}

function readStore(key: string): RailStore {
  try {
    const raw = globalThis.localStorage?.getItem(key)
    if (!raw) return DEFAULT_STORE
    const parsed: unknown = JSON.parse(raw)
    if (Array.isArray(parsed)) return { collapsed: keysOf(parsed), groupBy: "folder" }
    if (!parsed || typeof parsed !== "object") return DEFAULT_STORE
    const rec = parsed as { collapsed?: unknown; groupBy?: unknown }
    return {
      collapsed: keysOf(rec.collapsed),
      groupBy: rec.groupBy === "owner" ? "owner" : "folder",
    }
  } catch {
    return DEFAULT_STORE
  }
}

function writeStore(key: string, store: RailStore) {
  try {
    globalThis.localStorage?.setItem(
      key,
      JSON.stringify({ collapsed: Array.from(store.collapsed), groupBy: store.groupBy }),
    )
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
  folders,
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
  onMovePage,
  onRemoveFromFolder,
  onCreateFolder,
  onEditFolder,
  onShareFolder,
  onDeleteFolder,
  onMovePages,
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
  // Same rule for folders: the grouping choice and the folder verbs exist
  // only on a server that has folders.
  const foldersKnown = folders != null
  const folderBySlug = React.useMemo(() => new Map((folders ?? []).map((f) => [f.slug, f])), [folders])

  const activeCount = pageFilterCount(filters)
  const ownerLabel = (ref: string) => owners.find((o) => o.ref === ref)?.label ?? ref

  // ── View state ────────────────────────────────────────────────────────────
  // State is kept WITH the key it was read under. When the user or workspace
  // changes the stored value belongs to someone else, so it is dropped in the
  // same render rather than shown for a frame and then written back under
  // the new key — a persist effect keyed on the state alone would do exactly
  // that write.
  const storageKey = collapseStorageKey(workspaceId, currentUserId)
  const [store, setStore] = React.useState(() => ({ key: storageKey, ...readStore(storageKey) }))
  React.useEffect(() => {
    if (store.key !== storageKey) setStore({ key: storageKey, ...readStore(storageKey) })
  }, [storageKey, store.key])
  const current: RailStore = store.key === storageKey ? store : DEFAULT_STORE
  const collapsed = current.collapsed
  const groupBy: PagesGroupBy = foldersKnown ? current.groupBy : "owner"

  const setCollapsed = React.useCallback(
    (key: string, fold: boolean) => {
      setStore((prev) => {
        const base = prev.key === storageKey ? prev : { key: storageKey, ...readStore(storageKey) }
        if (base.collapsed.has(key) === fold) return base
        const next = new Set(base.collapsed)
        if (fold) next.add(key)
        else next.delete(key)
        const out = { ...base, collapsed: next }
        writeStore(storageKey, out)
        return out
      })
    },
    [storageKey],
  )

  const setGroupBy = React.useCallback(
    (next: PagesGroupBy) => {
      setStore((prev) => {
        const base = prev.key === storageKey ? prev : { key: storageKey, ...readStore(storageKey) }
        if (base.groupBy === next) return base
        const out = { ...base, groupBy: next }
        writeStore(storageKey, out)
        return out
      })
    },
    [storageKey],
  )

  // ── Groups ────────────────────────────────────────────────────────────────
  // Grouped from the DISPLAYED list, so a facet or a search narrows every
  // group and a group it empties disappears rather than standing there with
  // a zero. The header count is therefore "what is in it now" — except a
  // folder with nothing in it on an UNNARROWED list, which is drawn with its
  // zero: it is the person's own folder, and one that only appears once
  // something is filed in it cannot be filed into.
  const searching = search.trim() !== ""
  const narrowing = searching || activeCount > 0
  const groups = React.useMemo(() => {
    if (groupBy === "folder") {
      const all = groupPagesByFolder(displayed, folders ?? [])
      return narrowing ? all.filter((g) => g.pages.length > 0) : all
    }
    return groupPagesByOwner(displayed, currentUserId)
  }, [groupBy, displayed, folders, narrowing, currentUserId])

  const countOf = (g: PageGroup) =>
    g.kind === "folder" && !narrowing && g.pageCount !== undefined ? g.pageCount : g.pages.length

  // A search opens every group that has a match and hides the rest; the
  // saved fold is untouched and comes back the moment the query is cleared.
  const isOpen = (g: PageGroup) => searching || !collapsed.has(g.key)

  // The active page is always visible: selecting one inside a folded group
  // unfolds it. Keyed on the selection, not on the fold — the person may fold
  // the active group again afterwards and that has to stick.
  const activeGroupKey = React.useMemo(
    () => (selectedSlug ? groups.find((g) => g.pages.some((p) => p.slug === selectedSlug))?.key ?? null : null),
    [groups, selectedSlug],
  )
  // Keyed on the SELECTION, not on the group it lands in: opening page A,
  // folding its group and then choosing page B from the same group changes
  // no group key, and an effect that watched only the key left B hidden
  // (validation 2026-09-13). `selectedSlug` is in the list so every
  // selection re-runs it, even when the group is the one already open.
  React.useEffect(() => {
    if (selectedSlug && activeGroupKey) setCollapsed(activeGroupKey, false)
  }, [selectedSlug, activeGroupKey, setCollapsed])

  // ── Row menu ──────────────────────────────────────────────────────────────
  // Which row's "⋯" menu is open, by page slug. Owned here rather than by
  // each row so the list-level key handler can open it for the focused row.
  const [menuFor, setMenuFor] = React.useState<string | null>(null)
  const rowMenus = Boolean(onMovePage || onRemoveFromFolder)

  // ── Select mode (#2533) ───────────────────────────────────────────────────
  // A row in Select mode toggles instead of opening — click, Enter and Space
  // all go through the row's own `onSelect`, so the keyboard gets it for
  // free. Shift is read off the click on its way down (`onClickCapture`),
  // because `onSelect` carries no event; the range runs in the order the rows
  // are on screen, from the last row toggled to this one.
  const selectable = foldersKnown && Boolean(onMovePages)
  const [selecting, setSelecting] = React.useState(false)
  const [selected, setSelected] = React.useState<ReadonlySet<string>>(() => new Set())
  const anchor = React.useRef<string | null>(null)
  const shiftHeld = React.useRef(false)
  const visibleOrder = React.useMemo(() => groups.flatMap((g) => g.pages.map((p) => p.slug)), [groups])
  const leaveSelectMode = () => {
    setSelecting(false)
    setSelected(new Set())
    anchor.current = null
  }
  const toggleSelected = (slug: string, range: boolean) => {
    setSelected((prev) => {
      const next = new Set(prev)
      const from = anchor.current
      if (range && from !== null && from !== slug) {
        const a = visibleOrder.indexOf(from)
        const b = visibleOrder.indexOf(slug)
        if (a >= 0 && b >= 0) {
          const on = prev.has(from)
          for (const s of visibleOrder.slice(Math.min(a, b), Math.max(a, b) + 1)) {
            if (on) next.add(s)
            else next.delete(s)
          }
          return next
        }
      }
      if (next.has(slug)) next.delete(slug)
      else next.add(slug)
      return next
    })
    anchor.current = slug
  }
  const chosenPages = React.useMemo(() => pages.filter((p) => selected.has(p.slug)), [pages, selected])

  // ── Keyboard ──────────────────────────────────────────────────────────────
  // One handler on the list, not one per row: the rows already answer Enter
  // and Space through `ListRow`, and a header is a native button. What the
  // list adds is movement — Up/Down/Home/End between whatever is on screen,
  // Left to fold the group the focus is in (landing on its header, so the
  // focus never falls off a row that just vanished) and Right to unfold it —
  // and Shift+F10 or the ContextMenu key on a row, which opens its menu.
  const listRef = React.useRef<HTMLDivElement>(null)
  const onListKeyDown = (e: React.KeyboardEvent<HTMLDivElement>) => {
    const root = listRef.current
    const target = e.target as HTMLElement | null
    if (!root || !target) return
    const groupKey = target.dataset.railHeader ?? target.dataset.railRow
    if (groupKey == null) return

    if ((e.key === "F10" && e.shiftKey) || e.key === "ContextMenu") {
      const slug = target.dataset.pageSlug
      if (rowMenus && slug) {
        e.preventDefault()
        setMenuFor(slug)
      }
      return
    }
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
      // Folding is off while searching (see the header's onToggle); the
      // focus move to the header still happens, it is navigation.
      if (!searching) setCollapsed(groupKey, true)
      if (target.dataset.railRow != null) focusHeader(root, groupKey)
      return
    }
    if (e.key === "ArrowRight") {
      e.preventDefault()
      if (!searching) setCollapsed(groupKey, false)
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

          {/* A view choice, not a filter: it narrows nothing, so it is not
              counted on the trigger and not cleared with the rest. The
              facet's reset row IS the default state — Folder — and the one
              option is the other grouping; picking it off returns to the
              default, which is what the kit's reset row means. */}
          {foldersKnown && (
            <SidebarFacet
              label="Group by"
              resetLabel="Folder"
              resetActive={groupBy === "folder"}
              onReset={() => setGroupBy("folder")}
            >
              <SidebarFacetOption
                active={groupBy === "owner"}
                onToggle={() => setGroupBy(groupBy === "owner" ? "folder" : "owner")}
              >
                <CONCEPT_ICON.crews className="h-3.5 w-3.5 shrink-0 text-muted-foreground-soft" />
                <span className="truncate">Owner</span>
              </SidebarFacetOption>
            </SidebarFacet>
          )}
        </SidebarFilterPopover>
        {foldersKnown && onCreateFolder && (
          <button
            type="button"
            onClick={onCreateFolder}
            aria-label="New folder"
            title="New folder"
            className="kit-tap inline-flex h-8 w-8 shrink-0 items-center justify-center rounded-md border border-white/[0.08] bg-white/[0.04] text-muted-foreground transition-colors hover:text-foreground"
          >
            <FolderPlus className="h-3.5 w-3.5" aria-hidden />
          </button>
        )}
        {selectable && (
          <button
            type="button"
            onClick={() => (selecting ? leaveSelectMode() : setSelecting(true))}
            aria-label="Select pages"
            aria-pressed={selecting}
            title="Select several pages to move them together"
            className={cn(
              "kit-tap inline-flex h-8 w-8 shrink-0 items-center justify-center rounded-md border border-white/[0.08] bg-white/[0.04] text-muted-foreground transition-colors hover:text-foreground",
              selecting && "border-primary/40 bg-primary/15 text-primary-hover",
            )}
          >
            <ListChecks className="h-3.5 w-3.5" aria-hidden />
          </button>
        )}
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

      {selecting && (
        <div data-slot="pages-select-bar" className="flex items-center gap-2 border-b border-white/[0.06] px-3 py-1.5">
          <span className="type-nav-sub min-w-0 flex-1 truncate text-muted-foreground" aria-live="polite">
            {selected.size} selected
          </span>
          <button
            type="button"
            disabled={chosenPages.length === 0}
            onClick={() => {
              onMovePages?.(chosenPages)
              leaveSelectMode()
            }}
            className="type-nav-sub rounded-md border border-border/60 px-2 py-0.5 text-foreground/80 transition-colors hover:border-primary/40 hover:text-primary disabled:opacity-50"
          >
            Move to folder…
          </button>
          <button
            type="button"
            onClick={leaveSelectMode}
            className="type-nav-sub rounded-md px-1.5 py-0.5 text-muted-foreground transition-colors hover:text-foreground"
          >
            Cancel
          </button>
        </div>
      )}

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
              // While a search is open every group with a match is shown
              // open, so a fold here would change nothing on screen and
              // still be written to storage — a change the person would
              // only see after clearing the search (validation 2026-09-13).
              // The header stays a button, for focus and the tree walk, but
              // it folds nothing until the search is gone; Left/Right in
              // the keyboard handler follow the same rule.
              onToggle={() => {
                if (searching) return
                setCollapsed(group.key, !collapsed.has(group.key))
              }}
              label={
                group.kind === "folder" ? (
                  // The folder's icon in the folder's colour, before the
                  // name — one symbol, the one the picker showed, drawn once
                  // in the header. A folder without a colour keeps the muted
                  // icon.
                  <span className="inline-flex min-w-0 max-w-full items-center gap-1.5">
                    <FolderGlyph
                      icon={group.folder?.icon}
                      color={group.folder?.color}
                      className={cn("h-3.5 w-3.5", !group.folder?.color && "text-muted-foreground-soft")}
                    />
                    <span className="min-w-0 truncate">{group.label}</span>
                    {/* The one sharing fact everyone may know: this folder is
                        open to the whole workspace (#2533). Names are the
                        manager's to see, in Sharing. */}
                    {group.folder && folderBySlug.get(group.folder.slug)?.shared === "workspace" && (
                      <span
                        role="img"
                        data-slot="folder-shared-marker"
                        aria-label={SHARED_WITH_WORKSPACE_TITLE}
                        title={SHARED_WITH_WORKSPACE_TITLE}
                        className="inline-flex shrink-0 text-muted-foreground-soft"
                      >
                        <Globe className="h-3 w-3" aria-hidden />
                      </span>
                    )}
                  </span>
                ) : (
                  group.label
                )
              }
              count={countOf(group)}
              // The header is the only place the owner is written, and a crew
              // or folder name has no upper bound; the kit truncates it so the
              // count stays on the line. `title` carries the whole of it.
              headerClassName="min-w-0"
              headerProps={{ "data-rail-header": group.key, title: group.label }}
              // The section is a hover group so the folder menu beside its
              // header shows when the pointer is over the folder.
              className="group/section"
              actions={
                group.kind === "folder" && group.folder && (onEditFolder || onDeleteFolder || onShareFolder) ? (
                  <FolderHeaderMenu
                    folderName={group.label}
                    onRename={() => onEditFolder?.(group.folder!.slug)}
                    onShare={onShareFolder ? () => onShareFolder(group.folder!.slug) : undefined}
                    onDelete={() => onDeleteFolder?.(group.folder!.slug)}
                  />
                ) : undefined
              }
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
                  const inFolder = Boolean(page.folder)
                  return (
                    <motion.div
                      key={page.id || page.slug}
                      {...listRow}
                      initial={{ opacity: 0 }}
                      animate={{ opacity: 1 }}
                      exit={{ opacity: 0, height: 0 }}
                      className="overflow-hidden"
                      onClickCapture={selecting ? (e) => void (shiftHeld.current = e.shiftKey) : undefined}
                      onContextMenu={
                        rowMenus && !selecting
                          ? (e) => {
                              e.preventDefault()
                              setMenuFor(page.slug)
                            }
                          : undefined
                      }
                    >
                      <SidebarRow
                        selected={selecting ? selected.has(page.slug) : selectedSlug === page.slug}
                        onSelect={() => {
                          if (!selecting) return onSelectPage(page.slug)
                          toggleSelected(page.slug, shiftHeld.current)
                          shiftHeld.current = false
                        }}
                        data-rail-row={group.key}
                        data-page-slug={page.slug}
                        data-checked={selecting ? (selected.has(page.slug) ? "true" : "false") : undefined}
                        className="group/row"
                      >
                        {selecting && (
                          // Drawn, not driven: the row is the control (click,
                          // Enter, Space), and a second control inside it that
                          // also toggled would toggle twice on one click.
                          <Checkbox
                            checked={selected.has(page.slug)}
                            tabIndex={-1}
                            aria-hidden
                            className="pointer-events-none"
                          />
                        )}
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
                        {rowMenus && !selecting && (
                          <PageRowMenu
                            pageName={page.name}
                            inFolder={inFolder && Boolean(onRemoveFromFolder)}
                            open={menuFor === page.slug}
                            onOpenChange={(open) => setMenuFor(open ? page.slug : null)}
                            onMove={() => onMovePage?.(page)}
                            onRemoveFromFolder={() => onRemoveFromFolder?.(page)}
                          />
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
