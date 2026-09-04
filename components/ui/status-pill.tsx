import * as React from "react"

import { formatStatus, type StatusTone } from "@/lib/format-status"
import { cn } from "@/lib/utils"

/**
 * The one status pill: a dot and a word, in one of six tones. Never a colour
 * alone (README §2). Pass a raw status and it is formatted; pass a label to
 * override the word while keeping the tone rule.
 */
export const STATUS_PILL_TONE: Record<StatusTone, { pill: string; dot: string }> = {
  success: { pill: "border-success/25 bg-success/10 text-success", dot: "bg-success" },
  blue: { pill: "border-primary/25 bg-primary/10 text-primary-hover", dot: "bg-primary" },
  warn: { pill: "border-warn/25 bg-warn/10 text-warn", dot: "bg-warn" },
  danger: { pill: "border-destructive/25 bg-destructive/10 text-destructive", dot: "bg-destructive" },
  muted: { pill: "border-border bg-muted text-muted-foreground", dot: "bg-muted-foreground" },
  purple: { pill: "border-purple/25 bg-purple/10 text-purple-hover", dot: "bg-purple" },
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
  size?: "sm" | "md"
}

export function StatusPill({ status, label, tone, live = false, size = "sm", className, ...rest }: StatusPillProps) {
  const meta = formatStatus(status)
  const t = STATUS_PILL_TONE[tone ?? meta.tone]
  return (
    <span
      data-slot="status-pill"
      data-tone={tone ?? meta.tone}
      className={cn(
        "inline-flex shrink-0 items-center gap-1.5 rounded-full border font-semibold",
        size === "sm" ? "px-2 py-0.5 text-micro" : "px-2.5 py-1 text-label",
        t.pill,
        className,
      )}
      {...rest}
    >
      <span className={cn("h-1.5 w-1.5 rounded-full", t.dot, live && "animate-pulse motion-reduce:animate-none")} aria-hidden />
      {label ?? meta.label}
    </span>
  )
}
