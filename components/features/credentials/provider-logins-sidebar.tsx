"use client"

/**
 * The /credentials rail while the Providers tab is showing.
 *
 * Same kit as the secrets rail (`CredentialsSidebar`) and the same rule — every
 * count comes from the functions the list filters with — but a different set
 * of questions, because a seat is asked different things than a secret:
 * "which are paused?", "whose are these?", "does anything pay with a key?".
 * All four facets sit in the rail rather than behind the Filter button: each
 * is bounded (five statuses, five providers, two modes, one owner per person)
 * and each is asked on arrival, which is the test the secrets rail applies to
 * its Status and Tier sections.
 *
 * The rail does NOT list the seats. The Providers pane is a list of cards with
 * quota bars and a status pill — there is too much on each row to repeat it in
 * an 11px column beside it, and the secrets rail's argument ("the rail is the
 * list") was about a table that duplicated the rail, which the pane here is
 * not.
 */

import * as React from "react"
import { AlertTriangle, Check, Clock, CreditCard, KeyRound, LogIn, UserRound, Users } from "lucide-react"

import {
  SidebarCollapseButton,
  SidebarRow,
  SidebarSearch,
  SidebarSection,
  SidebarToolbar,
} from "@/components/layout/sidebar-kit"
import { getBrand, brandColor } from "@/lib/credential-providers/registry"
import {
  UNOWNED,
  type LoginFacetOption,
  type LoginFilters,
  type LoginStatusCounts,
  type LoginStatusFilter,
} from "@/lib/credentials/provider-logins"
import { cn } from "@/lib/utils"

export interface ProviderLoginsSidebarProps {
  filters: LoginFilters
  onFiltersChange: (next: LoginFilters) => void
  counts: LoginStatusCounts
  providers: LoginFacetOption[]
  modes: LoginFacetOption[]
  owners: LoginFacetOption[]
  onToggleCollapse: () => void
}

export function ProviderLoginsSidebar({
  filters, onFiltersChange, counts, providers, modes, owners, onToggleCollapse,
}: ProviderLoginsSidebarProps) {
  const set = (patch: Partial<LoginFilters>) => onFiltersChange({ ...filters, ...patch })
  const toggle = (list: string[], value: string) =>
    list.includes(value) ? list.filter((v) => v !== value) : [...list, value]

  const statusRows: {
    key: LoginStatusFilter
    label: string
    count: number
    icon: React.ComponentType<{ className?: string }>
    tone?: string
  }[] = [
    { key: "all", label: "All", count: counts.all, icon: Check },
    { key: "at_limit", label: "At limit", count: counts.at_limit, icon: AlertTriangle, tone: "text-warn" },
    { key: "expiring", label: "Expiring ≤ 30 d", count: counts.expiring, icon: Clock, tone: "text-warn" },
    { key: "needs_relogin", label: "Needs re-login", count: counts.needs_relogin, icon: LogIn, tone: "text-destructive" },
    { key: "unassigned", label: "Unassigned", count: counts.unassigned, icon: Users },
  ]

  return (
    <div className="flex h-full flex-col">
      <SidebarToolbar>
        <div className="min-w-0 flex-1">
          <SidebarSearch
            value={filters.search}
            onValueChange={(v) => set({ search: v })}
            placeholder="Search a provider login…"
            aria-label="Search provider logins"
          />
        </div>
        <SidebarCollapseButton collapsed={false} onToggle={onToggleCollapse} />
      </SidebarToolbar>

      {/* Every status row is printed, zeroes included, the way the secrets
          rail prints its tiers: "Needs re-login · 0" is the answer to a
          question, and a row that vanishes when the answer is none cannot
          be told apart from a question the console does not ask. */}
      <SidebarSection label="Status" count={statusRows.length} className="border-b border-white/[0.06]">
        {statusRows.map((row) => {
          const Icon = row.icon
          const empty = row.count === 0 && row.key !== "all"
          const selected = filters.status === row.key
          return (
            <SidebarRow key={row.key} selected={selected} onSelect={() => set({ status: row.key })}>
              <Icon
                className={cn("h-3 w-3 shrink-0 text-muted-foreground/70", row.tone, empty && !selected && "opacity-40")}
                aria-hidden="true"
              />
              <span className={cn("min-w-0 flex-1 truncate", empty && !selected && "text-foreground/40")}>
                {row.label}
              </span>
              <span
                className={cn(
                  "shrink-0 tabular-nums text-[10px] text-muted-foreground/60",
                  empty && "text-muted-foreground/35",
                )}
              >
                {row.count}
              </span>
            </SidebarRow>
          )
        })}
      </SidebarSection>

      <div className="min-h-0 flex-1 overflow-y-auto">
        {providers.length > 0 && (
          <SidebarSection label="Provider" count={providers.length} className="border-b border-white/[0.06]">
            {providers.map((opt) => {
              const brand = getBrand(opt.value)
              const Icon = brand.Icon
              return (
                <SidebarRow
                  key={opt.value}
                  selected={filters.provider.includes(opt.value)}
                  onSelect={() => set({ provider: toggle(filters.provider, opt.value) })}
                >
                  <Icon className="h-3.5 w-3.5 shrink-0" style={{ color: brandColor(brand) }} aria-hidden="true" />
                  <span className="min-w-0 flex-1 truncate">{opt.label}</span>
                  <span className="shrink-0 tabular-nums text-[10px] text-muted-foreground/60">{opt.count}</span>
                </SidebarRow>
              )
            })}
          </SidebarSection>
        )}

        {modes.length > 0 && (
          <SidebarSection label="Mode" count={modes.length} className="border-b border-white/[0.06]">
            {modes.map((opt) => {
              const Icon = opt.value === "api_key" ? KeyRound : CreditCard
              return (
                <SidebarRow
                  key={opt.value}
                  selected={filters.mode.includes(opt.value)}
                  onSelect={() => set({ mode: toggle(filters.mode, opt.value) })}
                >
                  <Icon className="h-3 w-3 shrink-0 text-muted-foreground/70" aria-hidden="true" />
                  <span className="min-w-0 flex-1 truncate">{opt.label}</span>
                  <span className="shrink-0 tabular-nums text-[10px] text-muted-foreground/60">{opt.count}</span>
                </SidebarRow>
              )
            })}
          </SidebarSection>
        )}

        {owners.length > 0 && (
          <SidebarSection label="Owner" count={owners.length}>
            {owners.map((opt) => (
              <SidebarRow
                key={opt.value}
                selected={filters.owner.includes(opt.value)}
                onSelect={() => set({ owner: toggle(filters.owner, opt.value) })}
              >
                <UserRound
                  className={cn("h-3 w-3 shrink-0 text-muted-foreground/70", opt.value === UNOWNED && "opacity-50")}
                  aria-hidden="true"
                />
                <span className="min-w-0 flex-1 truncate">{opt.label}</span>
                <span className="shrink-0 tabular-nums text-[10px] text-muted-foreground/60">{opt.count}</span>
              </SidebarRow>
            ))}
          </SidebarSection>
        )}
      </div>
    </div>
  )
}
