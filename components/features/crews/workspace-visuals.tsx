"use client"

import type * as React from "react"
import type { LucideIcon } from "lucide-react"
import { InlineEmpty } from "@/components/ui/inline-empty"
import { cn } from "@/lib/utils"

const TONE_INK: Record<"blue" | "purple" | "green" | "amber", string> = {
  blue: "var(--primary)",
  purple: "var(--purple)",
  green: "var(--success)",
  amber: "var(--warn)",
}

/** A glyph in a Harbor icon tile, tinted by tone. */
export function WorkspaceGlyph({ icon: Icon, tone = "blue", className }: { icon: LucideIcon; tone?: "blue" | "purple" | "green" | "amber"; className?: string }) {
  return (
    <span
      className={cn("icon-tile inline-flex h-9 w-9 shrink-0 items-center justify-center rounded-lg", className)}
      style={{ "--ic": TONE_INK[tone] } as React.CSSProperties}
    >
      <Icon className="h-4 w-4" aria-hidden="true" />
    </span>
  )
}

/** A card with nothing to show says so in one line (README §2), never a centred block. */
export function WorkspaceEmpty({ icon, title, description }: { icon: LucideIcon; title: string; description?: string }) {
  return (
    <InlineEmpty
      icon={icon}
      className="mt-3"
      text={
        <>
          <span className="text-foreground">{title}</span>
          {description && <span className="text-muted-foreground-soft"> {description}</span>}
        </>
      }
    />
  )
}
