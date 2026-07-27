"use client"

import * as React from "react"
import { Blocks, CircleHelp, Layers, Users, Wrench, Zap } from "lucide-react"
import type { LucideIcon } from "lucide-react"

import {
  SidebarRow,
  SidebarSearch,
  SidebarSection,
  SidebarCollapseButton,
  SidebarToolbar,
} from "@/components/layout/sidebar-kit"
import type { TabKey } from "./composio/types"
import type { ComposioStatus } from "./composio-integrations"

/**
 * The left panel while the Tools (MCP) tab is active.
 *
 * Composio's six views used to be a horizontal tab strip *inside* the page's
 * own tab strip — two navigations stacked, plus a third search box inside
 * three of the six. They are sidebar rows here instead, the same shape
 * Settings and Admin already use: vertical space is free, long labels and
 * counts fit, and a section sits next to the facets that narrow it.
 *
 * The facets below are the ones that mean something for tools — toolkit, the
 * Composio user an account belongs to — rather than the notification facets
 * the other tabs use, which were nonsense here.
 */

interface McpSection {
  key: TabKey
  label: string
  icon: LucideIcon
  hint: string
  count: (s: ComposioStatus) => React.ReactNode
}

const SECTIONS: McpSection[] = [
  {
    key: "catalog",
    label: "App catalog",
    icon: Blocks,
    hint: "Every app Composio can connect",
    count: (s) => s.counts.apps || undefined,
  },
  {
    key: "accounts",
    label: "Connected accounts",
    icon: Users,
    hint: "Which apps are connected, and by whom",
    count: (s) => s.counts.accounts || undefined,
  },
  {
    key: "agents",
    label: "Agent access",
    icon: Layers,
    hint: "Which agents may call which toolkits",
    count: (s) => (s.counts.agentsTotal ? `${s.counts.agentsBound}/${s.counts.agentsTotal}` : undefined),
  },
  {
    key: "tools",
    label: "Tools",
    icon: Wrench,
    hint: "Individual callable tools, per toolkit",
    count: () => undefined,
  },
  {
    key: "triggers",
    label: "Triggers",
    icon: Zap,
    hint: "Fire a routine when an app event happens",
    count: () => undefined,
  },
  {
    key: "mcp",
    label: "MCP endpoints",
    icon: CircleHelp,
    hint: "One endpoint per agent that has access",
    count: (s) => s.counts.endpoints || undefined,
  },
]

export interface McpFilters {
  toolkit: string | null
  user: string | null
}

export const EMPTY_MCP_FILTERS: McpFilters = { toolkit: null, user: null }

interface McpExplorerProps {
  status: ComposioStatus
  section: TabKey
  onSectionChange: (s: TabKey) => void
  search: string
  onSearchChange: (v: string) => void
  filters: McpFilters
  onFiltersChange: (f: McpFilters) => void
  onToggleCollapse: () => void
  /** Opens the API-key dialog from the not-configured state. */
  onAddApiKey: () => void
}

export function McpExplorer({
  status,
  section,
  onSectionChange,
  search,
  onSearchChange,
  filters,
  onFiltersChange,
  onToggleCollapse,
  onAddApiKey,
}: McpExplorerProps) {
  const set = (patch: Partial<McpFilters>) => onFiltersChange({ ...filters, ...patch })

  // Before a key is set there is nothing to navigate — offering six empty
  // sections would be six dead ends. One row, and it says what to do.
  if (!status.configured && !status.loading) {
    return (
      <div className="flex h-full flex-col">
        <SidebarToolbar>
          <SidebarSearch
            value=""
            onValueChange={() => {}}
            placeholder="Search…"
            aria-label="Search tools"
          />
          <SidebarCollapseButton collapsed={false} onToggle={onToggleCollapse} />
        </SidebarToolbar>
        <SidebarSection label="Tools (MCP)">
          <SidebarRow selected onSelect={onAddApiKey}>
            <CircleHelp className="h-3 w-3 shrink-0 text-muted-foreground/70" />
            <span className="min-w-0 flex-1 truncate">Setup</span>
          </SidebarRow>
        </SidebarSection>
        <p className="px-3 py-2 text-[11px] leading-relaxed text-muted-foreground">
          The sections appear once an API key is saved.
        </p>
      </div>
    )
  }

  return (
    <div className="flex h-full flex-col">
      <SidebarToolbar>
        <SidebarSearch
          value={search}
          onValueChange={onSearchChange}
          placeholder="Search apps, tools, agents…"
          aria-label="Search tools and apps"
        />
        <SidebarCollapseButton collapsed={false} onToggle={onToggleCollapse} />
      </SidebarToolbar>

      <div className="min-h-0 flex-1 overflow-y-auto pb-4">
        <SidebarSection label="Tools (MCP)" count={SECTIONS.length}>
          {SECTIONS.map((s) => {
            const Icon = s.icon
            const count = s.count(status)
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
                {count != null && (
                  <span className="shrink-0 tabular-nums text-[10px] text-muted-foreground/60">
                    {count}
                  </span>
                )}
              </SidebarRow>
            )
          })}
        </SidebarSection>

        {status.toolkits.length > 0 && (
          <SidebarSection label="Toolkit" count={status.toolkits.length}>
            {status.toolkits.map((t) => (
              <SidebarRow
                key={t.slug}
                selected={filters.toolkit === t.slug}
                onSelect={() => set({ toolkit: filters.toolkit === t.slug ? null : t.slug })}
              >
                <span className="min-w-0 flex-1 truncate">{t.slug}</span>
                <span className="shrink-0 tabular-nums text-[10px] text-muted-foreground/60">
                  {t.count}
                </span>
              </SidebarRow>
            ))}
          </SidebarSection>
        )}

        {status.users.length > 0 && (
          <SidebarSection label="User" count={status.users.length}>
            {status.users.map((u) => (
              <SidebarRow
                key={u.id}
                selected={filters.user === u.id}
                onSelect={() => set({ user: filters.user === u.id ? null : u.id })}
              >
                <span className="min-w-0 flex-1 truncate" title={u.id}>
                  {u.id}
                </span>
                <span className="shrink-0 tabular-nums text-[10px] text-muted-foreground/60">
                  {u.count}
                </span>
              </SidebarRow>
            ))}
          </SidebarSection>
        )}
      </div>
    </div>
  )
}

/** Heading + subtitle for the section currently in view. */
export function mcpSectionMeta(section: TabKey, status: ComposioStatus): {
  title: string
  subtitle: string
} {
  const c = status.counts
  switch (section) {
    case "catalog":
      return {
        title: "App catalog",
        subtitle: c.apps ? `${c.apps} apps · ${c.accounts} already connected` : "Loading the catalog…",
      }
    case "accounts":
      return {
        title: "Connected accounts",
        subtitle: `${c.accounts} ${c.accounts === 1 ? "account" : "accounts"} across ${c.users} ${c.users === 1 ? "user" : "users"}`,
      }
    case "agents":
      return {
        title: "Agent access",
        subtitle: `${c.agentsBound} of ${c.agentsTotal} agents have a Composio user`,
      }
    case "tools":
      return { title: "Tools", subtitle: "Individual callable tools, per toolkit" }
    case "triggers":
      return { title: "Triggers", subtitle: "Fire a routine when an app event happens" }
    case "mcp":
      return {
        title: "MCP endpoints",
        subtitle: `${c.endpoints} ${c.endpoints === 1 ? "endpoint" : "endpoints"} · one per agent with access`,
      }
  }
}
