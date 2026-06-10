"use client"

import type { ReactNode } from "react"
import { cn } from "@/lib/utils"

/**
 * Shared settings card shell.
 *
 * Every settings section renders one or more of these so the whole page
 * has a single, boring card treatment that matches the orchestration
 * dashboard cards (rounded-xl, border-border/60, tight padding, compact
 * uppercase/text-body title).
 *
 * ```tsx
 * <SettingsCard title="Account" description="Your identity on this instance">
 *   <SettingsRow label="Email">{email}</SettingsRow>
 *   <SettingsRow label="Full name">{name}</SettingsRow>
 * </SettingsCard>
 * ```
 *
 * Use `padded` for free-form content that doesn't use the row layout
 * (e.g. dropdowns, large forms). Default is zero-padded so SettingsRow
 * handles its own px/py.
 */
export function SettingsCard({
  title,
  description,
  actions,
  children,
  className,
  padded = false,
}: {
  title: string
  description?: string
  actions?: ReactNode
  children: ReactNode
  className?: string
  padded?: boolean
}) {
  return (
    <section className="space-y-2.5">
      <div className="flex items-end justify-between gap-3">
        <div className="min-w-0">
          <h3 className="text-body font-medium text-foreground/80 leading-none">{title}</h3>
          {description && (
            <p className="text-[11px] text-muted-foreground mt-1 leading-snug">{description}</p>
          )}
        </div>
        {actions && <div className="flex items-center gap-1.5 shrink-0">{actions}</div>}
      </div>
      <div
        className={cn(
          "rounded-xl border border-border/60 bg-card overflow-hidden",
          padded && "p-4",
          className,
        )}
      >
        {children}
      </div>
    </section>
  )
}

/**
 * Single row inside a SettingsCard: label + optional description on the
 * left, right-aligned content on the right. Uses text-xs for the label
 * and text-[11px] for the description so rows match the orchestration
 * row aesthetic.
 */
export function SettingsRow({
  label,
  description,
  children,
  border = true,
  className,
}: {
  label: ReactNode
  description?: ReactNode
  children: ReactNode
  border?: boolean
  className?: string
}) {
  return (
    <div
      className={cn(
        "flex items-center justify-between gap-4 px-4 py-2.5",
        border && "border-b border-border/40 last:border-b-0",
        className,
      )}
    >
      <div className="min-w-0 shrink-0">
        <div className="text-xs text-foreground">{label}</div>
        {description && (
          <div className="text-[11px] text-muted-foreground/80 mt-0.5 leading-snug">{description}</div>
        )}
      </div>
      <div className="flex items-center gap-2 min-w-0 justify-end">{children}</div>
    </div>
  )
}

/** Empty state row used when a list in a settings card has no items. */
export function SettingsEmpty({
  children,
}: {
  children: ReactNode
}) {
  return (
    <div className="px-4 py-6 text-center text-[11px] text-muted-foreground">
      {children}
    </div>
  )
}

/** Destructive variant of SettingsCard for "Danger zone" sections. */
export function SettingsDangerCard({
  title,
  description,
  actions,
  children,
}: {
  title: string
  description?: string
  actions?: ReactNode
  children: ReactNode
}) {
  return (
    <section className="space-y-2.5">
      <div className="flex items-end justify-between gap-3">
        <div>
          <h3 className="text-body font-medium text-destructive/90 leading-none">{title}</h3>
          {description && (
            <p className="text-[11px] text-muted-foreground mt-1 leading-snug">{description}</p>
          )}
        </div>
        {actions && <div className="flex items-center gap-1.5 shrink-0">{actions}</div>}
      </div>
      <div className="rounded-xl border border-destructive/30 bg-destructive/[0.02] overflow-hidden">
        {children}
      </div>
    </section>
  )
}
