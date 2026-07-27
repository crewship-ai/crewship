"use client"

import * as React from "react"
import { AnimatePresence, motion } from "motion/react"
import type { LucideIcon } from "lucide-react"

import { cn } from "@/lib/utils"
import {
  SidebarActiveChip,
  SidebarActiveChips,
  SidebarCollapseButton,
  SidebarFilterButton,
  SidebarRow,
  SidebarSearch,
  SidebarSection,
  SidebarToolbar,
} from "@/components/layout/sidebar-kit"
import { ProviderMark } from "./provider-marks"

/**
 * The left panel for every tab on this page.
 *
 * One component on purpose. The page previously had two explorers with the
 * same job and different habits — one listed its facets open in the rail, the
 * other did too but with different row markup, and neither used the Filter
 * popover that /issues and /routines use. "Left bars everywhere, but the same
 * logic everywhere" is the requirement; two near-identical implementations is
 * how that stops being true a month later.
 *
 * Shape, matching the rest of the app:
 *   toolbar   [🔍 search] [⧩ Filter (n)] [⇤]
 *   chips     active filters, removable
 *   sections  the views this tab has, with counts
 */

export interface ExplorerSection<K extends string> {
  key: K
  label: string
  icon: LucideIcon
  /** Hover text — what this view answers. */
  hint?: string
  count?: React.ReactNode
}

export interface FacetOption {
  value: string
  label: string
  count: number
  /** Provider key — renders that service's brand mark instead of a dot. */
  mark?: string
  /** Tailwind background class for a status dot. */
  dot?: string
}

/**
 * One thing in the list under the sections — a connection, a connected
 * account. The panel lists them for the same reason /routines lists routines:
 * a rail that only holds section links makes you open a section to find out
 * what is in it, and then the list you wanted is somewhere else on screen.
 */
export interface ExplorerItem {
  id: string
  label: string
  /** Second line: provider, user, whatever identifies this one. */
  sublabel?: string
  /** Provider key — renders that service's brand mark. */
  mark?: string
  /** Tailwind background class for a status dot. */
  dot?: string
}

export interface FacetGroup {
  key: string
  label: string
  options: FacetOption[]
  selected: string | null
  onSelect: (value: string | null) => void
}

interface ExplorerProps<K extends string> {
  sections: ExplorerSection<K>[]
  sectionsLabel: string
  section: K
  onSectionChange: (key: K) => void

  search: string
  onSearchChange: (value: string) => void
  searchPlaceholder: string
  searchAriaLabel: string

  facets: FacetGroup[]
  onClearFilters: () => void

  /** The things this tab holds, listed under the sections. */
  items: ExplorerItem[]
  itemsLabel: string
  selectedItemId: string | null
  onItemSelect: (id: string | null) => void
  /** Shown in place of the list when it is empty. */
  itemsEmpty?: React.ReactNode

  onToggleCollapse: () => void
  /** Rendered at the very bottom — instance-level controls, usually. */
  footer?: React.ReactNode
}

const POPOVER_ANIM = {
  initial: { opacity: 0, scale: 0.97, y: -4 },
  animate: { opacity: 1, scale: 1, y: 0 },
  exit: { opacity: 0, scale: 0.97, y: -4 },
  transition: { duration: 0.12 },
}

export function IntegrationsExplorer<K extends string>({
  sections,
  sectionsLabel,
  section,
  onSectionChange,
  search,
  onSearchChange,
  searchPlaceholder,
  searchAriaLabel,
  facets,
  onClearFilters,
  items,
  itemsLabel,
  selectedItemId,
  onItemSelect,
  itemsEmpty,
  onToggleCollapse,
  footer,
}: ExplorerProps<K>) {
  const [filterOpen, setFilterOpen] = React.useState(false)

  const activeCount = facets.filter((f) => f.selected !== null).length
  // A facet with nothing to choose from is a dead menu section; drop it rather
  // than render a heading over an empty list.
  const usableFacets = facets.filter((f) => f.options.length > 0 || f.selected !== null)

  return (
    <div className="flex h-full flex-col">
      <SidebarToolbar>
        <SidebarSearch
          value={search}
          onValueChange={onSearchChange}
          placeholder={searchPlaceholder}
          aria-label={searchAriaLabel}
        />
        {usableFacets.length > 0 && (
          <div className="relative shrink-0">
            <SidebarFilterButton
              activeCount={activeCount}
              aria-expanded={filterOpen}
              onClick={() => setFilterOpen((v) => !v)}
            />
            <AnimatePresence>
              {filterOpen && (
                <>
                  {/* Click-away catcher, same as the /issues filter. */}
                  <div className="fixed inset-0 z-40" onClick={() => setFilterOpen(false)} />
                  <motion.div
                    {...POPOVER_ANIM}
                    className={cn(
                      "absolute right-0 top-9 z-50 max-h-[340px] min-w-[210px] overflow-y-auto",
                      "rounded-lg border border-white/[0.1] bg-card py-1 shadow-xl",
                    )}
                  >
                    {activeCount > 0 && (
                      <button
                        type="button"
                        onClick={() => {
                          onClearFilters()
                          setFilterOpen(false)
                        }}
                        className="w-full px-3 py-1.5 text-left text-xs text-primary-hover hover:bg-white/[0.06]"
                      >
                        Clear all filters
                      </button>
                    )}
                    {usableFacets.map((group) => (
                      <div key={group.key}>
                        <div className="px-3 py-1 text-[9px] font-semibold uppercase tracking-wider text-foreground/40">
                          {group.label}
                        </div>
                        {group.options.map((opt) => (
                          <button
                            key={opt.value}
                            type="button"
                            onClick={() => {
                              group.onSelect(group.selected === opt.value ? null : opt.value)
                              setFilterOpen(false)
                            }}
                            className={cn(
                              "flex w-full items-center gap-2 px-3 py-1.5 text-left text-xs hover:bg-white/[0.06]",
                              group.selected === opt.value
                                ? "text-primary-hover"
                                : "text-muted-foreground/80",
                            )}
                          >
                            {opt.mark ? (
                              <ProviderMark
                                provider={opt.mark}
                                label={opt.label}
                                className="h-4 w-4 rounded-[4px]"
                              />
                            ) : opt.dot ? (
                              <span
                                className={cn("h-1.5 w-1.5 shrink-0 rounded-full", opt.dot)}
                                aria-hidden="true"
                              />
                            ) : null}
                            <span className="min-w-0 flex-1 truncate">{opt.label}</span>
                            <span className="shrink-0 tabular-nums text-[10px] text-muted-foreground/50">
                              {opt.count}
                            </span>
                          </button>
                        ))}
                      </div>
                    ))}
                  </motion.div>
                </>
              )}
            </AnimatePresence>
          </div>
        )}
        <SidebarCollapseButton collapsed={false} onToggle={onToggleCollapse} />
      </SidebarToolbar>

      <SidebarActiveChips>
        {facets.map((group) => {
          if (group.selected === null) return null
          const opt = group.options.find((o) => o.value === group.selected)
          return (
            <SidebarActiveChip key={group.key} onRemove={() => group.onSelect(null)}>
              {opt?.label ?? group.selected}
            </SidebarActiveChip>
          )
        })}
      </SidebarActiveChips>

      <div className="min-h-0 flex-1 overflow-y-auto pb-4">
        <SidebarSection label={sectionsLabel} count={sections.length}>
          {sections.map((s) => {
            const Icon = s.icon
            return (
              <SidebarRow
                key={s.key}
                selected={section === s.key}
                onSelect={() => onSectionChange(s.key)}
              >
                <Icon className="h-3 w-3 shrink-0 text-muted-foreground/70" aria-hidden="true" />
                <span className="min-w-0 flex-1 truncate" title={s.hint}>
                  {s.label}
                </span>
                {s.count != null && (
                  <span className="shrink-0 tabular-nums text-[10px] text-muted-foreground/60">
                    {s.count}
                  </span>
                )}
              </SidebarRow>
            )
          })}
        </SidebarSection>

        <SidebarSection label={itemsLabel} count={items.length}>
          {items.length === 0
            ? itemsEmpty
            : items.map((item) => (
                <SidebarRow
                  key={item.id}
                  selected={selectedItemId === item.id}
                  onSelect={() => onItemSelect(selectedItemId === item.id ? null : item.id)}
                >
                  {item.mark ? (
                    <ProviderMark
                      provider={item.mark}
                      label={item.label}
                      className="h-4 w-4 rounded-[4px]"
                    />
                  ) : item.dot ? (
                    <span
                      className={cn("h-1.5 w-1.5 shrink-0 rounded-full", item.dot)}
                      aria-hidden="true"
                    />
                  ) : null}
                  <span className="min-w-0 flex-1">
                    <span className="block truncate">{item.label}</span>
                    {item.sublabel && (
                      <span className="block truncate font-mono text-[10px] text-muted-foreground/60">
                        {item.sublabel}
                      </span>
                    )}
                  </span>
                  {item.dot && item.mark && (
                    <span
                      className={cn("h-1.5 w-1.5 shrink-0 rounded-full", item.dot)}
                      aria-hidden="true"
                    />
                  )}
                </SidebarRow>
              ))}
        </SidebarSection>

        {footer}
      </div>
    </div>
  )
}
