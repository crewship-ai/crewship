"use client"

/**
 * The Vault's secondary panel (Harbor 2): the spaces on top (Secrets, AI
 * providers), then the Secrets views with their counts. The toolbar's Filter
 * button opens the facets inside the panel, under the toolbar (kit COM-01,
 * v67 panel() → pk-fx); while it is closed, a removable chip shows each facet
 * in force.
 *
 * Folders, External vaults and Workspace keys are in the design but have no
 * backend yet; they are not drawn.
 */

import * as React from "react"
import type { LucideIcon } from "lucide-react"
import { AlertTriangle, Clock, CircleDashed, KeyRound, PackageX, Sparkles } from "lucide-react"

import {
  SidebarActiveChip,
  SidebarActiveChips,
  SidebarFilterButton,
  SidebarRow,
  SidebarSearch,
  SidebarSection,
  SidebarToolbar,
} from "@/components/layout/sidebar-kit"
import { SidebarFacetBlock, SidebarInlineFacet, useSidebarFacets } from "@/components/layout/sidebar-facets"
import { AgentAvatar } from "@/components/ui/agent-avatar"
import { CrewIcon } from "@/components/ui/crew-icon"
import type { CredentialCrewRef } from "@/hooks/use-credential-readiness"
import { getBrand, brandColor } from "@/lib/credential-providers/registry"
import {
  CLASSIFICATIONS,
  EMPTY_CREDENTIAL_FILTERS,
  type CredentialFacetOption,
  type CredentialFilters,
  type CredentialStatusFilter,
} from "@/lib/credentials/facets"
import { activeFacetCount, type VaultSpace } from "@/lib/credentials/vault-url-state"

export const VIEW_LABEL: Record<CredentialStatusFilter, string> = {
  all: "All secrets",
  attention: "Needs attention",
  recent: "Recently used",
  unused: "Not used",
  "missing-tool": "Missing tool",
  rejected: "Rejected",
  expiring: "Expire within 14 days",
}

const VIEW_ICON: Record<string, LucideIcon> = {
  all: KeyRound,
  attention: AlertTriangle,
  recent: Clock,
  unused: CircleDashed,
  "missing-tool": PackageX,
}

const CLASS_LABEL: Record<string, string> = { STANDARD: "Standard", RESTRICTED: "Restricted", SEALED: "Sealed" }

const meta = (n: number | undefined) => (
  <span className="ml-auto text-label tabular-nums text-muted-foreground-soft">{n ?? 0}</span>
)

/** The space switch, shared by both spaces' panels. */
export function VaultSpaces({
  space,
  counts,
  showProviders,
  onSpace,
}: {
  space: VaultSpace
  counts: { secrets: number; providers: number }
  showProviders: boolean
  onSpace: (s: VaultSpace) => void
}) {
  if (!showProviders) return null
  return (
    <nav aria-label="Vault spaces" className="border-b border-foreground/[0.06] pb-1.5 pt-1.5">
      <SidebarRow selected={space === "secrets"} onSelect={() => onSpace("secrets")} aria-current={space === "secrets" ? "page" : undefined}>
        <KeyRound className="size-3.5 shrink-0 text-muted-foreground" aria-hidden />
        <span className="min-w-0 flex-1 truncate">Secrets</span>
        {meta(counts.secrets)}
      </SidebarRow>
      <SidebarRow selected={space === "providers"} onSelect={() => onSpace("providers")} aria-current={space === "providers" ? "page" : undefined}>
        <Sparkles className="size-3.5 shrink-0 text-muted-foreground" aria-hidden />
        <span className="min-w-0 flex-1 truncate">AI providers</span>
        {meta(counts.providers)}
      </SidebarRow>
    </nav>
  )
}

export interface VaultPanelProps {
  filters: CredentialFilters
  onFiltersChange: (next: CredentialFilters) => void
  viewCounts: Record<"all" | "attention" | "recent" | "unused" | "missing-tool", number>
  shapes: CredentialFacetOption[]
  brands: CredentialFacetOption[]
  crews: CredentialFacetOption[]
  tiers: CredentialFacetOption[]
  classes: CredentialFacetOption[]
  scopes: CredentialFacetOption[]
  tags: CredentialFacetOption[]
  agents: CredentialFacetOption[]
  crewsById: Record<string, CredentialCrewRef>
  spaces: React.ReactNode
}

type ListKey = "shape" | "brand" | "crew" | "classification" | "scope" | "tag" | "agentId"

export function VaultPanel({
  filters,
  onFiltersChange,
  viewCounts,
  shapes,
  brands,
  crews,
  tiers,
  classes,
  scopes,
  tags,
  agents,
  crewsById,
  spaces,
}: VaultPanelProps) {
  const facets = useSidebarFacets()
  const set = (patch: Partial<CredentialFilters>) => onFiltersChange({ ...filters, ...patch })
  const toggle = (key: ListKey, value: string) => {
    const list = filters[key]
    set({ [key]: list.includes(value) ? list.filter((v) => v !== value) : [...list, value] } as Partial<CredentialFilters>)
  }
  const label = (opts: CredentialFacetOption[], v: string) => opts.find((o) => o.value === v)?.label ?? v

  const crewMark = (id: string) => {
    const crew = crewsById[id]
    return <CrewIcon icon={crew?.icon ?? ""} color={crew?.color ?? undefined} size="sm" className="!size-4 !rounded shrink-0" />
  }

  const groups: { key: ListKey; title: string; reset: string; options: CredentialFacetOption[]; mark?: (o: CredentialFacetOption) => React.ReactNode }[] = [
    { key: "shape", title: "Type", reset: "Any type", options: shapes },
    {
      key: "brand",
      title: "Service",
      reset: "Any service",
      options: brands,
      mark: (o) => {
        const b = getBrand(o.value)
        const Icon = b.Icon
        return <Icon className="size-3.5 shrink-0" style={{ color: brandColor(b) }} aria-hidden />
      },
    },
    { key: "crew", title: "Used by crew", reset: "Any crew", options: crews, mark: (o) => crewMark(o.value) },
    { key: "classification", title: "Classification", reset: "Any classification", options: classes },
    { key: "scope", title: "Scope", reset: "Any scope", options: scopes },
    { key: "tag", title: "Tag", reset: "Any tag", options: tags },
    { key: "agentId", title: "Assigned to", reset: "Any agent", options: agents, mark: (o) => <AgentAvatar seed={o.value} alt="" className="size-4 shrink-0 rounded-full" /> },
  ]

  const views: (keyof VaultPanelProps["viewCounts"])[] = ["all", "attention", "recent", "unused", "missing-tool"]
  const anyFacet = activeFacetCount(filters) > 0
  const selectedView = (v: CredentialStatusFilter) =>
    !anyFacet && (filters.status === v || (v === "attention" && (filters.status === "rejected" || filters.status === "expiring")))

  return (
    <div className="flex h-full min-h-0 flex-col">
      <SidebarToolbar>
        <div className="min-w-0 flex-1" data-vault-search>
          <SidebarSearch
            value={filters.search}
            onValueChange={(v) => set({ search: v })}
            placeholder="Search secrets by name, service or agent…"
            aria-label="Search secrets"
          />
        </div>
        <SidebarFilterButton activeCount={activeFacetCount(filters)} {...facets.triggerProps}>Filter</SidebarFilterButton>
      </SidebarToolbar>

      {/* COM-01: the facets open here, inside the panel under the toolbar. */}
      <SidebarFacetBlock
        {...facets.blockProps}
        label="Filter secrets"
        activeCount={activeFacetCount(filters)}
        onClear={() => onFiltersChange({ ...EMPTY_CREDENTIAL_FILTERS, status: filters.status, search: filters.search })}
      >
        {groups
          .filter((g) => g.options.length > 0)
          .map((g) => (
            <SidebarInlineFacet
              key={g.key}
              label={g.title}
              values={g.options.map((o) => ({ value: o.value, label: o.label, text: o.label, count: o.count, icon: g.mark?.(o) }))}
              selected={filters[g.key]}
              onToggle={(v) => toggle(g.key, v)}
              onClear={() => set({ [g.key]: [] } as Partial<CredentialFilters>)}
            />
          ))}
        {tiers.length > 0 && (
          <SidebarInlineFacet
            label="Keeper tier"
            values={tiers.map((o) => ({ value: o.value, label: o.label, count: o.count }))}
            selected={filters.tier}
            onToggle={(v) => set({ tier: filters.tier === v ? null : v })}
            onClear={() => set({ tier: null })}
          />
        )}
      </SidebarFacetBlock>

      <div className="min-h-0 flex-1 overflow-y-auto pb-2">
        {spaces}
        {!facets.open && <SidebarActiveChips className="pt-2">
          {groups.flatMap((g) =>
            filters[g.key].map((v) => {
              const text = `${g.title}: ${g.key === "classification" ? CLASS_LABEL[v] ?? v : label(g.options, v)}`
              return <SidebarActiveChip key={`${g.key}:${v}`} label={text} onRemove={() => toggle(g.key, v)}>{text}</SidebarActiveChip>
            }),
          )}
          {filters.tier && (
            <SidebarActiveChip label={`Tier: ${label(tiers, filters.tier)}`} onRemove={() => set({ tier: null })}>
              Tier: {label(tiers, filters.tier)}
            </SidebarActiveChip>
          )}
        </SidebarActiveChips>}

        <SidebarSection label="Views" />
        {views
          .filter((v) => v !== "missing-tool" || viewCounts[v] > 0)
          .map((v) => {
            const Icon = VIEW_ICON[v]
            return (
              <SidebarRow
                key={v}
                selected={selectedView(v)}
                aria-current={selectedView(v) ? "page" : undefined}
                onSelect={() => onFiltersChange({ ...EMPTY_CREDENTIAL_FILTERS, status: v, search: filters.search })}
              >
                <Icon className="size-3.5 shrink-0 text-muted-foreground" aria-hidden />
                <span className="min-w-0 flex-1 truncate">{VIEW_LABEL[v]}</span>
                {meta(viewCounts[v])}
              </SidebarRow>
            )
          })}
      </div>
    </div>
  )
}

/** Classification options with counts, every class listed (zero included). */
export function buildClassFacet(rows: { sensitivity?: string | null }[]): CredentialFacetOption[] {
  const counts = new Map<string, number>()
  for (const r of rows) {
    const v = (r.sensitivity ?? "").toUpperCase()
    const k = v === "RESTRICTED" || v === "SEALED" ? v : "STANDARD"
    counts.set(k, (counts.get(k) ?? 0) + 1)
  }
  return CLASSIFICATIONS.map((c) => ({ value: c, label: CLASS_LABEL[c], count: counts.get(c) ?? 0 }))
}
