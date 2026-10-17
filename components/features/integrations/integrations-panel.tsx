"use client"

import * as React from "react"
import type { LucideIcon } from "lucide-react"
import {
  AlertTriangle,
  Bell,
  Bot,
  Calendar,
  Circle,
  Clock,
  FileText,
  Grid3x3,
  KeyRound,
  Link2,
  Lock,
  Mail,
  MessageSquare,
  Pause,
  Play,
  Send,
  Server,
  Store,
  Terminal,
  User,
  Webhook,
  Workflow,
} from "lucide-react"

import {
  SidebarActiveChip,
  SidebarActiveChips,
  SidebarFacetOption,
  SidebarRow,
  SidebarSearch,
  SidebarSection,
  SidebarToolbar,
} from "@/components/layout/sidebar-kit"
import { useCloseSecondaryPanelSheet } from "@/components/layout/secondary-panel"
import { cn } from "@/lib/utils"
import type { IncomingKind } from "./incoming-model"
import type { IntegrationsSpace } from "./spaces"
import type { ChannelKindFilter } from "./channels-space"

export const SPACE_META: Record<IntegrationsSpace, { label: string; icon: LucideIcon; search: string; add: string }> = {
  tools: { label: "Tools", icon: Grid3x3, search: "Search apps…", add: "Connect an app" },
  triggers: { label: "Triggers", icon: Play, search: "Search triggers, routines, agents…", add: "New trigger" },
  channels: { label: "Channels", icon: MessageSquare, search: "Search channels…", add: "Add channel" },
  webhooks: { label: "Webhooks", icon: Webhook, search: "Search webhooks…", add: "Add webhook" },
}

const SPACE_ORDER: IntegrationsSpace[] = ["tools", "triggers", "channels", "webhooks"]

export interface PanelFilters {
  kind: ChannelKindFilter | null
  whose: "workspace" | "personal" | "everyone" | null
  checks: string | null
}

export const EMPTY_PANEL_FILTERS: PanelFilters = { kind: null, whose: null, checks: null }

function Row({ icon: Icon, label, count, selected, onSelect, locked }: { icon: LucideIcon; label: React.ReactNode; count?: React.ReactNode; selected?: boolean; onSelect: () => void; locked?: boolean }) {
  return (
    <SidebarRow selected={selected} onSelect={onSelect}>
      <Icon className="size-3.5 shrink-0 text-muted-foreground" aria-hidden />
      <span className="min-w-0 flex-1 truncate">{label}</span>
      {locked && <Lock className="size-3 shrink-0 text-muted-foreground-soft" aria-label="Admins only" />}
      {count != null && <span className="font-mono text-micro tabular-nums text-muted-foreground">{count}</span>}
    </SidebarRow>
  )
}

/** The four spaces, at the top of the panel; 1–4 switch between them. */
export function SpaceSwitcher({ space, counts, onSpace, segmented = false }: { space: IntegrationsSpace; counts: Partial<Record<IntegrationsSpace, React.ReactNode>>; onSpace: (s: IntegrationsSpace) => void; segmented?: boolean }) {
  if (segmented) {
    return (
      <div role="tablist" aria-label="Spaces" className="flex w-full gap-0.5 rounded-lg border border-border bg-surface-subtle p-0.5" data-slot="integrations-spaces">
        {SPACE_ORDER.map((s) => (
          <button
            key={s}
            type="button"
            role="tab"
            aria-selected={space === s}
            onClick={() => onSpace(s)}
            className={cn("kit-tap h-7 min-w-0 flex-1 truncate rounded-md px-1.5 text-label transition-colors", space === s ? "bg-card font-semibold text-foreground shadow-[0_0_0_1px_var(--border)]" : "text-muted-foreground hover:text-foreground")}
          >
            {SPACE_META[s].label}
          </button>
        ))}
      </div>
    )
  }
  return (
    <div role="tablist" aria-label="Spaces" aria-orientation="vertical" className="mx-1.5 mt-2 grid gap-0.5 rounded-lg border border-border bg-surface-subtle p-1" data-slot="integrations-spaces">
      {SPACE_ORDER.map((s, i) => {
        const Icon = SPACE_META[s].icon
        const on = space === s
        return (
          <button
            key={s}
            type="button"
            role="tab"
            aria-selected={on}
            title={`${SPACE_META[s].label} · ${i + 1}`}
            onClick={() => onSpace(s)}
            className={cn("kit-tap flex h-8 items-center gap-2 rounded-md px-2 text-body transition-colors", on ? "bg-card font-semibold text-foreground shadow-[0_0_0_1px_var(--border)]" : "text-muted-foreground hover:text-foreground")}
          >
            <Icon className={cn("size-[15px] shrink-0", on && "text-primary-hover")} aria-hidden />
            <span className="min-w-0 flex-1 truncate text-left">{SPACE_META[s].label}</span>
            {counts[s] != null && <span className="font-mono text-micro tabular-nums text-muted-foreground">{counts[s]}</span>}
          </button>
        )
      })}
    </div>
  )
}

export interface IntegrationsPanelProps {
  space: IntegrationsSpace
  view: string
  hasObject: boolean
  starts: IncomingKind | null
  search: string
  onSearch: (q: string) => void
  onSpace: (s: IntegrationsSpace) => void
  onView: (v: string) => void
  onStarts: (k: IncomingKind | null) => void
  filters: PanelFilters
  onFilters: (f: PanelFilters) => void
  spaceCounts: Partial<Record<IntegrationsSpace, React.ReactNode>>
  viewCounts: Record<string, number | undefined>
  kindCounts: Partial<Record<ChannelKindFilter, number>>
  startsCounts: Partial<Record<IncomingKind, number>>
  checksOptions: { label: string; count: number }[]
  isAdmin: boolean
  canSeeDeliveries: boolean
  onComposioKey: () => void
  composioKeyLabel: string | null
}

export function IntegrationsPanel(p: IntegrationsPanelProps) {
  const closeSheet = useCloseSecondaryPanelSheet()
  const go = (fn: () => void) => () => {
    fn()
    closeSheet()
  }
  const at = (v: string) => !p.hasObject && p.view === v
  const set = (patch: Partial<PanelFilters>) => p.onFilters({ ...p.filters, ...patch })

  let body: React.ReactNode
  if (p.space === "tools") {
    body = (
      <>
        <SidebarSection label="Views">
          <ul aria-label="Views">
            <Row icon={Link2} label="Connected" count={p.viewCounts.connected} selected={at("connected")} onSelect={go(() => p.onView("connected"))} />
            <Row icon={AlertTriangle} label="Needs attention" count={p.viewCounts.attention} selected={at("attention")} onSelect={go(() => p.onView("attention"))} />
            <Row icon={Circle} label="Not used" count={p.viewCounts.idle} selected={at("idle")} onSelect={go(() => p.onView("idle"))} />
            <Row icon={Store} label="Catalog" count={p.viewCounts.catalog} selected={at("catalog") || (p.view === "catalog" && p.hasObject)} onSelect={go(() => p.onView("catalog"))} />
          </ul>
        </SidebarSection>
        <SidebarSection label="Composio">
          <ul aria-label="Composio">
            <Row icon={KeyRound} label="Composio account" count={p.composioKeyLabel ? "Key set" : "No key"} onSelect={go(p.onComposioKey)} />
          </ul>
        </SidebarSection>
        <SidebarSection label="More">
          <ul aria-label="More">
            <Row icon={Server} label="Crew MCP servers" selected={at("crew-tools")} onSelect={go(() => p.onView("crew-tools"))} />
            <Row icon={Terminal} label="MCP endpoints" selected={at("mcp")} onSelect={go(() => p.onView("mcp"))} />
          </ul>
        </SidebarSection>
      </>
    )
  } else if (p.space === "triggers") {
    const kinds: { k: IncomingKind; label: string; icon: LucideIcon }[] = [
      { k: "routine", label: "Routines", icon: Workflow },
      { k: "agent", label: "Agents", icon: Bot },
      { k: "page", label: "Artifacts", icon: FileText },
    ]
    body = (
      <>
        <SidebarSection label="Views">
          <ul aria-label="Views">
            <Row icon={Play} label="All triggers" count={p.viewCounts.all} selected={at("all") && !p.starts && !p.filters.checks} onSelect={go(() => p.onView("all"))} />
            <Row icon={AlertTriangle} label="Failing" count={p.viewCounts.failing} selected={at("failing")} onSelect={go(() => p.onView("failing"))} />
            <Row icon={Circle} label="Turned off" count={p.viewCounts.off} selected={at("off")} onSelect={go(() => p.onView("off"))} />
          </ul>
        </SidebarSection>
        <SidebarSection label="Starts">
          <ul aria-label="Starts">
            {kinds.map((k) => (
              <Row key={k.k} icon={k.icon} label={k.label} count={p.startsCounts[k.k]} selected={!p.hasObject && p.starts === k.k} onSelect={go(() => p.onStarts(p.starts === k.k ? null : k.k))} />
            ))}
            <SidebarRow onSelect={go(() => window.location.assign("/routines"))}>
              <Calendar className="size-3.5 shrink-0 text-muted-foreground" aria-hidden />
              <span className="min-w-0 flex-1 truncate">Schedules</span>
              <span className="text-micro text-muted-foreground">in Routines</span>
            </SidebarRow>
          </ul>
        </SidebarSection>
        {p.checksOptions.length > 0 && (
          <SidebarSection label="Checks the sender">
            {p.checksOptions.map((o) => (
              <SidebarFacetOption key={o.label} active={p.filters.checks === o.label} onToggle={() => set({ checks: p.filters.checks === o.label ? null : o.label })}>
                <span className="min-w-0 flex-1 truncate">{o.label}</span>
                <span className="font-mono text-micro text-muted-foreground">{o.count}</span>
              </SidebarFacetOption>
            ))}
          </SidebarSection>
        )}
        <p className="px-3.5 py-3 text-label text-muted-foreground">A trigger&apos;s URL never changes. What it starts can: point it at another routine in Settings.</p>
      </>
    )
  } else if (p.space === "channels") {
    const kinds: { k: ChannelKindFilter; label: string; icon: LucideIcon }[] = [
      { k: "chat", label: "Chat apps", icon: MessageSquare },
      { k: "email", label: "Email", icon: Mail },
      { k: "push", label: "Push", icon: Bell },
      { k: "incident", label: "Incident tools", icon: AlertTriangle },
    ]
    body = (
      <>
        <SidebarSection label="Views">
          <ul aria-label="Views">
            <Row icon={Send} label="All channels" count={p.viewCounts.all} selected={at("all") && !p.filters.kind} onSelect={go(() => p.onView("all"))} />
            <Row icon={AlertTriangle} label="Failing" count={p.viewCounts.failing} selected={at("failing")} onSelect={go(() => p.onView("failing"))} />
            <Row icon={Pause} label="Paused" count={p.viewCounts.paused} selected={at("paused")} onSelect={go(() => p.onView("paused"))} />
            <Row icon={User} label="Mine" count={p.viewCounts.mine} selected={at("mine")} onSelect={go(() => p.onView("mine"))} />
            <Row icon={Bell} label="My notifications" selected={at("preferences")} onSelect={go(() => p.onView("preferences"))} />
            <Row icon={Clock} label="Deliveries" locked={!p.canSeeDeliveries} selected={at("deliveries")} onSelect={go(() => p.onView("deliveries"))} />
          </ul>
        </SidebarSection>
        <SidebarSection label="Kind">
          <ul aria-label="Kind">
            {kinds.map((k) => (
              <Row key={k.k} icon={k.icon} label={k.label} count={p.kindCounts[k.k] ?? 0} selected={!p.hasObject && p.filters.kind === k.k} onSelect={go(() => set({ kind: p.filters.kind === k.k ? null : k.k }))} />
            ))}
          </ul>
        </SidebarSection>
        <SidebarSection label="Whose">
          {(
            [
              ["workspace", "Workspace"],
              ["personal", "Personal"],
              ...(p.isAdmin ? ([["everyone", "Everyone's personal"]] as const) : []),
            ] as const
          ).map(([k, label]) => (
            <SidebarFacetOption key={k} active={p.filters.whose === k} onToggle={() => set({ whose: p.filters.whose === k ? null : k })}>
              {label}
            </SidebarFacetOption>
          ))}
        </SidebarSection>
      </>
    )
  } else {
    body = (
      <>
        <SidebarSection label="Views">
          <ul aria-label="Views">
            <Row icon={Webhook} label="All webhooks" count={p.viewCounts.all} selected={at("all")} onSelect={go(() => p.onView("all"))} />
            <Row icon={AlertTriangle} label="Failing" count={p.viewCounts.failing} selected={at("failing")} onSelect={go(() => p.onView("failing"))} />
            <Row icon={Pause} label="Paused" count={p.viewCounts.paused} selected={at("paused")} onSelect={go(() => p.onView("paused"))} />
          </ul>
        </SidebarSection>
        <p className="px-3.5 py-3 text-label text-muted-foreground">API and CLI tokens for your own scripts are in Settings › Sessions &amp; tokens.</p>
      </>
    )
  }

  const chips: React.ReactNode[] = []
  if (p.filters.kind) chips.push(<SidebarActiveChip key="k" onRemove={() => set({ kind: null })}>{p.filters.kind}</SidebarActiveChip>)
  if (p.filters.whose) chips.push(<SidebarActiveChip key="w" label={`Whose ${p.filters.whose}`} onRemove={() => set({ whose: null })}>Whose: {p.filters.whose}</SidebarActiveChip>)
  if (p.filters.checks) chips.push(<SidebarActiveChip key="c" onRemove={() => set({ checks: null })}>{p.filters.checks}</SidebarActiveChip>)

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <SidebarToolbar>
        <SidebarSearch value={p.search} onValueChange={p.onSearch} placeholder={SPACE_META[p.space].search} aria-label={SPACE_META[p.space].search.replace("…", "")} />
      </SidebarToolbar>
      <SidebarActiveChips className="pt-2">{chips}</SidebarActiveChips>
      <SpaceSwitcher space={p.space} counts={p.spaceCounts} onSpace={(s) => { p.onSpace(s); closeSheet() }} />
      <div className="min-h-0 flex-1 overflow-y-auto pb-3">{body}</div>
    </div>
  )
}
