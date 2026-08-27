"use client"

/**
 * The /credentials left rail — the /routines explorer shape, which is the
 * house pattern for a page whose left side lists things.
 *
 * The rail answers "which one?" and the Filter button answers "narrow it
 * how?". That split is why the body belongs to the CREDENTIALS themselves
 * and the facets sit behind an icon: the first version stacked Status,
 * Category, Scope and Tag down the rail, which reads well with four
 * credentials and badly with forty — the stack of ways to narrow the list
 * ends up taller than the list, and the list itself is nowhere in the rail
 * at all. /routines faced the same choice and made it the other way.
 *
 * The panel behind that button is the SHARED one (`SidebarFilterPopover` in
 * sidebar-kit), not a local copy. It used to be a local copy, and it was the
 * worst of the five: every pick called `setFilterOpen(false)`, so combining a
 * brand with a scope meant reopening the menu between them, and each facet held
 * exactly one value. Both behaviours now come from the kit and the facets are
 * lists — see `CredentialFilters` (#1776).
 *
 * STATUS stays in the rail rather than moving into the dropdown. It is the
 * question asked most often about a vault ("what is broken?"), it is
 * single-select, and its three rows are bounded — which Category and Scope,
 * both of which grow with the workspace, are not.
 *
 * TIER earns the same place on the same test: four rows, single-select, bounded
 * forever by the Keeper tier table. It is here rather than behind the Filter
 * button because it is the second question asked about a vault ("what is
 * dangerous?") and, until it was added, the answer was nowhere on this page at
 * all. It is also the one section that prints zeroes — see below.
 *
 * Every count comes from the same functions the list filters with
 * (`lib/credentials/facets.ts`), so a count can never disagree with what
 * clicking it selects. A facet with nothing behind it is omitted rather than
 * shown as zero: a row that filters to an empty list is a control that
 * appears to work and does not, which costs more trust than the missing row
 * costs discoverability.
 */

import * as React from "react"
import { AlertTriangle, ArrowUpDown, Bot, Building2, Check, Hash, KeyRound, Layers, ListChecks, PackageX, Shapes, ShieldCheck } from "lucide-react"
import { AnimatePresence, motion } from "motion/react"

import {
  SidebarCollapseButton,
  SidebarFacet,
  SidebarFacetOption,
  SidebarFilterPopover,
  SidebarRow,
  SidebarSearch,
  SidebarSection,
  SidebarToolbar,
} from "@/components/layout/sidebar-kit"
import { CrewIcon } from "@/components/ui/crew-icon"
import { AgentAvatar } from "@/components/ui/agent-avatar"
import { getBrand, brandColor } from "@/lib/credential-providers/registry"
import type { CredentialCrewRef } from "@/hooks/use-credential-readiness"
import type { CredentialFacetOption, CredentialFilters } from "@/lib/credentials/facets"
import { EMPTY_CREDENTIAL_FILTERS } from "@/lib/credentials/facets"
import { UNCLASSIFIED_TIER, tierMeta, type CredentialTierLevel } from "@/lib/credentials/tiers"
import { CredentialTierBadge } from "./credential-tier-badge"
import { cn } from "@/lib/utils"

/** What the rail needs from a credential. Deliberately narrow: the rail
 *  renders an icon, a name, a tier and a selection state, and nothing in this
 *  shape should tempt a caller into routing a secret through it. */
export interface SidebarCredential {
  id: string
  name: string
  provider: string
  type: string
  /** Keeper tier, 1–4, or null when the server did not report one. */
  tier?: CredentialTierLevel | null
  /** The server's own tier label, for the chip's tooltip. */
  tierLabel?: string | null
}

export interface CredentialsSidebarProps {
  filters: CredentialFilters
  onFiltersChange: (next: CredentialFilters) => void
  counts: { all: number; attention: number; missingTool: number }
  /** Brands in use — what the picker sets and the row icon already shows. */
  brands: CredentialFacetOption[]
  /** Credential shapes in use — the wizard's first question. */
  shapes?: CredentialFacetOption[]
  scopes: CredentialFacetOption[]
  /** Keeper tiers — every tier, including the empty ones. See the TIER section. */
  tiers?: CredentialFacetOption[]
  /** Agents holding at least one credential; the value is the agent id, which
   *  is what its avatar is keyed by. */
  agents?: CredentialFacetOption[]
  /** Tags in use, with counts — see buildTagFacet. */
  tags: CredentialFacetOption[]
  /** crew id → name + icon + colour, so a crew scope row draws the crew. */
  crewsById?: Record<string, CredentialCrewRef>
  /** How the credential list is ordered. Lives here because the list does. */
  sort?: CredentialSortKey
  onSortChange?: (next: CredentialSortKey) => void
  /**
   * Bulk selection, for callers that can delete.
   *
   * A MODE, not the resting state. A checkbox on every row all the time says
   * the list is a thing you tick, when it is overwhelmingly a thing you click
   * — and it puts a delete affordance one mis-click from every secret in the
   * vault. The toggle in the section header turns it on for the moment you
   * actually want it.
   *
   * Omit `onSelectModeChange` and there is no toggle and no checkbox, which is
   * what a role that cannot delete should see.
   */
  selectMode?: boolean
  onSelectModeChange?: (next: boolean) => void
  selectedIds?: ReadonlySet<string>
  onToggleSelected?: (id: string) => void
  onToggleCollapse: () => void
  /** The credentials the current filters leave — the rail's body. */
  credentials?: SidebarCredential[]
  selectedCredentialId?: string | null
  onSelectCredential?: (id: string) => void
}

/** How the rail can order the credential list. Mirrors the page's own sort,
 *  which moved here when the table that used to own it was removed — the order
 *  belongs to the list, and the rail is the list. */
export type CredentialSortKey = "last_used" | "name" | "created"

const SORT_LABELS: Record<CredentialSortKey, string> = {
  last_used: "Last used",
  name: "Name",
  created: "Added",
}

/**
 * One group inside the Filter panel.
 *
 * `onChange` rather than a `keyof CredentialFilters` the caller writes back
 * through: a computed key over a union widens the patch to `{ [x: string]:
 * string[] }`, which type-checks against nothing useful. A closure per group
 * keeps each write pinned to the one field it belongs to.
 */
interface FilterFacetGroup {
  /** React key, and the field this group owns — for reading, not for writing. */
  key: string
  label: string
  resetLabel: string
  /** Fallback glyph for a row with no mark of its own. */
  icon: React.ComponentType<{ className?: string }>
  options: CredentialFacetOption[]
  selected: string[]
  onChange: (next: string[]) => void
  /** How a group draws its rows with the real thing — a brand mark, a crew
   *  tile, an agent's avatar. */
  renderIcon?: (opt: CredentialFacetOption) => React.ReactNode
}

const dropdownAnim = {
  initial: { opacity: 0, y: -4 },
  animate: { opacity: 1, y: 0, transition: { duration: 0.14, ease: "easeOut" as const } },
  exit: { opacity: 0, y: -4, transition: { duration: 0.1, ease: "easeIn" as const } },
}

export function CredentialsSidebar({
  filters,
  onFiltersChange,
  counts,
  brands,
  shapes = [],
  scopes,
  tiers = [],
  agents = [],
  tags,
  crewsById = {},
  sort = "last_used",
  onSortChange,
  selectMode = false,
  onSelectModeChange,
  selectedIds,
  onToggleSelected,
  onToggleCollapse,
  credentials = [],
  selectedCredentialId = null,
  onSelectCredential,
}: CredentialsSidebarProps) {
  const [sortOpen, setSortOpen] = React.useState(false)
  const [statusOpen, setStatusOpen] = React.useState(true)
  const [tierOpen, setTierOpen] = React.useState(true)

  const set = (patch: Partial<CredentialFilters>) => onFiltersChange({ ...filters, ...patch })

  // Status and tier are excluded on purpose: both have their own always-visible
  // sections, so counting them here would badge the Filter button for a choice
  // already on screen. The badge exists to explain a short list; a selection the
  // rail is still showing as pressed explains itself.
  //
  // It sums LENGTHS now that a facet holds several values, so "two brands and a
  // scope" badges 3. It could only ever read 0–5 before, and in practice 0 or 1,
  // because the panel shut after the first pick.
  const activeFilterCount =
    filters.brand.length +
    filters.shape.length +
    filters.scope.length +
    filters.tag.length +
    filters.agentId.length

  /** Add or drop one value. Never touches another facet — that is the promise
   *  `SidebarFilterPopover` is built around, and it is the consumer's to keep. */
  const toggle = (list: string[], value: string) =>
    list.includes(value) ? list.filter((v) => v !== value) : [...list, value]

  // One descriptor per group, so the divider logic ("no rule above the first
  // one") stays right even when the workspace has no shapes, no agents or no
  // tags — an empty facet is dropped here rather than returning null from
  // inside, where it would still count as the first.
  //
  // Every group draws its rows with the thing they are ABOUT — the brand marks
  // in a category, the crew's own tile, the agent's own avatar. An earlier
  // version repeated one lucide glyph down each group, which is the same as no
  // icon at all: three rows that look identical are three rows you have to read.
  const allFacetGroups: FilterFacetGroup[] = [
    {
      key: "brand",
      label: "Brand",
      resetLabel: "All brands",
      icon: Shapes,
      options: brands,
      selected: filters.brand,
      onChange: (next) => set({ brand: next }),
      renderIcon: (opt) => <CategoryMarks providers={opt.providers} />,
    },
    {
      key: "shape",
      label: "Shape",
      resetLabel: "Any shape",
      icon: KeyRound,
      options: shapes,
      selected: filters.shape,
      onChange: (next) => set({ shape: next }),
    },
    {
      key: "scope",
      label: "Scope",
      resetLabel: "All scopes",
      icon: Layers,
      options: scopes,
      selected: filters.scope,
      onChange: (next) => set({ scope: next }),
      renderIcon: (opt) => <ScopeMark value={opt.value} crews={crewsById} />,
    },
    {
      key: "agentId",
      label: "Assigned to",
      resetLabel: "All agents",
      icon: Bot,
      options: agents,
      selected: filters.agentId,
      onChange: (next) => set({ agentId: next }),
      // AgentAvatar, not a background-image. DiceBear returns an unencoded
      // `data:image/svg+xml,<svg …>` URI, and the quotes inside the SVG make
      // Chromium's CSSOM reject the whole declaration — the element keeps its
      // box and paints nothing. Every other avatar in the product is an <img>
      // for that reason; this is the same component they use, so the face here
      // is the face everywhere else.
      renderIcon: (opt) => (
        <AgentAvatar seed={opt.value} data-agent-id={opt.value} className="h-4 w-4 shrink-0" />
      ),
    },
    {
      key: "tag",
      label: "Tag",
      resetLabel: "Any tag",
      icon: Hash,
      options: tags,
      selected: filters.tag,
      onChange: (next) => set({ tag: next }),
    },
  ]
  // A facet with nothing behind it is dropped, not rendered as a bare heading —
  // and dropped HERE, so `first` below still means "the first one on screen".
  const facetGroups = allFacetGroups.filter((group) => group.options.length > 0)

  const statusRows: {
    key: CredentialFilters["status"]
    label: string
    count: number
    icon: React.ComponentType<{ className?: string }>
    tone?: string
    always?: boolean
  }[] = [
    { key: "all", label: "All", count: counts.all, icon: Check, always: true },
    {
      key: "attention",
      label: "Needs attention",
      count: counts.attention,
      icon: AlertTriangle,
      tone: "text-warn",
    },
    {
      key: "missing-tool",
      label: "Missing tool",
      count: counts.missingTool,
      icon: PackageX,
      tone: "text-destructive",
    },
  ]

  return (
    <div className="flex h-full flex-col">
      <SidebarToolbar>
        <div className="min-w-0 flex-1">
          <SidebarSearch
            value={filters.search}
            onValueChange={(v) => set({ search: v })}
            placeholder="Search a secret or tool…"
            aria-label="Search credentials"
          />
        </div>
        <SidebarFilterPopover
          label="Filter credentials"
          activeCount={activeFilterCount}
          panelClassName="min-w-[210px]"
          onClear={() =>
            // Search, status and tier survive: a user who typed a query and then
            // clears the facets is narrowing, not starting over — and status and
            // tier were both chosen in the rail, which is still showing them as
            // pressed. Undoing a selection the user can see is not "clear
            // filters", it is a surprise.
            onFiltersChange({
              ...EMPTY_CREDENTIAL_FILTERS,
              search: filters.search,
              status: filters.status,
              tier: filters.tier,
            })
          }
        >
          {facetGroups.map((group, i) => {
            const Icon = group.icon
            return (
              <SidebarFacet
                key={group.key}
                label={group.label}
                resetLabel={group.resetLabel}
                resetActive={group.selected.length === 0}
                onReset={() => group.onChange([])}
                first={i === 0}
              >
                {group.options.map((opt) => (
                  <SidebarFacetOption
                    key={opt.value}
                    active={group.selected.includes(opt.value)}
                    onToggle={() => group.onChange(toggle(group.selected, opt.value))}
                  >
                    {group.renderIcon?.(opt) ?? (
                      <Icon className="h-3.5 w-3.5 shrink-0 opacity-60" aria-hidden="true" />
                    )}
                    <span className="min-w-0 flex-1 truncate">{opt.label}</span>
                    <span className="shrink-0 tabular-nums text-[10px] text-muted-foreground-soft">
                      {opt.count}
                    </span>
                  </SidebarFacetOption>
                ))}
              </SidebarFacet>
            )
          })}
        </SidebarFilterPopover>
        <SidebarCollapseButton collapsed={false} onToggle={onToggleCollapse} />
      </SidebarToolbar>

      <SidebarSection
        label="Status"
        count={statusRows.filter((r) => r.always || r.count > 0).length}
        collapsible
        collapsed={!statusOpen}
        onToggle={() => setStatusOpen(!statusOpen)}
        className="border-b border-white/[0.06]"
      >
        {statusRows
          .filter((row) => row.always || row.count > 0)
          .map((row) => {
            const Icon = row.icon
            return (
              <SidebarRow
                key={row.key}
                selected={filters.status === row.key}
                onSelect={() => set({ status: row.key })}
              >
                <Icon
                  className={cn("h-3 w-3 shrink-0 text-muted-foreground/70", row.tone)}
                  aria-hidden="true"
                />
                <span className="min-w-0 flex-1 truncate">{row.label}</span>
                <span className="shrink-0 tabular-nums text-[10px] text-muted-foreground/60">
                  {row.count}
                </span>
              </SidebarRow>
            )
          })}
      </SidebarSection>

      {/* ── Tier ── (single-select, and the one section that prints zeroes)
       *
       * Every other facet in this rail omits an empty row, because a control
       * that filters to nothing is a control that appears to work and does not.
       * Tier is the exception, deliberately: "L4 · critical — 0" is not an empty
       * control, it is the answer to "does anything here stop for a human?", and
       * an operator who cannot find the row cannot tell whether the answer is
       * "none" or "the console does not track that". Empty rows are dimmed, the
       * way /routines dims a status bucket holding nothing.
       */}
      {tiers.length > 0 && (
        <SidebarSection
          label="Tier"
          count={tiers.length}
          collapsible
          collapsed={!tierOpen}
          onToggle={() => setTierOpen(!tierOpen)}
          className="border-b border-white/[0.06]"
        >
          <SidebarRow selected={filters.tier === null} onSelect={() => set({ tier: null })}>
            <ShieldCheck className="h-3 w-3 shrink-0 text-muted-foreground/70" aria-hidden="true" />
            <span className="min-w-0 flex-1 truncate">Any tier</span>
            <span className="shrink-0 tabular-nums text-[10px] text-muted-foreground/60">
              {counts.all}
            </span>
          </SidebarRow>
          {tiers.map((opt) => {
            const level = opt.value === UNCLASSIFIED_TIER ? null : Number(opt.value)
            const dot = level === null ? "bg-muted-foreground/30" : tierMeta(level).dotClass
            const selected = filters.tier === opt.value
            const empty = opt.count === 0
            return (
              <SidebarRow
                key={opt.value}
                selected={selected}
                onSelect={() => set({ tier: selected ? null : opt.value })}
              >
                <span
                  className={cn("h-1.5 w-1.5 shrink-0 rounded-full", dot, empty && !selected && "opacity-40")}
                  aria-hidden="true"
                />
                <span
                  className={cn(
                    "min-w-0 flex-1 truncate",
                    empty && !selected && "text-foreground/40",
                  )}
                >
                  {opt.label}
                </span>
                <span
                  className={cn(
                    "shrink-0 tabular-nums text-[10px] text-muted-foreground/60",
                    empty && "text-muted-foreground/35",
                  )}
                >
                  {opt.count}
                </span>
              </SidebarRow>
            )
          })}
        </SidebarSection>
      )}

      <div className="flex min-h-0 flex-1 flex-col">
        <SidebarSection
          label="Credentials"
          count={credentials.length}
          actions={
            <div className="flex items-center gap-0.5">
              {onSelectModeChange && (
                <button
                  type="button"
                  aria-pressed={selectMode}
                  aria-label={selectMode ? "Leave selection mode" : "Select several credentials"}
                  title={selectMode ? "Done selecting" : "Select several"}
                  onClick={() => onSelectModeChange(!selectMode)}
                  className={cn(
                    "inline-flex h-5 items-center gap-1 rounded px-1 text-[10px] transition-colors",
                    selectMode
                      ? "bg-primary/15 text-primary"
                      : "text-muted-foreground/70 hover:bg-white/[0.06] hover:text-foreground",
                  )}
                >
                  <ListChecks className="h-3 w-3" aria-hidden="true" />
                  {selectMode ? "Done" : "Select"}
                </button>
              )}
              {onSortChange ? (
              <div className="relative">
                <button
                  type="button"
                  aria-label={`Sort credentials — ${SORT_LABELS[sort]}`}
                  title={`Sorted by ${SORT_LABELS[sort].toLowerCase()}`}
                  aria-expanded={sortOpen}
                  onClick={() => setSortOpen(!sortOpen)}
                  className="inline-flex h-5 items-center gap-1 rounded px-1 text-[10px] text-muted-foreground/70 transition-colors hover:bg-white/[0.06] hover:text-foreground"
                >
                  <ArrowUpDown className="h-3 w-3" aria-hidden="true" />
                  {SORT_LABELS[sort]}
                </button>
                <AnimatePresence>
                  {sortOpen && (
                    <>
                      <div className="fixed inset-0 z-40" onClick={() => setSortOpen(false)} />
                      <motion.div
                        {...dropdownAnim}
                        className="absolute right-0 top-6 z-50 min-w-[150px] rounded-lg border border-white/[0.1] bg-card py-1 shadow-xl"
                      >
                        {(Object.keys(SORT_LABELS) as CredentialSortKey[]).map((key) => (
                          <button
                            key={key}
                            type="button"
                            onClick={() => {
                              onSortChange(key)
                              setSortOpen(false)
                            }}
                            className={cn(
                              "flex w-full items-center gap-2 px-3 py-1.5 text-left text-xs hover:bg-white/[0.06]",
                              sort === key ? "text-primary-hover" : "text-muted-foreground/80",
                            )}
                          >
                            <span className="flex-1">{SORT_LABELS[key]}</span>
                            {sort === key && <Check className="h-3 w-3 shrink-0" aria-hidden="true" />}
                          </button>
                        ))}
                      </motion.div>
                    </>
                  )}
                </AnimatePresence>
              </div>
              ) : null}
            </div>
          }
        />
        {/* A labelled region, inherited from the table this rail replaced.
            "The row for GH_TOKEN" needs somewhere to be addressed — for a
            screen reader as much as for a test — and the rail is now the only
            place that row exists. */}
        <div
          role="region"
          aria-label="Credential list"
          className="min-h-0 flex-1 overflow-y-auto pb-1"
        >
          {credentials.map((c) => {
            const brand = getBrand(c.provider)
            const Icon = brand.Icon
            return (
              <SidebarRow
                key={c.id}
                selected={selectedCredentialId === c.id}
                onSelect={() => onSelectCredential?.(c.id)}
              >
                {selectMode && onToggleSelected && (
                  // stopPropagation, or ticking the box would also open the
                  // credential — and the whole point of ticking several is not
                  // navigating away between them.
                  <input
                    type="checkbox"
                    checked={selectedIds?.has(c.id) ?? false}
                    onChange={() => onToggleSelected(c.id)}
                    onClick={(e) => e.stopPropagation()}
                    // Space is the checkbox's own activation key AND the row's,
                    // so without this a tick also opened the credential.
                    onKeyDown={(e) => e.stopPropagation()}
                    className="h-3 w-3 shrink-0 cursor-pointer accent-primary"
                    aria-label={`Select ${c.name}`}
                  />
                )}
                <Icon
                  className="h-3.5 w-3.5 shrink-0"
                  style={{ color: brandColor(brand) }}
                  aria-hidden="true"
                />
                <span className="min-w-0 flex-1 truncate font-mono text-[11px]">{c.name}</span>
                {/* The tier travels with the name. Which secret you are about to
                    open and how hard it is guarded are one question, and this
                    rail answered only half of it. */}
                {c.tier !== undefined && (
                  <CredentialTierBadge level={c.tier} serverLabel={c.tierLabel} />
                )}
              </SidebarRow>
            )
          })}
          {credentials.length === 0 && (
            <p className="px-3 py-2 text-[11px] text-muted-foreground/60">
              Nothing matches these filters.
            </p>
          )}
        </div>
      </div>
    </div>
  )
}

/**
 * The brands actually inside a category, as their own marks.
 *
 * Up to three, overlapped, commonest first — enough to recognise the group
 * without turning the row into a logo wall. Showing only the top one would say
 * "Source control" beside the GitHub mark while GitLab sits in the same bucket,
 * which reads as a claim about what the row selects rather than what it holds.
 */
function CategoryMarks({ providers }: { providers?: string[] }) {
  const shown = (providers ?? []).slice(0, 3)
  if (shown.length === 0) {
    return <Shapes className="h-3.5 w-3.5 shrink-0 opacity-60" aria-hidden="true" />
  }
  return (
    <span className="flex shrink-0 items-center" aria-hidden="true">
      {shown.map((provider, i) => {
        const brand = getBrand(provider)
        const Icon = brand.Icon
        return (
          <Icon
            key={`${provider}-${i}`}
            className={cn("h-3.5 w-3.5 shrink-0", i > 0 && "-ml-1.5")}
            style={{ color: brandColor(brand) }}
          />
        )
      })}
    </span>
  )
}

/**
 * A scope row's own mark: the workspace, or the crew's own tile.
 *
 * A crew we hold no record of still gets a row — hiding it would hide its
 * credentials — so it falls back to the generic stack glyph rather than to a
 * crew tile that would invent an icon and a colour for it.
 */
function ScopeMark({
  value,
  crews,
}: {
  value: string
  crews: Record<string, CredentialCrewRef>
}) {
  if (value === "WORKSPACE") {
    return <Building2 className="h-3.5 w-3.5 shrink-0 text-muted-foreground/70" aria-hidden="true" />
  }
  const crew = value.startsWith("crew:") ? crews[value.slice("crew:".length)] : undefined
  if (!crew) {
    return <Layers className="h-3.5 w-3.5 shrink-0 opacity-60" aria-hidden="true" />
  }
  return (
    <CrewIcon
      // getCrewIconDef resolves an unknown name to the default glyph, so an
      // empty string is the "use the fallback" value rather than a crash.
      icon={crew.icon ?? ""}
      color={crew.color ?? undefined}
      size="sm"
      className="!h-4 !w-4 !rounded shrink-0"
    />
  )
}
