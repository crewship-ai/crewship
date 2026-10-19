"use client"

import { motion, AnimatePresence } from "motion/react"
import { WifiOff } from "lucide-react"
import { Spinner } from "@/components/ui/spinner"
import { Button } from "@/components/ui/button"
import { useAppBannerSlot } from "@/components/layout/app-banners"
import { outageCopy, realtimeOutage, useAnnouncedOutage } from "@/components/layout/realtime-outage"
import type { WSReason } from "@/hooks/use-websocket"

import { spring } from "@/lib/motion"
import { cn } from "@/lib/utils"

interface ReconnectBannerProps {
  status: string
  reason?: WSReason | null
  queuedCount?: number
  /** "Retry now", offered only once the fast retries are spent. */
  onRetry?: () => void
}

/**
 * The chat socket's thin bar, in the app banner's vocabulary
 * (realtime-outage.ts): nothing for 4 s, then a neutral "Reconnecting…", a
 * warning only when Crewship cannot be reached. It joins the app banner queue
 * as a page's own offline banner, so when the app's realtime banner already
 * says the same thing this one steps aside: one indicator per screen.
 */
export function ReconnectBanner({ status, reason = null, queuedCount = 0, onRetry }: ReconnectBannerProps) {
  const outage = useAnnouncedOutage(realtimeOutage(status, reason))
  const slot = useAppBannerSlot("offline", outage !== null)
  const copy = outage ? outageCopy(outage, queuedCount) : null
  return (
    <AnimatePresence>
      {slot && copy && (
        <motion.div
          initial={{ y: -32, opacity: 0 }}
          animate={{ y: 0, opacity: 1 }}
          exit={{ y: -32, opacity: 0 }}
          transition={spring.smooth}
          className={cn(
            "shrink-0 flex items-center justify-center gap-2 px-4 py-1.5 text-xs border-b",
            copy.tone === "warn"
              ? "bg-chip-warn-bg text-chip-warn-fg border-warn/40 dark:border-warn"
              : "bg-chip-neutral-bg text-chip-neutral-fg border-border",
          )}
          role="status"
          aria-live="polite"
          data-tone={copy.tone}
          data-state={outage}
        >
          {copy.spinner ? <Spinner className="h-3 w-3" /> : <WifiOff className="h-3 w-3" aria-hidden />}
          <span>{copy.text}</span>
          {copy.retry && onRetry && (
            <Button size="sm" variant="outline" className="h-6 px-2 text-xs pointer-coarse:min-h-[44px]" onClick={onRetry}>
              Retry now
            </Button>
          )}
        </motion.div>
      )}
    </AnimatePresence>
  )
}
