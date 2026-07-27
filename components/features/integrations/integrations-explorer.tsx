"use client"

import * as React from "react"
import { Bell, Bot, Globe, Mail, MessageSquare, Siren, Smartphone } from "lucide-react"
import type { LucideIcon } from "lucide-react"

import { cn } from "@/lib/utils"
import {
  SidebarActiveChip,
  SidebarActiveChips,
  SidebarCollapseButton,
  SidebarRow,
  SidebarSearch,
  SidebarSection,
  SidebarToolbar,
} from "@/components/layout/sidebar-kit"
import {
  KIND_LABEL,
  STATUS_LABEL,
  type ConnectionFilters,
  type ConnectionKind,
  type ConnectionRow,
  type ConnectionStatus,
} from "./connection-model"

/**
 * The Integrations sidebar — the same explorer shape every other faceted page
 * uses (RoutinesExplorer, the /issues rail): toolbar with search + collapse,
 * then sections of counted rows.
 *
 * The counts are computed on the rows the OTHER facets already narrowed to, so
 * a facet never offers a bucket that would come back empty. The one exception
 * is the facet being counted itself — Kind counts ignore the active Kind, or
 * every kind but the selected one would read 0 and the list would look broken.
 */

const KIND_ICON: Record<ConnectionKind, LucideIcon> = {
  chat: MessageSquare,
  push: Smartphone,
  incident: Siren,
  email: Mail,
  webhook: Globe,
  tools: Bot,
}

const STATUS_DOT: Record<ConnectionStatus, string> = {
  delivering: "bg-emerald-400",
  failing: "bg-red-400",
  never_used: "bg-amber-400",
  disabled: "bg-muted-foreground/40",
  unknown: "bg-sky-400",
}

const KIND_ORDER: ConnectionKind[] = ["chat", "push", "incident", "email", "webhook", "tools"]
const STATUS_ORDER: ConnectionStatus[] = [
  "delivering",
  "failing",
  "never_used",
  "unknown",
  "disabled",
]

interface IntegrationsExplorerProps {
  /** Every row, before search and before facets. */
  rows: ConnectionRow[]
  search: string
  onSearchChange: (v: string) => void
  filters: ConnectionFilters
  onFiltersChange: (f: ConnectionFilters) => void
  onToggleCollapse: () => void
  /** Placeholder reflects the active tab so search reads as contextual. */
  searchPlaceholder?: string
}

export function IntegrationsExplorer({
  rows,
  search,
  onSearchChange,
  filters,
  onFiltersChange,
  onToggleCollapse,
  searchPlaceholder = "Search connections, services…",
}: IntegrationsExplorerProps) {
  // Count a facet's buckets against the rows the OTHER facets allow, so the
  // numbers answer "what would I get if I clicked this" rather than "how many
  // exist somewhere".
  const countBy = React.useCallback(
    <K extends string>(
      ignore: keyof ConnectionFilters,
      key: (r: ConnectionRow) => K,
    ): Record<K, number> => {
      const out = {} as Record<K, number>
      for (const r of rows) {
        if (ignore !== "kind" && filters.kind !== "all" && r.kind !== filters.kind) continue
        if (ignore !== "status" && filters.status !== "all" && r.status !== filters.status) continue
        if (ignore !== "scope" && filters.scope !== "all" && r.scope !== filters.scope) continue
        if (ignore !== "provider" && filters.provider && r.provider !== filters.provider) continue
        const k = key(r)
        out[k] = (out[k] ?? 0) + 1
      }
      return out
    },
    [rows, filters],
  )

  const kindCounts = countBy("kind", (r) => r.kind)
  const statusCounts = countBy("status", (r) => r.status)
  const scopeCounts = countBy("scope", (r) => r.scope)
  const providerCounts = countBy("provider", (r) => r.provider)
  const providerLabels = React.useMemo(() => {
    const m = new Map<string, string>()
    for (const r of rows) m.set(r.provider, r.providerLabel)
    return m
  }, [rows])

  const set = (patch: Partial<ConnectionFilters>) => onFiltersChange({ ...filters, ...patch })

  const totalShown = Object.values(kindCounts).reduce((a: number, b) => a + (b as number), 0)

  const kinds = KIND_ORDER.filter((k) => (kindCounts[k] ?? 0) > 0 || filters.kind === k)
  const statuses = STATUS_ORDER.filter((s) => (statusCounts[s] ?? 0) > 0 || filters.status === s)
  const providers = [...providerLabels.keys()]
    .filter((p) => (providerCounts[p] ?? 0) > 0 || filters.provider === p)
    .sort((a, b) => (providerCounts[b] ?? 0) - (providerCounts[a] ?? 0) || a.localeCompare(b))

  return (
    <div className="flex h-full flex-col">
      <SidebarToolbar>
        <SidebarSearch
          value={search}
          onValueChange={onSearchChange}
          placeholder={searchPlaceholder}
          aria-label="Search connections and services"
        />
        <SidebarCollapseButton collapsed={false} onToggle={onToggleCollapse} />
      </SidebarToolbar>

      <SidebarActiveChips>
        {filters.kind !== "all" && (
          <SidebarActiveChip onRemove={() => set({ kind: "all" })}>
            {KIND_LABEL[filters.kind]}
          </SidebarActiveChip>
        )}
        {filters.status !== "all" && (
          <SidebarActiveChip onRemove={() => set({ status: "all" })}>
            {STATUS_LABEL[filters.status]}
          </SidebarActiveChip>
        )}
        {filters.scope !== "all" && (
          <SidebarActiveChip onRemove={() => set({ scope: "all" })}>
            {filters.scope === "workspace" ? "Workspace" : "Personal"}
          </SidebarActiveChip>
        )}
        {filters.provider && (
          <SidebarActiveChip onRemove={() => set({ provider: null })}>
            {providerLabels.get(filters.provider) ?? filters.provider}
          </SidebarActiveChip>
        )}
      </SidebarActiveChips>

      <div className="min-h-0 flex-1 overflow-y-auto pb-4">
        <SidebarSection label="Kind" count={kinds.length}>
          <FacetRow
            icon={Bell}
            label="All connections"
            count={totalShown}
            selected={filters.kind === "all"}
            onSelect={() => set({ kind: "all" })}
          />
          {kinds.map((k) => (
            <FacetRow
              key={k}
              icon={KIND_ICON[k]}
              label={KIND_LABEL[k]}
              count={kindCounts[k] ?? 0}
              selected={filters.kind === k}
              onSelect={() => set({ kind: filters.kind === k ? "all" : k })}
            />
          ))}
        </SidebarSection>

        {statuses.length > 0 && (
          <SidebarSection label="Status" count={statuses.length}>
            {statuses.map((s) => (
              <FacetRow
                key={s}
                dot={STATUS_DOT[s]}
                label={STATUS_LABEL[s]}
                count={statusCounts[s] ?? 0}
                selected={filters.status === s}
                onSelect={() => set({ status: filters.status === s ? "all" : s })}
              />
            ))}
          </SidebarSection>
        )}

        <SidebarSection label="Scope">
          <FacetRow
            label="Workspace"
            hint="Shared — ADMIN or OWNER"
            count={scopeCounts.workspace ?? 0}
            selected={filters.scope === "workspace"}
            onSelect={() => set({ scope: filters.scope === "workspace" ? "all" : "workspace" })}
          />
          <FacetRow
            label="Personal"
            hint="Yours — self-service"
            count={scopeCounts.personal ?? 0}
            selected={filters.scope === "personal"}
            onSelect={() => set({ scope: filters.scope === "personal" ? "all" : "personal" })}
          />
        </SidebarSection>

        {providers.length > 0 && (
          <SidebarSection label="Service" count={providers.length}>
            {providers.map((p) => (
              <FacetRow
                key={p}
                label={providerLabels.get(p) ?? p}
                count={providerCounts[p] ?? 0}
                selected={filters.provider === p}
                onSelect={() => set({ provider: filters.provider === p ? null : p })}
              />
            ))}
          </SidebarSection>
        )}

        {rows.length === 0 && (
          <p className="px-3 py-4 text-[11px] leading-relaxed text-muted-foreground">
            Nothing connected yet. Open <span className="text-foreground/70">Catalog</span> to see
            every service this instance can deliver to.
          </p>
        )}
      </div>
    </div>
  )
}

function FacetRow({
  icon: Icon,
  dot,
  label,
  hint,
  count,
  selected,
  onSelect,
}: {
  icon?: LucideIcon
  dot?: string
  label: string
  hint?: string
  count: number
  selected: boolean
  onSelect: () => void
}) {
  return (
    <SidebarRow selected={selected} onSelect={onSelect}>
      {dot ? (
        <span className={cn("h-1.5 w-1.5 shrink-0 rounded-full", dot)} aria-hidden="true" />
      ) : Icon ? (
        <Icon className="h-3 w-3 shrink-0 text-muted-foreground/70" aria-hidden="true" />
      ) : null}
      <span className="min-w-0 flex-1 truncate" title={hint}>
        {label}
      </span>
      <span className="shrink-0 tabular-nums text-[10px] text-muted-foreground/60">{count}</span>
    </SidebarRow>
  )
}
