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
          ? "bg-warn/10 text-warn dark:bg-warn/30 dark:text-warn"
          : "bg-destructive/10 text-destructive dark:bg-destructive/30 dark:text-destructive"
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
