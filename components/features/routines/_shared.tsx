"use client"

import * as React from "react"
import { cn } from "@/lib/utils"

// Shared visual primitives used by every routine detail sub-tab so
// the surfaces feel like one product. Pulled out of the Overview tab
// after we redesigned in Stripe/Vercel style and applied the same
// chrome to Editor / Runs / Versions / Schedules / Webhooks / Wait
// points.

interface CardProps {
  title?: string
  subtitle?: string
  action?: React.ReactNode
  tone?: "default" | "violet" | "emerald" | "amber"
  className?: string
  children: React.ReactNode
}

const TONE_BORDER: Record<NonNullable<CardProps["tone"]>, string> = {
  default: "border-white/[0.06]",
  violet: "border-violet-500/15",
  emerald: "border-emerald-500/15",
  amber: "border-amber-500/15",
}

export function Card({ title, subtitle, action, tone = "default", className, children }: CardProps) {
  return (
    <div className={cn("overflow-hidden rounded-xl border bg-card", TONE_BORDER[tone], className)}>
      {(title || action) && (
        <div className="flex items-center gap-2 border-b border-white/[0.04] px-4 py-2.5">
          {title && (
            <span className="text-xs font-semibold uppercase tracking-wider text-muted-foreground">
              {title}
            </span>
          )}
          {subtitle && <span className="text-[11px] text-muted-foreground/50">{subtitle}</span>}
          {action && <span className="ml-auto">{action}</span>}
        </div>
      )}
      <div>{children}</div>
    </div>
  )
}

interface EmptyStateProps {
  icon: React.ComponentType<{ className?: string }>
  title: string
  description?: string
  action?: React.ReactNode
}

export function EmptyState({ icon: Icon, title, description, action }: EmptyStateProps) {
  return (
    <div className="flex flex-col items-center justify-center px-6 py-12 text-center">
      <div className="mb-3 flex h-12 w-12 items-center justify-center rounded-xl bg-white/[0.04]">
        <Icon className="h-6 w-6 text-muted-foreground/60" />
      </div>
      <div className="text-sm font-medium text-foreground">{title}</div>
      {description && (
        <p className="mt-1.5 max-w-sm text-[13px] leading-relaxed text-muted-foreground">{description}</p>
      )}
      {action && <div className="mt-4">{action}</div>}
    </div>
  )
}

interface PillProps {
  tone?: "default" | "emerald" | "rose" | "amber" | "blue" | "violet"
  children: React.ReactNode
  className?: string
}

const PILL_TONE: Record<NonNullable<PillProps["tone"]>, string> = {
  default: "bg-white/[0.06] text-muted-foreground ring-white/[0.08]",
  emerald: "bg-emerald-500/12 text-emerald-300 ring-emerald-500/30",
  rose: "bg-rose-500/12 text-rose-300 ring-rose-500/30",
  amber: "bg-amber-500/12 text-amber-300 ring-amber-500/30",
  blue: "bg-blue-500/12 text-blue-300 ring-blue-500/30",
  violet: "bg-violet-500/12 text-violet-300 ring-violet-500/30",
}

export function Pill({ tone = "default", children, className }: PillProps) {
  return (
    <span
      className={cn(
        "inline-flex items-center gap-1.5 rounded-full px-2.5 py-0.5 text-[11px] font-medium ring-1",
        PILL_TONE[tone],
        className,
      )}
    >
      {children}
    </span>
  )
}

interface FieldLabelProps {
  children: React.ReactNode
  className?: string
}

export function FieldLabel({ children, className }: FieldLabelProps) {
  return (
    <label
      className={cn(
        "block text-[11px] font-semibold uppercase tracking-wider text-muted-foreground",
        className,
      )}
    >
      {children}
    </label>
  )
}
