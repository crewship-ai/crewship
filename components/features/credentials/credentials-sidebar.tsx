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
 * STATUS stays in the rail rather than moving into the dropdown. It is the
 * question asked most often about a vault ("what is broken?"), it is
 * single-select, and its three rows are bounded — which Category and Scope,
 * both of which grow with the workspace, are not.
 *
 * Every count comes from the same functions the list filters with
 * (`lib/credentials/facets.ts`), so a count can never disagree with what
 * clicking it selects. A facet with nothing behind it is omitted rather than
 * shown as zero: a row that filters to an empty list is a control that
 * appears to work and does not, which costs more trust than the missing row
 * costs discoverability.
 */

import * as React from "react"
import { AlertTriangle, Check, Hash, Layers, PackageX, Shapes } from "lucide-react"
import { AnimatePresence, motion } from "motion/react"

import {
  SidebarCollapseButton,
  SidebarFilterButton,
  SidebarRow,
  SidebarSearch,
  SidebarSection,
  SidebarToolbar,
} from "@/components/layout/sidebar-kit"
import { getBrand, brandColor } from "@/lib/credential-providers/registry"
import type { CredentialFacetOption, CredentialFilters } from "@/lib/credentials/facets"
import { EMPTY_CREDENTIAL_FILTERS } from "@/lib/credentials/facets"
import { cn } from "@/lib/utils"

/** What the rail needs from a credential. Deliberately narrow: the rail
 *  renders an icon, a name and a selection state, and nothing in this shape
 *  should tempt a caller into routing a secret through it. */
export interface SidebarCredential {
  id: string
  name: string
  provider: string
  type: string
}

export interface CredentialsSidebarProps {
  filters: CredentialFilters
  onFiltersChange: (next: CredentialFilters) => void
  counts: { all: number; attention: number; missingTool: number }
  categories: CredentialFacetOption[]
  scopes: CredentialFacetOption[]
  tags: string[]
  onToggleCollapse: () => void
  /** The credentials the current filters leave — the rail's body. */
  credentials?: SidebarCredential[]
  selectedCredentialId?: string | null
  onSelectCredential?: (id: string) => void
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
  categories,
  scopes,
  tags,
  onToggleCollapse,
  credentials = [],
  selectedCredentialId = null,
  onSelectCredential,
}: CredentialsSidebarProps) {
  const [filterOpen, setFilterOpen] = React.useState(false)
  const [statusOpen, setStatusOpen] = React.useState(true)

  const set = (patch: Partial<CredentialFilters>) => onFiltersChange({ ...filters, ...patch })

  // Status is excluded on purpose: it has its own always-visible section, so
  // counting it here would badge the Filter button for a choice already on
  // screen. The badge exists to explain a short list; a status selection
  // explains itself.
  const activeFilterCount =
    (filters.category ? 1 : 0) + (filters.scope ? 1 : 0) + (filters.tag ? 1 : 0)

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
        <div className="relative shrink-0">
          <SidebarFilterButton
            activeCount={activeFilterCount}
            aria-expanded={filterOpen}
            onClick={() => setFilterOpen(!filterOpen)}
          />
          <AnimatePresence>
            {filterOpen && (
              <>
                <div className="fixed inset-0 z-40" onClick={() => setFilterOpen(false)} />
                <motion.div
                  {...dropdownAnim}
                  className="absolute right-0 top-9 z-50 max-h-[360px] min-w-[210px] overflow-y-auto rounded-lg border border-white/[0.1] bg-card py-1 shadow-xl"
                >
                  <FacetGroup
                    label="Category"
                    icon={Shapes}
                    options={categories}
                    selected={filters.category}
                    onSelect={(value) => {
                      set({ category: value })
                      setFilterOpen(false)
                    }}
                  />
                  <FacetGroup
                    label="Scope"
                    icon={Layers}
                    options={scopes}
                    selected={filters.scope}
                    onSelect={(value) => {
                      set({ scope: value })
                      setFilterOpen(false)
                    }}
                  />
                  {tags.length > 0 && (
                    <FacetGroup
                      label="Tag"
                      icon={Hash}
                      options={tags.map((t) => ({ value: t, label: t, count: 0 }))}
                      selected={filters.tag}
                      hideCounts
                      onSelect={(value) => {
                        set({ tag: value })
                        setFilterOpen(false)
                      }}
                    />
                  )}
                  {activeFilterCount > 0 && (
                    <>
                      <div className="mt-1 border-t border-white/[0.06]" />
                      <button
                        type="button"
                        onClick={() => {
                          // Search and status survive: a user who typed a
                          // query and then clears the facets is narrowing,
                          // not starting over.
                          onFiltersChange({
                            ...EMPTY_CREDENTIAL_FILTERS,
                            search: filters.search,
                            status: filters.status,
                          })
                          setFilterOpen(false)
                        }}
                        className="w-full px-3 py-1.5 text-left text-xs text-primary-hover hover:bg-white/[0.06]"
                      >
                        Clear filters
                      </button>
                    </>
                  )}
                </motion.div>
              </>
            )}
          </AnimatePresence>
        </div>
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

      <div className="flex min-h-0 flex-1 flex-col">
        <SidebarSection label="Credentials" count={credentials.length} />
        <div className="min-h-0 flex-1 overflow-y-auto pb-1">
          {credentials.map((c) => {
            const brand = getBrand(c.provider)
            const Icon = brand.Icon
            return (
              <SidebarRow
                key={c.id}
                selected={selectedCredentialId === c.id}
                onSelect={() => onSelectCredential?.(c.id)}
              >
                <Icon
                  className="h-3.5 w-3.5 shrink-0"
                  style={{ color: brandColor(brand) }}
                  aria-hidden="true"
                />
                <span className="min-w-0 flex-1 truncate font-mono text-[11px]">{c.name}</span>
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

/** One labelled group inside the Filter dropdown. Clicking the selected
 *  option again clears it, so the dropdown needs no separate "any" row. */
function FacetGroup({
  label,
  icon: Icon,
  options,
  selected,
  onSelect,
  hideCounts = false,
}: {
  label: string
  icon: React.ComponentType<{ className?: string }>
  options: CredentialFacetOption[]
  selected: string | null
  onSelect: (value: string | null) => void
  hideCounts?: boolean
}) {
  if (options.length === 0) return null
  return (
    <>
      <div className="px-3 py-1 text-[9px] font-semibold uppercase tracking-wider text-muted-foreground-soft">
        {label}
      </div>
      {options.map((opt) => (
        <button
          key={opt.value}
          type="button"
          onClick={() => onSelect(selected === opt.value ? null : opt.value)}
          className={cn(
            "flex w-full items-center gap-2 px-3 py-1.5 text-left text-xs hover:bg-white/[0.06]",
            selected === opt.value ? "text-primary-hover" : "text-muted-foreground/80",
          )}
        >
          <Icon className="h-3.5 w-3.5 shrink-0 opacity-60" aria-hidden="true" />
          <span className="min-w-0 flex-1 truncate">{opt.label}</span>
          {!hideCounts && (
            <span className="shrink-0 tabular-nums text-[10px] text-muted-foreground-soft">
              {opt.count}
            </span>
          )}
        </button>
      ))}
    </>
  )
}
