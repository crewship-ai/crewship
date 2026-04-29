"use client"

import { motion, AnimatePresence } from "motion/react"
import { Loader2, WifiOff } from "lucide-react"

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
          className="absolute top-0 inset-x-0 z-30 flex items-center justify-center gap-2 px-4 py-1.5 text-xs bg-amber-50 dark:bg-amber-950/30 text-amber-800 dark:text-amber-200 border-b border-amber-200 dark:border-amber-900"
          role="status"
          aria-live="polite"
        >
          {status === "connecting" ? (
            <Loader2 className="h-3 w-3 animate-spin" />
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
