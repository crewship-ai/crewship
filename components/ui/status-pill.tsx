import * as React from "react"

import { formatStatus, type StatusTone } from "@/lib/format-status"
import { cn } from "@/lib/utils"

/**
 * The one status pill: a dot and a word, in one of six tones. Never a colour
 * alone (README §2). Pass a raw status and it is formatted; pass a label to
 * override the word while keeping the tone rule.
 *
 * This is the only component that shows a status or a severity. Counts and
 * neutral labels use Badge. A feature tone word (destructive, bad, high,
 * emerald …) becomes a tone through lib/status-tone; a new local *Pill or
 * *StatusBadge outside components/ui fails components/__tests__/status-pill-guard.
 */
export const STATUS_PILL_TONE: Record<StatusTone, { pill: string; dot: string }> = {
  // Harbor chips: an opaque tinted fill with a text pair measured per theme
  // (--chip-*-bg / -fg, pinned ≥ 4.5:1 in theme-contrast.test). The dot is
  // the text colour, so tone reads twice without depending on hue alone.
  success: { pill: "bg-chip-ok-bg text-chip-ok-fg", dot: "bg-current" },
  blue: { pill: "bg-chip-info-bg text-chip-info-fg", dot: "bg-current" },
  warn: { pill: "bg-chip-warn-bg text-chip-warn-fg", dot: "bg-current" },
  danger: { pill: "bg-chip-danger-bg text-chip-danger-fg", dot: "bg-current" },
  muted: { pill: "bg-chip-neutral-bg text-chip-neutral-fg", dot: "bg-current" },
  purple: { pill: "bg-chip-violet-bg text-chip-violet-fg", dot: "bg-current" },
}

export interface StatusPillProps extends Omit<React.HTMLAttributes<HTMLSpanElement>, "children"> {
  /** Raw status (IN_PROGRESS, running, pending_review …). */
  status?: string | null
  /** Override the word; the tone still comes from `status` or `tone`. */
  label?: React.ReactNode
  /** Override the tone. */
  tone?: StatusTone
  /** Pulse the dot — only for something that is genuinely live. */
  live?: boolean
  /** A glyph in place of the dot (a spinner, a lock). Sized by the caller, h-3 w-3. */
  icon?: React.ReactNode
  size?: "sm" | "md"
}

export function StatusPill({ status, label, tone, live = false, icon, size = "sm", className, ...rest }: StatusPillProps) {
  const meta = formatStatus(status)
  const t = STATUS_PILL_TONE[tone ?? meta.tone]
  return (
    <span
      data-slot="status-pill"
      data-tone={tone ?? meta.tone}
      className={cn(
        "inline-flex shrink-0 items-center gap-1.5 rounded-full border border-transparent font-mono font-semibold tracking-[0.02em]",
        size === "sm" ? "h-5 px-2 text-[0.65625rem]" : "h-6 px-2.5 text-micro",
        t.pill,
        className,
      )}
      {...rest}
    >
      {icon ?? (
        <span
          data-slot="status-pill-dot"
          className={cn("h-1.5 w-1.5 rounded-full opacity-80", t.dot, live && "animate-pulse motion-reduce:animate-none")}
          aria-hidden
        />
      )}
      {label ?? meta.label}
    </span>
  )
}
