"use client"

import { Loader2, WifiOff } from "lucide-react"
import { Button } from "@/components/ui/button"
import { useRealtime } from "@/hooks/use-realtime"
import { AppBanner, useAppBannerSlot } from "@/components/layout/app-banners"
import { outageCopy, realtimeOutage, useAnnouncedOutage } from "@/components/layout/realtime-outage"

/**
 * The realtime banner (#3783): nothing for the first 4 s of an outage, then a
 * neutral "Reconnecting…" (or "Crewship is restarting · reconnecting…", or
 * "You're offline…"), and a warning with Retry now only once the fast retries
 * are spent ("Can't reach Crewship · retrying every 30 s"). When the socket
 * returns the bar just goes; there is no "Reconnected". Vocabulary in
 * realtime-outage.ts.
 *
 * It holds the app's one banner slot at the highest priority, so a page's own
 * offline banner and the chat's reconnect banner step aside while it shows.
 *
 * Renders nothing for "unavailable": a restricted session has no realtime by
 * design (the allowlist refuses /ws-token), so there is nothing to reconnect
 * and a banner saying otherwise would be permanent and false.
 */
export function RealtimeStatusBanner() {
  const { status, reason, retry } = useRealtime()
  const outage = useAnnouncedOutage(realtimeOutage(status, reason))
  const slot = useAppBannerSlot("realtime", outage !== null)
  if (!slot || !outage) return null

  const copy = outageCopy(outage)
  return (
    <AppBanner
      tone={copy.tone}
      icon={copy.spinner ? Loader2 : WifiOff}
      className={copy.spinner ? "[&>svg:first-child]:animate-spin" : undefined}
      data-state={outage}
      data-testid="realtime-banner"
      action={
        copy.retry && retry ? (
          <Button size="sm" variant="outline" className="page-banner-action" onClick={retry}>
            Retry now
          </Button>
        ) : undefined
      }
    >
      {copy.text}
    </AppBanner>
  )
}
