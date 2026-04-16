"use client"

import * as React from "react"
import {
  Bot,
  Check,
  ExternalLink,
  Globe,
  KeyRound,
  Loader2,
  Plus,
  Settings2,
  Terminal,
  Trash2,
  Users,
} from "lucide-react"
import { toast } from "sonner"

import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { SectionCard } from "@/components/ui/section-card"
import { StatusBadge } from "@/components/ui/status-badge"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { CredentialPicker } from "@/components/features/mcp/components/credential-picker"
import { useCredentials } from "@/components/features/mcp/hooks/use-credentials"
import { cn } from "@/lib/utils"

import { OAuthAutoConnect } from "./oauth-auto-connect"
import { TestConnectionButton } from "./test-connection-button"
import { parseArgs, parseEnv, serializeArgs, serializeEnv } from "./helpers"
import type { AgentInfo, CrewInfo, CrewIntegration } from "./types"

interface ExpandedPanelProps {
  server: CrewIntegration
  crews: CrewInfo[]
  agents: AgentInfo[]
  agentBindings: Record<string, Set<string>>
  bindingIds: Record<string, Record<string, string>>
  confirmDeleteId: string | null
  canManage: boolean
  workspaceId: string | null
  onPatch: (fields: Record<string, unknown>) => Promise<void>
  onCrewMove: (newCrewId: string) => Promise<void>
  onAgentToggle: (agent: AgentInfo, hasAccess: boolean, hasAny: boolean) => Promise<void>
  onDelete: () => Promise<void>
  onConfirmDeleteChange: (v: boolean) => void
  onRefresh: () => void
}

/**
 * Expanded row content for a single integration entry — splits into
 * Scope & Assignment, Server Configuration, OAuth, Environment
 * Variables, Test Connection, and Actions. All handlers flow up via
 * props so the parent list component stays the single source of truth
 * for the integration collection.
 */
export function ExpandedPanel({
  server,
  crews,
  agents,
  agentBindings,
  bindingIds,
  confirmDeleteId,
  onRefresh,
  canManage,
  workspaceId,
  onPatch,
  onCrewMove,
  onAgentToggle,
  onDelete,
  onConfirmDeleteChange,
}: ExpandedPanelProps) {
  const { credentials, loading: credLoading, fetchCredentials, addCredential } = useCredentials(
    canManage ? (workspaceId ?? undefined) : undefined,
  )

  // Local state for inputs (save on blur)
  const [name, setName] = React.useState(server.name)
  const [displayName, setDisplayName] = React.useState(server.display_name || "")
  const [command, setCommand] = React.useState(server.command ?? "")
  const [args, setArgs] = React.useState(parseArgs(server.args_json))
  const [url, setUrl] = React.useState(server.endpoint ?? "")
  const [transport, setTransport] = React.useState(server.transport)
  const [envVars, setEnvVars] = React.useState(parseEnv(server.env_json))

  // Sync local state if server data changes (after refetch)
  React.useEffect(() => {
    setName(server.name)
    setDisplayName(server.display_name || "")
    setCommand(server.command ?? "")
    setArgs(parseArgs(server.args_json))
    setUrl(server.endpoint ?? "")
    setTransport(server.transport)
    setEnvVars(parseEnv(server.env_json))
  }, [server])

  const hasAnyBindings = (agentBindings[server.id]?.size ?? 0) > 0

  function handleBlur(field: string, value: string) {
    switch (field) {
      case "name":
        if (value !== server.name) onPatch({ name: value })
        break
      case "display_name":
        if (value !== (server.display_name || "")) onPatch({ display_name: value })
        break
      case "command":
        if (value !== (server.command ?? "")) onPatch({ command: value })
        break
      case "args":
        if (value !== parseArgs(server.args_json)) onPatch({ args_json: serializeArgs(value) })
        break
      case "url":
        if (value !== (server.endpoint ?? "")) onPatch({ endpoint: value })
        break
    }
  }

  function handleTransportChange(newTransport: string) {
    setTransport(newTransport)
    if (newTransport !== server.transport) {
      onPatch({ transport: newTransport })
    }
  }

  function handleEnvBlur() {
    const newJson = serializeEnv(envVars)
    const oldJson = server.env_json ?? "{}"
    if (newJson !== oldJson) {
      onPatch({ env_json: newJson })
    }
  }

  function addEnvVar() {
    setEnvVars((prev) => [...prev, { key: "", value: "" }])
  }

  function removeEnvVar(idx: number) {
    const updated = envVars.filter((_, i) => i !== idx)
    setEnvVars(updated)
    // Save immediately on remove
    const newJson = serializeEnv(updated)
    const oldJson = server.env_json ?? "{}"
    if (newJson !== oldJson) {
      onPatch({ env_json: newJson })
    }
  }

  function updateEnvVar(idx: number, field: "key" | "value", val: string) {
    setEnvVars((prev) => prev.map((e, i) => (i === idx ? { ...e, [field]: val } : e)))
  }

  // OAuth discovery state
  const [oauthDiscovered, setOauthDiscovered] = React.useState(false)
  const [discovering, setDiscovering] = React.useState(false)

  async function discoverOAuth(mcpUrl: string) {
    if (!workspaceId || transport !== "streamable-http") return
    try {
      const parsed = new URL(mcpUrl)
      if (parsed.protocol !== "https:" && parsed.protocol !== "http:") return
    } catch {
      setOauthDiscovered(false)
      return
    }
    setDiscovering(true)
    try {
      const res = await fetch(
        `/api/v1/oauth/discover?workspace_id=${workspaceId}`,
        {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ mcp_url: mcpUrl }),
        },
      )
      if (res.ok) {
        setOauthDiscovered(true)
      } else {
        setOauthDiscovered(false)
      }
    } catch {
      setOauthDiscovered(false)
    } finally {
      setDiscovering(false)
    }
  }

  // Auto-discover on mount if URL is already set
  React.useEffect(() => {
    if (server.transport === "streamable-http" && server.endpoint && server.auth_status === "none") {
      discoverOAuth(server.endpoint)
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [server.id])

  const isConfirming = confirmDeleteId === server.id

  return (
    <div className="bg-surface-subtle border-t border-border px-6 py-5 space-y-4">
      {/* Section 1: Scope & Assignment */}
      <SectionCard surface="subtle" className="p-4 space-y-4">
        <div className="flex items-center gap-2 text-body font-medium">
          <Users className="h-4 w-4 text-muted-foreground" />
          Scope & Assignment
        </div>

        <div className="grid grid-cols-1 sm:grid-cols-2 gap-4">
          <div className="space-y-1.5">
            <Label htmlFor={`crew-${server.id}`} className="text-label">
              Assigned to crew
            </Label>
            <Select
              value={server.crew_id}
              onValueChange={(v) => onCrewMove(v)}
              disabled={!canManage}
            >
              <SelectTrigger id={`crew-${server.id}`} className="h-8 text-label">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {crews.map((c) => (
                  <SelectItem key={c.id} value={c.id}>
                    {c.name}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
        </div>

        {/* Agent assignment */}
        {agents.length > 0 && (
          <div className="space-y-2">
            <Label className="text-label">Agent access</Label>
            <div className="flex flex-wrap gap-1.5">
              {agents.map((a) => {
                const bound = agentBindings[server.id]?.has(a.id) ?? false
                const hasAccess = hasAnyBindings ? bound : false
                return (
                  <button
                    key={a.id}
                    type="button"
                    aria-pressed={hasAccess}
                    className={cn(
                      "inline-flex items-center gap-1.5 rounded-md border px-2.5 py-1 text-label transition-colors",
                      hasAccess
                        ? "bg-primary/10 border-primary/30 text-primary"
                        : "bg-muted/30 border-border text-muted-foreground"
                    )}
                    onClick={() => onAgentToggle(a, hasAccess, hasAnyBindings)}
                    disabled={!canManage}
                    title={hasAccess ? `Remove ${a.name} access` : `Grant ${a.name} access`}
                  >
                    {hasAccess && <Check className="h-3 w-3" />}
                    <Bot className="h-3 w-3" />
                    {a.name}
                  </button>
                )
              })}
            </div>
            <p className="text-label text-muted-foreground">
              {hasAnyBindings
                ? "Only selected agents have access. Click to toggle."
                : "No agents have access yet. Click an agent to grant access."}
            </p>
          </div>
        )}
      </SectionCard>

      {/* Section 2: Server Configuration */}
      <SectionCard surface="subtle" className="p-4 space-y-4">
        <div className="flex items-center gap-2 text-body font-medium">
          <Settings2 className="h-4 w-4 text-muted-foreground" />
          Server Configuration
        </div>

        <div className="grid grid-cols-1 sm:grid-cols-2 gap-4">
          <div className="space-y-1.5">
            <Label htmlFor={`name-${server.id}`} className="text-label">
              Server name
            </Label>
            <Input
              id={`name-${server.id}`}
              className="h-8 text-label"
              value={name}
              onChange={(e) => setName(e.target.value)}
              onBlur={() => handleBlur("name", name)}
              readOnly={!canManage}
            />
          </div>
          <div className="space-y-1.5">
            <Label htmlFor={`display-${server.id}`} className="text-label">
              Display name
            </Label>
            <Input
              id={`display-${server.id}`}
              className="h-8 text-label"
              value={displayName}
              onChange={(e) => setDisplayName(e.target.value)}
              onBlur={() => handleBlur("display_name", displayName)}
              readOnly={!canManage}
            />
          </div>
        </div>

        <div className="space-y-1.5">
          <Label htmlFor={`transport-${server.id}`} className="text-label">
            Transport
          </Label>
          <Select
            value={transport}
            onValueChange={handleTransportChange}
            disabled={!canManage}
          >
            <SelectTrigger id={`transport-${server.id}`} className="h-8 text-label w-40">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="stdio">
                <span className="flex items-center gap-1.5">
                  <Terminal className="h-3 w-3" /> Stdio
                </span>
              </SelectItem>
              <SelectItem value="streamable-http">
                <span className="flex items-center gap-1.5">
                  <Globe className="h-3 w-3" /> HTTP
                </span>
              </SelectItem>
            </SelectContent>
          </Select>
        </div>

        {transport === "stdio" ? (
          <div className="grid grid-cols-1 sm:grid-cols-2 gap-4">
            <div className="space-y-1.5">
              <Label htmlFor={`cmd-${server.id}`} className="text-label">
                Command
              </Label>
              <Input
                id={`cmd-${server.id}`}
                className="h-8 text-label font-mono"
                placeholder="npx"
                value={command}
                onChange={(e) => setCommand(e.target.value)}
                onBlur={() => handleBlur("command", command)}
                readOnly={!canManage}
              />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor={`args-${server.id}`} className="text-label">
                Arguments
              </Label>
              <Input
                id={`args-${server.id}`}
                className="h-8 text-label font-mono"
                placeholder="-y @modelcontextprotocol/server-github"
                value={args}
                onChange={(e) => setArgs(e.target.value)}
                onBlur={() => handleBlur("args", args)}
                readOnly={!canManage}
              />
            </div>
          </div>
        ) : (
          <div className="space-y-1.5">
            <div className="flex items-center gap-2">
              <Label htmlFor={`url-${server.id}`} className="text-label">
                URL
              </Label>
              {discovering && (
                <span className="inline-flex items-center gap-1 text-micro text-muted-foreground">
                  <Loader2 className="h-3 w-3 animate-spin" />
                  Checking...
                </span>
              )}
              {!discovering && oauthDiscovered && (
                <StatusBadge status="IN_PROGRESS" label={
                  <span className="inline-flex items-center gap-1">
                    <ExternalLink className="h-2.5 w-2.5" />
                    OAuth detected
                  </span>
                } />
              )}
            </div>
            <Input
              id={`url-${server.id}`}
              className="h-8 text-label font-mono"
              placeholder="https://example.com/mcp"
              value={url}
              onChange={(e) => setUrl(e.target.value)}
              onBlur={() => {
                handleBlur("url", url)
                if (url && url !== (server.endpoint ?? "")) {
                  discoverOAuth(url)
                }
              }}
              readOnly={!canManage}
            />
          </div>
        )}
      </SectionCard>

      {/* Section 3: OAuth Auto-Connect (HTTP servers only) */}
      {canManage && transport === "streamable-http" && (url || server.endpoint) && (server.auth_status !== "none" || oauthDiscovered) && (
        <OAuthAutoConnect
          serverName={server.name}
          mcpURL={url || server.endpoint || ""}
          workspaceId={workspaceId}
          authStatus={server.auth_status}
          onCredentialCreated={async (credId: string) => {
            if (!workspaceId) return
            const failures: string[] = []
            // Update existing bindings with credential
            const bindingsForServer = agentBindings[server.id]
            if (bindingsForServer && bindingsForServer.size > 0) {
              for (const agentId of Array.from(bindingsForServer)) {
                const bId = bindingIds[server.id]?.[agentId]
                if (bId) {
                  try {
                    const res = await fetch(`/api/v1/agents/${agentId}/integrations/${bId}?workspace_id=${workspaceId}`, {
                      method: "PATCH",
                      headers: { "Content-Type": "application/json" },
                      body: JSON.stringify({ credential_id: credId, cred_type: "bearer" }),
                    })
                    if (!res.ok) failures.push(`patch agent ${agentId}: HTTP ${res.status}`)
                  } catch (e) {
                    failures.push(`patch agent ${agentId}: ${String(e)}`)
                  }
                }
              }
            } else {
              // No bindings yet — auto-grant access to ALL agents in the crew with credential
              for (const agent of agents) {
                try {
                  const res = await fetch(`/api/v1/agents/${agent.id}/integrations?workspace_id=${workspaceId}`, {
                    method: "POST",
                    headers: { "Content-Type": "application/json" },
                    body: JSON.stringify({
                      mcp_server_id: server.id,
                      mcp_server_scope: "crew",
                      credential_id: credId,
                      cred_type: "bearer",
                      enabled: true,
                    }),
                  })
                  if (!res.ok) failures.push(`grant agent ${agent.id}: HTTP ${res.status}`)
                } catch (e) {
                  failures.push(`grant agent ${agent.id}: ${String(e)}`)
                }
              }
            }
            onRefresh()
            if (failures.length > 0) {
              toast.error(`OAuth connected but ${failures.length} binding(s) failed — check logs`)
              console.error("agent binding failures after OAuth connect", failures)
            } else {
              toast.success("OAuth connected! All agents have access.")
            }
          }}
        />
      )}

      {/* Section 4: Environment Variables
          Hidden only for HTTP servers that actually use OAuth. Non-OAuth
          streamable-http servers still need API keys or other env-based auth. */}
      {!(transport === "streamable-http" && (server.auth_status !== "none" || oauthDiscovered)) && <SectionCard surface="subtle" className="p-4 space-y-4">
        <div className="flex items-center justify-between">
          <div className="flex items-center gap-2 text-body font-medium">
            <KeyRound className="h-4 w-4 text-muted-foreground" />
            Environment Variables
          </div>
          {canManage && (
            <Button
              variant="outline"
              size="sm"
              className="h-7 text-label"
              onClick={addEnvVar}
            >
              <Plus className="mr-1 h-3 w-3" />
              Add Variable
            </Button>
          )}
        </div>

        {envVars.length === 0 ? (
          <p className="text-label text-muted-foreground">No environment variables configured.</p>
        ) : (
          <div className="space-y-2">
            {envVars.map((env, idx) => (
              <div key={idx} className="flex items-center gap-2">
                <Input
                  className="h-8 text-label font-mono flex-1"
                  placeholder="KEY"
                  value={env.key}
                  onChange={(e) => updateEnvVar(idx, "key", e.target.value)}
                  onBlur={handleEnvBlur}
                  readOnly={!canManage}
                  aria-label={`Environment variable key ${idx + 1}`}
                />
                <span className="text-label text-muted-foreground">=</span>
                {canManage && workspaceId ? (
                  <div className="flex-1">
                    <CredentialPicker
                      envKey={env.key}
                      envValue={env.value}
                      credentials={credentials}
                      credLoading={credLoading}
                      workspaceId={workspaceId}
                      onFetchCredentials={fetchCredentials}
                      onAddCredential={addCredential}
                      onChangeValue={(val) => {
                        updateEnvVar(idx, "value", val)
                        // Save immediately after credential selection
                        const updated = envVars.map((e, i) => (i === idx ? { ...e, value: val } : e))
                        onPatch({ env_json: serializeEnv(updated) })
                      }}
                    />
                  </div>
                ) : (
                  <Input
                    className="h-8 text-label font-mono flex-1"
                    placeholder="value"
                    value={env.value ? "••••••••" : ""}
                    readOnly
                    tabIndex={-1}
                    aria-label={`Environment variable value ${idx + 1} (redacted)`}
                  />
                )}
                {canManage && (
                  <Button
                    variant="ghost"
                    size="sm"
                    className="h-8 w-8 p-0 text-muted-foreground hover:text-destructive"
                    onClick={() => removeEnvVar(idx)}
                    aria-label={`Remove environment variable ${env.key || idx + 1}`}
                  >
                    <Trash2 className="h-3.5 w-3.5" />
                  </Button>
                )}
              </div>
            ))}
          </div>
        )}
      </SectionCard>}

      {/* Section 5: Test Connection */}
      {canManage && <TestConnectionButton
        serverId={server.id}
        crewId={server.crew_id}
        workspaceId={workspaceId}
      />}

      {/* Section 6: Actions */}
      {canManage && (
        <div className="flex justify-end">
          {isConfirming ? (
            <div className="flex items-center gap-2">
              <span className="text-body text-muted-foreground">Delete this integration?</span>
              <Button
                variant="destructive"
                size="sm"
                onClick={onDelete}
              >
                Confirm Delete
              </Button>
              <Button
                variant="outline"
                size="sm"
                onClick={() => onConfirmDeleteChange(false)}
              >
                Cancel
              </Button>
            </div>
          ) : (
            <Button
              variant="outline"
              size="sm"
              className="text-destructive hover:text-destructive hover:bg-destructive/10"
              onClick={() => onConfirmDeleteChange(true)}
            >
              <Trash2 className="mr-1.5 h-3.5 w-3.5" />
              Delete Integration
            </Button>
          )}
        </div>
      )}
    </div>
  )
}
