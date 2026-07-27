"use client"

import { useState } from "react"
import { Bell, Bot, Globe, Mail, MessageSquare, User } from "lucide-react"
import { cn } from "@/lib/utils"
import { useAbilities } from "@/hooks/use-abilities"
import { NotificationChannelsSection } from "@/components/features/settings/sections/notification-channels-section"
import { NotificationPrefsSection } from "@/components/features/settings/sections/notification-prefs-section"
import { NotificationChannelsOverview } from "./notification-channels-overview"

type Tab = "channels" | "prefs" | "overview"

interface NotificationIntegrationsProps {
  workspaceId: string
}

/**
 * Notification channels on the Integrations page.
 *
 * They used to live under Settings, which reads as "my personal preferences".
 * A fleet of connections a whole org depends on does not belong behind that
 * door — and an admin needs ONE page that shows every connection this
 * instance has, which is impossible if notification channels sit somewhere
 * else. Settings → Notifications now redirects here so there is exactly one
 * place, not two.
 *
 * The per-user preference matrix comes along as a tab. It IS personal, but
 * splitting it from the channels it refers to would put one object in two
 * navigations, which is the failure mode this move exists to avoid.
 */
export function NotificationIntegrations({ workspaceId }: NotificationIntegrationsProps) {
  const [tab, setTab] = useState<Tab>("channels")
  const { abilities } = useAbilities()
  // The workspace-wide overview is admin-only; the tab is hidden rather than
  // shown-and-refused so a member isn't offered something they can't open.
  const canSeeOverview = abilities.can("manage", "Workspace")

  const tabs: { key: Tab; label: string; icon: typeof Bell }[] = [
    { key: "channels", label: "Channels", icon: MessageSquare },
    { key: "prefs", label: "My preferences", icon: User },
    ...(canSeeOverview ? [{ key: "overview" as Tab, label: "All connections", icon: Globe }] : []),
  ]

  return (
    <div className="space-y-4">
      <div className="flex items-center gap-2">
        <Bell className="h-4 w-4 text-foreground/60" />
        <h2 className="text-body font-medium text-foreground/80">Notifications</h2>
        <span className="text-[11px] text-muted-foreground">
          · where Crewship reaches you
        </span>
      </div>

      <div className="flex items-center gap-1 border-b border-border/50">
        {tabs.map(({ key, label, icon: Icon }) => (
          <button
            key={key}
            type="button"
            onClick={() => setTab(key)}
            className={cn(
              "flex items-center gap-1.5 px-3 py-1.5 text-xs transition-colors border-b-2 -mb-px",
              tab === key
                ? "border-primary text-foreground"
                : "border-transparent text-muted-foreground hover:text-foreground/80",
            )}
            aria-current={tab === key ? "page" : undefined}
          >
            <Icon className="size-3" />
            {label}
          </button>
        ))}
      </div>

      {tab === "channels" && <NotificationChannelsSection workspaceId={workspaceId} />}
      {tab === "prefs" && <NotificationPrefsSection workspaceId={workspaceId} />}
      {tab === "overview" && canSeeOverview && (
        <NotificationChannelsOverview workspaceId={workspaceId} />
      )}
    </div>
  )
}

/** Icon for a channel type, shared by the sections and the overview. */
export function channelTypeIcon(type: string) {
  if (type === "email") return Mail
  if (type === "shoutrrr") return MessageSquare
  if (type === "agent") return Bot
  return Globe
}
