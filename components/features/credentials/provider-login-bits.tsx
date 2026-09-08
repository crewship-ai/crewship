"use client"

/**
 * The three marks a provider login wears wherever it is drawn — in the
 * Providers list, on its own page, and in an agent's "Pays with" picker. One
 * file so the pill that says "at limit → 11:55" on the list is the pill that
 * says it on the agent, and a quota bar means the same width everywhere.
 */

import * as React from "react"

import { Pill, type DetailTone } from "@/components/ui/detail"
import { getBrand, brandColor } from "@/lib/credential-providers/registry"
import {
  formatClock,
  loginStatusLabel,
  type LoginCredential,
  type LoginStatusTone,
  type ProviderLogin,
} from "@/lib/credentials/provider-logins"
import { cn } from "@/lib/utils"

const PILL_TONE: Record<LoginStatusTone, DetailTone> = {
  success: "success",
  warn: "warn",
  destructive: "destructive",
  default: "default",
}

export function LoginStatusPill({ credential, className }: { credential: LoginCredential; className?: string }) {
  const { label, tone, status } = loginStatusLabel(credential)
  return (
    <Pill tone={PILL_TONE[tone]} className={cn("whitespace-nowrap", className)} data-testid={`login-status-${status}`}>
      {label}
    </Pill>
  )
}

/** The brand's own mark in a rounded tile — the row's leading glyph. */
export function LoginBrandMark({ provider, size = "md" }: { provider: string; size?: "sm" | "md" }) {
  const brand = getBrand(provider)
  const Icon = brand.Icon
  return (
    <span
      className={cn(
        "flex shrink-0 items-center justify-center rounded-lg border border-border/60 bg-surface-raised",
        size === "sm" ? "h-6 w-6" : "h-8 w-8",
      )}
      aria-hidden="true"
    >
      <Icon className={size === "sm" ? "h-3.5 w-3.5" : "h-4 w-4"} style={{ color: brandColor(brand) }} />
    </span>
  )
}

/**
 * One quota window. Amber past 80 %, red at the limit — a bar that is only
 * ever blue has to be read to be understood, and the whole point of drawing
 * it is not having to.
 */
export function QuotaBar({
  label, pct, className,
}: {
  label: string
  pct: number | null | undefined
  className?: string
}) {
  const known = typeof pct === "number" && Number.isFinite(pct)
  const value = known ? Math.max(0, Math.min(100, pct)) : 0
  const fill = value >= 100 ? "bg-destructive" : value >= 80 ? "bg-warn" : "bg-primary/70"
  return (
    <div className={cn("flex items-center gap-2", className)}>
      <span className="w-12 shrink-0 type-meta text-muted-foreground">{label}</span>
      <span
        role="progressbar"
        aria-label={`${label} window`}
        aria-valuemin={0}
        aria-valuemax={100}
        aria-valuenow={known ? value : undefined}
        className="h-1.5 w-24 shrink-0 overflow-hidden rounded-full bg-white/[0.06]"
      >
        {known && <span className={cn("block h-full rounded-full", fill)} style={{ width: `${Math.max(value, 2)}%` }} />}
      </span>
      <span className="w-9 shrink-0 text-right font-mono type-meta tabular-nums text-foreground/80">
        {known ? `${Math.round(value)}%` : "—"}
      </span>
    </div>
  )
}

/**
 * Both windows, or the honest sentence when the provider's windows cannot be
 * read (P-D not there yet, or a provider without rate-limit headers).
 */
export function QuotaSummary({ login, className }: { login: ProviderLogin; className?: string }) {
  if (!login.quota) {
    return (
      <p className={cn("type-meta text-muted-foreground-soft", className)}>quota not readable for this provider</p>
    )
  }
  return (
    <div className={cn("flex flex-col gap-1", className)}>
      <QuotaBar label="5h" pct={login.quota.window_5h_pct} />
      <QuotaBar label="weekly" pct={login.quota.window_weekly_pct} />
      {login.quota.resets_at && (
        <span className="type-meta text-muted-foreground-soft">resets {formatClock(login.quota.resets_at)}</span>
      )}
    </div>
  )
}
