"use client"

import * as React from "react"
import Link from "next/link"
import { Bot, KeyRound, Plug, RefreshCw, Wrench } from "lucide-react"
import { toast } from "sonner"

import { OAuthAutoConnect } from "@/components/features/integrations/oauth-auto-connect"
import { Button } from "@/components/ui/button"
import { InlineEmpty } from "@/components/ui/inline-empty"
import { StatusPill } from "@/components/ui/status-pill"
import { apiFetch } from "@/lib/api-fetch"
import { entityHref } from "@/lib/entity-links"
import { cn } from "@/lib/utils"
import {
  bindCredentialToCrewTool,
  fetchCrewTools,
  needsCredential,
  sortCrewTools,
  type CrewTool,
} from "../crew-tools"

const ENDPOINT_TRANSPORTS = new Set(["streamable-http", "sse", "http"])

interface CredentialOption {
  id: string
  name: string
  provider?: string | null
  type?: string | null
}

function authPillProps(tool: CrewTool): { status: string; label?: string; tone?: "muted" } {
  switch (tool.auth_status) {
    case "missing":
      return { status: "missing", label: "No credential" }
    case "none":
      return { status: "none", label: "No auth needed", tone: "muted" }
    default:
      return { status: tool.auth_status }
  }
}

/**
 * The Crew tools section of /integrations: every crew-scoped MCP server in
 * the workspace, the crew it belongs to as a link, how many agents may call
 * it, its auth state as a pill, and — for the ones without a credential —
 * Connect, which binds one to every agent in the crew.
 */
export function CrewToolsView({
  workspaceId,
  search,
  initialServerId,
  canManage,
}: {
  workspaceId: string
  search: string
  /** `?server=` from a "Connect" link elsewhere: that row opens connecting. */
  initialServerId: string | null
  canManage: boolean
}) {
  const [tools, setTools] = React.useState<CrewTool[] | null>(null)
  const [error, setError] = React.useState<string | null>(null)
  const [openId, setOpenId] = React.useState<string | null>(initialServerId)

  const load = React.useCallback(async () => {
    setError(null)
    try {
      setTools(sortCrewTools(await fetchCrewTools(workspaceId)))
    } catch (e) {
      setError(e instanceof Error ? e.message : "Could not load crew tools")
    }
  }, [workspaceId])

  React.useEffect(() => {
    void load()
  }, [load])

  const q = search.trim().toLowerCase()
  const visible = (tools ?? []).filter(
    (t) => !q || [t.name, t.display_name, t.crew_name, t.crew_slug, t.transport].some((f) => f?.toLowerCase().includes(q)),
  )
  const gaps = (tools ?? []).filter(needsCredential).length

  return (
    <div className="space-y-4 p-4 md:p-6" data-testid="crew-tools-view">
      <div className="flex flex-wrap items-baseline gap-x-2 gap-y-1">
        <h2 className="text-sm font-medium text-foreground/90">Crew tools</h2>
        <span className="text-xs text-muted-foreground">
          MCP servers every agent in a crew can call
          {tools ? ` · ${tools.length} ${tools.length === 1 ? "server" : "servers"}` : ""}
          {gaps > 0 ? ` · ${gaps} without a credential` : ""}
        </span>
        <span className="flex-1" />
        <Button variant="ghost" size="sm" onClick={() => void load()} title="Reload">
          <RefreshCw className="h-3 w-3" /> Refresh
        </Button>
      </div>

      {error ? (
        <div role="alert" className="flex items-center gap-3 rounded-xl border border-destructive/40 bg-destructive/5 px-4 py-3 text-sm">
          <span className="flex-1">{error}</span>
          <Button variant="outline" size="sm" onClick={() => void load()}>Retry</Button>
        </div>
      ) : tools === null ? (
        <div className="h-24 animate-pulse rounded-xl border border-border/60 bg-card" aria-busy="true" />
      ) : tools.length === 0 ? (
        <InlineEmpty
          icon={Wrench}
          text="No crew has an MCP server yet. Add one on a crew's Settings tab under MCP servers, or with the CLI."
          action={<code className="font-mono text-[11px] text-muted-foreground">crewship integration crew add</code>}
        />
      ) : visible.length === 0 ? (
        <InlineEmpty icon={Wrench} text={<>Nothing matches “{search.trim()}”.</>} />
      ) : (
        <ul className="rounded-xl border border-border/60 bg-card divide-y divide-border/50">
          {visible.map((tool) => {
            const gap = needsCredential(tool)
            const open = openId === tool.id
            const pill = authPillProps(tool)
            return (
              <li key={tool.id} id={`crew-tool-${tool.id}`} className={cn(open && "bg-primary/[0.03]")}>
                <div className="flex flex-wrap items-center gap-3 px-4 py-3">
                  <span className="inline-flex h-7 w-7 shrink-0 items-center justify-center rounded-md bg-purple/15 text-purple-hover">
                    <Plug className="h-3.5 w-3.5" aria-hidden />
                  </span>
                  <span className="min-w-0 flex-1 basis-40">
                    <span className="block truncate text-body font-semibold text-foreground/90">{tool.display_name || tool.name}</span>
                    <span className="block truncate font-mono text-[10px] text-muted-foreground">
                      {tool.transport}{tool.endpoint ? ` · ${tool.endpoint}` : tool.command ? ` · ${tool.command}` : ""}{tool.enabled ? "" : " · disabled"}
                    </span>
                  </span>
                  <Link href={entityHref({ kind: "crew", slug: tool.crew_slug, tab: "settings" })} className="inline-flex items-center gap-1.5 text-label text-foreground/85 hover:underline">
                    {tool.crew_name}
                  </Link>
                  <span className="inline-flex items-center gap-1 text-label text-muted-foreground" title="Agents that may call it">
                    <Bot className="h-3 w-3" aria-hidden />
                    {tool.agent_binding_count}
                  </span>
                  <StatusPill status={pill.status} label={pill.label} tone={pill.tone} />
                  {gap && canManage && (
                    <Button variant={open ? "outline" : "soft"} size="sm" onClick={() => setOpenId(open ? null : tool.id)}>
                      {open ? "Close" : tool.auth_status === "expired" ? "Reconnect" : "Connect"}
                    </Button>
                  )}
                </div>
                {open && gap && canManage && (
                  <ConnectPanel
                    workspaceId={workspaceId}
                    tool={tool}
                    onDone={async () => {
                      setOpenId(null)
                      await load()
                    }}
                  />
                )}
              </li>
            )
          })}
        </ul>
      )}
    </div>
  )
}

/** Two ways in: an existing vault credential, or OAuth for an HTTP server. */
function ConnectPanel({ workspaceId, tool, onDone }: { workspaceId: string; tool: CrewTool; onDone: () => Promise<void> }) {
  const [credentials, setCredentials] = React.useState<CredentialOption[] | null>(null)
  const [picked, setPicked] = React.useState("")
  const [busy, setBusy] = React.useState(false)

  React.useEffect(() => {
    let cancelled = false
    apiFetch(`/api/v1/credentials?workspace_id=${encodeURIComponent(workspaceId)}&limit=500`)
      .then((r) => (r.ok ? r.json() : []))
      .then((rows: unknown) => { if (!cancelled) setCredentials(Array.isArray(rows) ? (rows as CredentialOption[]) : []) })
      .catch(() => { if (!cancelled) setCredentials([]) })
    return () => { cancelled = true }
  }, [workspaceId])

  const bind = async (credentialId: string) => {
    setBusy(true)
    try {
      const { bound, failures } = await bindCredentialToCrewTool({ workspaceId, tool, credentialId })
      if (failures.length > 0) {
        toast.error(`Bound for ${bound} ${bound === 1 ? "agent" : "agents"}, ${failures.length} failed`, { description: failures[0] })
      } else {
        toast.success(`${tool.display_name || tool.name} connected for ${bound} ${bound === 1 ? "agent" : "agents"} in ${tool.crew_name}`)
      }
      await onDone()
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "Could not bind the credential")
    } finally {
      setBusy(false)
    }
  }

  const oauthPossible = ENDPOINT_TRANSPORTS.has(tool.transport) && Boolean(tool.endpoint)

  return (
    <div className="grid gap-4 border-t border-border/50 px-4 py-4 md:grid-cols-2" data-testid="crew-tool-connect">
      <div className="space-y-2">
        <div className="inline-flex items-center gap-1.5 text-[11px] font-semibold uppercase tracking-wider text-foreground/70">
          <KeyRound className="h-3.5 w-3.5 text-muted-foreground-soft" aria-hidden /> Use a credential from the vault
        </div>
        <p className="text-label text-muted-foreground">
          Bound as a bearer token for every agent in {tool.crew_name}; they pick it up on their next run.
        </p>
        {credentials === null ? (
          <div className="h-8 animate-pulse rounded-md bg-muted" aria-busy="true" />
        ) : credentials.length === 0 ? (
          <InlineEmpty icon={KeyRound} text="The vault is empty." action={<Link href={entityHref({ kind: "credentials" })} className="text-primary-hover hover:underline">Add a secret →</Link>} />
        ) : (
          <div className="flex items-center gap-2">
            <select
              aria-label="Credential"
              value={picked}
              onChange={(e) => setPicked(e.target.value)}
              className="h-8 min-w-0 flex-1 rounded-md border border-border bg-background px-2 text-sm"
            >
              <option value="">Pick a credential…</option>
              {credentials.map((c) => (
                <option key={c.id} value={c.id}>{c.name}{c.provider ? ` · ${c.provider}` : ""}</option>
              ))}
            </select>
            <Button variant="soft" size="sm" disabled={!picked || busy} onClick={() => void bind(picked)}>
              {busy ? "Binding…" : "Bind"}
            </Button>
          </div>
        )}
        {credentials && credentials.length > 0 && !picked && (
          <p className="text-[11px] text-muted-foreground">Pick a credential to enable Bind.</p>
        )}
      </div>
      <div className="space-y-2">
        <div className="inline-flex items-center gap-1.5 text-[11px] font-semibold uppercase tracking-wider text-foreground/70">
          <Plug className="h-3.5 w-3.5 text-muted-foreground-soft" aria-hidden /> Or sign in with OAuth
        </div>
        {oauthPossible ? (
          <OAuthAutoConnect
            serverName={tool.name}
            mcpURL={tool.endpoint ?? ""}
            workspaceId={workspaceId}
            authStatus={tool.auth_status as "connected" | "missing" | "expired" | "none"}
            onCredentialCreated={bind}
          />
        ) : (
          <p className="text-label text-muted-foreground">
            OAuth needs an HTTP endpoint; this server runs over {tool.transport}. Use a vault credential instead.
          </p>
        )}
      </div>
    </div>
  )
}
