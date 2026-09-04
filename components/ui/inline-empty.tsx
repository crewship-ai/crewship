import * as React from "react"
import type { LucideIcon } from "lucide-react"

import { cn } from "@/lib/utils"

/**
 * The empty state a card wears when it has nothing to say: one line, an icon,
 * an action (README §2). A centred 150px block turned a healthy, idle
 * workspace into a screen of empty cards; this keeps the card's geometry and
 * says what would appear here and how to make it appear.
 */
export function InlineEmpty({
  icon: Icon,
  text,
  action,
  className,
}: {
  icon: LucideIcon
  text: React.ReactNode
  action?: React.ReactNode
  className?: string
}) {
  return (
    <div
      data-slot="inline-empty"
      className={cn(
        "flex items-center gap-2.5 rounded-lg border border-dashed border-border/60 px-3 py-2.5 text-label text-muted-foreground",
        className,
      )}
    >
      <Icon className="h-3.5 w-3.5 shrink-0 text-muted-foreground-soft" aria-hidden />
      <span className="min-w-0 flex-1">{text}</span>
      {action}
    </div>
  )
}
