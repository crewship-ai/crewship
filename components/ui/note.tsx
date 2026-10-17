import * as React from "react"
import { AlertTriangle, Info } from "lucide-react"
import type { LucideIcon } from "lucide-react"

import { cn } from "@/lib/utils"

/**
 * A quiet note inside a dialog or a step (Harbor 2 kit, COM-08): grey for
 * information, tinted for a warning. Not a banner: a banner is the state of
 * a page; a note explains the field or choice next to it.
 */
export function Note({
  tone = "info",
  icon,
  className,
  children,
  ...props
}: React.ComponentProps<"div"> & { tone?: "info" | "warn"; icon?: LucideIcon }) {
  const Icon = icon ?? (tone === "warn" ? AlertTriangle : Info)
  return (
    <div
      data-slot="note"
      data-tone={tone}
      className={cn(
        "flex items-start gap-2 rounded-lg px-2.5 py-2 text-label leading-relaxed",
        tone === "warn" ? "bg-chip-warn-bg text-foreground" : "bg-muted text-muted-foreground",
        className,
      )}
      {...props}
    >
      <Icon
        className={cn("mt-0.5 size-3.5 shrink-0", tone === "warn" ? "text-chip-warn-fg" : "text-muted-foreground-soft")}
        aria-hidden
      />
      <div className="min-w-0 flex-1">{children}</div>
    </div>
  )
}
