"use client"

import { motion, AnimatePresence } from "motion/react"
import { WifiOff } from "lucide-react"
import { Spinner } from "@/components/ui/spinner"

import { spring } from "@/lib/motion"

interface ReconnectBannerProps {
  status: "connected" | "connecting" | "disconnected" | string
  queuedCount?: number
}

export function ReconnectBanner({ status, queuedCount = 0 }: ReconnectBannerProps) {
  const visible = status === "connecting" || status === "disconnected"
  return (
    <AnimatePresence>
      {visible && (
        <motion.div
          initial={{ y: -32, opacity: 0 }}
          animate={{ y: 0, opacity: 1 }}
          exit={{ y: -32, opacity: 0 }}
          transition={spring.smooth}
          className="shrink-0 flex items-center justify-center gap-2 px-4 py-1.5 text-xs bg-chip-warn-bg text-chip-warn-fg border-b border-warn/40 dark:border-warn"
          role="status"
          aria-live="polite"
        >
          {status === "connecting" ? (
            <Spinner className="h-3 w-3" />
          ) : (
            <WifiOff className="h-3 w-3" />
          )}
          <span>
            {status === "connecting" ? "Reconnecting…" : "Disconnected"}
            {queuedCount > 0 &&
              ` · ${queuedCount} message${queuedCount !== 1 ? "s" : ""} queued`}
          </span>
        </motion.div>
      )}
    </AnimatePresence>
  )
}
