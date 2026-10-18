"use client"

import * as React from "react"
import { Plug, Plus, Terminal } from "lucide-react"
import { toast } from "@/lib/toast"

import { SpaceCrumb } from "./space-crumb"
import { SubBar, SubBarIconButton, SubBarPrimary } from "@/components/layout/sub-bar"
import { SecondaryPanel } from "@/components/layout/secondary-panel"
import { CreateSurface, CreateSurfaceBody, CreateSurfaceHeader } from "@/components/layout/create-surface"
import { PageSaveBar, PageSaveProvider, usePageSaveGuard } from "@/components/ui/page-save-bar"
import { useAbilities } from "@/hooks/use-abilities"
import { useSession } from "@/hooks/use-auth"
import { useIsMobile } from "@/hooks/use-mobile"
import { useUserPreference } from "@/hooks/use-user-preference"
import { useNotificationChannels, type ChannelPatchBody } from "@/hooks/use-notification-channels"
import { useNotificationProviders } from "@/hooks/use-notification-providers"
import { useNotificationDeliveries } from "@/hooks/use-notification-deliveries"
import { NotificationPrefsSection } from "./notification-prefs-section"
import { ComposioIntegrations, type ComposioStatus } from "./composio-integrations"
import { McpEndpointsTab } from "./composio/mcp-endpoints-tab"
import { channelKind, deriveStatus, isForeignPersonal, rowMatches, type ConnectionRow } from "./connection-model"
import { CrewToolsView } from "./views/crew-tools-view"
import { IncomingWebhooksView } from "./views/incoming-webhooks-view"
import { NewTriggerDialog } from "./new-trigger-dialog"
import { useIncomingEndpoints } from "./use-incoming-endpoints"
import type { IncomingKind, IncomingTarget } from "./incoming-model"
import { AddChannelDialog, type AddChannelMode } from "./add-channel-dialog"
import { ChannelsList, DeliveriesLog, matchesView, type ChannelsView } from "./channels-space"
import { ChannelPage, CHANNEL_PANES, type ChannelPane } from "./channel-page"
import { TriggerPage, TriggersList, TRIGGER_PANES, type TriggerPane } from "./triggers-space"
import { filterTriggers, triggerRows, type TriggerView } from "./triggers-model"
import { AppAccountPage, APP_PANES, CatalogAppPage, CatalogView, ToolSearchResults, ToolsList, type AppPane } from "./tools-space"
import { accountRows, filterAccounts, TOOL_SEARCH_MIN, type ToolsListView } from "./tools-model"
import { EMPTY_PANEL_FILTERS, IntegrationsPanel, SPACE_META, SpaceSwitcher, type PanelFilters } from "./integrations-panel"
import { CliLines, SpaceHead } from "./integrations-ui"
import { defaultView, integrationsSearch, parseIntegrationsRoute, SPACES, type IntegrationsRoute, type IntegrationsSpace } from "./spaces"

/**
 * /integrations — four spaces in the shared SecondaryPanel (Harbor 2):
 * Tools, Triggers, Channels, Webhooks. Each has its views, one primary action
 * in the top bar, and object pages at `?id=` with tabs at `&pane=`. Old
 * `?tab=&section=` links are read by spaces.ts.
 */

/** Kept for links and tests that still import it: the old three-tab route, now a space route. */
export { parseIntegrationsRoute as initialIntegrationsRoute } from "./spaces"

/** How far back "Sent today" and a channel's state look. */
const DELIVERY_LIMIT = 200

const EMPTY_STATUS: ComposioStatus = {
  loading: true,
  configured: false,
  keyLabel: null,
  counts: { apps: 0, accounts: 0, users: 0, agentsBound: 0, agentsTotal: 0, endpoints: 0 },
  toolkits: [],
  users: [],
  accounts: [],
  agents: [],
  bindings: {},
}

const CLI_LINES: [string, string][] = [
  ["crewship integration composio toolkits", "Apps in Composio"],
  ["crewship integration composio connect github --user <user>", "Connect an app"],
  ["crewship integration composio bind <agent> --user <user> --app github:read", "Give an agent access"],
  ["crewship routine webhooks list", "Triggers of routines"],
  ["crewship routine webhooks create --slug <routine> --name github-prs --ingress-profile github", "New trigger"],
  ["crewship notifychannel list", "Channels and webhooks"],
  ["crewship notifychannel add --type webhook --url https://…", "New webhook"],
]

export function IntegrationsLayout({ workspaceId }: { workspaceId: string }) {
  // Dialogs, selection and drafts belong to one workspace. Edits on an
  // object page (app access, a trigger's mapping) wait on one Save bar.
  return (
    <PageSaveProvider key={workspaceId}>
      <WorkspaceIntegrationsLayout workspaceId={workspaceId} />
    </PageSaveProvider>
  )
}

function WorkspaceIntegrationsLayout({ workspaceId }: { workspaceId: string }) {
  const { abilities } = useAbilities()
  const canManageWorkspace = abilities.can("manage", "Workspace")
  const currentUserId = useSession().data?.user.id ?? null
  const isMobile = useIsMobile()
  const [lastSpace, setLastSpace] = useUserPreference<IntegrationsSpace>("integrations.space", "tools")

  // Prerendered page: the first render must not read the URL. It is applied
  // before paint instead, so a deep link never flashes the default.
  const [route, setRouteState] = React.useState<IntegrationsRoute>(() => parseIntegrationsRoute("", "tools"))
  const [search, setSearch] = React.useState("")
  const [filters, setFilters] = React.useState<PanelFilters>(EMPTY_PANEL_FILTERS)
  const lastSpaceRef = React.useRef(lastSpace)
  lastSpaceRef.current = lastSpace

  React.useLayoutEffect(() => {
    const read = () => setRouteState(parseIntegrationsRoute(window.location.search, SPACES.includes(lastSpaceRef.current) ? lastSpaceRef.current : "tools"))
    read()
    // An old ?tab=&section= link is rewritten to the space it now names.
    const first = parseIntegrationsRoute(window.location.search, SPACES.includes(lastSpaceRef.current) ? lastSpaceRef.current : "tools")
    const q = integrationsSearch(window.location.search, first)
    if (window.location.search.slice(1) !== q) window.history.replaceState(null, "", `${window.location.pathname}?${q}${window.location.hash}`)
    window.addEventListener("popstate", read)
    return () => window.removeEventListener("popstate", read)
  }, [])

  /** Moves; a new object or space is a history entry, a view or tab change is not. */
  const setRoute = React.useCallback((next: IntegrationsRoute) => {
    setRouteState((prev) => {
      const q = integrationsSearch(window.location.search, next)
      const url = `${window.location.pathname}?${q}${window.location.hash}`
      if (prev.space !== next.space || prev.id !== next.id) window.history.pushState(null, "", url)
      else window.history.replaceState(null, "", url)
      return next
    })
  }, [])
  // Leaving an object page with unsaved edits asks first.
  const guard = usePageSaveGuard()
  const [toolFocus, setToolFocus] = React.useState<string | null>(null)
  const goSpace = React.useCallback(
    (space: IntegrationsSpace) =>
      guard(() => {
        setSearch("")
        setFilters(EMPTY_PANEL_FILTERS)
        setLastSpace(space)
        setToolFocus(null)
        setRoute({ space, view: defaultView(space), id: null, pane: null, server: null, starts: null, target: null })
      }),
    [setRoute, setLastSpace, guard],
  )
  const goView = (view: string) => guard(() => setRoute({ ...route, view, id: null, pane: null }))
  const open = (id: string, view = route.view) => guard(() => setRoute({ ...route, view, id, pane: null }))
  const back = () =>
    guard(() => {
      setToolFocus(null)
      setRoute({ ...route, id: null, pane: null })
    })
  // A trigger's Mapping draft lives on its tab; app access is kept across tabs.
  const setPane = (pane: string) => (route.space === "triggers" ? guard(() => setRoute({ ...route, pane })) : setRoute({ ...route, pane }))
  const openTool = (accountId: string, tool: string) =>
    guard(() => {
      setToolFocus(tool)
      setRoute({ ...route, view: "connected", id: accountId, pane: "tools" })
    })

  // 1–4 switch spaces, as the panel's hints say.
  React.useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.metaKey || e.ctrlKey || e.altKey) return
      const t = e.target as HTMLElement | null
      if (t && (t.isContentEditable || /^(INPUT|TEXTAREA|SELECT)$/.test(t.tagName))) return
      if (document.querySelector("[role=dialog],[role=alertdialog]")) return
      const i = ["1", "2", "3", "4"].indexOf(e.key)
      if (i >= 0) goSpace(SPACES[i])
    }
    window.addEventListener("keydown", onKey)
    return () => window.removeEventListener("keydown", onKey)
  }, [goSpace])

  // ---- data ----------------------------------------------------------------
  const includeEveryone = filters.whose === "everyone" && canManageWorkspace
  const { channels, loading: channelsLoading, error: channelsError, refresh: refreshChannels, create, remove, sendTest, sendDraftTest, patch } = useNotificationChannels(workspaceId, { includeEveryone })
  const { providers } = useNotificationProviders(workspaceId)
  const { deliveries, loading: deliveriesLoading, error: deliveriesError, forbidden: deliveriesForbidden, refresh: refreshDeliveries } = useNotificationDeliveries(workspaceId, { limit: DELIVERY_LIMIT })
  const canSeeDeliveries = !deliveriesForbidden
  const incoming = useIncomingEndpoints(workspaceId, true)
  const [composio, setComposio] = React.useState<ComposioStatus>(EMPTY_STATUS)
  const [apiKeyOpen, setApiKeyOpen] = React.useState(false)
  const [connectRequest, setConnectRequest] = React.useState<{ toolkit?: { slug: string; name: string } } | null>(null)
  const [composioRefresh, setComposioRefresh] = React.useState(0)
  const clearConnectRequest = React.useCallback(() => setConnectRequest(null), [])

  const providerOf = React.useCallback((name: string) => providers.find((p) => p.provider === name), [providers])

  const rows = React.useMemo<ConnectionRow[]>(() => {
    const byChannel = new Map<string, typeof deliveries>()
    for (const d of deliveries) byChannel.set(d.channel_id, [...(byChannel.get(d.channel_id) ?? []), d])
    return channels.map((ch) => {
      const mine = byChannel.get(ch.id) ?? []
      const provider = ch.type === "shoutrrr" ? (ch.provider ?? "unknown") : ch.type
      const target = ch.type === "email" ? ch.to : ch.type === "webhook" ? ch.url : undefined
      const foreign = isForeignPersonal(ch, currentUserId)
      const label = ch.type === "shoutrrr" ? (providerOf(provider)?.label ?? provider) : ch.type === "email" ? "Email" : "Webhook"
      return {
        id: ch.id,
        kind: channelKind(ch, (n) => providerOf(n)?.category),
        name: foreign ? `${label} — another member's` : ch.type === "webhook" ? (target ?? label).replace(/^https?:\/\//, "") : target || label,
        detail: `${provider}${ch.scope === "user" ? " · personal" : ""}`,
        provider,
        providerLabel: label,
        scope: ch.scope === "user" ? "personal" : "workspace",
        enabled: ch.enabled,
        categories: ch.categories ?? [],
        status: deriveStatus(ch.enabled, canSeeDeliveries ? mine : null),
        sent24h: null,
        lastDelivery: mine.map((d) => d.created_at).sort().at(-1) ?? null,
        source: "channel",
        readOnly: foreign,
        channel: ch,
      } satisfies ConnectionRow
    })
  }, [channels, deliveries, canSeeDeliveries, currentUserId, providerOf])

  const channelRows = React.useMemo(() => rows.filter((r) => r.kind !== "webhook"), [rows])
  const webhookRows = React.useMemo(() => rows.filter((r) => r.kind === "webhook"), [rows])
  const spaceRows = route.space === "webhooks" ? webhookRows : channelRows
  const channelView = (route.space === "webhooks" || route.space === "channels") && ["all", "failing", "paused", "mine"].includes(route.view) ? (route.view as ChannelsView) : "all"
  const visibleChannels = spaceRows.filter(
    (r) =>
      matchesView(r, channelView, currentUserId) &&
      (route.space !== "channels" || !filters.kind || r.kind === filters.kind) &&
      (route.space !== "channels" || !filters.whose || (filters.whose === "everyone" ? r.readOnly : r.scope === filters.whose)) &&
      rowMatches(r, search),
  )

  const tRows = React.useMemo(() => triggerRows(incoming.rows), [incoming.rows])
  const triggerView = (["all", "failing", "off"].includes(route.view) ? route.view : "all") as TriggerView
  const visibleTriggers = filterTriggers(tRows, { view: triggerView, starts: route.starts, target: route.target, checks: filters.checks, search })
  const pageTargets = incoming.targets.filter((t) => t.kind === "page" && (!route.target || t.id === route.target || t.slug === route.target) && `${t.name} ${t.slug}`.toLowerCase().includes(search.trim().toLowerCase()))

  const aRows = React.useMemo(() => accountRows(composio.accounts, composio.agents, composio.bindings), [composio.accounts, composio.agents, composio.bindings])
  const toolsView = (["connected", "attention", "idle"].includes(route.view) ? route.view : "connected") as ToolsListView
  const visibleAccounts = filterAccounts(aRows, toolsView, search, null)
  const connectedSlugs = React.useMemo(() => new Set(composio.accounts.map((a) => a.toolkit.slug)), [composio.accounts])

  // ---- actions ---------------------------------------------------------------
  const active = React.useRef(true)
  React.useEffect(() => () => void (active.current = false), [])
  const onPatch = async (id: string, body: ChannelPatchBody) => {
    await patch(id, body)
  }
  const onTest = async (row: ConnectionRow) => {
    try {
      await sendTest(row.id)
      if (active.current) toast.success("Test sent", { description: `to ${row.name}` })
    } catch (e) {
      if (active.current) toast.error("Test send failed", { description: e instanceof Error ? e.message : undefined })
    }
  }
  const onRemove = async (row: ConnectionRow) => {
    try {
      await remove(row.id)
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "The channel was not removed")
      throw e
    }
    toast.success(row.kind === "webhook" ? "Webhook removed" : "Channel removed")
    back()
  }

  const [addMode, setAddMode] = React.useState<AddChannelMode | null>(null)
  const [incomingCreate, setIncomingCreate] = React.useState<{ target?: IncomingTarget } | null>(null)
  const [cliOpen, setCliOpen] = React.useState(false)
  const [pageTarget, setPageTarget] = React.useState<IncomingTarget | null>(null)

  const primary = () => {
    if (route.space === "tools") {
      if (composio.configured) setConnectRequest({})
      else setApiKeyOpen(true)
    } else if (route.space === "triggers") setIncomingCreate({})
    else setAddMode(route.space === "webhooks" ? "webhook" : "channel")
  }

  // ---- main column -------------------------------------------------------------
  let main: React.ReactNode
  let objectName: string | null = null
  if (route.space === "channels" || route.space === "webhooks") {
    const row = route.id ? rows.find((r) => r.id === route.id) ?? null : null
    if (route.id) {
      objectName = row?.name ?? null
      main = row ? (
        <ChannelPage
          workspaceId={workspaceId}
          row={row}
          pane={(CHANNEL_PANES as readonly string[]).includes(route.pane ?? "") ? (route.pane as ChannelPane) : "overview"}
          onPane={setPane}
          deliveries={canSeeDeliveries ? deliveries.filter((d) => d.channel_id === row.id) : null}
          providerOff={row.channel?.type === "shoutrrr" && providerOf(row.provider)?.enabled === false}
          canEdit={!row.readOnly && (row.scope === "personal" || canManageWorkspace)}
          onBack={back}
          onPatch={onPatch}
          onTest={onTest}
          onRemove={onRemove}
        />
      ) : (
        <ChannelsList webhooks={route.space === "webhooks"} rows={[]} allRows={channelsLoading ? [] : spaceRows} view="all" kind={null} whose={null} search="" deliveries={[]} canSeeDeliveries={false} currentUserId={currentUserId} loading={channelsLoading} error={channelsError} onRetry={() => void refreshChannels()} onOpen={open} onAdd={primary} onView={goView} onClearFilters={back} />
      )
    } else if (route.space === "channels" && route.view === "preferences") {
      main = (
        <div className="grid min-w-0 gap-3.5">
          <SpaceHead title="My notifications" description="Which kinds of events reach you, and on which of your channels." />
          <NotificationPrefsSection workspaceId={workspaceId} />
        </div>
      )
    } else if (route.space === "channels" && route.view === "deliveries") {
      main = <DeliveriesLog deliveries={deliveries} rows={rows} loading={deliveriesLoading} error={deliveriesError} forbidden={deliveriesForbidden} onRetry={() => void refreshDeliveries()} onOpenChannel={(id) => setRoute({ ...route, space: rows.find((r) => r.id === id)?.kind === "webhook" ? "webhooks" : "channels", view: "all", id, pane: "deliveries" })} />
    } else {
      main = (
        <ChannelsList
          webhooks={route.space === "webhooks"}
          rows={visibleChannels}
          allRows={spaceRows}
          view={channelView}
          kind={route.space === "channels" ? filters.kind : null}
          whose={route.space === "channels" && filters.whose !== "everyone" ? filters.whose : null}
          search={search}
          deliveries={deliveries}
          canSeeDeliveries={canSeeDeliveries}
          currentUserId={currentUserId}
          loading={channelsLoading}
          error={channelsError}
          onRetry={() => void refreshChannels()}
          onOpen={(id) => open(id)}
          onAdd={primary}
          onView={goView}
          onClearFilters={() => {
            setSearch("")
            setFilters(EMPTY_PANEL_FILTERS)
            goView("all")
          }}
        />
      )
    }
  } else if (route.space === "triggers") {
    if (pageTarget) {
      main = <IncomingWebhooksView workspaceId={workspaceId} data={incoming} section="page" search="" targetId={pageTarget.id} onSelect={setPageTarget} onBack={() => setPageTarget(null)} onAdd={(t) => setIncomingCreate({ target: t })} />
    } else if (route.id) {
      const row = tRows.find((r) => r.id === route.id) ?? null
      objectName = row?.name ?? null
      main = (
        <TriggerPage
          workspaceId={workspaceId}
          row={row}
          data={incoming}
          pane={(TRIGGER_PANES as readonly string[]).includes(route.pane ?? "") ? (route.pane as TriggerPane) : "overview"}
          onPane={setPane}
          canEdit={canManageWorkspace}
          onBack={back}
          onDeleted={back}
        />
      )
    } else {
      main = (
        <TriggersList
          rows={visibleTriggers}
          allRows={tRows}
          pages={pageTargets}
          view={triggerView}
          starts={route.starts}
          filtered={triggerView !== "all" || route.starts !== null || filters.checks !== null || search.trim() !== ""}
          loading={incoming.loading}
          error={incoming.error}
          onRetry={incoming.refresh}
          onOpen={(id) => open(id)}
          onOpenTarget={setPageTarget}
          onAdd={primary}
          onView={goView}
          onClearFilters={() => {
            setSearch("")
            setFilters(EMPTY_PANEL_FILTERS)
            setRoute({ ...route, view: "all", starts: null, target: null, id: null })
          }}
        />
      )
    }
  } else {
    if (route.view === "catalog" && route.id) {
      const app = composio.catalog?.find((t) => t.slug === route.id)
      objectName = app?.name ?? route.id
      main = (
        <CatalogAppPage
          app={app}
          slug={route.id}
          connectedAccountId={composio.accounts.find((a) => a.toolkit.slug === route.id)?.id ?? null}
          canManage={canManageWorkspace}
          configured={composio.configured}
          onBack={back}
          onConnect={() => setConnectRequest({ toolkit: { slug: route.id!, name: app?.name ?? route.id! } })}
          onOpenConnected={(id) => open(id, "connected")}
        />
      )
    } else if (route.view === "catalog") {
      main = <CatalogView catalog={composio.catalog ?? []} loading={composio.catalogLoading ?? composio.loading} total={composio.counts.apps} connected={connectedSlugs} configured={composio.configured} canManage={canManageWorkspace} onOpen={(slug) => open(slug)} onAddKey={() => setApiKeyOpen(true)} />
    } else if (route.view === "crew-tools") {
      main = (
        <div className="grid min-w-0 gap-3.5">
          <SpaceHead title="Crew MCP servers" description="MCP servers a crew's agents call, and whether each has a credential." />
          <CrewToolsView workspaceId={workspaceId} search={search} initialServerId={route.server} onServerConsumed={() => setRoute({ ...route, server: null })} canManage={canManageWorkspace} />
        </div>
      )
    } else if (route.view === "mcp") {
      main = (
        <div className="grid min-w-0 gap-3.5">
          <SpaceHead title="MCP endpoints" description="How each agent with an app reaches Composio. For diagnosis." />
          <McpEndpointsTab agents={composio.agents} bindings={composio.bindings} loading={composio.loading} />
        </div>
      )
    } else if (route.id) {
      const row = aRows.find((r) => r.account.id === route.id) ?? null
      objectName = row ? row.slug : null
      main = (
        <AppAccountPage
          key={route.id}
          workspaceId={workspaceId}
          row={row}
          status={composio}
          pane={(APP_PANES as readonly string[]).includes(route.pane ?? "") ? (route.pane as AppPane) : "overview"}
          onPane={setPane}
          canManage={canManageWorkspace}
          onBack={back}
          onChanged={() => setComposioRefresh((n) => n + 1)}
          onGone={back}
          onTriggers={() => goSpace("triggers")}
          toolFocus={toolFocus ?? undefined}
        />
      )
    } else {
      main = (
        <ToolsList
          rows={visibleAccounts}
          allRows={aRows}
          view={toolsView}
          status={composio}
          filtered={toolsView !== "connected" || search.trim() !== ""}
          canManage={canManageWorkspace}
          onOpen={(id) => open(id)}
          onConnect={primary}
          onView={goView}
          onAddKey={() => setApiKeyOpen(true)}
          onClearFilters={() => {
            setSearch("")
            goView("connected")
          }}
          toolSearch={
            composio.configured && search.trim().length >= TOOL_SEARCH_MIN ? (
              <ToolSearchResults workspaceId={workspaceId} apps={aRows.filter((r) => !r.signedOut).map((r) => ({ slug: r.slug, accountId: r.account.id }))} query={search} onOpenTool={openTool} />
            ) : undefined
          }
        />
      )
    }
  }

  const meta = SPACE_META[route.space]
  const startsCounts: Partial<Record<IncomingKind, number>> = {
    routine: tRows.filter((r) => r.kind === "routine").length,
    agent: tRows.filter((r) => r.kind === "agent").length,
    page: incoming.targets.filter((t) => t.kind === "page").length,
  }
  const checksOptions = ["GitHub signature", "Crewship signature"].map((label) => ({ label, count: tRows.filter((r) => r.checks === label).length })).filter((o) => o.count > 0)

  return (
    <div className="flex h-[calc(100dvh-var(--app-header-h)-var(--mobile-tab-bar-h))] flex-col bg-background">
      <SubBar
        icon={Plug}
        title="Integrations"
        section={
          <>
            <SpaceCrumb area="Integrations" value={route.space} options={SPACES.map((k) => ({ key: k, label: SPACE_META[k].label }))} onChange={goSpace} />
            {objectName && <span className="font-semibold"> › {objectName}</span>}
          </>
        }
        heading={false}
        ariaLabel="Integrations"
        actions={
          <>
            <SubBarIconButton icon={Terminal} aria-label="Command line" title="Command line" onClick={() => setCliOpen(true)} />
            <SubBarPrimary icon={Plus} onClick={primary} aria-label={meta.add} data-action="space-primary">
              <span className="max-sm:sr-only">{meta.add}</span>
            </SubBarPrimary>
          </>
        }
      />

      <div className="relative flex flex-1 overflow-hidden">
        <SecondaryPanel id="integrations" label="Integrations">
          <IntegrationsPanel
            space={route.space}
            view={route.view}
            hasObject={route.id !== null || pageTarget !== null}
            starts={route.starts}
            search={search}
            onSearch={setSearch}
            onSpace={goSpace}
            onView={(v) => {
              setPageTarget(null)
              goView(v)
            }}
            onStarts={(k) => {
              setPageTarget(null)
              setRoute({ ...route, view: "all", id: null, pane: null, starts: k, target: null })
            }}
            filters={filters}
            onFilters={setFilters}
            spaceCounts={{ tools: composio.configured ? aRows.length : undefined, triggers: incoming.error ? undefined : tRows.length, channels: channelRows.length, webhooks: webhookRows.length }}
            viewCounts={
              route.space === "tools"
                ? { connected: aRows.length, attention: aRows.filter((r) => r.signedOut).length, idle: aRows.filter((r) => r.agents.length === 0).length, catalog: composio.counts.apps || undefined }
                : route.space === "triggers"
                  ? { all: tRows.length, failing: tRows.filter((r) => r.failing).length, off: tRows.filter((r) => !r.enabled).length }
                  : { all: spaceRows.length, failing: spaceRows.filter((r) => r.status === "failing").length, paused: spaceRows.filter((r) => !r.enabled).length, mine: spaceRows.filter((r) => matchesView(r, "mine", currentUserId)).length }
            }
            kindCounts={{ chat: channelRows.filter((r) => r.kind === "chat").length, email: channelRows.filter((r) => r.kind === "email").length, push: channelRows.filter((r) => r.kind === "push").length, incident: channelRows.filter((r) => r.kind === "incident").length }}
            startsCounts={startsCounts}
            checksOptions={checksOptions}
            isAdmin={canManageWorkspace}
            canSeeDeliveries={canSeeDeliveries}
            onComposioKey={() => (canManageWorkspace ? setApiKeyOpen(true) : toast.info("Ask an admin to change the Composio key"))}
            composioKeyLabel={composio.keyLabel}
          />
        </SecondaryPanel>

        <div className="relative flex min-w-0 flex-1 flex-col">
          <div className="relative min-w-0 flex-1 overflow-y-auto bg-background" data-slot="integrations-main">
            {/* Bottom room so the Save bar never covers the last card. */}
            <div className="mx-auto grid w-full max-w-[1240px] min-w-0 gap-3.5 px-4 pb-24 pt-4 md:px-7 md:pt-6">
              {isMobile && <SpaceSwitcher segmented space={route.space} counts={{}} onSpace={goSpace} />}
              {main}
            </div>
          </div>
          <PageSaveBar />
        </div>
      </div>

      {/* Composio loads here for every space (counts, the catalog) and hosts
          its own dialogs: the API key and Connect with its sign-in popup. */}
      <ComposioIntegrations
        embedded
        headless
        search={route.space === "tools" && route.view === "catalog" ? search : ""}
        apiKeyOpen={apiKeyOpen}
        onApiKeyOpenChange={setApiKeyOpen}
        onStatus={setComposio}
        connectRequest={connectRequest}
        onConnectRequestHandled={clearConnectRequest}
        refreshKey={composioRefresh}
      />

      <AddChannelDialog
        mode={addMode}
        onClose={() => setAddMode(null)}
        providers={providers}
        canCreateWorkspace={canManageWorkspace}
        create={create}
        sendDraftTest={sendDraftTest}
        onCreated={(id, webhook) => setRoute({ ...route, space: webhook ? "webhooks" : "channels", view: "all", id, pane: null })}
      />
      {incomingCreate && (
        <NewTriggerDialog
          key={workspaceId}
          workspaceId={workspaceId}
          data={incoming}
          initialTarget={incomingCreate.target}
          onClose={() => setIncomingCreate(null)}
          onCreated={(t) => {
            if (t.kind === "page") setPageTarget(t)
            else setRoute({ ...route, space: "triggers", view: "all", id: null, starts: t.kind, target: t.id })
          }}
        />
      )}
      {cliOpen && (
        <CreateSurface open onOpenChange={(o) => !o && setCliOpen(false)} size="md">
          <CreateSurfaceHeader icon={Terminal} title="Integrations from the command line" onClose={() => setCliOpen(false)} />
          <CreateSurfaceBody>
            <CliLines lines={CLI_LINES} />
          </CreateSurfaceBody>
        </CreateSurface>
      )}
    </div>
  )
}
