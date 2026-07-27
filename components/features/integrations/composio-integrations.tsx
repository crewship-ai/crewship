"use client"

import * as React from "react"
import Link from "next/link"
import { Plug, ShieldCheck, KeyRound, RefreshCw } from "lucide-react"

import { useWorkspace } from "@/hooks/use-workspace"
import { Button } from "@/components/ui/button"
import { Skeleton } from "@/components/ui/skeleton"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { cn } from "@/lib/utils"
import { apiFetch } from "@/lib/api-fetch"
import { invalidate, readThrough } from "@/lib/stale-cache"

import type {
  ConnectedAccount,
  Inventory,
  ToolkitInfo,
  ToolkitsResp,
  ComposioSettings,
  AgentLite,
  AgentBindingsMap,
  TabKey,
} from "./composio/types"
import { CatalogTab } from "./composio/catalog-tab"
import { ConnectedAccountsTab } from "./composio/connected-accounts-tab"
import { AgentAccessTab } from "./composio/agent-access-tab"
import { ToolsTab } from "./composio/tools-tab"
import { TriggersTab } from "./composio/triggers-tab"
import { McpEndpointsTab } from "./composio/mcp-endpoints-tab"

// Managed-integration (Composio) admin surface rendered at /integrations.
// Restructured to the approved wireframe: a KPI row + tabbed sections
// (Catalog · Connected accounts · Agent access · Tools · Triggers · MCP
// endpoints). All data is fetched from /api/v1/integrations/composio/* (plus
// /api/v1/agents for the agent-access + MCP views); every endpoint is
// workspace-scoped via ?workspace_id=. When the provider isn't configured we
// keep the existing "Add API key" empty state instead of the tabs.

const TABS: { key: TabKey; label: string }[] = [
  { key: "catalog", label: "Catalog" },
  { key: "accounts", label: "Connected accounts" },
  { key: "agents", label: "Agent access" },
  { key: "tools", label: "Tools" },
  { key: "triggers", label: "Triggers" },
  { key: "mcp", label: "MCP endpoints" },
]

/**
 * What the host page needs in order to render this surface's chrome for it:
 * the left-panel counts, the SubBar description, and whether the API key is
 * set. Reported upward rather than duplicated, so there is one fetch and one
 * source of truth for both.
 */
export interface ComposioStatus {
  loading: boolean
  configured: boolean
  /** "env" or the key's label; null when no key is set. */
  keyLabel: string | null
  counts: {
    apps: number
    accounts: number
    users: number
    agentsBound: number
    agentsTotal: number
    endpoints: number
  }
  /** Toolkit slug -> connected account count, for the Toolkit facet. */
  toolkits: { slug: string; count: number }[]
  /** Composio user id -> connected account count, for the User facet. */
  users: { id: string; count: number }[]
  /** Every connected account, flattened — the host lists and details these. */
  accounts: ConnectedAccount[]
  /** Agents + their bindings, so a detail can answer "who can act through it". */
  agents: AgentLite[]
  bindings: AgentBindingsMap
}

export interface ComposioIntegrationsProps {
  /**
   * Controlled section. When provided, the component's own tab strip is not
   * rendered — the host owns navigation (today: the left panel).
   */
  section?: TabKey
  /**
   * true = the host page renders the identity and actions. Without this the
   * component draws a second "Integrations" heading and a second Refresh
   * button inside a page that already has both.
   */
  embedded?: boolean
  /** Controlled API-key dialog, so the host can open it from its own SubBar. */
  apiKeyOpen?: boolean
  onApiKeyOpenChange?: (open: boolean) => void
  /** Search term from the host's unified box; falls back to internal state. */
  search?: string
  /** Reports counts + configured state upward. Must be a stable callback. */
  onStatus?: (status: ComposioStatus) => void
}

export function ComposioIntegrations({
  section,
  embedded = false,
  apiKeyOpen,
  onApiKeyOpenChange,
  search: searchProp,
  onStatus,
}: ComposioIntegrationsProps = {}) {
  const { workspaceId, loading: wsLoading } = useWorkspace()

  // Latest workspace id, mirrored into a ref so async loaders can detect when a
  // late response belongs to a workspace the operator has already switched away
  // from — and bail before clobbering the current workspace's state.
  const wsRef = React.useRef(workspaceId)
  React.useEffect(() => {
    wsRef.current = workspaceId
  }, [workspaceId])

  // ── Inventory (connected accounts + auth configs) ──
  const [data, setData] = React.useState<Inventory | null>(null)
  const [loading, setLoading] = React.useState(true)
  const [error, setError] = React.useState<string | null>(null)

  const load = React.useCallback(async (wid: string) => {
    // Serve whatever is cached first. Leaving and re-entering this tab used to
    // pay the full round trip again and show skeletons for it, even when the
    // data was seconds old — see lib/stale-cache.ts.
    const { value, fresh, fromCache } = readThrough(`composio:${wid}:inventory`, async () => {
      const r = await apiFetch(`/api/v1/integrations/composio/inventory?workspace_id=${wid}`)
      if (!r.ok) throw new Error(`Request failed (${r.status})`)
      return (await r.json()) as Inventory
    })
    if (value && wsRef.current === wid) {
      setData(value)
      setLoading(false)
    } else {
      setLoading(true)
    }
    setError(null)
    try {
      const j = await fresh
      if (wsRef.current !== wid) return // workspace switched mid-flight
      setData(j)
    } catch (e) {
      if (wsRef.current !== wid) return
      // A background refresh that fails while cached data is on screen is not
      // worth an error banner over data the user can still see.
      if (!fromCache) setError(e instanceof Error ? e.message : "Failed to load inventory")
    } finally {
      if (wsRef.current === wid) setLoading(false)
    }
  }, [])

  React.useEffect(() => {
    if (workspaceId) void load(workspaceId)
  }, [workspaceId, load])

  // ── Catalog (toolkits) ──
  const [toolkits, setToolkits] = React.useState<ToolkitInfo[]>([])
  const [total, setTotal] = React.useState(0)
  const [searchState, setSearch] = React.useState("")
  // The host's box wins when it is driving; the internal one stays for the
  // stand-alone rendering the feature flag can still fall back to.
  const search = searchProp ?? searchState
  const [tkLoading, setTkLoading] = React.useState(true)

  const loadToolkits = React.useCallback(async (wid: string, q: string) => {
    // Cached per query, so going back to the unfiltered catalog after a search
    // is instant — this is the slowest of the four calls, since it reaches
    // Composio's own API.
    const { value, fresh } = readThrough(`composio:${wid}:toolkits:${q}`, async () => {
      const params = new URLSearchParams({ workspace_id: wid })
      if (q) params.set("search", q)
      const r = await apiFetch(`/api/v1/integrations/composio/toolkits?${params}`)
      if (!r.ok) throw new Error(String(r.status))
      return (await r.json()) as ToolkitsResp
    })
    if (value && wsRef.current === wid) {
      setToolkits(value.toolkits ?? [])
      setTotal(value.total ?? 0)
      setTkLoading(false)
    } else {
      setTkLoading(true)
    }
    try {
      const j = await fresh
      if (wsRef.current !== wid) return // workspace switched mid-flight
      setToolkits(j.toolkits ?? [])
      setTotal(j.total ?? 0)
    } catch {
      if (wsRef.current !== wid) return
      if (!value) setToolkits([])
    } finally {
      if (wsRef.current === wid) setTkLoading(false)
    }
  }, [])

  // Debounce the catalog search so each keystroke doesn't hammer Composio.
  React.useEffect(() => {
    if (!workspaceId) return
    const t = setTimeout(() => void loadToolkits(workspaceId, search), 300)
    return () => clearTimeout(t)
  }, [workspaceId, search, loadToolkits])

  // ── Agents + their Composio bindings (agent-access + MCP + KPI) ──
  const [agents, setAgents] = React.useState<AgentLite[]>([])
  const [bindings, setBindings] = React.useState<AgentBindingsMap>({})
  const [agentsLoading, setAgentsLoading] = React.useState(true)

  const loadAgents = React.useCallback(async (wid: string) => {
    // The priciest of the four: one call for the agent list, then one per
    // agent for its bindings. Cached as a unit — re-running an N+1 fan-out on
    // every tab entry is what made this surface feel slow.
    const { value, fresh } = readThrough(`composio:${wid}:agents`, async () => {
      const r = await apiFetch(`/api/v1/agents?workspace_id=${wid}`)
      if (!r.ok) throw new Error(String(r.status))
      const list = (await r.json()) as AgentLite[]
      // Fetch each agent's Composio binding in parallel. A failed lookup for
      // one agent shouldn't blank the whole table — degrade that row to
      // "no access".
      const entries = await Promise.all(
        list.map(async (a): Promise<[string, AgentBindingsMap[string]]> => {
          try {
            const br = await apiFetch(
              `/api/v1/integrations/composio/agents/${a.id}/bind?workspace_id=${wid}`,
            )
            if (!br.ok) return [a.id, []]
            const bj = (await br.json()) as { bindings?: AgentBindingsMap[string] }
            return [a.id, bj.bindings ?? []]
          } catch {
            return [a.id, []]
          }
        }),
      )
      return { agents: list, bindings: Object.fromEntries(entries) as AgentBindingsMap }
    })

    if (value && wsRef.current === wid) {
      setAgents(value.agents)
      setBindings(value.bindings)
      setAgentsLoading(false)
    } else {
      setAgentsLoading(true)
    }
    try {
      const j = await fresh
      if (wsRef.current !== wid) return // workspace switched mid-flight
      setAgents(j.agents)
      setBindings(j.bindings)
    } catch {
      if (wsRef.current !== wid) return
      if (!value) {
        setAgents([])
        setBindings({})
      }
    } finally {
      if (wsRef.current === wid) setAgentsLoading(false)
    }
  }, [])

  React.useEffect(() => {
    if (workspaceId) void loadAgents(workspaceId)
  }, [workspaceId, loadAgents])

  // Slugs already configured (auth config exists) or connected (has accounts).
  const configuredSlugs = React.useMemo(() => {
    const s = new Set<string>()
    data?.auth_configs.forEach((ac) => s.add(ac.toolkit.slug))
    data?.users.forEach((u) => u.connected_accounts.forEach((a) => s.add(a.toolkit.slug)))
    return s
  }, [data])

  // ── Settings (API key) ──
  const [settings, setSettings] = React.useState<ComposioSettings | null>(null)
  const [keyOpenState, setKeyOpenState] = React.useState(false)
  const keyOpen = apiKeyOpen ?? keyOpenState
  const setKeyOpen = React.useCallback(
    (open: boolean) => {
      setKeyOpenState(open)
      onApiKeyOpenChange?.(open)
    },
    [onApiKeyOpenChange],
  )
  const [connect, setConnect] = React.useState<{
    toolkit?: { slug: string; name: string }
    userId?: string
  } | null>(null)

  const loadSettings = React.useCallback(async (wid: string) => {
    const { value, fresh } = readThrough(`composio:${wid}:settings`, async () => {
      const r = await apiFetch(`/api/v1/integrations/composio/settings?workspace_id=${wid}`)
      if (!r.ok) throw new Error(String(r.status))
      return (await r.json()) as ComposioSettings
    })
    if (value && wsRef.current === wid) setSettings(value)
    try {
      const j = await fresh
      if (wsRef.current !== wid) return // workspace switched mid-flight
      setSettings(j)
    } catch {
      /* non-fatal */
    }
  }, [])

  React.useEffect(() => {
    if (workspaceId) void loadSettings(workspaceId)
  }, [workspaceId, loadSettings])

  const refreshAll = React.useCallback(
    (wid: string) => {
      // Refresh means refresh: drop this workspace's cache first, or the
      // button would re-render the same values it was pressed to replace.
      invalidate(`composio:${wid}:`)
      void load(wid)
      void loadToolkits(wid, search)
      void loadSettings(wid)
      void loadAgents(wid)
    },
    [load, loadToolkits, search, loadSettings, loadAgents],
  )

  const [tabState, setTab] = React.useState<TabKey>("catalog")
  const tab = section ?? tabState

  const busy = wsLoading || loading

  // KPI figures.
  const connectedCount = React.useMemo(
    () => (data?.users ?? []).reduce((n, u) => n + u.connected_accounts.length, 0),
    [data],
  )
  const userCount = data?.users.length ?? 0
  const agentsBound = React.useMemo(
    () => agents.filter((a) => (bindings[a.id]?.length ?? 0) > 0).length,
    [agents, bindings],
  )
  // Toolkit slugs the operator already has accounts for — quick-pick for Tools.
  const connectedSlugs = React.useMemo(() => {
    const s = new Set<string>()
    data?.users.forEach((u) => u.connected_accounts.forEach((a) => s.add(a.toolkit.slug)))
    return Array.from(s)
  }, [data])

  // Prefer the explicit settings flag; fall back to the inventory's enabled bit
  // while settings is still loading.
  const configured = settings ? settings.configured : data?.enabled ?? false

  // Facet material for the host's left panel: how many connected accounts sit
  // under each toolkit and each Composio user.
  const toolkitCounts = React.useMemo(() => {
    const m = new Map<string, number>()
    data?.users.forEach((u) =>
      u.connected_accounts.forEach((a) => m.set(a.toolkit.slug, (m.get(a.toolkit.slug) ?? 0) + 1)),
    )
    return [...m.entries()]
      .map(([slug, count]) => ({ slug, count }))
      .sort((a, b) => b.count - a.count || a.slug.localeCompare(b.slug))
  }, [data])

  const allAccounts = React.useMemo(
    () => (data?.users ?? []).flatMap((u) => u.connected_accounts),
    [data],
  )

  const userCounts = React.useMemo(
    () =>
      (data?.users ?? [])
        .map((u) => ({ id: u.user_id, count: u.connected_accounts.length }))
        .sort((a, b) => b.count - a.count || a.id.localeCompare(b.id)),
    [data],
  )

  // Push the summary up. Keyed on the primitives it is built from rather than
  // on the object, which would be a fresh identity every render and would loop
  // the host straight back into this effect.
  const keyLabel = settings?.configured
    ? settings.source === "env"
      ? "env"
      : settings.label ?? "set"
    : null
  const endpointCount = React.useMemo(
    () => agents.filter((a) => (bindings[a.id]?.length ?? 0) > 0).length,
    [agents, bindings],
  )
  React.useEffect(() => {
    onStatus?.({
      loading: busy,
      configured,
      keyLabel,
      counts: {
        apps: total,
        accounts: connectedCount,
        users: userCount,
        agentsBound,
        agentsTotal: agents.length,
        endpoints: endpointCount,
      },
      toolkits: toolkitCounts,
      users: userCounts,
      accounts: allAccounts,
      agents,
      bindings,
    })
  }, [
    onStatus,
    busy,
    configured,
    keyLabel,
    total,
    connectedCount,
    userCount,
    agentsBound,
    agents,
    bindings,
    allAccounts,
    endpointCount,
    toolkitCounts,
    userCounts,
  ])

  return (
    <div
      className={cn(
        "space-y-5 bg-background",
        // The host page already supplies page padding when it embeds us.
        embedded ? "p-0" : "p-4 md:p-6 pb-6",
      )}
    >
      {!embedded && (
        <div className="flex items-center justify-between gap-3 flex-wrap">
          <div className="flex items-center gap-2">
            <Plug className="h-4 w-4 text-foreground/60" />
            <h1 className="text-body font-medium text-foreground/80">Integrations</h1>
            <span className="text-[11px] text-muted-foreground">· powered by Composio</span>
            {settings?.configured && (
              <span className="ml-1 inline-flex items-center gap-1 rounded-full border border-emerald-400/30 bg-emerald-500/[0.08] px-2 py-0.5 text-[10px] text-emerald-400">
                ● key set{settings.source === "env" ? " (env)" : settings.label ? ` · ${settings.label}` : ""}
              </span>
            )}
          </div>
          <div className="flex items-center gap-2">
            <Button variant="outline" size="sm" onClick={() => setKeyOpen(true)}>
              <KeyRound className="h-3.5 w-3.5" />
              API key
            </Button>
            <Button
              variant="outline"
              size="sm"
              onClick={() => workspaceId && refreshAll(workspaceId)}
              disabled={busy}
            >
              <RefreshCw className={cn("h-3.5 w-3.5", busy && "animate-spin")} />
              Refresh
            </Button>
          </div>
        </div>
      )}

      {keyOpen && workspaceId && (
        <ApiKeyModal
          workspaceId={workspaceId}
          current={settings}
          onClose={() => setKeyOpen(false)}
          onChanged={() => {
            setKeyOpen(false)
            refreshAll(workspaceId)
          }}
        />
      )}

      {connect && workspaceId && (
        <ConnectModal
          workspaceId={workspaceId}
          toolkit={connect.toolkit}
          presetUserId={connect.userId}
          users={data?.users.map((u) => u.user_id) ?? []}
          onClose={() => setConnect(null)}
          onConnected={() => {
            setConnect(null)
            refreshAll(workspaceId)
          }}
        />
      )}

      {busy && <InventorySkeleton />}

      {!busy && error && (
        <div className="rounded-xl border border-red-500/20 bg-red-500/5 p-4 text-sm text-red-300">
          Couldn&apos;t load Composio inventory: {error}
        </div>
      )}

      {!busy && !error && !configured && <NotConfigured onAddKey={() => setKeyOpen(true)} />}

      {!busy && !error && configured && workspaceId && data && (
        <>
          {/* KPI row. Suppressed when embedded: the host renders the same
              four figures from the status it already receives. */}
          {!embedded && (
          <div className="grid gap-3 grid-cols-2 lg:grid-cols-4">
            <KpiCard label="Connectable apps" value={total || "—"} sub="Composio catalog" />
            <KpiCard
              label="Connected accounts"
              value={connectedCount}
              sub={`across ${userCount} user${userCount === 1 ? "" : "s"}`}
            />
            <KpiCard label="Users" value={userCount} sub="isolation buckets" />
            <KpiCard
              label="Agents bound"
              value={
                <span>
                  {agentsBound}
                  <span className="text-base text-muted-foreground"> / {agents.length}</span>
                </span>
              }
              sub="have a Composio user"
            />
          </div>
          )}

          {/* Tabs — only when nobody outside is driving the section. Two
              navigations for one set of views is what made this read as a
              page inside a page. */}
          {section === undefined && (
            <div className="flex gap-1 overflow-x-auto border-b border-white/10">
              {TABS.map((t) => (
                <button
                  key={t.key}
                  type="button"
                  onClick={() => setTab(t.key)}
                  className={cn(
                    "whitespace-nowrap border-b-2 px-3.5 py-2.5 text-[13px] transition-colors",
                    tab === t.key
                      ? "border-blue-400 text-foreground"
                      : "border-transparent text-muted-foreground hover:text-foreground/80",
                  )}
                >
                  {t.label}
                </button>
              ))}
            </div>
          )}

          {/* Tab content */}
          {tab === "catalog" && (
            <CatalogTab
              toolkits={toolkits}
              total={total}
              search={search}
              onSearch={setSearch}
              hideSearch={searchProp !== undefined}
              loading={tkLoading}
              configuredSlugs={configuredSlugs}
              onConnect={(toolkit) => setConnect({ toolkit })}
            />
          )}

          {tab === "accounts" && (
            <ConnectedAccountsTab
              workspaceId={workspaceId}
              data={data}
              onConnectForUser={(userId) => setConnect({ userId })}
              onChanged={() => refreshAll(workspaceId)}
            />
          )}

          {tab === "agents" && (
            <AgentAccessTab
              workspaceId={workspaceId}
              agents={agents}
              bindings={bindings}
              loading={agentsLoading}
              onChanged={() => refreshAll(workspaceId)}
            />
          )}

          {tab === "tools" && <ToolsTab workspaceId={workspaceId} suggestions={connectedSlugs} />}

          {tab === "triggers" && (
            <TriggersTab
              workspaceId={workspaceId}
              users={data.users.map((u) => u.user_id)}
            />
          )}

          {tab === "mcp" && (
            <McpEndpointsTab agents={agents} bindings={bindings} loading={agentsLoading} />
          )}
        </>
      )}
    </div>
  )
}

function KpiCard({
  label,
  value,
  sub,
}: {
  label: string
  value: React.ReactNode
  sub: string
}) {
  return (
    <div className="rounded-xl border border-white/10 bg-card p-4">
      <div className="text-[11px] uppercase tracking-wider text-muted-foreground">{label}</div>
      <div className="mt-1.5 text-2xl font-semibold text-foreground">{value}</div>
      <div className="mt-0.5 text-[11px] text-muted-foreground">{sub}</div>
    </div>
  )
}

function InventorySkeleton() {
  return (
    <div className="space-y-4">
      <div className="grid gap-3 grid-cols-2 lg:grid-cols-4">
        {Array.from({ length: 4 }).map((_, i) => (
          <Skeleton key={i} className="h-20 rounded-xl" />
        ))}
      </div>
      <Skeleton className="h-9 w-full" />
      <div className="grid gap-3 grid-cols-2 sm:grid-cols-3 lg:grid-cols-4">
        {Array.from({ length: 4 }).map((_, i) => (
          <Skeleton key={i} className="h-16 rounded-xl" />
        ))}
      </div>
    </div>
  )
}

function ApiKeyModal({
  workspaceId,
  current,
  onClose,
  onChanged,
}: {
  workspaceId: string
  current: ComposioSettings | null
  onClose: () => void
  onChanged: () => void
}) {
  const [apiKey, setApiKey] = React.useState("")
  const [label, setLabel] = React.useState(current?.label ?? "")
  const [saving, setSaving] = React.useState(false)
  const [err, setErr] = React.useState<string | null>(null)

  const save = async () => {
    setSaving(true)
    setErr(null)
    try {
      const r = await apiFetch(`/api/v1/integrations/composio/settings?workspace_id=${workspaceId}`, {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ api_key: apiKey.trim(), base_url: current?.base_url ?? "", label: label.trim() }),
      })
      if (!r.ok) {
        const body = await r.json().catch(() => null)
        throw new Error(body?.detail || `Failed (${r.status})`)
      }
      onChanged()
    } catch (e) {
      setErr(e instanceof Error ? e.message : "Failed to save")
    } finally {
      setSaving(false)
    }
  }

  const remove = async () => {
    setSaving(true)
    setErr(null)
    try {
      const r = await apiFetch(`/api/v1/integrations/composio/settings?workspace_id=${workspaceId}`, {
        method: "DELETE",
      })
      if (!r.ok) throw new Error(`Failed (${r.status})`)
      onChanged()
    } catch (e) {
      setErr(e instanceof Error ? e.message : "Failed to remove")
    } finally {
      setSaving(false)
    }
  }

  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="block max-w-md rounded-xl border-white/10 bg-card shadow-2xl sm:max-w-md">
        <DialogHeader>
          <DialogTitle className="text-base">Composio API key</DialogTitle>
          <DialogDescription className="text-xs leading-relaxed">
            Stored encrypted for this workspace. We validate it against Composio before saving.
            From app.composio.dev → your project → Settings → API keys.
          </DialogDescription>
        </DialogHeader>

        <div className="mt-4 space-y-3">
          <div>
            <label className="mb-1 block text-xs text-muted-foreground">Project API key</label>
            <input
              type="password"
              value={apiKey}
              onChange={(e) => setApiKey(e.target.value)}
              placeholder="ak_…"
              className="w-full rounded-lg border border-white/10 bg-background px-3 py-2 font-mono text-sm focus:border-blue-400/50 focus:outline-none"
            />
          </div>
          <div>
            <label className="mb-1 block text-xs text-muted-foreground">Label (optional)</label>
            <input
              value={label}
              onChange={(e) => setLabel(e.target.value)}
              placeholder="Crewship_dev_1"
              className="w-full rounded-lg border border-white/10 bg-background px-3 py-2 text-sm focus:border-blue-400/50 focus:outline-none"
            />
          </div>
        </div>

        {err && <div className="mt-3 text-xs text-red-400">{err}</div>}

        <div className="mt-5 flex items-center justify-between gap-2">
          <div>
            {current?.source === "workspace" && (
              <Button variant="ghost" size="sm" onClick={remove} disabled={saving}>
                Remove key
              </Button>
            )}
          </div>
          <div className="flex gap-2">
            <Button variant="outline" size="sm" onClick={onClose} disabled={saving}>
              Cancel
            </Button>
            <Button size="sm" onClick={save} disabled={saving || !apiKey.trim()}>
              {saving ? "Validating…" : "Validate & save"}
            </Button>
          </div>
        </div>
      </DialogContent>
    </Dialog>
  )
}

function ConnectModal({
  workspaceId,
  toolkit,
  presetUserId,
  users,
  onClose,
  onConnected,
}: {
  workspaceId: string
  toolkit?: { slug: string; name: string }
  presetUserId?: string
  users: string[]
  onClose: () => void
  onConnected: () => void
}) {
  // When invoked without a fixed toolkit (the per-user "+ Connect account"
  // path) the operator types the app slug here.
  const [toolkitSlug, setToolkitSlug] = React.useState(toolkit?.slug ?? "")
  const [userId, setUserId] = React.useState(presetUserId ?? users[0] ?? "")
  const [busy, setBusy] = React.useState(false)
  const [err, setErr] = React.useState<string | null>(null)

  const title = toolkit ? `Connect ${toolkit.name}` : "Connect an app"

  const connect = async () => {
    const uid = userId.trim()
    const slug = (toolkit?.slug ?? toolkitSlug).trim()
    if (!slug) {
      setErr("Enter an app (toolkit slug, e.g. gmail).")
      return
    }
    if (!uid) {
      setErr("Enter a user id (the person/mailbox this account belongs to).")
      return
    }
    setBusy(true)
    setErr(null)
    // Open the popup synchronously, inside the click gesture, so browser popup
    // blockers don't kill it. We navigate it to Composio's hosted OAuth once the
    // request resolves (and close it if there's no redirect or an error).
    const popup = window.open("", "_blank", "noopener,noreferrer")
    try {
      const r = await apiFetch(`/api/v1/integrations/composio/connect?workspace_id=${workspaceId}`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ toolkit: slug, user_id: uid }),
      })
      const body = await r.json().catch(() => null)
      if (!r.ok) throw new Error(body?.detail || `Failed (${r.status})`)
      if (body?.redirect_url) {
        // Point the pre-opened tab at Composio's hosted OAuth; the account lands
        // under user_id when the user finishes. We refresh on return.
        if (popup) popup.location.href = body.redirect_url
        else window.open(body.redirect_url, "_blank", "noopener,noreferrer")
      } else {
        popup?.close()
      }
      onConnected()
    } catch (e) {
      popup?.close()
      setErr(e instanceof Error ? e.message : "Failed to start connection")
    } finally {
      setBusy(false)
    }
  }

  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="block max-w-md rounded-xl border-white/10 bg-card shadow-2xl sm:max-w-md">
        <DialogHeader>
          <DialogTitle className="text-base">{title}</DialogTitle>
          <DialogDescription className="text-xs leading-relaxed">
            Authorize the app via OAuth. Pick the Composio user (the person/mailbox) this account
            belongs to — agents bound to that user will be able to use it.
          </DialogDescription>
        </DialogHeader>

        <div className="mt-4 space-y-3">
          {!toolkit && (
            <div>
              <label className="mb-1 block text-xs text-muted-foreground">App (toolkit slug)</label>
              <input
                value={toolkitSlug}
                onChange={(e) => setToolkitSlug(e.target.value)}
                placeholder="e.g. gmail, github, slack"
                className="w-full rounded-lg border border-white/10 bg-background px-3 py-2 text-sm focus:border-blue-400/50 focus:outline-none"
              />
            </div>
          )}
          {users.length > 0 && (
            <div>
              <label className="mb-1 block text-xs text-muted-foreground">Existing user</label>
              <select
                value={users.includes(userId) ? userId : ""}
                onChange={(e) => setUserId(e.target.value)}
                className="w-full rounded-lg border border-white/10 bg-background px-3 py-2 font-mono text-xs focus:border-blue-400/50 focus:outline-none"
              >
                <option value="">— new user —</option>
                {users.map((u) => (
                  <option key={u} value={u}>
                    {u}
                  </option>
                ))}
              </select>
            </div>
          )}
          <div>
            <label className="mb-1 block text-xs text-muted-foreground">User id</label>
            <input
              value={userId}
              onChange={(e) => setUserId(e.target.value)}
              placeholder="e.g. alice@acme.com or a stable user id"
              className="w-full rounded-lg border border-white/10 bg-background px-3 py-2 font-mono text-xs focus:border-blue-400/50 focus:outline-none"
            />
          </div>
        </div>

        {err && <div className="mt-3 text-xs text-red-400">{err}</div>}

        <div className="mt-5 flex justify-end gap-2">
          <Button variant="outline" size="sm" onClick={onClose} disabled={busy}>
            Cancel
          </Button>
          <Button
            size="sm"
            onClick={connect}
            disabled={busy || !userId.trim() || (!toolkit && !toolkitSlug.trim())}
          >
            {busy ? "Starting…" : "Connect with OAuth"}
          </Button>
        </div>
      </DialogContent>
    </Dialog>
  )
}

function NotConfigured({ onAddKey }: { onAddKey: () => void }) {
  return (
    <div
      className={cn(
        "mx-auto mt-8 max-w-xl rounded-xl border border-white/10 bg-card p-8 text-center",
        "shadow-lg shadow-blue-500/5",
      )}
    >
      <div className="mx-auto mb-4 flex h-12 w-12 items-center justify-center rounded-xl bg-blue-500/10">
        <Plug className="h-6 w-6 text-blue-400" />
      </div>
      <h2 className="text-base font-semibold text-foreground">Connect Composio to get started</h2>
      <p className="mt-2 text-sm leading-relaxed text-muted-foreground">
        We&apos;re replacing self-hosted MCP servers with a managed integration platform
        (Composio). Add your Composio project API key to browse 1,000+ apps and connect them.
      </p>
      <div className="mt-5">
        <Button onClick={onAddKey}>
          <KeyRound className="h-3.5 w-3.5" />
          Add API key
        </Button>
      </div>
      <div className="mt-6 grid gap-3 text-left sm:grid-cols-2">
        <div className="rounded-lg border border-white/10 bg-white/[0.02] p-3">
          <ShieldCheck className="h-4 w-4 text-blue-400" />
          <div className="mt-2 text-xs font-medium text-foreground/90">Per-user OAuth</div>
          <div className="mt-0.5 text-[11px] leading-relaxed text-muted-foreground">
            Each agent acts on behalf of the connected user — no shared secrets.
          </div>
        </div>
        <div className="rounded-lg border border-white/10 bg-white/[0.02] p-3">
          <Plug className="h-4 w-4 text-blue-400" />
          <div className="mt-2 text-xs font-medium text-foreground/90">Hundreds of apps</div>
          <div className="mt-0.5 text-[11px] leading-relaxed text-muted-foreground">
            A single managed catalog replaces hand-configured MCP endpoints.
          </div>
        </div>
      </div>
      <div className="mt-6 flex items-center justify-center">
        <Link
          href="/credentials"
          className="inline-flex items-center gap-1.5 text-xs text-blue-400 transition-all hover:gap-2.5"
        >
          <KeyRound className="h-3.5 w-3.5" />
          Manage credentials in the meantime
        </Link>
      </div>
    </div>
  )
}
