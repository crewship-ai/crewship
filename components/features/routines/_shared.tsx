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
  icon?: React.ComponentType<{ className?: string }>
  action?: React.ReactNode
  tone?: "default" | "violet" | "emerald" | "amber"
  className?: string
  children: React.ReactNode
}

// Border tokens use the semantic --border CSS variable from
// app/globals.css (via the `border-border` Tailwind class) so the
// surface family matches DashboardCard / shadcn Card / Sidebar exactly.
const TONE_BORDER: Record<NonNullable<CardProps["tone"]>, string> = {
  default: "border-border/60",
  violet: "border-violet-500/30",
  emerald: "border-emerald-500/30",
  amber: "border-amber-500/30",
}

export function Card({ title, subtitle, icon: Icon, action, tone = "default", className, children }: CardProps) {
  return (
    <div className={cn("overflow-hidden rounded-xl border bg-card", TONE_BORDER[tone], className)}>
      {(title || action) && (
        <div className="flex items-center gap-2 border-b border-border/40 px-4 py-2.5">
          {title && (
            <div className="inline-flex items-center gap-1.5">
              {Icon && <Icon className="h-3.5 w-3.5 text-foreground/40" />}
              <span className="text-[11px] font-semibold uppercase tracking-wider text-foreground/70">
                {title}
              </span>
            </div>
          )}
          {subtitle && (
            <span className="font-mono text-[10px] text-muted-foreground/60">{subtitle}</span>
          )}
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

// Color tokens aligned with lib/colors.ts STATUS_BADGE_CLASSES (used
// in Inbox/Issues/Activity status badges) — same `bg-{c}-500/20
// text-{c}-400` pattern, no ring, so pills across the app are
// visually identical regardless of which page rendered them.
const PILL_TONE: Record<NonNullable<PillProps["tone"]>, string> = {
  default: "bg-muted text-muted-foreground",
  emerald: "bg-emerald-500/20 text-emerald-400",
  rose: "bg-rose-500/20 text-rose-400",
  amber: "bg-amber-500/20 text-amber-400",
  blue: "bg-blue-500/20 text-blue-400",
  violet: "bg-violet-500/20 text-violet-400",
}

export function Pill({ tone = "default", children, className }: PillProps) {
  return (
    <span
      className={cn(
        "inline-flex items-center gap-1.5 rounded-full px-2.5 py-0.5 text-[11px] font-medium",
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
