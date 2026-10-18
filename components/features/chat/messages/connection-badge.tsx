"use client"

import { Wifi, WifiOff } from "lucide-react"

import { cn } from "@/lib/utils"
import { Spinner } from "@/components/ui/spinner"

/**
 * The socket, but only when it is worth saying something about.
 *
 * A permanently-green "Connected" pill is a status light for a state that is
 * true ~100% of the time, in the top-left corner of the thing the reader came
 * to read. What the badge is genuinely FOR is the other two states: typing
 * into a chat whose socket has gone is typing into a void, and that is worth
 * a lot of pixels. So it shows up exactly then, and says nothing otherwise.
 */
export function ConnectionBadge({ status }: { status: string }) {
  if (status === "connected") return null
  return (
    <div className={cn(
      "flex items-center gap-1.5 px-2 py-0.5 rounded-full text-micro font-medium",
      status === "connected"
        ? "bg-success/10 text-success dark:bg-success/30 dark:text-success"
        : status === "connecting"
          ? "bg-chip-warn-bg text-chip-warn-fg"
          : "bg-chip-danger-bg text-chip-danger-fg"
    )}>
      {status === "connected" ? (
        <Wifi className="h-3 w-3" />
      ) : status === "connecting" ? (
        <Spinner className="h-3 w-3" />
      ) : (
        <WifiOff className="h-3 w-3" />
      )}
      <span className="capitalize">{status}</span>
    </div>
  )
}
