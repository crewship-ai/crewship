"use client"

/**
 * The /credentials left rail — the house explorer shape (sidebar-kit), the
 * same one Integrations, Settings and Routines use.
 *
 * Wireframe screen 1 groups it Status → Category → Scope, and that order is
 * the point: the first question about a vault is "what is broken", not "what
 * is in it". Every row carries its own count and comes from the same
 * functions the list filters with (`lib/credentials/facets.ts`), so a count
 * can never disagree with what clicking it selects.
 *
 * A facet with nothing behind it is omitted rather than shown as zero. A row
 * that filters to an empty list is a control that appears to work and does
 * not, which costs more trust than the missing row costs discoverability.
 */

import * as React from "react"
import { AlertTriangle, Check, Layers, PackageX } from "lucide-react"

import {
  SidebarCollapseButton,
  SidebarRow,
  SidebarSearch,
  SidebarSection,
  SidebarToolbar,
} from "@/components/layout/sidebar-kit"
import type { CredentialFacetOption, CredentialFilters } from "@/lib/credentials/facets"
import { EMPTY_CREDENTIAL_FILTERS } from "@/lib/credentials/facets"
import { cn } from "@/lib/utils"

export interface CredentialsSidebarProps {
  filters: CredentialFilters
  onFiltersChange: (next: CredentialFilters) => void
  counts: { all: number; attention: number; missingTool: number }
  categories: CredentialFacetOption[]
  scopes: CredentialFacetOption[]
  tags: string[]
  onToggleCollapse: () => void
}

export function CredentialsSidebar({
  filters,
  onFiltersChange,
  counts,
  categories,
  scopes,
  tags,
  onToggleCollapse,
}: CredentialsSidebarProps) {
  const set = (patch: Partial<CredentialFilters>) => onFiltersChange({ ...filters, ...patch })

  // The search box is deliberately NOT part of "clear filters": a user who
  // typed a query and then clears the facets is narrowing, not restarting.
  const facetsActive =
    filters.status !== "all" || filters.category !== null || filters.scope !== null || filters.tag !== null

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
        <SidebarSearch
          value={filters.search}
          onValueChange={(v) => set({ search: v })}
          placeholder="Search a secret or tool…"
          aria-label="Search credentials"
        />
        <SidebarCollapseButton collapsed={false} onToggle={onToggleCollapse} />
      </SidebarToolbar>

      {facetsActive && (
        <div className="px-2 pb-2">
          <button
            type="button"
            onClick={() => onFiltersChange({ ...EMPTY_CREDENTIAL_FILTERS, search: filters.search })}
            className="text-[11px] text-primary-hover hover:underline"
          >
            Clear filters
          </button>
        </div>
      )}

      <div className="min-h-0 flex-1 overflow-y-auto pb-4">
        <SidebarSection label="Status">
          {statusRows
            .filter((row) => row.always || row.count > 0)
            .map((row) => {
              const Icon = row.icon
              const selected = filters.status === row.key
              return (
                <SidebarRow key={row.key} selected={selected} onSelect={() => set({ status: row.key })}>
                  <Icon className={cn("h-3 w-3 shrink-0 text-muted-foreground/70", row.tone)} aria-hidden="true" />
                  <span className="min-w-0 flex-1 truncate">{row.label}</span>
                  <span className="shrink-0 tabular-nums text-[10px] text-muted-foreground/60">{row.count}</span>
                </SidebarRow>
              )
            })}
        </SidebarSection>

        <FacetSection
          label="Category"
          options={categories}
          selected={filters.category}
          onSelect={(value) => set({ category: value })}
        />

        <FacetSection
          label="Scope"
          options={scopes}
          selected={filters.scope}
          onSelect={(value) => set({ scope: value })}
          icon={Layers}
        />

        {tags.length > 0 && (
          <SidebarSection label="Tag" count={tags.length}>
            {tags.map((tag) => (
              <SidebarRow
                key={tag}
                selected={filters.tag === tag}
                onSelect={() => set({ tag: filters.tag === tag ? null : tag })}
              >
                <span className="min-w-0 flex-1 truncate font-mono text-[11px]">{tag}</span>
              </SidebarRow>
            ))}
          </SidebarSection>
        )}
      </div>
    </div>
  )
}

function FacetSection({
  label,
  options,
  selected,
  onSelect,
  icon: Icon,
}: {
  label: string
  options: CredentialFacetOption[]
  selected: string | null
  onSelect: (value: string | null) => void
  icon?: React.ComponentType<{ className?: string }>
}) {
  if (options.length === 0) return null
  return (
    <SidebarSection label={label} count={options.length}>
      {options.map((opt) => (
        <SidebarRow
          key={opt.value}
          selected={selected === opt.value}
          onSelect={() => onSelect(selected === opt.value ? null : opt.value)}
        >
          {Icon && <Icon className="h-3 w-3 shrink-0 text-muted-foreground/70" aria-hidden="true" />}
          <span className="min-w-0 flex-1 truncate">{opt.label}</span>
          <span className="shrink-0 tabular-nums text-[10px] text-muted-foreground/60">{opt.count}</span>
        </SidebarRow>
      ))}
    </SidebarSection>
  )
}
