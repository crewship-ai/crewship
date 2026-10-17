"use client"

/**
 * The Vault's secondary panel in the AI providers space (Harbor 2): the
 * spaces on top, then the views with their counts, the account groups (each
 * opens its own page) and the facets behind the toolbar's Filter button, with
 * a removable chip for each one in force.
 *
 * The space is OWNER/ADMIN only, and the panel says so in one line.
 */

import * as React from "react"
import type { LucideIcon } from "lucide-react"
import { AlertTriangle, CircleDashed, FolderPlus, Layers, Lock, Sparkles, UserRound } from "lucide-react"

import {
  SidebarActiveChip,
  SidebarActiveChips,
  SidebarFacet,
  SidebarFacetOption,
  SidebarFilterPopover,
  SidebarRow,
  SidebarSearch,
  SidebarSection,
  SidebarToolbar,
} from "@/components/layout/sidebar-kit"
import {
  EMPTY_LOGIN_FILTERS,
  type LoginFacetOption,
  type LoginFilters,
  type LoginStatusFilter,
} from "@/lib/credentials/provider-logins"
import { activeLoginFacetCount } from "@/lib/credentials/vault-url-state"
import type { ProviderPool } from "@/hooks/use-provider-pools"
import { LoginBrandMark } from "./provider-login-bits"

export const PROVIDER_VIEW_LABEL: Partial<Record<LoginStatusFilter, string>> = {
  all: "All accounts",
  attention: "Needs attention",
  unassigned: "Not paying for anything",
  mine: "My accounts",
}

const VIEW_ICON: Record<string, LucideIcon> = {
  all: Sparkles,
  attention: AlertTriangle,
  unassigned: CircleDashed,
  mine: UserRound,
}

const meta = (n: number | undefined) => (
  <span className="ml-auto font-mono text-label tabular-nums text-muted-foreground-soft">{n ?? 0}</span>
)

export type ProviderViewCounts = Record<"all" | "attention" | "unassigned" | "mine", number>

type ListKey = "provider" | "mode" | "owner"

export interface ProvidersPanelProps {
  filters: LoginFilters
  onFiltersChange: (next: LoginFilters) => void
  viewCounts: ProviderViewCounts
  providers: LoginFacetOption[]
  modes: LoginFacetOption[]
  owners: LoginFacetOption[]
  /** The account groups on the first page; undefined while they load. */
  pools: ProviderPool[] | undefined
  poolsError?: boolean
  /** The open group's page, if any. */
  selectedPool: string | null
  onOpenPool: (id: string) => void
  onNewPool?: () => void
  spaces: React.ReactNode
}

export function ProvidersPanel({
  filters,
  onFiltersChange,
  viewCounts,
  providers,
  modes,
  owners,
  pools,
  poolsError,
  selectedPool,
  onOpenPool,
  onNewPool,
  spaces,
}: ProvidersPanelProps) {
  const set = (patch: Partial<LoginFilters>) => onFiltersChange({ ...filters, ...patch })
  const toggle = (key: ListKey, value: string) => {
    const list = filters[key]
    set({ [key]: list.includes(value) ? list.filter((v) => v !== value) : [...list, value] } as Partial<LoginFilters>)
  }
  const label = (opts: LoginFacetOption[], v: string) => opts.find((o) => o.value === v)?.label ?? v

  const groups: { key: ListKey; title: string; reset: string; options: LoginFacetOption[]; mark?: (o: LoginFacetOption) => React.ReactNode }[] = [
    { key: "provider", title: "Provider", reset: "Any provider", options: providers, mark: (o) => <LoginBrandMark provider={o.value} size="sm" /> },
    { key: "mode", title: "How it pays", reset: "Any way", options: modes.map((m) => ({ ...m, label: m.value === "api_key" ? "API key · money" : "Subscription · limits" })) },
    { key: "owner", title: "Owner", reset: "Anyone", options: owners },
  ]
  const anyFacet = activeLoginFacetCount(filters) > 0
  const views: (keyof ProviderViewCounts)[] = ["all", "attention", "unassigned", "mine"]
  const selectedView = (v: LoginStatusFilter) => !anyFacet && !selectedPool && filters.status === v

  return (
    <div className="flex h-full min-h-0 flex-col">
      <SidebarToolbar>
        <div className="min-w-0 flex-1">
          <SidebarSearch
            value={filters.search}
            onValueChange={(v) => set({ search: v })}
            placeholder="Search accounts by name, owner or plan…"
            aria-label="Search AI provider accounts"
          />
        </div>
        <SidebarFilterPopover
          label="Filter accounts"
          activeCount={activeLoginFacetCount(filters)}
          panelClassName="w-64 max-h-[min(480px,70dvh)]"
          onClear={() => onFiltersChange({ ...EMPTY_LOGIN_FILTERS, status: filters.status, search: filters.search })}
        >
          {groups
            .filter((g) => g.options.length > 0)
            .map((g, i) => (
              <SidebarFacet key={g.key} label={g.title} resetLabel={g.reset} resetActive={filters[g.key].length === 0} onReset={() => set({ [g.key]: [] } as Partial<LoginFilters>)} first={i === 0}>
                {g.options.slice(0, 12).map((o) => (
                  <SidebarFacetOption key={o.value} active={filters[g.key].includes(o.value)} onToggle={() => toggle(g.key, o.value)}>
                    {g.mark?.(o)}
                    <span className="min-w-0 truncate">{o.label}</span>
                    {meta(o.count)}
                  </SidebarFacetOption>
                ))}
              </SidebarFacet>
            ))}
        </SidebarFilterPopover>
      </SidebarToolbar>

      <div className="min-h-0 flex-1 overflow-y-auto pb-2">
        {spaces}
        <SidebarActiveChips className="pt-2">
          {groups.flatMap((g) =>
            filters[g.key].map((v) => {
              const text = `${g.title}: ${label(g.options, v)}`
              return <SidebarActiveChip key={`${g.key}:${v}`} label={text} onRemove={() => toggle(g.key, v)}>{text}</SidebarActiveChip>
            }),
          )}
        </SidebarActiveChips>

        <SidebarSection label="Views" />
        {views.map((v) => {
          const Icon = VIEW_ICON[v]
          return (
            <SidebarRow
              key={v}
              selected={selectedView(v)}
              aria-current={selectedView(v) ? "page" : undefined}
              onSelect={() => onFiltersChange({ ...EMPTY_LOGIN_FILTERS, status: v, search: filters.search })}
            >
              <Icon className="size-3.5 shrink-0 text-muted-foreground" aria-hidden />
              <span className="min-w-0 flex-1 truncate">{PROVIDER_VIEW_LABEL[v]}</span>
              {meta(viewCounts[v])}
            </SidebarRow>
          )
        })}

        <SidebarSection
          label="Account groups"
          count={pools ? pools.length : undefined}
          actions={onNewPool ? (
            <button type="button" onClick={onNewPool} aria-label="New account group" title="New account group"
              className="kit-tap inline-flex size-6 items-center justify-center rounded text-muted-foreground hover:bg-muted hover:text-foreground">
              <FolderPlus className="size-3.5" aria-hidden />
            </button>
          ) : undefined}
        />
        {poolsError ? (
          <p role="alert" className="mx-3.5 text-label text-warn">Account groups could not be loaded.</p>
        ) : pools === undefined ? (
          <p role="status" className="mx-3.5 text-label text-muted-foreground">Loading…</p>
        ) : pools.length === 0 ? (
          <p className="mx-3.5 text-label text-muted-foreground">No account groups yet.</p>
        ) : (
          pools.map((p) => (
            <SidebarRow key={p.id} selected={selectedPool === p.id} aria-current={selectedPool === p.id ? "page" : undefined} onSelect={() => onOpenPool(p.id)}>
              <Layers className="size-3.5 shrink-0 text-muted-foreground" aria-hidden />
              <span className="min-w-0 flex-1 truncate">{p.name}</span>
              {meta(p.member_count)}
            </SidebarRow>
          ))
        )}

        <p role="note" className="mx-3.5 mt-4 flex items-start gap-1.5 text-label text-muted-foreground">
          <Lock className="mt-0.5 size-3.5 shrink-0" aria-hidden />
          <span>Owners and admins manage AI provider accounts.</span>
        </p>
      </div>
    </div>
  )
}
