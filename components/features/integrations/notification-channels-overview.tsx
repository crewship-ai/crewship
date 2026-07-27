"use client"

import { useCallback, useEffect, useState } from "react"
import { Bot, Globe, Mail, MessageSquare, User } from "lucide-react"
import { apiFetch } from "@/lib/api-fetch"
import { SettingsCard, SettingsEmpty } from "@/components/features/settings/shared"
import { Skeleton } from "@/components/ui/skeleton"
import { cn } from "@/lib/utils"
import type { NotificationChannel } from "@/hooks/use-notification-channels"

interface OverviewProps {
  workspaceId: string
}

interface ChannelWithAgents extends NotificationChannel {
  agentCount?: number
}

/**
 * The admin overview: every notification connection in the workspace on one
 * page, including other members' personal channels.
 *
 * This is the reason the whole surface moved out of Settings. An admin
 * running a workspace with dozens of people needs to be able to answer "what
 * is this instance wired into, and who wired it" without opening each
 * member's settings — which is not possible at all when the connections live
 * behind a personal-preferences door.
 *
 * It shows THAT a member has a channel and of what kind, never where it
 * points: the server redacts other members' destinations, because a Telegram
 * chat id is a contact detail rather than workspace configuration.
 */
export function NotificationChannelsOverview({ workspaceId }: OverviewProps) {
  const [channels, setChannels] = useState<ChannelWithAgents[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)

  const load = useCallback(async () => {
    if (!workspaceId) return
    setLoading(true)
    try {
      const res = await apiFetch(
        `/api/v1/notification-channels?workspace_id=${encodeURIComponent(workspaceId)}&scope=all`,
      )
      if (!res.ok) throw new Error(`load connections: ${res.status}`)
      const body = await res.json()
      const list: ChannelWithAgents[] = Array.isArray(body?.channels) ? body.channels : []

      // Agent grants per channel — the "can an agent speak here?" column,
      // which is the part of this page an admin is most likely auditing.
      await Promise.all(
        list.map(async (ch) => {
          try {
            const r = await apiFetch(
              `/api/v1/notification-channels/${encodeURIComponent(ch.id)}/agents?workspace_id=${encodeURIComponent(workspaceId)}`,
            )
            if (r.ok) {
              const b = await r.json()
              ch.agentCount = Array.isArray(b?.agents) ? b.agents.length : 0
            }
          } catch {
            // A per-channel failure must not blank the whole page.
          }
        }),
      )
      setChannels(list)
      setError(null)
    } catch (e) {
      setError(e instanceof Error ? e.message : "failed to load")
      setChannels([])
    } finally {
      setLoading(false)
    }
  }, [workspaceId])

  useEffect(() => {
    void load()
  }, [load])

  if (loading && channels.length === 0) {
    return <Skeleton className="h-[220px] rounded-xl" />
  }

  return (
    <SettingsCard
      title="All connections"
      description={
        channels.length === 0
          ? "Nothing connected yet"
          : `${channels.length} connection${channels.length === 1 ? "" : "s"} across this workspace`
      }
    >
      {error ? (
        <SettingsEmpty>Failed to load connections ({error})</SettingsEmpty>
      ) : channels.length === 0 ? (
        <SettingsEmpty>No notification channels in this workspace yet.</SettingsEmpty>
      ) : (
        channels.map((ch, i) => {
          const Icon =
            ch.type === "email" ? Mail : ch.type === "shoutrrr" ? MessageSquare : Globe
          const isPersonal = ch.scope === "user"
          const target =
            ch.type === "email" ? ch.to : ch.type === "shoutrrr" ? ch.provider : ch.url
          return (
            <div
              key={ch.id}
              className={cn(
                "flex items-center gap-3 px-4 py-2.5 text-xs",
                i < channels.length - 1 && "border-b border-border/40",
              )}
            >
              <Icon className="size-3 shrink-0 text-muted-foreground" />
              <span className="min-w-0 flex-1 truncate font-mono text-[11px]">
                {/* Empty target = a personal channel belonging to someone
                    else, redacted server-side. Say so, rather than showing a
                    blank cell that reads as a bug. */}
                {target || <span className="text-muted-foreground/60 italic">private</span>}
              </span>

              <span className="flex shrink-0 items-center gap-1 text-[10px] text-muted-foreground">
                {isPersonal ? <User className="size-2.5" /> : <Globe className="size-2.5" />}
                {isPersonal ? "personal" : "workspace"}
              </span>

              {ch.agentCount !== undefined && ch.agentCount > 0 && (
                <span
                  className="flex shrink-0 items-center gap-1 text-[10px] text-muted-foreground"
                  title={`${ch.agentCount} agent${ch.agentCount === 1 ? "" : "s"} may post here`}
                >
                  <Bot className="size-2.5" />
                  {ch.agentCount}
                </span>
              )}

              <span className="shrink-0 text-[10px] text-muted-foreground/70">
                {ch.categories && ch.categories.length > 0
                  ? `${ch.categories.length} categories`
                  : "all categories"}
              </span>

              {!ch.enabled && (
                <span className="shrink-0 text-[10px] text-muted-foreground/70">(disabled)</span>
              )}
            </div>
          )
        })
      )}
    </SettingsCard>
  )
}
