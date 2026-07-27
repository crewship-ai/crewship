"use client"

import * as React from "react"
import { AnimatePresence, motion } from "motion/react"
import { Bell, Blocks, Clock, Link2, Plug, RefreshCw, Wrench } from "lucide-react"
import { toast } from "sonner"

import { SubBar, SubBarPrimary, SubBarSecondary } from "@/components/layout/sub-bar"
import { SidebarCollapseButton } from "@/components/layout/sidebar-kit"
import { Switch } from "@/components/ui/switch"
import { cn } from "@/lib/utils"
import { apiFetch } from "@/lib/api-fetch"
import { useAbilities } from "@/hooks/use-abilities"
import { useSession } from "@/hooks/use-auth"
import { useNotificationChannels } from "@/hooks/use-notification-channels"
import { useNotificationProviders } from "@/hooks/use-notification-providers"
import { useNotificationDeliveries } from "@/hooks/use-notification-deliveries"
import { NotificationPrefsSection } from "@/components/features/settings/sections/notification-prefs-section"
import { ComposioIntegrations } from "./composio-integrations"
import type { Inventory } from "./composio/types"

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
import { IntegrationsExplorer } from "./integrations-explorer"
import { ConnectionsView } from "./views/connections-view"
import { CatalogView, catalogSize, TOOLS_SECTION } from "./views/catalog-view"
import { DeliveriesView } from "./views/deliveries-view"
import { AddChannelDialog, type AddChannelTarget } from "./add-channel-dialog"

/**
 * /integrations — one page, the same chrome as Routines.
 *
 * This page used to be the only one in the app not built on the canonical
 * SubBar + sidebar-kit; eight others are. That is why it read as a different
 * program: no tabs, no facets, no search, and a creation form permanently
 * occupying the top of the screen above the list of what you already had.
 *
 * The four tabs are the four questions a person actually arrives with:
 *   Connections   — what is hooked up, and is it working?
 *   Catalog       — what COULD I hook up?
 *   Notifications — which categories reach me, on which connection?
 *   Deliveries    — why didn't that notification arrive?
 */

// Notifications and managed tools are two different KINDS of integration —
// one is where Crewship reaches a human, the other is what an agent may call —
// so they get their own tabs rather than being interleaved. What they share is
// this page: "what is this instance wired into" has one answer, not two
// screens that drift apart.
const TABS = [
  { id: "connections" as const, label: "Connections", icon: Link2 },
  { id: "catalog" as const, label: "Catalog", icon: Blocks },
  { id: "tools" as const, label: "Tools (MCP)", icon: Wrench },
  { id: "notifications" as const, label: "Notifications", icon: Bell },
  { id: "deliveries" as const, label: "Deliveries", icon: Clock },
] as const

type IntegrationsTab = (typeof TABS)[number]["id"]

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

  const [tab, setTab] = React.useState<IntegrationsTab>("connections")
  const [search, setSearch] = React.useState("")
  const [filters, setFilters] = React.useState<ConnectionFilters>(EMPTY_FILTERS)
  const [collapsed, setCollapsed] = React.useState(false)
  const [addTarget, setAddTarget] = React.useState<AddChannelTarget | null>(null)

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
    loading: providersLoading,
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

  const composio = useComposioInventory(workspaceId)

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

    // Composio connected accounts are connections too — the same question
    // ("is GitHub hooked up?") should not have two places to look. Their
    // lifecycle stays in the Composio surface, so the row is read-only here.
    const toolRows: ConnectionRow[] = (composio.inventory?.users ?? []).flatMap((u) =>
      u.connected_accounts.map((acct) => ({
        id: acct.id,
        kind: "tools" as const,
        name: acct.toolkit.slug,
        detail: `composio · ${acct.user_id}`,
        provider: "composio",
        providerLabel: "Composio",
        scope: "workspace" as const,
        enabled: acct.status.toUpperCase() === "ACTIVE",
        categories: [],
        status: acct.status.toUpperCase() === "ACTIVE" ? ("unknown" as const) : ("disabled" as const),
        sent24h: null,
        lastDelivery: null,
        source: "composio" as const,
        // Composio owns an account's lifecycle; editing it from here would be
        // a second, weaker copy of a flow that already exists.
        readOnly: true,
        account: acct,
      })),
    )

    return [...channelRows, ...toolRows]
  }, [
    channels,
    deliveries,
    composio.inventory,
    canSeeDeliveries,
    currentUserId,
    providerCategoryOf,
    providerLabelOf,
  ])

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
    void composio.refresh()
  }, [refreshChannels, refreshProviders, refreshDeliveries, composio])

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

  const handleDelete = async (row: ConnectionRow) => {
    if (!window.confirm(`Delete the connection to ${row.name}?`)) return
    try {
      await remove(row.id)
      toast.success("Connection deleted")
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "Failed to delete the connection")
    }
  }

  const handlePick = (entry: { key: string; section: string; label: string }) => {
    if (entry.section === TOOLS_SECTION) {
      // Composio owns its own connect flow (OAuth per toolkit), so the card is
      // a doorway to that surface rather than a dead end — which is what it
      // was when this tab did not exist.
      setTab("tools")
      return
    }
    setAddTarget(
      entry.key === "email" || entry.key === "webhook"
        ? { kind: entry.key, label: entry.label }
        : { kind: "shoutrrr", provider: entry.key, label: entry.label },
    )
  }

  const searchPlaceholder =
    tab === "catalog" ? "Search services…" : tab === "deliveries" ? "Search deliveries…" : "Search connections…"

  const failing = rows.filter((r) => r.status === "failing").length
  // Same number the Catalog tab renders — see catalogSize's comment.
  const serviceCount = providers.length > 0 ? catalogSize(providers.length) : 0
  const toolCount = rows.filter((r) => r.kind === "tools").length

  return (
    <div className="flex h-[calc(100vh-48px)] flex-col bg-background">
      <SubBar<IntegrationsTab>
        icon={Plug}
        title="Integrations"
        description={
          <>
            {rows.length} {rows.length === 1 ? "connection" : "connections"} ·{" "}
            {serviceCount} {serviceCount === 1 ? "service" : "services"} available
            {failing > 0 && ` · ${failing} failing`}
          </>
        }
        ariaLabel="Integrations"
        tabs={TABS.map((t) => ({
          id: t.id,
          label: t.label,
          icon: t.icon,
          badge:
            t.id === "connections"
              ? rows.length || undefined
              : t.id === "catalog"
                ? serviceCount || undefined
                : t.id === "tools"
                  ? toolCount || undefined
                  : undefined,
        }))}
        activeTab={tab}
        onTabChange={setTab}
        tools={
          canManageWorkspace && tab === "connections" ? (
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
              icon={Plug}
              onClick={() => setTab("catalog")}
              title="Browse every service this instance can deliver to"
            >
              Add connection
            </SubBarPrimary>
          </>
        }
      />

      <div className="flex flex-1 overflow-hidden">
        <aside
          className={cn(
            "shrink-0 overflow-hidden border-r border-white/[0.06] bg-card transition-all",
            collapsed ? "w-9" : "w-[280px]",
          )}
        >
          {collapsed ? (
            <div className="flex h-full flex-col items-center pt-1.5">
              <SidebarCollapseButton collapsed onToggle={() => setCollapsed(false)} />
            </div>
          ) : (
            <IntegrationsExplorer
              rows={rows}
              search={search}
              onSearchChange={setSearch}
              filters={filters}
              onFiltersChange={setFilters}
              onToggleCollapse={() => setCollapsed(true)}
              searchPlaceholder={searchPlaceholder}
            />
          )}
        </aside>

        <div className="relative flex-1 overflow-y-auto bg-background">
          <AnimatePresence mode="wait">
            <motion.div
              key={tab}
              initial={{ opacity: 0 }}
              animate={{ opacity: 1 }}
              exit={{ opacity: 0 }}
              transition={{ duration: 0.15 }}
            >
              {tab === "connections" && (
                <ConnectionsView
                  rows={visibleRows}
                  totalRows={rows.length}
                  loading={channelsLoading && channels.length === 0}
                  error={channelsError}
                  canSeeDeliveries={canSeeDeliveries}
                  canManageWorkspace={canManageWorkspace}
                  search={search}
                  catalogMatches={catalogMatches}
                  onOpenCatalog={() => setTab("catalog")}
                  onToggleEnabled={handleToggle}
                  onTest={handleTest}
                  onDelete={handleDelete}
                />
              )}

              {tab === "catalog" && (
                <CatalogView
                  providers={providers}
                  categories={providerCategories}
                  usage={usage}
                  loading={providersLoading && providers.length === 0}
                  search={search}
                  onPick={handlePick}
                  composioConfigured={composio.inventory?.enabled ?? false}
                />
              )}

              {tab === "tools" && (
                <ComposioIntegrations />
              )}

              {tab === "notifications" && (
                <div className="p-4 md:p-6">
                  <NotificationPrefsSection workspaceId={workspaceId} />
                </div>
              )}

              {tab === "deliveries" && (
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

/**
 * Composio's connected accounts, read-only.
 *
 * Kept to the one endpoint the Connections table needs. The full management
 * surface (agent access, tools, triggers, MCP endpoints) stays where it is;
 * duplicating it here would recreate the two-places-for-one-thing problem
 * this page is fixing.
 */
function useComposioInventory(workspaceId: string | null) {
  const [inventory, setInventory] = React.useState<Inventory | null>(null)

  const refresh = React.useCallback(async () => {
    if (!workspaceId) return
    try {
      const r = await apiFetch(
        `/api/v1/integrations/composio/inventory?workspace_id=${encodeURIComponent(workspaceId)}`,
      )
      if (!r.ok) {
        setInventory(null)
        return
      }
      setInventory((await r.json()) as Inventory)
    } catch {
      // Composio being unreachable must not take the notification channels
      // down with it — the tools rows simply do not appear.
      setInventory(null)
    }
  }, [workspaceId])

  React.useEffect(() => {
    void refresh()
  }, [refresh])

  return { inventory, refresh }
}

function titleCase(s: string): string {
  return s.charAt(0).toUpperCase() + s.slice(1)
}
