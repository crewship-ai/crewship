"use client"

import { useCallback, useEffect, useState } from "react"
import { Bell, Plug, TriangleAlert } from "lucide-react"
import {
  CreateSurfaceLoading,
  CreateSurfaceNotice,
  CreateSurfaceSection,
  CreateSurfaceToggleRow,
} from "@/components/layout/create-surface"
import { Switch } from "@/components/ui/switch"
import { apiFetch } from "@/lib/api-fetch"
import { devWarn } from "@/lib/client-log"

/**
 * What this agent may reach — its own integrations and notification channels.
 *
 * Both are per-agent, not per-crew, and that is the point: a Security Analyst
 * and a Copywriter in the same crew should not necessarily hold the same
 * tools. The crew's container decides what is INSTALLED; these decide what
 * this one agent may CALL and where it may post.
 *
 * ## Why this is applied after create, not in the create body
 *
 * POST /api/v1/agents takes no bindings — createAgentRequest has no field for
 * either (agents_create.go). Both endpoints are keyed on an agent that
 * already exists:
 *
 *   POST /api/v1/agents/{agentId}/integrations        {mcp_server_id, mcp_server_scope}
 *   POST /api/v1/notification-channels/{id}/agents    {agent_id}
 *
 * So the form collects the intent and `applyAgentAccess` spends it once the
 * agent has an id — the same two-phase shape the crew wizard already uses for
 * its PATCH-only fields. A binding that fails is reported, never swallowed:
 * the agent exists either way, and an agent silently missing the tool it was
 * created for is worse than being told to add it on the canvas.
 */

export interface AccessIntegration {
  id: string
  name: string
  display_name: string
  transport: string
  enabled: boolean
  /** How many agents in the workspace are bound to this server, from
   *  GET /api/v1/integrations. Drives the opt-in warning below. */
  agent_binding_count?: number
}

export interface AccessChannel {
  id: string
  type: string
  provider?: string
  to?: string
  url?: string
  enabled: boolean
  scope?: string
}

export interface AgentAccessCatalog {
  integrations: AccessIntegration[]
  channels: AccessChannel[]
  loading: boolean
  /** Set when a list could not be read. The section still renders. */
  error: string | null
}

/**
 * Read both catalogues.
 *
 * Deliberately tolerant: a workspace with no integrations and no channels is
 * the common case on a fresh install, and neither list failing should stop
 * anyone creating an agent. `error` drives a notice, not a block.
 */
export function useAgentAccessCatalog(workspaceId: string, enabled: boolean): AgentAccessCatalog {
  const [integrations, setIntegrations] = useState<AccessIntegration[]>([])
  const [channels, setChannels] = useState<AccessChannel[]>([])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    if (!enabled || !workspaceId) return
    let cancelled = false
    setLoading(true)
    setError(null)

    const ws = encodeURIComponent(workspaceId)
    // Both routes are wsCtx-wrapped and 400 without workspace_id in the query
    // — the same trap the crew-template list fell into.
    Promise.allSettled([
      apiFetch(`/api/v1/integrations?workspace_id=${ws}`).then((r) =>
        r.ok ? r.json() : Promise.reject(new Error(`integrations HTTP ${r.status}`)),
      ),
      apiFetch(`/api/v1/notification-channels?workspace_id=${ws}`).then((r) =>
        r.ok ? r.json() : Promise.reject(new Error(`channels HTTP ${r.status}`)),
      ),
    ]).then(([ints, chans]) => {
      if (cancelled) return
      const problems: string[] = []

      if (ints.status === "fulfilled") {
        setIntegrations(Array.isArray(ints.value) ? ints.value : [])
      } else {
        problems.push("integrations")
        devWarn("agent access: integrations", ints.reason)
      }

      if (chans.status === "fulfilled") {
        // This one is wrapped: {"channels": [...]}. The integrations route
        // returns a bare array. Not a typo — they are different handlers.
        const list = chans.value?.channels
        setChannels(Array.isArray(list) ? list : [])
      } else {
        problems.push("notification channels")
        devWarn("agent access: channels", chans.reason)
      }

      setError(problems.length ? problems.join(" and ") : null)
      setLoading(false)
    })

    return () => { cancelled = true }
  }, [workspaceId, enabled])

  return { integrations, channels, loading, error }
}

/** A channel has no name column — build one that does not leak the target. */
export function channelLabel(c: AccessChannel): string {
  const kind = c.provider || c.type || "channel"
  if (c.to) return `${kind} · ${c.to}`
  if (c.url) {
    // Host only. A webhook URL's path is the credential for most providers
    // (Slack, Discord), and this is a picker, not the channel's detail page.
    try {
      return `${kind} · ${new URL(c.url).host}`
    } catch {
      return kind
    }
  }
  return kind
}

export interface AgentAccessSelection {
  integrationIds: string[]
  channelIds: string[]
}

/**
 * Spend the selection against a freshly-created agent.
 *
 * Returns the labels that failed rather than throwing: the agent is already
 * created by the time this runs, so a rejected binding is a partial result to
 * report, not an error to unwind.
 */
export async function applyAgentAccess(
  workspaceId: string,
  agentId: string,
  selection: AgentAccessSelection,
  catalog: { integrations: AccessIntegration[]; channels: AccessChannel[] },
): Promise<string[]> {
  const ws = encodeURIComponent(workspaceId)
  const failed: string[] = []

  await Promise.all([
    ...selection.integrationIds.map(async (id) => {
      const label = catalog.integrations.find((i) => i.id === id)?.display_name || id
      try {
        const res = await apiFetch(`/api/v1/agents/${encodeURIComponent(agentId)}/integrations?workspace_id=${ws}`, {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          // Everything reachable from GET /api/v1/integrations is
          // workspace-scoped by definition; crew-scoped servers are a
          // different list and are not offered here.
          body: JSON.stringify({ mcp_server_id: id, mcp_server_scope: "workspace", enabled: true }),
        })
        if (!res.ok) failed.push(label)
      } catch (e) {
        devWarn("bind integration", e)
        failed.push(label)
      }
    }),
    ...selection.channelIds.map(async (id) => {
      const channel = catalog.channels.find((c) => c.id === id)
      const label = channel ? channelLabel(channel) : id
      try {
        // Note the shape: this one is keyed on the CHANNEL and takes the
        // agent in the body, the mirror of the integrations route.
        const res = await apiFetch(`/api/v1/notification-channels/${encodeURIComponent(id)}/agents?workspace_id=${ws}`, {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ agent_id: agentId }),
        })
        if (!res.ok) failed.push(label)
      } catch (e) {
        devWarn("pair channel", e)
        failed.push(label)
      }
    }),
  ])

  return failed
}

interface SectionProps {
  catalog: AgentAccessCatalog
  selection: AgentAccessSelection
  onChange: (next: AgentAccessSelection) => void
}

export function AgentAccessSection({ catalog, selection, onChange }: SectionProps) {
  const { integrations, channels, loading, error } = catalog

  const toggle = useCallback(
    (key: keyof AgentAccessSelection, id: string) => {
      const current = selection[key]
      onChange({
        ...selection,
        [key]: current.includes(id) ? current.filter((x) => x !== id) : [...current, id],
      })
    },
    [selection, onChange],
  )

  return (
    <CreateSurfaceSection
      title="Tools & notifications"
      icon={Plug}
      accent="sky"
      hint="what this agent may reach — not the whole crew"
    >
      {loading && <CreateSurfaceLoading rows={2} />}

      {error && (
        <CreateSurfaceNotice tone="warn" icon={TriangleAlert}>
          The {error} list did not load. The agent will be created without any; add them from its
          canvas afterwards.
        </CreateSurfaceNotice>
      )}

      {!loading && !error && integrations.length === 0 && channels.length === 0 && (
        <p className="text-[11px] leading-relaxed text-muted-foreground">
          This workspace has no integrations or notification channels yet. Add them under
          Integrations and Settings → Notifications, then grant them per agent — here, or on the
          agent&apos;s canvas.
        </p>
      )}

      {/* No "granting this revokes it from everyone else" warning here any
       * more, and that is the point of #2072 rather than an omission.
       *
       * A workspace server used to resolve for every agent exactly while
       * NOBODY was bound to it — "available to all" was inferred from
       * `COUNT(bindings) == 0` — so the first grant made on this form flipped
       * the server to opt-in and revoked it from the whole roster. #2070
       * warned about that here because the resolver could not be trusted;
       * the audience is now a stored column (`default_access`) that only an
       * explicit change touches, so a grant made here costs no other agent
       * anything and there is nothing to warn about. */}

      {integrations.length > 0 && (
        <div className="flex flex-col gap-1.5">
          <span className="text-[11px] uppercase tracking-wider text-muted-foreground-soft">
            Integrations
          </span>
          {integrations.map((i) => (
            <CreateSurfaceToggleRow
              key={i.id}
              icon={Plug}
              accent="sky"
              label={i.display_name || i.name}
              hint={i.enabled ? i.transport : `${i.transport} · disabled workspace-wide`}
              control={
                <Switch
                  checked={selection.integrationIds.includes(i.id)}
                  onCheckedChange={() => toggle("integrationIds", i.id)}
                  aria-label={i.display_name || i.name}
                  // A workspace integration that is off reaches nothing, so
                  // granting it here would be a promise the platform breaks.
                  disabled={!i.enabled}
                />
              }
            />
          ))}
        </div>
      )}

      {channels.length > 0 && (
        <div className="flex flex-col gap-1.5">
          <span className="text-[11px] uppercase tracking-wider text-muted-foreground-soft">
            Notification channels
          </span>
          {channels.map((c) => (
            <CreateSurfaceToggleRow
              key={c.id}
              icon={Bell}
              accent="amber"
              label={channelLabel(c)}
              hint={c.scope === "user" ? "personal channel" : "workspace channel"}
              control={
                <Switch
                  checked={selection.channelIds.includes(c.id)}
                  onCheckedChange={() => toggle("channelIds", c.id)}
                  aria-label={channelLabel(c)}
                  disabled={!c.enabled}
                />
              }
            />
          ))}
        </div>
      )}
    </CreateSurfaceSection>
  )
}
