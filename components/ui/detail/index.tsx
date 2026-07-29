"use client"

import * as React from "react"

import { cn } from "@/lib/utils"

// =============================================================================
// Detail surface kit.
//
// Where this came from: the routine detail screen was the one surface designed
// as a whole instead of grown a card at a time, and it reads that way. Its
// primitives lived in components/features/routines/_shared.tsx — reachable only
// by routines — so every other screen re-invented the same card, the same
// header, the same pill, at its own size. A count across app/ + components/
// found twelve different ways to write "small text" (text-xs 1292×, text-[11px]
// 752×, text-[10px] 729×, the token scale 505×) and 211 hand-rolled
// border-white/10 dividers.
//
// So the routine vocabulary moves here unchanged, and every detail screen —
// agent, crew, issue, credential — builds from it. Adopting these is a visual
// no-op for routines and a correction everywhere else.
//
// The type roles (.type-title / .type-row / .type-section / .type-meta) live in
// app/globals.css. Nothing in here hardcodes a pixel size.
// =============================================================================

export type DetailTone = "default" | "success" | "destructive" | "warn" | "blue" | "purple"

const TONE_BORDER: Record<"default" | "purple" | "success" | "warn", string> = {
  default: "border-border/60",
  purple: "border-purple/30",
  success: "border-success/30",
  warn: "border-warn/30",
}

const TONE_FILL: Record<DetailTone, string> = {
  default: "bg-muted text-muted-foreground",
  success: "bg-success/20 text-success",
  destructive: "bg-destructive/20 text-destructive",
  warn: "bg-warn/20 text-warn",
  blue: "bg-primary/20 text-primary",
  purple: "bg-purple/20 text-purple",
}

const TONE_ICON: Record<DetailTone, string> = {
  default: "bg-surface-raised text-muted-foreground",
  success: "bg-success text-background",
  destructive: "bg-destructive text-background",
  warn: "bg-warn text-background",
  blue: "bg-primary text-primary-foreground",
  purple: "bg-purple text-background",
}

export interface DetailCardProps {
  /** Rendered UPPERCASE — this is a section header, not a sentence. */
  title?: string
  /** Muted note beside the title: "step by step", "blast radius", a count. */
  subtitle?: string
  icon?: React.ComponentType<{ className?: string }>
  action?: React.ReactNode
  tone?: keyof typeof TONE_BORDER
  className?: string
  /** Removes the body padding for tables and lists that own their own. */
  bare?: boolean
  /** One muted line under the body — what the setting means in practice. */
  footer?: React.ReactNode
  children: React.ReactNode
}

export function DetailCard({
  title, subtitle, icon: Icon, action, tone = "default", className, bare = false, footer, children,
}: DetailCardProps) {
  return (
    <div className={cn("overflow-hidden rounded-xl border bg-card", TONE_BORDER[tone], className)}>
      {(title || action) && (
        <div className="flex items-center gap-2 border-b border-hairline px-4 py-2.5">
          {title && (
            <span className="inline-flex items-center gap-1.5">
              {Icon && <Icon className="h-3.5 w-3.5 text-muted-foreground-soft" />}
              <span className="type-section text-foreground/70">{title}</span>
            </span>
          )}
          {subtitle && <span className="type-meta font-mono text-muted-foreground">{subtitle}</span>}
          {action && <span className="ml-auto">{action}</span>}
        </div>
      )}
      <div className={cn(!bare && "p-4")}>{children}</div>
      {footer && (
        <p className="type-meta border-t border-hairline bg-surface-subtle/60 px-4 py-2 leading-relaxed text-muted-foreground-soft">
          {footer}
        </p>
      )}
    </div>
  )
}

export interface PillProps {
  tone?: DetailTone
  children: React.ReactNode
  className?: string
}

export function Pill({ tone = "default", children, className }: PillProps) {
  return (
    <span
      className={cn(
        "inline-flex items-center gap-1.5 rounded-full px-2.5 py-0.5 font-medium",
        "text-[0.6875rem] leading-4",
        TONE_FILL[tone],
        className,
      )}
    >
      {children}
    </span>
  )
}

export interface FieldLabelProps {
  children: React.ReactNode
  className?: string
}

export function FieldLabel({ children, className }: FieldLabelProps) {
  return <label className={cn("type-section block text-muted-foreground", className)}>{children}</label>
}

export interface EmptyStateProps {
  icon: React.ComponentType<{ className?: string }>
  title: string
  description?: string
  action?: React.ReactNode
}

export function EmptyState({ icon: Icon, title, description, action }: EmptyStateProps) {
  return (
    <div className="flex flex-col items-center justify-center px-6 py-12 text-center">
      <div className="mb-3 flex h-12 w-12 items-center justify-center rounded-xl bg-white/[0.04]">
        <Icon className="h-6 w-6 text-muted-foreground" />
      </div>
      <div className="text-body font-medium text-foreground">{title}</div>
      {description && <p className="type-row mt-1.5 max-w-sm leading-relaxed text-muted-foreground">{description}</p>}
      {action && <div className="mt-4">{action}</div>}
    </div>
  )
}

export interface StatItem {
  label: string
  value: React.ReactNode
  /** Renders the value in mono — ids, cron expressions, durations. */
  mono?: boolean
  tone?: "default" | "success" | "warn" | "destructive"
}

const STAT_TONE: Record<NonNullable<StatItem["tone"]>, string> = {
  default: "text-foreground",
  success: "text-success",
  warn: "text-warn",
  destructive: "text-destructive",
}

/**
 * The horizontal figures band. One bordered strip split by hairlines, never a
 * row of separate cards — separate cards read as six things to act on, and
 * these are one thing to glance at.
 */
export function StatStrip({ items, className }: { items: StatItem[]; className?: string }) {
  return (
    <div className={cn("flex flex-wrap overflow-hidden rounded-xl border border-border/60 bg-card", className)}>
      {items.map((s) => (
        <div key={s.label} className="min-w-[120px] flex-1 border-r border-hairline px-4 py-2.5 last:border-r-0">
          <div className="type-meta uppercase tracking-wide text-muted-foreground-soft">{s.label}</div>
          <div
            className={cn(
              "type-row mt-0.5 truncate font-semibold tabular-nums",
              s.mono && "font-mono font-medium",
              STAT_TONE[s.tone ?? "default"],
            )}
          >
            {s.value}
          </div>
        </div>
      ))}
    </div>
  )
}

export interface TickRowProps {
  icon?: React.ComponentType<{ className?: string }>
  label: React.ReactNode
  /** Mono detail rendered right after the label — a path, an id, an arg. */
  detail?: React.ReactNode
  /** Sub-line under the label for anything that needs a sentence. */
  note?: React.ReactNode
  status?: "ok" | "failed" | "running" | "pending"
  meta?: React.ReactNode
}

const TICK: Record<NonNullable<TickRowProps["status"]>, { glyph: string; className: string }> = {
  ok: { glyph: "✓", className: "text-success" },
  failed: { glyph: "✕", className: "text-destructive" },
  running: { glyph: "●", className: "text-primary animate-pulse" },
  pending: { glyph: "·", className: "text-muted-foreground-soft" },
}

/**
 * The "how it ran" row: what happened on the left, a tick on the right. The
 * tick is the thing people scan for, so it sits on the ragged right edge where
 * the eye can run down it, not inline where the label length hides it.
 */
export function TickRow({ icon: Icon, label, detail, note, status, meta }: TickRowProps) {
  const tick = status ? TICK[status] : null
  return (
    <div className="flex items-start gap-2.5 py-1.5">
      {Icon && <Icon className="mt-px h-3.5 w-3.5 shrink-0 text-muted-foreground-soft" />}
      <div className="min-w-0 flex-1">
        <div className="type-row flex flex-wrap items-baseline gap-x-2">
          <span className="text-foreground">{label}</span>
          {detail && <span className="type-meta font-mono text-muted-foreground">{detail}</span>}
        </div>
        {note && <div className="type-meta mt-0.5 text-muted-foreground">{note}</div>}
      </div>
      {meta && <span className="type-meta shrink-0 font-mono text-muted-foreground-soft">{meta}</span>}
      {tick && <span className={cn("type-row shrink-0 leading-none", tick.className)}>{tick.glyph}</span>}
    </div>
  )
}

export interface StepRowProps {
  /** 1-based position, or an icon when the step is the trigger. */
  index?: number
  icon?: React.ComponentType<{ className?: string }>
  title: React.ReactNode
  badge?: { label: string; tone?: DetailTone }
  body?: React.ReactNode
  tone?: DetailTone
}

/** Numbered step, as used by "what it does". */
export function StepRow({ index, icon: Icon, title, badge, body, tone = "default" }: StepRowProps) {
  return (
    <div className="flex items-start gap-3 py-2">
      <span
        className={cn(
          "mt-0.5 flex h-5 w-5 shrink-0 items-center justify-center rounded-md font-medium",
          "text-[0.625rem] leading-none",
          TONE_ICON[tone],
        )}
      >
        {Icon ? <Icon className="h-3 w-3" /> : index}
      </span>
      <div className="min-w-0 flex-1">
        <div className="type-row flex flex-wrap items-center gap-2">
          <span className="text-foreground">{title}</span>
          {badge && <Pill tone={badge.tone ?? "purple"}>{badge.label}</Pill>}
        </div>
        {body && <div className="type-meta mt-0.5 font-mono text-muted-foreground">{body}</div>}
      </div>
    </div>
  )
}

export interface EntityChipProps {
  icon?: React.ComponentType<{ className?: string }>
  label: string
  /** Muted suffix — a scope, a slug, a count. */
  note?: string
  tone?: DetailTone
  onClick?: () => void
  href?: string
}

/** An agent, a credential, a domain — anything the surface points at. */
export function EntityChip({ icon: Icon, label, note, tone = "default", onClick, href }: EntityChipProps) {
  const inner = (
    <>
      {Icon && <Icon className="h-3 w-3 shrink-0" />}
      <span className="truncate">{label}</span>
      {note && <span className="type-meta opacity-70">{note}</span>}
    </>
  )
  const className = cn(
    "inline-flex max-w-full items-center gap-1.5 rounded-md border px-2 py-0.5 font-medium",
    "text-[0.6875rem] leading-4 transition-colors",
    TONE_FILL[tone],
    "border-transparent",
    (onClick || href) && "cursor-pointer hover:brightness-125",
  )
  if (href) return <a href={href} className={className}>{inner}</a>
  if (onClick) return <button type="button" onClick={onClick} className={className}>{inner}</button>
  return <span className={className}>{inner}</span>
}
