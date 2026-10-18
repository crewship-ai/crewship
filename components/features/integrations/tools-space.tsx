"use client"

import * as React from "react"
import { AlertTriangle, Bot, Circle, Grid3x3, KeyRound, Link2, Plug, RefreshCw, Search, Terminal, Trash2, Users, Webhook, Wrench } from "lucide-react"
import { toast } from "@/lib/toast"

import { Button } from "@/components/ui/button"
import { StatusPill } from "@/components/ui/status-pill"
import { ConfirmDialog } from "@/components/ui/confirm-dialog"
import { Skeleton } from "@/components/ui/skeleton"
import { AgentAvatar } from "@/components/ui/agent-avatar"
import { PageEmpty, PageError, PageLoading, PageNoResults, PageNotFound } from "@/components/layout/page-state"
import { SettingsCard, SettingsDangerCard, SettingsRow, SettingsSaveBar, SettingsSegmented } from "@/components/features/settings/shared"
import { apiFetch } from "@/lib/api-fetch"
import { cn } from "@/lib/utils"
import type { ComposioStatus } from "./composio-integrations"
import type { ActiveTriggersResp, AgentLite, ToolkitInfo, TriggerInstance } from "./composio/types"
import { ToolkitIcon, toolkitLabel } from "./composio/shared"
import { ToolsTab } from "./composio/tools-tab"
import { accessChange, accessSummary, agentsByCrew, searchToolsAcrossApps, TOOL_SEARCH_MIN, type AccessLevel, type AccountRow, type ToolHit, type ToolsListView } from "./tools-model"
import { Banner, CellMain, CliLines, HealthStrip, InfoCard, MetaChip, ObjectPage, ResponsiveList, SpaceHead } from "./integrations-ui"

/**
 * Tools (Harbor 2, I-TOOLS / I-CATALOG / I-APP): apps agents act in, through
 * Composio. Access is per agent (the server binds agents, not crews).
 *
 * Not drawn, because the server has none: per-crew access and "New crews"
 * defaults, switching an app's tools on or off for everyone, creating a
 * Composio trigger (nothing in Crewship receives it yet), "Built in" tools.
 */

function AppMark({ slug, logo, size = 28 }: { slug: string; logo?: string; size?: 28 | 30 | 52 }) {
  return (
    <span className={cn("inline-flex shrink-0 items-center justify-center rounded-lg border border-border bg-card", size === 52 ? "size-[52px]" : size === 30 ? "size-[30px]" : "size-7")} aria-hidden>
      <ToolkitIcon toolkit={{ slug, logo }} size={size === 52 ? 30 : 18} />
    </span>
  )
}

export function AccountState({ row }: { row: AccountRow }) {
  if (row.signedOut) return <StatusPill tone="danger" label="Signed out" />
  if (row.agents.length === 0) return <StatusPill tone="muted" label="Not used" />
  return <StatusPill tone="success" label="Working" />
}

const VIEW_TITLE: Record<ToolsListView, string> = { connected: "Tools", attention: "Needs attention", idle: "Not used" }

/** Composio is not set up yet: the catalog stays reachable, with the key as the next step. */
export function ComposioKeyBanner({ canManage, onAddKey }: { canManage: boolean; onAddKey: () => void }) {
  return (
    <Banner tone="info" title="Add a Composio API key to connect apps" action={canManage ? <Button size="sm" onClick={onAddKey}><KeyRound className="size-3.5" aria-hidden />Add key</Button> : undefined}>
      {canManage ? "Apps connect through Composio; the key is kept for this workspace." : "Ask an admin to add the workspace's Composio API key."}
    </Banner>
  )
}

export function ToolsList({
  rows,
  allRows,
  view,
  status,
  filtered,
  canManage,
  onOpen,
  onConnect,
  onView,
  onAddKey,
  onClearFilters,
  toolSearch,
}: {
  rows: AccountRow[]
  allRows: AccountRow[]
  view: ToolsListView
  status: ComposioStatus
  filtered: boolean
  canManage: boolean
  onOpen: (id: string) => void
  onConnect: () => void
  onView: (v: ToolsListView | "catalog") => void
  onAddKey: () => void
  onClearFilters: () => void
  /** Tools of connected apps that match the panel search (N12), under the apps. */
  toolSearch?: React.ReactNode
}) {
  const signedOut = allRows.filter((r) => r.signedOut).length
  const idle = allRows.filter((r) => r.agents.length === 0).length
  let body: React.ReactNode
  if (status.loading && allRows.length === 0) body = <PageLoading variant="cards" />
  else if (!status.configured)
    // v67's one empty state. The key is the next step, so it is the one
    // action here (the banner that used to sit above asked for the same).
    body = (
      <PageEmpty
        icon={Plug}
        title="Nothing connected"
        description={canManage ? "Give agents tools, start work from outside and reach people." : "Give agents tools, start work from outside and reach people. Ask an admin to add the workspace's Composio API key."}
        primary={canManage ? { label: "Add Composio key", icon: KeyRound, onClick: onAddKey } : undefined}
        secondary={{ label: "Open the catalog", onClick: () => onView("catalog") }}
        cli="crewship integration composio key set --key ak_…"
      />
    )
  else if (status.error && allRows.length === 0) body = <PageError title="Connected apps could not load" description="Triggers and Channels are not affected." onRetry={undefined} detail={status.error} />
  else if (allRows.length === 0)
    body = <PageEmpty icon={Plug} title="Nothing connected" description="Give agents tools, start work from outside and reach people." primary={{ label: "Connect an app", onClick: onConnect }} cli="crewship integration composio inventory" />
  else if (rows.length === 0 && toolSearch) body = <p className="text-label text-muted-foreground">No connected app matches; tools that do are below.</p>
  else if (rows.length === 0) body = <PageNoResults onClear={onClearFilters} />
  else
    body = (
      <>
        <p className="text-label text-muted-foreground">{rows.length} {rows.length === 1 ? "app" : "apps"}</p>
        <ResponsiveList
          caption="Connected apps"
          columns={[
            { key: "a", label: "App" },
            { key: "c", label: "Account" },
            { key: "x", label: "Access" },
            { key: "s", label: "State" },
          ]}
          items={rows.map((r) => ({
            id: r.account.id,
            label: `Open ${toolkitLabel(r.slug)}`,
            lead: <AppMark slug={r.slug} logo={r.account.toolkit.logo} size={30} />,
            title: toolkitLabel(r.slug),
            sub: accessSummary(r),
            state: <AccountState row={r} />,
            cells: [
              <CellMain key="a" lead={<AppMark slug={r.slug} logo={r.account.toolkit.logo} />} title={toolkitLabel(r.slug)} sub={`${r.agents.length} ${r.agents.length === 1 ? "agent" : "agents"}`} />,
              <span key="c" className="font-mono text-label text-muted-foreground">{r.account.id}</span>,
              <span key="x" className="flex min-w-0 items-center gap-1.5">
                {r.agents.length > 0 && (
                  <span className="flex shrink-0 -space-x-1.5">
                    {r.agents.slice(0, 4).map(({ agent }) => (
                      <AgentAvatar key={agent.id} seed={agent.avatar_seed || agent.name} style={agent.avatar_style || agent.crew?.avatar_style} avatarUrl={agent.avatar_url} className="size-5 rounded-md ring-2 ring-card" />
                    ))}
                  </span>
                )}
                <span className="truncate text-label text-muted-foreground">{accessSummary(r)}</span>
              </span>,
              <AccountState key="s" row={r} />,
            ],
          }))}
          onOpen={onOpen}
        />
      </>
    )
  return (
    <div className="grid min-w-0 gap-3.5">
      <SpaceHead title={VIEW_TITLE[view]} description="Apps agents can act in. Each is a Composio account; access is given per agent." />
      {!filtered && status.configured && allRows.length > 0 && (
        <HealthStrip
          label="Tools at a glance"
          items={[
            { key: "n", tone: "ok", icon: Link2, label: "Connected apps", value: allRows.length, sub: "through Composio", onClick: () => onView("connected") },
            { key: "out", tone: signedOut ? "bad" : "muted", icon: AlertTriangle, label: "Signed out", value: signedOut, onClick: () => onView("attention") },
            { key: "agents", tone: "muted", icon: Bot, label: "Agents with apps", value: status.counts.agentsBound, sub: `of ${status.counts.agentsTotal}` },
            { key: "idle", tone: "muted", icon: Circle, label: "Not used", value: idle, sub: "connected, no agent", onClick: () => onView("idle") },
          ]}
        />
      )}
      {body}
      {toolSearch}
    </div>
  )
}

/**
 * Tools whose name matches the panel search, across every connected app
 * (N12). A hit opens that app's page on Tools with the tool marked.
 */
export function ToolSearchResults({
  workspaceId,
  apps,
  query,
  onOpenTool,
}: {
  workspaceId: string
  apps: { slug: string; accountId: string }[]
  query: string
  onOpenTool: (accountId: string, tool: string) => void
}) {
  const q = query.trim()
  const appsKey = apps.map((a) => `${a.slug}:${a.accountId}`).join(",")
  const appsRef = React.useRef(apps)
  appsRef.current = apps
  const active = q.length >= TOOL_SEARCH_MIN && apps.length > 0
  const [state, setState] = React.useState<{ q: string; loading: boolean; hits: ToolHit[]; failed: string[] } | null>(null)
  React.useEffect(() => {
    if (!active) {
      setState(null)
      return
    }
    const ctrl = new AbortController()
    setState((s) => ({ q, loading: true, hits: s?.hits ?? [], failed: [] }))
    const t = setTimeout(() => {
      searchToolsAcrossApps(workspaceId, appsRef.current, q, ctrl.signal)
        .then((r) => setState({ q, loading: false, ...r }))
        .catch(() => {})
    }, 300)
    return () => {
      clearTimeout(t)
      ctrl.abort()
    }
  }, [workspaceId, q, appsKey, active])
  if (!active || !state) return null
  return (
    <InfoCard title={`Tools matching “${q}”`} icon={Search}>
      {state.loading && state.hits.length === 0 ? (
        <div className="grid gap-2 px-4 py-3">
          <Skeleton className="h-4 w-56" />
          <Skeleton className="h-4 w-40" />
        </div>
      ) : state.hits.length === 0 ? (
        <p className="px-4 py-3 text-body text-muted-foreground">No tool in a connected app matches “{state.q}”.</p>
      ) : (
        <ul className="divide-y divide-border">
          {state.hits.map((h) => (
            <li key={`${h.slug}:${h.tool.slug}`}>
              <button
                type="button"
                aria-label={`Open ${h.tool.slug} in ${toolkitLabel(h.slug)}`}
                onClick={() => onOpenTool(h.accountId, h.tool.slug)}
                className="kit-tap flex w-full min-w-0 items-center gap-2.5 px-4 py-2 text-left hover:bg-[var(--row-hover-bg)]"
              >
                <AppMark slug={h.slug} />
                <span className="flex min-w-0 flex-1 flex-col">
                  <span className="truncate font-mono text-label text-foreground">{h.tool.slug}</span>
                  <span className="truncate text-label text-muted-foreground">{h.tool.name}</span>
                </span>
                <span className="shrink-0 text-label text-muted-foreground max-sm:hidden">{toolkitLabel(h.slug)} › Tools</span>
              </button>
            </li>
          ))}
        </ul>
      )}
      {state.failed.length > 0 && <p className="border-t border-border px-4 py-2 text-label text-muted-foreground">{state.failed.map(toolkitLabel).join(", ")} did not answer.</p>}
    </InfoCard>
  )
}

export function CatalogView({
  catalog,
  loading,
  total,
  connected,
  configured,
  canManage,
  onOpen,
  onAddKey,
}: {
  catalog: ToolkitInfo[]
  loading: boolean
  total: number
  connected: Set<string>
  configured: boolean
  canManage: boolean
  onOpen: (slug: string) => void
  onAddKey: () => void
}) {
  return (
    <div className="grid min-w-0 gap-3.5">
      <SpaceHead title="Catalog" description="Apps your agents can use through Composio. Sign in once; you choose which agents get which tools." />
      {!configured && <ComposioKeyBanner canManage={canManage} onAddKey={onAddKey} />}
      {configured && <p className="text-label text-muted-foreground">{catalog.length} apps{total > catalog.length ? ` · Composio has ${total}; search finds the rest` : ""}</p>}
      {loading && catalog.length === 0 ? (
        <PageLoading variant="cards" />
      ) : configured && catalog.length === 0 ? (
        <PageNoResults />
      ) : (
        <ul className="grid grid-cols-[repeat(auto-fill,minmax(250px,1fr))] gap-2.5" aria-label="Catalog">
          {catalog.map((t) => (
            <li key={t.slug}>
              <button type="button" onClick={() => onOpen(t.slug)} className="kit-tap grid h-full w-full min-w-0 grid-cols-[auto_minmax(0,1fr)] content-start gap-x-2.5 gap-y-2 rounded-card border border-border bg-card p-3 text-left hover:border-line-strong">
                <AppMark slug={t.slug} logo={t.meta.logo} size={30} />
                <span className="flex min-w-0 flex-col">
                  <b className="truncate text-body font-semibold text-foreground">{t.name}</b>
                  <small className="line-clamp-2 text-label text-muted-foreground">{t.meta.description || t.meta.categories?.[0]?.name || t.slug}</small>
                </span>
                <span className="col-span-2 flex items-center gap-2">
                  <span className="text-label text-muted-foreground">{t.meta.tools_count ? `${t.meta.tools_count} tools` : t.slug}</span>
                  {connected.has(t.slug) && <StatusPill tone="success" label="Connected" />}
                </span>
              </button>
            </li>
          ))}
        </ul>
      )}
    </div>
  )
}

export function CatalogAppPage({ app, slug, connectedAccountId, canManage, configured, onBack, onConnect, onOpenConnected }: { app: ToolkitInfo | undefined; slug: string; connectedAccountId: string | null; canManage: boolean; configured: boolean; onBack: () => void; onConnect: () => void; onOpenConnected: (id: string) => void }) {
  const name = app?.name ?? toolkitLabel(slug)
  return (
    <ObjectPage
      backLabel="Catalog"
      onBack={onBack}
      lead={<AppMark slug={slug} logo={app?.meta.logo} size={52} />}
      title={name}
      description={app?.meta.description ?? app?.meta.categories?.[0]?.name}
      chips={app?.meta.tools_count ? <MetaChip icon={Bot}>{app.meta.tools_count} tools</MetaChip> : undefined}
      tabs={[{ id: "overview", label: "Overview" }]}
      tab="overview"
      onTab={() => {}}
    >
      <div className="flex flex-wrap gap-2">
        {connectedAccountId ? (
          <Button variant="outline" size="sm" onClick={() => onOpenConnected(connectedAccountId)}>Open connected</Button>
        ) : (
          <Button size="sm" disabled={!canManage || !configured} onClick={onConnect} title={!canManage ? "Managers and admins connect apps" : undefined}>
            <Plug className="size-3.5" aria-hidden />
            Connect {name}
          </Button>
        )}
      </div>
      <div className="grid gap-3.5 lg:grid-cols-[2fr_1fr]">
        <InfoCard title="What agents can do" icon={Bot}>
          <ul className="grid gap-1.5 px-4 py-3 text-body text-foreground">
            <li>Read: list, get, search</li>
            <li>Write: create, update, comment</li>
            <li>Delete: only with Full access, or picked in Custom</li>
          </ul>
        </InfoCard>
        <InfoCard title="How it connects" icon={KeyRound}>
          <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1 px-4 py-3 text-label">
            <dt className="text-muted-foreground">Through</dt>
            <dd>Composio</dd>
            <dt className="text-muted-foreground">Sign-in</dt>
            <dd>Your {name} account, in a popup</dd>
            <dt className="text-muted-foreground">Who can connect</dt>
            <dd>Managers and admins</dd>
          </dl>
        </InfoCard>
      </div>
    </ObjectPage>
  )
}

export const APP_PANES = ["overview", "access", "tools", "triggers", "settings"] as const
export type AppPane = (typeof APP_PANES)[number]

async function send(url: string, init: RequestInit): Promise<void> {
  const r = await apiFetch(url, init)
  if (!r.ok) {
    const body = await r.json().catch(() => null)
    throw new Error(body?.detail || body?.error || `Failed (${r.status})`)
  }
}

export function AppAccountPage({
  workspaceId,
  row,
  status,
  pane,
  onPane,
  canManage,
  onBack,
  onChanged,
  onGone,
  onTriggers,
  toolFocus,
}: {
  workspaceId: string
  row: AccountRow | null
  status: ComposioStatus
  pane: AppPane
  onPane: (p: AppPane) => void
  canManage: boolean
  onBack: () => void
  /** Reload Composio after a change. */
  onChanged: () => void
  onGone: () => void
  onTriggers: () => void
  /** A tool found from the panel search: Tools opens searched for it, its row marked. */
  toolFocus?: string
}) {
  const [busy, setBusy] = React.useState<string | null>(null)
  const [confirm, setConfirm] = React.useState<"revoke" | "remove" | null>(null)
  // Access is a draft on the page's Save bar (KIT-04): agent id -> level.
  const [draft, setDraft] = React.useState<Record<string, AccessLevel>>({})
  // Levels just saved, shown until the reloaded bindings arrive.
  const [applied, setApplied] = React.useState<Record<string, AccessLevel>>({})
  React.useEffect(() => setApplied({}), [status.bindings])
  if (!row) return status.loading ? <PageLoading rows={4} /> : <PageNotFound what="connected app" back={{ label: "Back to Tools", onClick: onBack }} />
  const name = toolkitLabel(row.slug)
  const base = `/api/v1/integrations/composio/accounts/${encodeURIComponent(row.account.id)}`
  const q = `workspace_id=${encodeURIComponent(workspaceId)}`

  const refreshAccount = async () => {
    setBusy("refresh")
    try {
      await send(`${base}/refresh?${q}`, { method: "POST" })
      toast.success(`${name} account renewed`)
      onChanged()
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "Sign-in could not be renewed")
    } finally {
      setBusy(null)
    }
  }

  const savedLevel = (agentId: string): AccessLevel => applied[agentId] ?? (status.bindings[agentId] ?? []).find((x) => x.toolkit === row.slug)?.mode ?? "off"

  const choose = (agent: AgentLite, level: AccessLevel) => {
    if (level !== "off" && accessChange(status.bindings[agent.id] ?? [], row.slug, level, row.account.user_id) === null) {
      toast.error(`${agent.name} acts as another Composio user; change it on the agent's Tools and access`)
      return
    }
    setDraft((d) => {
      const next = { ...d }
      if (level === savedLevel(agent.id)) delete next[agent.id]
      else next[agent.id] = level
      return next
    })
  }

  /** One bind per changed agent; a refused agent keeps its draft and is named. */
  const saveAccess = async () => {
    const failures: string[] = []
    for (const [agentId, level] of Object.entries(draft)) {
      const label = status.agents.find((a) => a.id === agentId)?.name ?? agentId
      try {
        const change = accessChange(status.bindings[agentId] ?? [], row.slug, level, row.account.user_id)
        if (!change) throw new Error("acts as another Composio user")
        const url = `/api/v1/integrations/composio/agents/${encodeURIComponent(agentId)}/bind?${q}`
        if (change.kind === "unbind") await send(`${url}&toolkit=${encodeURIComponent(change.toolkit)}`, { method: "DELETE" })
        else await send(url, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(change.payload) })
        setApplied((m) => ({ ...m, [agentId]: level }))
        setDraft((d) => {
          const next = { ...d }
          delete next[agentId]
          return next
        })
      } catch (e) {
        failures.push(`${label}: ${e instanceof Error ? e.message : "not saved"}`)
      }
    }
    onChanged()
    if (failures.length > 0) throw new Error(`${failures.join("; ")}.`)
  }

  let body: React.ReactNode
  if (pane === "access") {
    const groups = agentsByCrew(status.agents)
    body = (
      <>
        {groups.length === 0 ? (
          <InfoCard title="Access per agent" icon={Users}><p className="px-4 py-3 text-body text-muted-foreground">No agents yet.</p></InfoCard>
        ) : (
          groups.map((g) => (
            <SettingsCard key={g.crew} title={g.crew} icon={Users}>
              {g.agents.map((a) => {
                const b = (status.bindings[a.id] ?? []).find((x) => x.toolkit === row.slug)
                const level: AccessLevel = draft[a.id] ?? savedLevel(a.id)
                const options = [
                  { value: "off" as AccessLevel, label: "Off" },
                  { value: "read" as AccessLevel, label: "Read-only" },
                  { value: "full" as AccessLevel, label: "Full" },
                  ...(savedLevel(a.id) === "custom" ? [{ value: "custom" as AccessLevel, label: `Custom · ${b?.tools?.length ?? 0} tools` }] : []),
                ]
                return (
                  <SettingsRow key={a.id} label={<span className="flex items-center gap-2"><AgentAvatar seed={a.avatar_seed || a.name} style={a.avatar_style || a.crew?.avatar_style} avatarUrl={a.avatar_url} className="size-5 rounded-md" />{a.name}</span>}>
                    <SettingsSegmented label={`${name} access for ${a.name}`} options={options} value={level} disabled={!canManage || busy !== null} onChange={(v) => choose(a, v)} />
                  </SettingsRow>
                )
              })}
            </SettingsCard>
          ))
        )}
        <p className="text-label text-muted-foreground">Read-only allows tools that only read (list, get, search). Full allows every tool of the app. A custom tool list is picked on the agent's Tools and access; it is kept here. New agents do not get the app automatically.</p>
      </>
    )
  } else if (pane === "tools") {
    body = (
      <>
        <ToolsTab workspaceId={workspaceId} suggestions={[row.slug]} initialSearch={toolFocus} highlight={toolFocus} />
        <p className="text-label text-muted-foreground">The tools are read-only here. Which an agent may call is its access level; a custom list is picked on the agent.</p>
      </>
    )
  } else if (pane === "triggers") {
    body = <AppTriggers workspaceId={workspaceId} accountId={row.account.id} name={name} onTriggers={onTriggers} />
  } else if (pane === "settings") {
    body = (
      <>
        <SettingsCard title="Account" icon={Link2}>
          <SettingsRow label="Composio account" description={row.account.id}>
            <Button variant="outline" size="sm" disabled={!canManage || busy !== null} onClick={() => void refreshAccount()}>
              <RefreshCw className="size-3.5" aria-hidden />
              Sign in again
            </Button>
          </SettingsRow>
        </SettingsCard>
        <SettingsCard title="Command line" icon={Terminal}>
          <CliLines
            lines={[
              [`crewship integration composio bind <agent> --user ${row.account.user_id} --app ${row.slug}:read`, "Give an agent read-only access"],
              [`crewship integration composio tools ${row.slug}`, "The app's tools"],
              [`crewship integration composio account refresh ${row.account.id}`, "Renew the account"],
            ]}
          />
        </SettingsCard>
        {canManage && (
          <SettingsDangerCard title="Danger zone">
            <SettingsRow label="Revoke" description={`${row.agents.length} ${row.agents.length === 1 ? "agent loses" : "agents lose"} ${name} at their next run; the account stays listed`}>
              <Button variant="outline" size="sm" onClick={() => setConfirm("revoke")}>Revoke…</Button>
            </SettingsRow>
            <SettingsRow label="Disconnect" description="Removes the account from Composio">
              <Button variant="outline" size="sm" onClick={() => setConfirm("remove")}>
                <Trash2 className="size-3.5" aria-hidden />
                Disconnect…
              </Button>
            </SettingsRow>
          </SettingsDangerCard>
        )}
      </>
    )
  } else {
    body = (
      <>
        {row.signedOut && (
          <Banner tone="bad" title="Signed out" action={canManage ? <Button size="sm" disabled={busy !== null} onClick={() => void refreshAccount()}>Sign in again</Button> : undefined}>
            The Composio account is {row.account.status.toLowerCase()}. Agents run without {name} until someone signs in again.
          </Banner>
        )}
        <div className="grid gap-3.5 lg:grid-cols-[2fr_1fr]">
          <InfoCard title="Who uses it" icon={Users} right={canManage ? <Button size="sm" variant="outline" onClick={() => onPane("access")}>Give to agents</Button> : undefined}>
            {row.agents.length === 0 ? (
              <p className="px-4 py-3 text-body text-muted-foreground">No agent has it yet.</p>
            ) : (
              <ul className="divide-y divide-border">
                {row.agents.map(({ agent, mode }) => (
                  <li key={agent.id} className="flex min-w-0 items-center gap-2.5 px-4 py-2">
                    <AgentAvatar seed={agent.avatar_seed || agent.name} style={agent.avatar_style || agent.crew?.avatar_style} avatarUrl={agent.avatar_url} className="size-6 rounded-md" />
                    <span className="min-w-0 flex-1 truncate text-body text-foreground">{agent.name}</span>
                    <span className="text-label text-muted-foreground">{agent.crew?.name ?? ""} · {mode === "read" ? "Read-only" : mode === "full" ? "Full" : "Custom"}</span>
                  </li>
                ))}
              </ul>
            )}
          </InfoCard>
          <InfoCard title="Account" icon={KeyRound}>
            <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1 px-4 py-3 text-label">
              <dt className="text-muted-foreground">Composio account</dt>
              <dd className="break-all font-mono">{row.account.id}</dd>
              <dt className="text-muted-foreground">Signed in</dt>
              <dd className={row.signedOut ? "text-destructive" : undefined}>{row.signedOut ? row.account.status : "Valid"}</dd>
              <dt className="text-muted-foreground">Agents see</dt>
              <dd>Tools only, never the token</dd>
            </dl>
          </InfoCard>
        </div>
      </>
    )
  }

  return (
    <>
      <ObjectPage
        backLabel="Tools"
        onBack={onBack}
        lead={<AppMark slug={row.slug} logo={row.account.toolkit.logo} size={52} />}
        title={name}
        description={<>Composio account <span className="font-mono">{row.account.id}</span></>}
        chips={
          <>
            <AccountState row={row} />
            <MetaChip icon={Bot}>{row.agents.length} {row.agents.length === 1 ? "agent" : "agents"}</MetaChip>
          </>
        }
        tabs={[
          { id: "overview", label: "Overview" },
          { id: "access", label: "Access" },
          { id: "tools", label: "Tools" },
          { id: "triggers", label: "Triggers" },
          { id: "settings", label: "Settings" },
        ]}
        tab={pane}
        onTab={(t) => onPane(t as AppPane)}
      >
        {body}
      </ObjectPage>
      <SettingsSaveBar label={`${name} access`} count={Object.keys(draft).length} onSave={saveAccess} onDiscard={() => setDraft({})} />
      <ConfirmDialog
        open={confirm !== null}
        onOpenChange={(o) => !o && setConfirm(null)}
        title={confirm === "remove" ? `Disconnect ${name}?` : `Revoke ${name}?`}
        consequences={
          confirm === "remove"
            ? [
                { tone: "lost", text: `Agents lose ${name} at their next run` },
                { tone: "lost", text: "The account is removed from Composio; connecting again needs a new sign-in" },
              ]
            : [
                { tone: "lost", text: `Agents lose ${name} at their next run` },
                { tone: "kept", text: "The account stays listed; Sign in again restores it" },
              ]
        }
        confirmLabel={confirm === "remove" ? "Disconnect" : "Revoke"}
        destructive
        onConfirm={async () => {
          if (confirm === "remove") await send(`${base}?${q}`, { method: "DELETE" })
          else await send(`${base}/revoke?${q}`, { method: "POST" })
          toast.success(confirm === "remove" ? `${name} disconnected` : `${name} revoked`)
          onChanged()
          if (confirm === "remove") onGone()
        }}
      />
    </>
  )
}

/** Active Composio triggers of this account, read-only: nothing in Crewship receives them yet. */
function AppTriggers({ workspaceId, accountId, name, onTriggers }: { workspaceId: string; accountId: string; name: string; onTriggers: () => void }) {
  const [state, setState] = React.useState<{ loading: boolean; error: string | null; items: TriggerInstance[] }>({ loading: true, error: null, items: [] })
  React.useEffect(() => {
    const ctrl = new AbortController()
    apiFetch(`/api/v1/integrations/composio/triggers/active?workspace_id=${encodeURIComponent(workspaceId)}`, { signal: ctrl.signal })
      .then(async (r) => {
        if (!r.ok) throw new Error(`Failed (${r.status})`)
        const j = (await r.json()) as ActiveTriggersResp
        setState({ loading: false, error: null, items: (j.triggers ?? []).filter((t) => t.connected_account_id === accountId) })
      })
      .catch((e) => {
        if (!ctrl.signal.aborted) setState({ loading: false, error: e instanceof Error ? e.message : "Failed", items: [] })
      })
    return () => ctrl.abort()
  }, [workspaceId, accountId])
  return (
    <>
      <Banner tone="warn" title="Crewship cannot receive app events yet" action={<Button size="sm" variant="outline" onClick={onTriggers}><Webhook className="size-3.5" aria-hidden />Webhook triggers</Button>}>
        Until it can, use a webhook trigger from {name} itself if it offers one.
      </Banner>
      <InfoCard title={`${name} triggers set in Composio`} icon={Grid3x3}>
        {state.loading ? (
          <div className="px-4 py-3"><Skeleton className="h-4 w-40" /></div>
        ) : state.error ? (
          <p className="px-4 py-3 text-body text-destructive">{state.error}</p>
        ) : state.items.length === 0 ? (
          <p className="px-4 py-3 text-body text-muted-foreground">None.</p>
        ) : (
          <ul className="divide-y divide-border">
            {state.items.map((t) => (
              <li key={t.id} className="flex items-center gap-2.5 px-4 py-2">
                <Wrench className="size-3.5 text-muted-foreground" aria-hidden />
                <span className="min-w-0 flex-1 truncate font-mono text-label text-foreground">{t.trigger_name}</span>
                <StatusPill tone="warn" label={t.disabled_at ? "Off" : "No receiver yet"} />
              </li>
            ))}
          </ul>
        )}
      </InfoCard>
    </>
  )
}
