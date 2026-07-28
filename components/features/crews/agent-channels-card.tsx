"use client"

import * as React from "react"
import { Bell, BellOff, Loader2 } from "lucide-react"
import { toast } from "sonner"

import { Button } from "@/components/ui/button"
import { Skeleton } from "@/components/ui/skeleton"
import { Switch } from "@/components/ui/switch"
import { cn } from "@/lib/utils"
import { apiFetch } from "@/lib/api-fetch"
import { invalidate } from "@/lib/stale-cache"
import { useAbilities } from "@/hooks/use-abilities"
import { useNotificationChannels } from "@/hooks/use-notification-channels"
import { useAgentReach } from "@/hooks/use-agent-reach"
import { ProviderMark } from "@/components/features/integrations/provider-marks"

/**
 * Which notification channels this agent may post to, on the tab that already
 * owns "what can this agent reach" — beside Connectors, above Credentials.
 *
 * It belongs here and not on Overview: Skills & Tools is the access surface,
 * and a channel grant is access. Putting it in both places is the
 * two-places-for-one-thing this whole surface has been un-doing.
 *
 * Writable, unlike the first cut. A read-only list next to a Connectors card
 * with "Manage access" reads as an oversight, and the authority question has
 * a clean answer: the server already gates each pairing on the CHANNEL —
 * MANAGER+ for a workspace channel, ownership for a personal one — so a
 * toggle here is checked exactly as it would be from the channel's own page.
 * A refusal surfaces verbatim rather than being pre-guessed in the UI.
 */

interface AgentChannelsCardProps {
  agentId: string
  agentName: string
  workspaceId: string
}

export function AgentChannelsCard({ agentId, agentName, workspaceId }: AgentChannelsCardProps) {
  const { channels: granted, loading, refresh } = useAgentReach(workspaceId, agentId)
  const { channels: allChannels, loading: listLoading } = useNotificationChannels(workspaceId)
  const { abilities } = useAbilities()
  const canManage = abilities.can("manage", "Workspace")

  const [editing, setEditing] = React.useState(false)
  const [busyId, setBusyId] = React.useState<string | null>(null)

  const grantedIds = React.useMemo(() => new Set(granted.map((c) => c.id)), [granted])

  const toggle = async (channelId: string, next: boolean) => {
    setBusyId(channelId)
    try {
      const ws = encodeURIComponent(workspaceId)
      const ch = encodeURIComponent(channelId)
      const res = next
        ? await apiFetch(`/api/v1/notification-channels/${ch}/agents?workspace_id=${ws}`, {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ agent_id: agentId }),
          })
        : await apiFetch(
            `/api/v1/notification-channels/${ch}/agents/${encodeURIComponent(agentId)}?workspace_id=${ws}`,
            { method: "DELETE" },
          )
      if (!res.ok) {
        const body = await res.json().catch(() => null)
        // The server's own words: it knows whether this is a workspace channel
        // needing MANAGER+ or a personal one needing ownership. Guessing here
        // would be a second, weaker copy of that rule.
        throw new Error(body?.error ?? body?.detail ?? `Request failed (${res.status})`)
      }
      invalidate(`notify:${workspaceId}:agent-channels:${agentId}`)
      await refresh()
      toast.success(next ? `${agentName} may now post there` : `${agentName} can no longer post there`)
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "Failed to change the grant")
    } finally {
      setBusyId(null)
    }
  }

  return (
    <div className="overflow-hidden rounded-xl border border-white/8 bg-card">
      <div className="flex flex-wrap items-center gap-2 px-4 py-3">
        <Bell className="h-3.5 w-3.5 shrink-0 text-foreground/60" />
        <span className="text-sm font-medium">Notifications</span>
        <span className="text-[11px] text-muted-foreground">· channels it may post to</span>
        <span className="flex-1" />
        {canManage && !listLoading && allChannels.length > 0 && (
          <Button variant="outline" size="sm" onClick={() => setEditing((v) => !v)}>
            {editing ? "Done" : "Manage access"}
          </Button>
        )}
      </div>

      <div className="border-t border-white/5 px-4 py-3">
        {loading ? (
          <Skeleton className="h-5 w-52 rounded" />
        ) : editing ? (
          <ul className="space-y-1.5">
            {allChannels.map((c) => {
              const on = grantedIds.has(c.id)
              const label = c.type === "shoutrrr" ? (c.provider ?? "chat") : c.type
              return (
                <li key={c.id} className="flex items-center gap-2.5 text-xs">
                  <Switch
                    size="sm"
                    checked={on}
                    disabled={busyId !== null}
                    onCheckedChange={(next) => toggle(c.id, next)}
                    aria-label={`${on ? "Revoke" : "Grant"} ${agentName} access to this ${label} channel`}
                  />
                  <ProviderMark
                    provider={c.provider || c.type}
                    label={label}
                    className="h-4 w-4 rounded-[4px]"
                  />
                  <span className="min-w-0 flex-1 truncate capitalize text-foreground/85">
                    {label}
                  </span>
                  <span className="shrink-0 font-mono text-[10px] text-muted-foreground/60">
                    {c.scope === "user" ? "personal" : "workspace"}
                  </span>
                  {busyId === c.id && (
                    <Loader2 className="h-3 w-3 shrink-0 animate-spin text-muted-foreground" />
                  )}
                </li>
              )
            })}
          </ul>
        ) : granted.length === 0 ? (
          <p className="flex items-start gap-2 text-[11px] leading-relaxed text-muted-foreground">
            <BellOff className="mt-0.5 h-3 w-3 shrink-0" />
            <span>
              {agentName} cannot send a notification of its own accord. That is the default —
              an agent gets no channel until a human grants one.
            </span>
          </p>
        ) : (
          <div className="flex flex-wrap items-center gap-1.5">
            {granted.map((c) => (
              <span
                key={c.id}
                title={
                  c.enabled
                    ? `${agentName} may post to this ${c.provider || c.type} channel`
                    : "The grant stands, but the channel is switched off — nothing is delivered"
                }
                className={cn(
                  "inline-flex items-center gap-1.5 rounded-full border px-2 py-0.5 text-[11px]",
                  c.enabled
                    ? "border-white/10 bg-white/[0.03] text-foreground/85"
                    : "border-white/8 bg-white/[0.02] text-muted-foreground line-through",
                )}
              >
                <ProviderMark
                  provider={c.provider || c.type}
                  label={c.provider || c.type}
                  className="h-4 w-4 rounded-[4px]"
                />
                <span className="capitalize">{c.provider || c.type}</span>
              </span>
            ))}
          </div>
        )}
      </div>
    </div>
  )
}
