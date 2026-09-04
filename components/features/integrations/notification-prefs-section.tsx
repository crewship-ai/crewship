"use client"

import { useCallback, useMemo, useState } from "react"
import { channelMatches } from "./delivery-search"
import { Bell, BellOff, Check, Globe, Mail, MessageSquare, Send, VolumeX } from "lucide-react"
import { toast } from "sonner"
import { Button } from "@/components/ui/button"
import { Skeleton } from "@/components/ui/skeleton"
import { Spinner } from "@/components/ui/spinner"
import { cn } from "@/lib/utils"
import { useNotificationChannels } from "@/hooks/use-notification-channels"
import { useNotificationPrefs, type PrefCell } from "@/hooks/use-notification-prefs"
import {
  MUTE_CATEGORY,
  NOTIFICATION_CATEGORY_GROUPS,
  labelForCategory,
} from "@/lib/notification-categories"
import { SettingsCard, SettingsEmpty } from "@/components/features/settings/shared"

interface NotificationPrefsSectionProps {
  workspaceId: string
  /** The page's search box, narrowing the connection columns. */
  search?: string
}

function channelIcon(type: string) {
  if (type === "email") return Mail
  if (type === "shoutrrr") return MessageSquare
  return Globe
}

/**
 * Settings → Notification prefs: the Linear/Novu-style category x channel
 * matrix (issue #1412) — the AUTHENTICATED caller's OWN preferences over
 * every workspace channel an admin allowlisted for that category, plus
 * their own personal channels. A cell toggles off/immediate; the "*" mute
 * row overrides every cell for that channel.
 */
export function NotificationPrefsSection({ workspaceId, search = "" }: NotificationPrefsSectionProps) {
  const { channels, loading: channelsLoading } = useNotificationChannels(workspaceId)
  const { cells, loading: prefsLoading, error, setCell } = useNotificationPrefs(workspaceId)
  const [pendingKey, setPendingKey] = useState<string | null>(null)
  const [testingId, setTestingId] = useState<string | null>(null)
  const { sendTest } = useNotificationChannels(workspaceId)

  const loading = channelsLoading || prefsLoading
  const usableChannels = useMemo(
    () => channels.filter((c) => c.enabled && channelMatches(c, search)),
    [channels, search],
  )

  const stateOf = useCallback(
    (category: string, channelId: string): PrefCell["state"] => {
      const cell = cells.find((c) => c.category === category && c.channel_id === channelId)
      return cell?.state ?? "off"
    },
    [cells],
  )

  const isMuted = useCallback(
    (channelId: string) => stateOf(MUTE_CATEGORY, channelId) === "immediate",
    [stateOf],
  )

  const channelLabel = useCallback(
    (channelId: string) => {
      const ch = usableChannels.find((c) => c.id === channelId)
      if (!ch) return channelId
      return ch.type === "email" ? ch.to : ch.type === "shoutrrr" ? ch.provider : ch.url
    },
    [usableChannels],
  )

  const handleToggle = useCallback(
    async (category: string, channelId: string) => {
      const key = `${category}:${channelId}`
      const current = stateOf(category, channelId)
      const next: PrefCell["state"] = current === "immediate" ? "off" : "immediate"
      // Taxonomy v2 replaced the hardcoded 9-category list this used to read.
      // labelForCategory also covers the retired keys, so a stored preference
      // written before the migration still names itself in the toast.
      const catLabel = labelForCategory(category)
      setPendingKey(key)
      try {
        await setCell({ category, channel_id: channelId, state: next })
        toast.success(
          next === "immediate" ? `${catLabel} set to immediate delivery` : `${catLabel} turned off`,
          { description: channelLabel(channelId) },
        )
      } catch (e) {
        toast.error("Failed to update preference", {
          description: e instanceof Error ? e.message : undefined,
        })
      } finally {
        setPendingKey(null)
      }
    },
    [setCell, stateOf, channelLabel],
  )

  const handleToggleMute = useCallback(
    async (channelId: string) => {
      const key = `${MUTE_CATEGORY}:${channelId}`
      const next: PrefCell["state"] = isMuted(channelId) ? "off" : "immediate"
      setPendingKey(key)
      try {
        await setCell({ category: MUTE_CATEGORY, channel_id: channelId, state: next })
        toast.success(next === "immediate" ? "Channel muted" : "Channel unmuted", {
          description: channelLabel(channelId),
        })
      } catch (e) {
        toast.error("Failed to update mute", {
          description: e instanceof Error ? e.message : undefined,
        })
      } finally {
        setPendingKey(null)
      }
    },
    [setCell, isMuted, channelLabel],
  )

  const handleTest = useCallback(
    async (channelId: string) => {
      setTestingId(channelId)
      try {
        await sendTest(channelId)
        toast.success("Test notification sent")
      } catch (e) {
        toast.error("Test send failed", { description: e instanceof Error ? e.message : undefined })
      } finally {
        setTestingId(null)
      }
    },
    [sendTest],
  )

  if (loading && channels.length === 0) {
    return (
      <div className="space-y-5">
        <Skeleton className="h-[280px] rounded-xl" />
      </div>
    )
  }

  return (
    <div className="space-y-5">
      <SettingsCard
        title="Preference matrix"
        description="Click a cell to toggle immediate delivery for that category on that channel"
        padded
      >
        {error ? (
          <SettingsEmpty>Failed to load preferences ({error})</SettingsEmpty>
        ) : usableChannels.length === 0 ? (
          <div className="flex flex-col items-center justify-center py-10 text-center">
            <div className="w-8 h-8 rounded-lg bg-muted/50 flex items-center justify-center mb-2">
              <BellOff className="h-3.5 w-3.5 text-muted-foreground" />
            </div>
            <div className="text-xs font-medium text-foreground/80">No channels available yet</div>
            <div className="text-[11px] text-muted-foreground mt-0.5 max-w-xs">
              Add a workspace or personal channel under the Notifications tab, then come back here to
              decide what gets delivered to it.
            </div>
          </div>
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full text-xs border-collapse">
              <thead>
                <tr className="border-b border-border/60">
                  <th className="text-left font-medium text-muted-foreground px-3 py-2 sticky left-0 bg-card">
                    Category
                  </th>
                  {usableChannels.map((ch) => {
                    const Icon = channelIcon(ch.type)
                    const muted = isMuted(ch.id)
                    const mutePending = pendingKey === `${MUTE_CATEGORY}:${ch.id}`
                    return (
                      <th key={ch.id} className="px-3 py-2 min-w-[140px]">
                        <div className="flex flex-col items-center gap-1">
                          <div className="flex items-center gap-1.5 text-[11px] font-medium text-foreground/80">
                            <Icon className="size-3" />
                            <span className="truncate max-w-[100px]" title={ch.type === "email" ? ch.to : ch.provider || ch.url}>
                              {ch.type === "email" ? ch.to : ch.type === "shoutrrr" ? ch.provider : ch.url}
                            </span>
                          </div>
                          <div className="flex items-center gap-1">
                            <button
                              type="button"
                              onClick={() => handleToggleMute(ch.id)}
                              disabled={mutePending}
                              aria-label={muted ? "Unmute channel" : "Mute channel"}
                              className={cn(
                                "flex items-center gap-0.5 rounded px-1.5 py-0.5 text-[10px] transition-colors",
                                muted
                                  ? "bg-destructive/10 text-destructive"
                                  : "text-muted-foreground hover:bg-muted/60",
                              )}
                            >
                              {mutePending ? <Spinner className="size-2.5" /> : <VolumeX className="size-2.5" />}
                              {muted ? "Muted" : "Mute"}
                            </button>
                            <Button
                              type="button"
                              variant="ghost"
                              size="icon"
                              className="h-5 w-5"
                              disabled={testingId === ch.id}
                              onClick={() => handleTest(ch.id)}
                              aria-label="Send test notification"
                            >
                              {testingId === ch.id ? <Spinner className="size-2.5" /> : <Send className="size-2.5" />}
                            </Button>
                          </div>
                        </div>
                      </th>
                    )
                  })}
                </tr>
              </thead>
              <tbody>
                {NOTIFICATION_CATEGORY_GROUPS.flatMap((group) => [
                  // Group heading — taxonomy v2 has 17 rows, and a flat grid
                  // of that height is unreadable. The heading spans the whole
                  // table so the eye can find "Routines" without counting.
                  <tr key={`group:${group.key}`} className="border-b border-border/30">
                    <td
                      colSpan={usableChannels.length + 1}
                      className="px-3 pt-3 pb-1 text-[10px] font-medium uppercase tracking-wide text-muted-foreground sticky left-0 bg-card"
                    >
                      {group.label}
                    </td>
                  </tr>,
                  ...group.categories.map((cat) => (
                  <tr key={cat.key} className="border-b border-border/30 last:border-b-0">
                    <td
                      className="px-3 py-2 pl-5 text-foreground/80 sticky left-0 bg-card whitespace-nowrap"
                      title={cat.hint}
                    >
                      {cat.label}
                    </td>
                    {usableChannels.map((ch) => {
                      const allowed = !ch.categories || ch.categories.length === 0 || ch.categories.includes(cat.key)
                      const muted = isMuted(ch.id)
                      const on = stateOf(cat.key, ch.id) === "immediate"
                      const key = `${cat.key}:${ch.id}`
                      const pending = pendingKey === key
                      return (
                        <td key={ch.id} className="px-3 py-2 text-center">
                          {!allowed ? (
                            <span className="text-[10px] text-muted-foreground/40" title="Not allowed on this channel by an admin">
                              —
                            </span>
                          ) : (
                            <button
                              type="button"
                              onClick={() => handleToggle(cat.key, ch.id)}
                              disabled={pending || muted}
                              aria-pressed={on}
                              aria-label={`${cat.label} on ${ch.type} — ${on ? "immediate" : "off"}`}
                              title={muted ? "Channel is muted" : on ? "Immediate — click to turn off" : "Off — click for immediate delivery"}
                              className={cn(
                                "inline-flex h-5 w-5 items-center justify-center rounded border transition-colors",
                                muted
                                  ? "border-border/40 opacity-40 cursor-not-allowed"
                                  : on
                                    ? "border-primary bg-primary text-primary-foreground"
                                    : "border-border/60 hover:border-foreground/40",
                              )}
                            >
                              {pending ? <Spinner className="size-2.5" /> : on ? <Check className="size-3" /> : null}
                            </button>
                          )}
                        </td>
                      )
                    })}
                  </tr>
                  )),
                ])}
              </tbody>
            </table>
          </div>
        )}
      </SettingsCard>

      <p className="flex items-center gap-1.5 text-[11px] text-muted-foreground">
        <Bell className="size-3" />
        A cell you never set stays off — nothing is delivered externally until you opt in. Approvals and
        escalations always deliver immediately when on, ahead of any anti-storm throttling.
      </p>
    </div>
  )
}
