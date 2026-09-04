"use client"

import * as React from "react"
import { AnimatePresence, motion } from "motion/react"
import {
  Bell,
  Blocks,
  CircleHelp,
  Clock,
  KeyRound,
  Layers,
  Link2,
  Plug,
  Plus,
  RefreshCw,
  SlidersHorizontal,
  Users,
  Wrench,
  Zap,
} from "lucide-react"
import { toast } from "sonner"

import { SubBar, SubBarPrimary, SubBarSecondary } from "@/components/layout/sub-bar"
import { SidebarCollapseButton } from "@/components/layout/sidebar-kit"
import { Switch } from "@/components/ui/switch"
import { cn } from "@/lib/utils"
import { invalidate } from "@/lib/stale-cache"
import { useAbilities } from "@/hooks/use-abilities"
import { useIsMobile } from "@/hooks/use-mobile"
import { ConfirmDialog } from "@/components/ui/confirm-dialog"
import { useSession } from "@/hooks/use-auth"
import { useNotificationChannels } from "@/hooks/use-notification-channels"
import { useNotificationProviders } from "@/hooks/use-notification-providers"
import { useNotificationDeliveries } from "@/hooks/use-notification-deliveries"
import { KpiCard } from "@/components/features/dashboard/kpi-card"
import { NotificationPrefsSection } from "./notification-prefs-section"
import { ComposioIntegrations, type ComposioStatus } from "./composio-integrations"
import { brandLogo } from "./composio/shared"
import type { TabKey } from "./composio/types"

import {
  applyFilters,
  channelKind,
  deriveStatus,
  EMPTY_FILTERS,
  isForeignPersonal,
  rowMatches,
  type ConnectionFilters,
  type ConnectionRow,
} from "./connection-model"
import { IntegrationsExplorer, type ExplorerSection } from "./explorer"
import { mcpFacets, notificationFacets } from "./facets"
import { EMPTY_MCP_FILTERS, type McpFilters } from "./mcp-filters"
import { ConnectionsView } from "./views/connections-view"
import { CrewToolsView } from "./views/crew-tools-view"
import { ConnectionDetail } from "./views/connection-detail"
import { ToolAccountDetail } from "./views/tool-account-detail"
import { buildServiceOptions, catalogSections, catalogSize } from "./service-catalog"
import { DeliveriesView } from "./views/deliveries-view"
import { AddChannelDialog, type AddChannelTarget } from "./add-channel-dialog"
import { AddIntegrationDialog, type ServiceOption } from "./add-integration-dialog"

/**
 * /integrations — two tabs, one shape.
 *
 * The page settled at two top-level tabs because there are exactly two kinds
 * of thing here: places Crewship reaches a HUMAN, and things an AGENT can act
 * through. Everything else people used to reach for a tab for — the preference
 * matrix, the delivery log, Composio's six views — is a SECTION inside its
 * tab, in the left panel, with the same toolbar and the same Filter popover on
 * both sides.
 *
 * That symmetry is the point. "Left bars everywhere" was already true; "the
 * same logic in every left bar" was not, and a page with five top tabs and two
 * differently-behaving rails is not simpler for having more entry points.
 */

const TABS = [
  { id: "notifications" as const, label: "Notifications", icon: Bell },
  { id: "tools" as const, label: "Tools (MCP)", icon: Wrench },
] as const

/** Sections inside the Notifications tab. */
type NotifySection = "connections" | "preferences" | "deliveries"

const NOTIFY_SECTIONS: ExplorerSection<NotifySection>[] = [
  {
    key: "connections",
    label: "Connections",
    icon: Link2,
    hint: "What is hooked up, and is it working",
  },
  {
    key: "preferences",
    label: "My preferences",
    icon: SlidersHorizontal,
    hint: "Which categories reach you, on which connection",
  },
  {
    key: "deliveries",
    label: "Deliveries",
    icon: Clock,
    hint: "Why a notification did or did not arrive",
  },
]

/** Sections inside the Tools (MCP) tab — Composio's six views. */
const MCP_SECTIONS: Omit<ExplorerSection<TabKey>, "count">[] = [
  { key: "catalog", label: "App catalog", icon: Blocks, hint: "Every app Composio can connect" },
  {
    key: "accounts",
    label: "Connected accounts",
    icon: Users,
    hint: "Which apps are connected, and by whom",
  },
  {
    key: "agents",
    label: "Agent access",
    icon: Layers,
    hint: "Which agents may call which toolkits",
  },
  { key: "tools", label: "Tools", icon: Wrench, hint: "Individual callable tools, per toolkit" },
  { key: "triggers", label: "Triggers", icon: Zap, hint: "Fire a routine on an app event" },
  {
    key: "mcp",
    label: "MCP endpoints",
    icon: CircleHelp,
    hint: "One endpoint per agent that has access",
  },
]

type IntegrationsTab = (typeof TABS)[number]["id"]

/** The Tools tab's sections: Composio's six views plus the crew-scoped MCP
 *  servers, which lived on neither tab until #2303. */
type ToolsSection = TabKey | "crew-tools"

const CREW_TOOLS_SECTION: ExplorerSection<ToolsSection> = {
  key: "crew-tools",
  label: "Crew tools",
  icon: Wrench,
  hint: "MCP servers a crew's agents call, and whether each has a credential",
}

/**
 * Where a `?tab=&section=` link opens.
 *
 * This page is the single home for notification channels, the preference
 * matrix and Composio's tool views, which only works if a link can name a
 * section: /settings forwards two retired tabs here, and the docs point at
 * individual views. Unknown or cross-tab values fall back to the defaults
 * rather than leaving a panel selecting a section its tab does not own.
 */
export function initialIntegrationsRoute(search: string): {
  tab: IntegrationsTab
  notifySection: NotifySection
  mcpSection: ToolsSection
  /** `?server=`: the crew tool a "Connect" link elsewhere points at. */
  server: string | null
} {
  const p = new URLSearchParams(search)
  const tab: IntegrationsTab = p.get("tab") === "tools" ? "tools" : "notifications"
  const section = p.get("section")

  const notifyMatch = NOTIFY_SECTIONS.find((s) => s.key === section)
  const mcpMatch = section === "crew-tools" ? CREW_TOOLS_SECTION : MCP_SECTIONS.find((s) => s.key === section)
  const server = p.get("server")

  return {
    tab,
    notifySection: tab === "notifications" && notifyMatch ? notifyMatch.key : "connections",
    mcpSection: tab === "tools" && mcpMatch ? (mcpMatch.key as ToolsSection) : "accounts",
    server: tab === "tools" && server ? server : null,
  }
}

/**
 * What the Tools panel shows before a key is saved. Offering the six real
 * sections would be six dead ends; one row that says what to do is not.
 */
const SETUP_ONLY_SECTION: ExplorerSection<ToolsSection>[] = [
  { key: "catalog", label: "Setup", icon: KeyRound, hint: "Add a Composio API key" },
]

/** Dot colour per connection status, shared by the list and the facets. */
const STATUS_DOT: Record<string, string> = {
  delivering: "bg-success",
  failing: "bg-destructive",
  never_used: "bg-warn",
  disabled: "bg-muted-foreground/40",
  unknown: "bg-info",
}

/** How far back the "Sent · 24h" column and the status column look. */
const DELIVERY_WINDOW_MS = 24 * 60 * 60 * 1000
/** Enough rows for the log view without paging; the API caps server-side too. */
const DELIVERY_LIMIT = 200

export function IntegrationsLayout({ workspaceId }: { workspaceId: string }) {
  const { abilities } = useAbilities()
  const canManageWorkspace = abilities.can("manage", "Workspace")
  const currentUserId = useSession().data?.user.id ?? null

  // The admin overview used to be a separate screen ("All connections"),
  // which put the same objects in two places — the exact failure this page is
  // undoing. It is a toggle on the one list instead: an admin can widen it to
  // include other members' personal connections, and those rows arrive with
  // their destinations redacted and their actions off.
  const [includeEveryone, setIncludeEveryone] = React.useState(false)

  // This page is statically prerendered, so the first render must not read
  // the URL: the served HTML was built without one, and a client that renders
  // a different tab from it is a hydration mismatch. The URL is applied in a
  // layout effect instead — before paint, so a deep link still opens on the
  // section it named rather than flashing the default.
  const [tab, setTab] = React.useState<IntegrationsTab>("notifications")
  const [notifySection, setNotifySection] = React.useState<NotifySection>("connections")
  // Which item the left panel has open — a connection, or a tool account.
  // Selecting one takes over the main column, exactly as picking a routine
  // does on /routines.
  const [selectedConnectionId, setSelectedConnectionId] = React.useState<string | null>(null)
  const [selectedAccountId, setSelectedAccountId] = React.useState<string | null>(null)
  const [search, setSearch] = React.useState("")
  const [filters, setFilters] = React.useState<ConnectionFilters>(EMPTY_FILTERS)
  const [collapsed, setCollapsed] = React.useState(false)
  const [addTarget, setAddTarget] = React.useState<AddChannelTarget | null>(null)
  const [addOpen, setAddOpen] = React.useState(false)

  // Tools tab: which of Composio's six views is showing, and the facets that
  // narrow it. Held here because the left panel renders them and the main
  // column obeys them — the same split every other tab already uses.
  const [mcpSection, setMcpSection] = React.useState<ToolsSection>("accounts")
  const [linkedServerId, setLinkedServerId] = React.useState<string | null>(null)
  const [mcpFilters, setMcpFilters] = React.useState<McpFilters>(EMPTY_MCP_FILTERS)
  const [apiKeyOpen, setApiKeyOpen] = React.useState(false)
  const [composioStatus, setComposioStatus] = React.useState<ComposioStatus>({
    loading: true,
    configured: false,
    keyLabel: null,
    counts: { apps: 0, accounts: 0, users: 0, agentsBound: 0, agentsTotal: 0, endpoints: 0 },
    toolkits: [],
    users: [],
    accounts: [],
    agents: [],
    bindings: {},
  })

  // Apply the incoming ?tab=&section= once, before paint.
  React.useLayoutEffect(() => {
    const r = initialIntegrationsRoute(window.location.search)
    setTab(r.tab)
    setNotifySection(r.notifySection)
    setMcpSection(r.mcpSection)
    setLinkedServerId(r.server)
  }, [])

  // On a phone the rail is 280px of a 390px screen: it does not sit beside the
  // content, it replaces it, and the KPI cards beside it rendered one word per
  // line. Collapse it when the viewport narrows and open it as an overlay
  // instead — the same treatment /credentials gives its rail.
  const isMobile = useIsMobile()
  React.useEffect(() => {
    if (isMobile) setCollapsed(true)
  }, [isMobile])

  // Keep the URL naming what is on screen, so any view here can be linked to —
  // by /settings' retired tabs, by the docs, by a colleague pasting "look at
  // Deliveries". replaceState, not a route push: this is the same page.
  //
  // Skips its first run: that one fires with the pre-URL defaults still in
  // state and would overwrite the very link the layout effect just read.
  const urlSynced = React.useRef(false)
  React.useEffect(() => {
    if (typeof window === "undefined") return
    if (!urlSynced.current) {
      urlSynced.current = true
      return
    }
    const url = new URL(window.location.href)
    url.searchParams.set("tab", tab)
    url.searchParams.set("section", tab === "notifications" ? notifySection : mcpSection)
    window.history.replaceState(null, "", url.toString())
  }, [tab, notifySection, mcpSection])

  const {
    channels,
    loading: channelsLoading,
    error: channelsError,
    refresh: refreshChannels,
    create,
    remove,
    sendTest,
    sendDraftTest,
    patch,
  } = useNotificationChannels(workspaceId, {
    includeEveryone: includeEveryone && canManageWorkspace,
  })

  const {
    providers,
    categories: providerCategories,
    refresh: refreshProviders,
  } = useNotificationProviders(workspaceId)

  // The delivery log is ADMIN-only. Asking for it as a MEMBER is not an
  // error — the hook reports `forbidden` separately, and every consumer
  // treats that as "cannot know" rather than "nothing happened".
  const {
    deliveries,
    loading: deliveriesLoading,
    error: deliveriesError,
    forbidden: deliveriesForbidden,
    refresh: refreshDeliveries,
  } = useNotificationDeliveries(workspaceId, { limit: DELIVERY_LIMIT })

  const providerCategoryOf = React.useCallback(
    (name: string) => providers.find((p) => p.provider === name)?.category,
    [providers],
  )
  const providerLabelOf = React.useCallback(
    (name: string) => providers.find((p) => p.provider === name)?.label ?? name,
    [providers],
  )

  const canSeeDeliveries = !deliveriesForbidden

  // ---- normalise both backends into one row list -------------------------
  const rows = React.useMemo<ConnectionRow[]>(() => {
    const since = Date.now() - DELIVERY_WINDOW_MS
    const byChannel = new Map<string, typeof deliveries>()
    for (const d of deliveries) {
      const list = byChannel.get(d.channel_id) ?? []
      list.push(d)
      byChannel.set(d.channel_id, list)
    }

    const channelRows: ConnectionRow[] = channels.map((ch) => {
      const mine = byChannel.get(ch.id) ?? []
      const recent = mine.filter((d) => Date.parse(d.created_at) >= since)
      const provider = ch.type === "shoutrrr" ? (ch.provider ?? "unknown") : ch.type
      const target = ch.type === "email" ? ch.to : ch.type === "webhook" ? ch.url : ch.provider
      const lastSent = mine
        .map((d) => d.created_at)
        .sort()
        .at(-1)
      const foreign = isForeignPersonal(ch, currentUserId)
      return {
        id: ch.id,
        kind: channelKind(ch, providerCategoryOf),
        // A channel has no user-given name in the data model, so the
        // destination IS the name; inventing "Discord channel #1" would read
        // as a label someone chose. On the admin overview another member's
        // destination is a blank string (redacted server-side) — say whose it
        // is rather than rendering an empty cell that looks like a bug.
        name: foreign
          ? `${providerLabelOf(provider)} — another member's`
          : target || providerLabelOf(provider),
        detail: `${provider}${ch.scope === "user" ? " · personal" : ""}`,
        provider,
        providerLabel: ch.type === "shoutrrr" ? providerLabelOf(provider) : titleCase(ch.type),
        scope: ch.scope === "user" ? "personal" : "workspace",
        enabled: ch.enabled,
        categories: ch.categories ?? [],
        status: deriveStatus(ch.enabled, canSeeDeliveries ? mine : null),
        sent24h: canSeeDeliveries ? recent.filter((d) => d.status === "sent").length : null,
        lastDelivery: lastSent ?? null,
        source: "channel",
        readOnly: foreign,
        channel: ch,
      }
    })

    // Composio accounts are deliberately NOT in this list any more. They were,
    // and it was the single most confusing thing on the page: a Discord webhook
    // and a Gmail tool account sat in one table under one word, with different
    // owners, different lifecycles and different meanings of "connected". They
    // live under Tools, where their own six views already describe them.
    return channelRows
  }, [channels, deliveries, canSeeDeliveries, currentUserId, providerCategoryOf, providerLabelOf])

  const visibleRows = React.useMemo(
    () => applyFilters(rows, filters).filter((r) => rowMatches(r, search)),
    [rows, filters, search],
  )

  /** Connections per provider, for the catalog's "already in use" badge. */
  const usage = React.useMemo(() => {
    const out: Record<string, number> = {}
    for (const r of rows) out[r.provider] = (out[r.provider] ?? 0) + 1
    return out
  }, [rows])

  /** Catalog hits for the current search — powers the empty-state hint. */
  const catalogMatches = React.useMemo(() => {
    const q = search.trim().toLowerCase()
    if (!q) return 0
    return providers.filter(
      (p) =>
        p.label.toLowerCase().includes(q) ||
        p.provider.toLowerCase().includes(q) ||
        p.blurb.toLowerCase().includes(q),
    ).length
  }, [providers, search])

  // ---- actions ------------------------------------------------------------
  const refreshAll = React.useCallback(() => {
    void refreshChannels()
    void refreshProviders()
    void refreshDeliveries()
    // Composio is only mounted on its own tab, so there is nothing to call —
    // dropping its cache is what makes the next visit fetch rather than paint
    // what this button was pressed to replace.
    if (workspaceId) invalidate(`composio:${workspaceId}:`)
  }, [refreshChannels, refreshProviders, refreshDeliveries, workspaceId])

  const handleToggle = async (row: ConnectionRow, next: boolean) => {
    try {
      await patch(row.id, { enabled: next })
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "Failed to update the connection")
    }
  }

  const handleTest = async (row: ConnectionRow) => {
    try {
      await sendTest(row.id)
      toast.success("Test sent", { description: `to ${row.name}` })
    } catch (e) {
      toast.error("Test send failed", {
        description: e instanceof Error ? e.message : undefined,
      })
    }
  }

  // The dialog decides; handleDelete resolves true only once the connection
  // is gone, so a caller that closes a detail view afterwards does not do it
  // under the open dialog, nor after a Cancel.
  const [pendingDelete, setPendingDelete] = React.useState<{ row: ConnectionRow; resolve: (deleted: boolean) => void } | null>(null)
  const handleDelete = (row: ConnectionRow) =>
    new Promise<boolean>((resolve) => setPendingDelete({ row, resolve }))
  const deleteConnection = async (row: ConnectionRow) => {
    try {
      await remove(row.id)
      toast.success("Connection deleted")
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "Failed to delete the connection")
      throw e
    }
  }

  const handlePickService = (service: ServiceOption) => {
    setAddTarget(
      service.key === "email" || service.key === "webhook"
        ? { kind: service.key, label: service.label }
        : { kind: "shoutrrr", provider: service.key, label: service.label },
    )
  }

  /**
   * Tools chosen in the wizard. Composio owns its own connect flow (OAuth per
   * toolkit), so this is a doorway rather than a form — straight to the app
   * catalog, or to the key dialog when there is no key yet, because browsing
   * a catalog you cannot connect from is a dead end.
   */
  const handlePickTools = () => {
    setTab("tools")
    if (composioStatus.configured) {
      setMcpSection("catalog")
    } else {
      setApiKeyOpen(true)
    }
  }

  const services = React.useMemo(
    () => buildServiceOptions(providers, usage),
    [providers, usage],
  )
  const serviceSections = React.useMemo(
    () => catalogSections(providerCategories),
    [providerCategories],
  )

  // ── explorer wiring ─────────────────────────────────────────────────────
  const notifySections: ExplorerSection<NotifySection>[] = React.useMemo(
    () =>
      NOTIFY_SECTIONS.map((sec) => ({
        ...sec,
        count:
          sec.key === "connections"
            ? rows.length || undefined
            : sec.key === "deliveries"
              ? canSeeDeliveries
                ? deliveries.length || undefined
                : undefined
              : undefined,
      })),
    [rows.length, deliveries.length, canSeeDeliveries],
  )

  const mcpSections: ExplorerSection<ToolsSection>[] = React.useMemo(() => {
    const c = composioStatus.counts
    const counts: Partial<Record<TabKey, React.ReactNode>> = {
      catalog: c.apps || undefined,
      accounts: c.accounts || undefined,
      agents: c.agentsTotal ? `${c.agentsBound}/${c.agentsTotal}` : undefined,
      mcp: c.endpoints || undefined,
    }
    // Crew tools first: they exist on every instance, Composio only with a key.
    const composio: ExplorerSection<ToolsSection>[] = composioStatus.configured
      ? MCP_SECTIONS.map((sec) => ({ ...sec, count: counts[sec.key] }))
      : SETUP_ONLY_SECTION
    return [CREW_TOOLS_SECTION, ...composio]
  }, [composioStatus.counts, composioStatus.configured])

  const notifyFacetGroups = React.useMemo(
    () => notificationFacets(rows, filters, setFilters),
    [rows, filters],
  )
  const mcpFacetGroups = React.useMemo(
    () => mcpFacets(composioStatus, mcpFilters, setMcpFilters),
    [composioStatus, mcpFilters],
  )

  // The panel lists the actual connections, not just section links — the
  // /routines rail lists routines for the same reason. Search and the Filter
  // popover narrow this list, so what you see on the left is what the main
  // column is showing.
  const connectionItems = React.useMemo(
    () =>
      visibleRows.map((r) => ({
        id: r.id,
        label: r.name,
        sublabel: r.providerLabel,
        mark: r.provider,
        dot: STATUS_DOT[r.status],
      })),
    [visibleRows],
  )

  const accountItems = React.useMemo(() => {
    const q = search.trim().toLowerCase()
    return composioStatus.accounts
      .filter((a) => {
        if (mcpFilters.toolkit && a.toolkit.slug !== mcpFilters.toolkit) return false
        if (mcpFilters.user && a.user_id !== mcpFilters.user) return false
        if (q && !a.toolkit.slug.toLowerCase().includes(q) && !a.user_id.toLowerCase().includes(q))
          return false
        return true
      })
      .map((a) => ({
        id: a.id,
        label: a.toolkit.slug,
        sublabel: a.user_id,
        mark: a.toolkit.slug,
        // Composio serves artwork for its whole catalog; without it these rows
        // fall back to two-letter tiles while the column beside them shows the
        // real icon for the same account.
        logoUrl: a.toolkit.logo || brandLogo(a.toolkit.slug),
        dot: a.status.toUpperCase() === "ACTIVE" ? "bg-success" : "bg-warn",
      }))
  }, [composioStatus.accounts, mcpFilters, search])

  const selectedConnection = React.useMemo(
    () => rows.find((r) => r.id === selectedConnectionId) ?? null,
    [rows, selectedConnectionId],
  )
  const selectedAccount = React.useMemo(
    () => composioStatus.accounts.find((a) => a.id === selectedAccountId) ?? null,
    [composioStatus.accounts, selectedAccountId],
  )

  /** This connection's slice of the log; null when the caller may not read it. */
  const selectedDeliveries = React.useMemo(() => {
    if (!canSeeDeliveries || !selectedConnectionId) return null
    return deliveries.filter((d) => d.channel_id === selectedConnectionId)
  }, [canSeeDeliveries, deliveries, selectedConnectionId])

  const notifySearchPlaceholder =
    notifySection === "deliveries" ? "Search deliveries…" : "Search connections…"

  const failing = rows.filter((r) => r.status === "failing").length
  // The number the Add-integration wizard will offer — see catalogSize.
  const serviceCount = providers.length > 0 ? catalogSize(providers.length) : 0
  const toolCount = composioStatus.counts.accounts

  return (
    <div className="flex h-[calc(100vh-48px)] flex-col bg-background">
      <SubBar<IntegrationsTab>
        icon={Plug}
        title="Integrations"
        description={
          tab === "tools" ? (
            composioStatus.configured ? (
              <>
                {composioStatus.counts.accounts} connected ·{" "}
                {composioStatus.counts.apps || "…"} apps available
                {composioStatus.keyLabel && ` · key ${composioStatus.keyLabel}`}
              </>
            ) : (
              <>Tools (MCP) — not configured</>
            )
          ) : (
            <>
              {rows.length} {rows.length === 1 ? "connection" : "connections"} ·{" "}
              {serviceCount} {serviceCount === 1 ? "service" : "services"} available
              {failing > 0 && ` · ${failing} failing`}
            </>
          )
        }
        ariaLabel="Integrations"
        tabs={TABS.map((t) => ({
          id: t.id,
          label: t.label,
          icon: t.icon,
          badge:
            t.id === "notifications"
              ? rows.length || undefined
              : toolCount || undefined,
        }))}
        activeTab={tab}
        onTabChange={setTab}
        tools={
          canManageWorkspace && tab === "notifications" && notifySection === "connections" ? (
            <label className="flex cursor-pointer items-center gap-1.5 pr-1 text-[11px] text-muted-foreground">
              <Switch
                size="sm"
                checked={includeEveryone}
                onCheckedChange={setIncludeEveryone}
                aria-label="Include everyone's personal connections"
              />
              <span className="hidden sm:inline">Everyone&apos;s connections</span>
            </label>
          ) : undefined
        }
        actions={
          <>
            <SubBarSecondary icon={RefreshCw} onClick={refreshAll} title="Reload every list">
              Refresh
            </SubBarSecondary>
            <SubBarPrimary
              icon={Plus}
              onClick={() => setAddOpen(true)}
              title="Connect a notification service, or managed tools for agents"
            >
              Add integration
            </SubBarPrimary>
          </>
        }
      />

      <div className="relative flex flex-1 overflow-hidden">
        {/* Tapping away closes the overlay; without it the only way back to
            the content on a phone is a collapse button the rail is covering. */}
        {isMobile && !collapsed && (
          <button
            type="button"
            aria-label="Close the integrations list"
            onClick={() => setCollapsed(true)}
            className="absolute inset-0 z-20 bg-black/50"
          />
        )}
        <aside
          className={cn(
            "shrink-0 border-r border-white/[0.06] bg-card transition-all",
            isMobile && !collapsed && "absolute inset-y-0 left-0 z-30 shadow-2xl",
            // `overflow-hidden` only while collapsing, where it is what keeps
            // the content from spilling out of a 36px rail. Leaving it on when
            // expanded clipped the Filter popover at the panel's edge — the
            // menu opened and was sliced off, which reads as a broken control.
            // The inner list keeps its own scroller, so nothing else needs it.
            collapsed ? "w-9 overflow-hidden" : "w-[280px] overflow-visible",
          )}
        >
          {collapsed ? (
            <div className="flex h-full flex-col items-center pt-1.5">
              <SidebarCollapseButton collapsed onToggle={() => setCollapsed(false)} />
            </div>
          ) : tab === "tools" ? (
            /* Same component, different inputs. The facets differ because
               Kind/Status/Scope/Service filter nothing here — but the toolbar,
               the Filter popover and the section rows are identical. */
            <IntegrationsExplorer<ToolsSection>
              sectionsLabel="Tools (MCP)"
              sections={mcpSections}
              section={composioStatus.configured || mcpSection === "crew-tools" ? mcpSection : "catalog"}
              onSectionChange={(next) => {
                if (next === "crew-tools" || composioStatus.configured) {
                  setMcpSection(next)
                  setSelectedAccountId(null)
                  if (isMobile) setCollapsed(true)
                } else {
                  setApiKeyOpen(true)
                }
              }}
              search={search}
              onSearchChange={setSearch}
              searchPlaceholder="Search apps, tools, agents…"
              searchAriaLabel="Search apps, tools and agents"
              facets={composioStatus.configured ? mcpFacetGroups : []}
              onClearFilters={() => setMcpFilters(EMPTY_MCP_FILTERS)}
              items={composioStatus.configured ? accountItems : []}
              itemsLabel="Connected accounts"
              selectedItemId={selectedAccountId}
              onItemSelect={setSelectedAccountId}
              itemsEmpty={
                <p className="px-3 py-2 text-[11px] leading-relaxed text-muted-foreground">
                  {composioStatus.configured
                    ? "No accounts match. Connect one from the app catalog."
                    : "The sections and accounts appear once an API key is saved."}
                </p>
              }
              onToggleCollapse={() => setCollapsed(true)}
              footer={
                /* The API key configures the instance; it is not something you
                   reach for beside "Add integration". It sits at the foot of
                   the panel it configures, showing its state rather than just
                   a button. */
                composioStatus.configured ? (
                  <button
                    type="button"
                    onClick={() => setApiKeyOpen(true)}
                    className="mx-1.5 mt-2 flex w-[calc(100%-0.75rem)] items-center gap-2 rounded-md px-2 py-1.5 text-left text-[11px] text-muted-foreground transition-colors hover:bg-white/[0.03] hover:text-foreground"
                  >
                    <KeyRound className="h-3 w-3 shrink-0" />
                    <span className="min-w-0 flex-1 truncate">
                      API key{composioStatus.keyLabel ? ` · ${composioStatus.keyLabel}` : ""}
                    </span>
                    <span className="shrink-0 text-[10px] text-muted-foreground/50">change</span>
                  </button>
                ) : undefined
              }
            />
          ) : (
            <IntegrationsExplorer<NotifySection>
              sectionsLabel="Notifications"
              sections={notifySections}
              section={notifySection}
              onSectionChange={setNotifySection}
              search={search}
              onSearchChange={setSearch}
              searchPlaceholder={notifySearchPlaceholder}
              searchAriaLabel="Search connections and deliveries"
              facets={notifyFacetGroups}
              onClearFilters={() => setFilters(EMPTY_FILTERS)}
              items={connectionItems}
              itemsLabel="Connections"
              selectedItemId={selectedConnectionId}
              onItemSelect={(id) => {
                setSelectedConnectionId(id)
                // A connection's detail belongs to the Connections view; picking
                // one while the preference matrix is showing should take you to
                // the thing you picked.
                if (id) setNotifySection("connections")
              }}
              itemsEmpty={
                <p className="px-3 py-3 text-[11px] leading-relaxed text-muted-foreground">
                  {rows.length === 0
                    ? "Nothing connected yet. Use Add integration to see every service this instance can reach."
                    : "Nothing matches the current search and filters."}
                </p>
              }
              onToggleCollapse={() => setCollapsed(true)}
            />
          )}
        </aside>

        <div className="relative flex-1 overflow-y-auto bg-background">
          <AnimatePresence mode="wait">
            <motion.div
              key={
                tab === "tools"
                  ? `tools:${selectedAccountId ?? mcpSection}`
                  : `notify:${selectedConnectionId ?? notifySection}`
              }
              initial={{ opacity: 0 }}
              animate={{ opacity: 1 }}
              exit={{ opacity: 0 }}
              transition={{ duration: 0.15 }}
            >
              {tab === "notifications" &&
                notifySection === "connections" &&
                selectedConnection && (
                  <ConnectionDetail
                    workspaceId={workspaceId}
                    row={selectedConnection}
                    deliveries={selectedDeliveries}
                    onBack={() => setSelectedConnectionId(null)}
                    onToggleEnabled={handleToggle}
                    onTest={handleTest}
                    onDelete={async (row) => {
                      if (!(await handleDelete(row))) return
                      setSelectedConnectionId(null)
                    }}
                  />
                )}

              {tab === "notifications" &&
                notifySection === "connections" &&
                !selectedConnection && (
                <ConnectionsView
                  rows={visibleRows}
                  totalRows={rows.length}
                  loading={channelsLoading && channels.length === 0}
                  error={channelsError}
                  canSeeDeliveries={canSeeDeliveries}
                  canManageWorkspace={canManageWorkspace}
                  search={search}
                  catalogMatches={catalogMatches}
                  onOpenAdd={() => setAddOpen(true)}
                  onToggleEnabled={handleToggle}
                  onTest={handleTest}
                  onDelete={async (row) => { await handleDelete(row) }}
                  onSelect={setSelectedConnectionId}
                />
              )}

              {tab === "tools" && selectedAccount && (
                <ToolAccountDetail
                  account={selectedAccount}
                  agents={composioStatus.agents}
                  bindings={composioStatus.bindings}
                  onBack={() => setSelectedAccountId(null)}
                />
              )}

              {tab === "tools" && !selectedAccount && mcpSection === "crew-tools" && (
                <CrewToolsView
                  workspaceId={workspaceId}
                  search={search}
                  initialServerId={linkedServerId}
                  onServerConsumed={() => setLinkedServerId(null)}
                  canManage={canManageWorkspace}
                />
              )}

              {tab === "tools" && !selectedAccount && mcpSection !== "crew-tools" && (
                <div className="space-y-4 p-4 md:p-6">
                  {composioStatus.configured && (
                    <>
                      <div className="flex flex-wrap items-baseline gap-x-2 gap-y-1">
                        <h2 className="text-sm font-medium text-foreground/90">
                          {MCP_SECTIONS.find((x) => x.key === mcpSection)?.label ?? "Tools"}
                        </h2>
                        <span className="text-xs text-muted-foreground">
                          {MCP_SECTIONS.find((x) => x.key === mcpSection)?.hint}
                        </span>
                      </div>
                      <div className="grid grid-cols-2 gap-4 sm:grid-cols-4">
                        <KpiCard
                          label="Connectable apps"
                          value={composioStatus.counts.apps || "—"}
                          subtitle="Composio catalog"
                        />
                        <KpiCard
                          label="Connected"
                          value={composioStatus.counts.accounts}
                          valueColor={
                            composioStatus.counts.accounts > 0 ? "rgb(52, 211, 153)" : undefined
                          }
                          subtitle={`across ${composioStatus.counts.users} ${composioStatus.counts.users === 1 ? "user" : "users"}`}
                        />
                        <KpiCard
                          label="Agents bound"
                          value={composioStatus.counts.agentsBound}
                          subtitle={`of ${composioStatus.counts.agentsTotal} agents`}
                        />
                        <KpiCard
                          label="MCP endpoints"
                          value={composioStatus.counts.endpoints}
                          subtitle="one per bound agent"
                        />
                      </div>
                    </>
                  )}
                  <ComposioIntegrations
                    embedded
                    section={composioStatus.configured ? (mcpSection as TabKey) : undefined}
                    search={search}
                    apiKeyOpen={apiKeyOpen}
                    onApiKeyOpenChange={setApiKeyOpen}
                    onStatus={setComposioStatus}
                  />
                </div>
              )}

              {tab === "notifications" && notifySection === "preferences" && (
                <div className="p-4 md:p-6">
                  <NotificationPrefsSection workspaceId={workspaceId} />
                </div>
              )}

              {tab === "notifications" && notifySection === "deliveries" && (
                <DeliveriesView
                  deliveries={deliveries}
                  rows={rows}
                  loading={deliveriesLoading}
                  error={deliveriesError}
                  forbidden={deliveriesForbidden}
                />
              )}
            </motion.div>
          </AnimatePresence>
        </div>
      </div>

      <ConfirmDialog
        open={pendingDelete !== null}
        onOpenChange={(open) => {
          if (!open) {
            pendingDelete?.resolve(false)
            setPendingDelete(null)
          }
        }}
        title={pendingDelete ? `Delete the connection to ${pendingDelete.row.name}?` : ""}
        consequences={[
          { tone: "lost", text: "Nothing is delivered there any more; routines that notify through it report a delivery failure" },
          { tone: "kept", text: "The delivery log keeps what was already sent" },
          { tone: "kept", text: "Add integration recreates it" },
        ]}
        confirmLabel="Delete connection"
        destructive
        onConfirm={async () => {
          if (!pendingDelete) return
          await deleteConnection(pendingDelete.row)
          pendingDelete.resolve(true)
        }}
      />
      <AddIntegrationDialog
        open={addOpen}
        onOpenChange={setAddOpen}
        services={services}
        sections={serviceSections}
        onPickService={handlePickService}
        onPickTools={handlePickTools}
        toolsConfigured={composioStatus.configured}
      />

      <AddChannelDialog
        target={addTarget}
        onClose={() => {
          setAddTarget(null)
          void refreshChannels()
        }}
        providers={providers}
        canCreateWorkspace={canManageWorkspace}
        create={create}
        sendDraftTest={sendDraftTest}
      />
    </div>
  )
}

function titleCase(s: string): string {
  return s.charAt(0).toUpperCase() + s.slice(1)
}
