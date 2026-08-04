"use client"

// The tinted-header card.
//
// Lifted from the routine detail's "Last run", which is the one place on that
// page where colour carries meaning rather than decoration: it says how a
// thing ended before a word of it is read, and does it with a 6%-opacity
// gradient rather than shouting. It was the piece people kept pointing at.
//
// The rule that keeps it worth having: ONE of these per column. A page where
// every card has a tinted header is a page where the tint says nothing — the
// eye needs somewhere plain to rest for the coloured thing to be the coloured
// thing. Everything else stays a plain DetailCard, with `tone` at most
// colouring its border.

import * as React from "react"

import { cn } from "@/lib/utils"

export type TintTone = "success" | "destructive" | "warn" | "info" | "neutral"

const HEADER_WASH: Record<TintTone, string> = {
  success: "bg-gradient-to-r from-success/[0.06] to-transparent",
  destructive: "bg-gradient-to-r from-destructive/[0.06] to-transparent",
  warn: "bg-gradient-to-r from-warn/[0.06] to-transparent",
  info: "bg-gradient-to-r from-primary/[0.06] to-transparent",
  neutral: "",
}

const BADGE: Record<TintTone, string> = {
  success: "bg-success/20 text-success",
  destructive: "bg-destructive/20 text-destructive",
  warn: "bg-warn/20 text-warn",
  info: "bg-primary/20 text-primary",
  neutral: "bg-surface-raised text-muted-foreground",
}

const BORDER: Record<TintTone, string> = {
  success: "border-success/25",
  destructive: "border-destructive/25",
  warn: "border-warn/25",
  info: "border-primary/25",
  neutral: "border-border/60",
}

export interface TintedCardProps {
  tone?: TintTone
  icon: React.ComponentType<{ className?: string }>
  /** The headline — "Last run · completed", "Blocked by ENG-1". */
  title: React.ReactNode
  /** Mono sub-line under the title: a run id, a slug, a hash. */
  subtitle?: React.ReactNode
  children?: React.ReactNode
  className?: string
}

export function TintedCard({
  tone = "neutral",
  icon: Icon,
  title,
  subtitle,
  children,
  className,
}: TintedCardProps) {
  return (
    <div
      className={cn("overflow-hidden rounded-xl border bg-card", BORDER[tone], className)}
    >
      <div
        className={cn(
          "flex items-center gap-3 border-b border-border/40 px-4 py-3",
          HEADER_WASH[tone],
        )}
      >
        <div
          className={cn(
            "flex h-8 w-8 shrink-0 items-center justify-center rounded-full",
            BADGE[tone],
          )}
        >
          <Icon className="h-4 w-4" />
        </div>
        <div className="min-w-0 flex-1">
          <div className="truncate text-[13px] font-medium">{title}</div>
          {subtitle && (
            <div className="truncate font-mono text-[10px] text-muted-foreground">{subtitle}</div>
          )}
        </div>
      </div>
      {children && <div className="space-y-2 px-4 py-3">{children}</div>}
    </div>
  )
}

/** A labelled figure inside a tinted card's body. Three to a row. */
export function TintedFacts({ items }: { items: { label: string; value: string }[] }) {
  return (
    <dl className="grid grid-cols-3 gap-2 text-[11px]">
      {items.map((f) => (
        <div key={f.label} className="min-w-0">
          <dt className="text-[10px] uppercase tracking-wider text-muted-foreground-soft">
            {f.label}
          </dt>
          <dd className="truncate tabular-nums text-foreground/85">{f.value}</dd>
        </div>
      ))}
    </dl>
  )
}
