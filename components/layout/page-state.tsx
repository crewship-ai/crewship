"use client"

import * as React from "react"
import { AlertTriangle, Clock, Info, Lock, Plus, RefreshCw, Search, SearchX, WifiOff } from "lucide-react"
import type { LucideIcon } from "lucide-react"

import { cn } from "@/lib/utils"
import { Button } from "@/components/ui/button"

/**
 * Page states (Harbor 2 kit, #3366). Every page shows the same eight states
 * the same way, so a person always knows whether something is loading, empty,
 * filtered away, broken, closed to them, gone, offline or contested:
 *
 *   PageLoading      skeleton; after 1 s a quiet "Still loading…" note
 *   PageEmpty        area tile, what belongs here, one primary action, an
 *                    example, and the CLI command that does the same
 *   PageNoResults    the filters that hid everything, Clear / Search all time
 *   PageError        what failed, what is kept, Retry, the request id
 *   PageNoAccess     which role is needed and who can grant it
 *   PageNotFound     the thing is gone or the link is wrong
 *   OfflineBanner    one banner; the data under it is dimmed (`StaleData`)
 *   SaveConflict     theirs vs. yours, Keep mine / Take theirs
 *
 * Copy is the caller's: these components fix the shape, not the words.
 */

function Tile({ icon: Icon, tone = "accent", size = 48 }: { icon: LucideIcon; tone?: "accent" | "danger"; size?: 40 | 48 }) {
  return (
    <span className={cn("page-tile", tone === "danger" && "page-tile-danger", size === 40 && "page-tile-40")} aria-hidden>
      <Icon />
    </span>
  )
}

export interface PageAction {
  label: string
  onClick?: () => void
  href?: string
  icon?: LucideIcon
}

function ActionButton({ action, variant }: { action: PageAction; variant: "default" | "outline" | "ghost" }) {
  const Icon = action.icon
  const body = (
    <>
      {Icon && <Icon className="size-3.5" aria-hidden />}
      {action.label}
    </>
  )
  return action.href ? (
    <Button asChild size="sm" variant={variant}>
      <a href={action.href}>{body}</a>
    </Button>
  ) : (
    <Button size="sm" variant={variant} onClick={action.onClick}>
      {body}
    </Button>
  )
}

/** How long a load may take before the page says so (Harbor 2: no spinner on a full page). */
export const SLOW_LOAD_MS = 10_000

/** Skeleton rows or cards. After 10 s a note says it is still loading, with Retry when given. */
export function PageLoading({
  variant = "list",
  rows = 7,
  className,
  onRetry,
}: {
  variant?: "list" | "cards"
  rows?: number
  className?: string
  onRetry?: () => void
}) {
  const [slow, setSlow] = React.useState(false)
  React.useEffect(() => {
    const t = setTimeout(() => setSlow(true), SLOW_LOAD_MS)
    return () => clearTimeout(t)
  }, [])
  return (
    <div className={cn("page-state page-state-wide", className)} role="status" aria-live="polite" aria-busy="true" data-state="loading">
      <span className="page-sk page-sk-h" />
      <span className="page-sk page-sk-p" />
      {variant === "cards" ? (
        <div className="page-sk-cards">
          {Array.from({ length: 6 }, (_, i) => <span key={i} className="page-sk page-sk-card" />)}
        </div>
      ) : (
        <div className="page-sk-list">
          {Array.from({ length: rows }, (_, i) => (
            <div key={i} className="page-sk-row">
              <span className="page-sk page-sk-av" />
              <span className="page-sk-t">
                <span className="page-sk page-sk-l" style={{ width: `${50 + ((i * 17) % 40)}%` }} />
                <span className="page-sk page-sk-l" style={{ width: `${25 + ((i * 11) % 30)}%` }} />
              </span>
            </div>
          ))}
        </div>
      )}
      <p className="page-sk-note">
        {slow ? (
          <>
            <Clock className="size-3.5" aria-hidden /> Still loading…
            {onRetry && (
              <Button size="sm" variant="ghost" onClick={onRetry}>
                Retry
              </Button>
            )}
          </>
        ) : (
          <span className="sr-only">Loading</span>
        )}
      </p>
    </div>
  )
}

/** The page has nothing yet. One primary action; the CLI hint is optional. */
export function PageEmpty({
  icon,
  title,
  description,
  primary,
  secondary,
  cli,
  className,
}: {
  icon: LucideIcon
  title: string
  description: React.ReactNode
  primary?: PageAction
  secondary?: PageAction
  cli?: string
  className?: string
}) {
  return (
    <div className={cn("page-state", className)} data-state="empty">
      <div className="page-es">
        <Tile icon={icon} />
        <h2>{title}</h2>
        <p>{description}</p>
        {(primary || secondary) && (
          <div className="page-es-actions">
            {primary && <ActionButton action={{ icon: Plus, ...primary }} variant="default" />}
            {secondary && <ActionButton action={secondary} variant="outline" />}
          </div>
        )}
        {cli && (
          <span className="page-es-cli">
            or <code>{cli}</code>
          </span>
        )}
      </div>
    </div>
  )
}

/** Filters hid everything. Names the filters so the person knows why. */
export function PageNoResults({
  filters,
  onClear,
  onWiden,
  widenLabel = "Search all time",
  className,
}: {
  /** A short readable list: "Status: Failed · Crew: Research · Last 7 days". */
  filters?: React.ReactNode
  onClear?: () => void
  onWiden?: () => void
  widenLabel?: string
  className?: string
}) {
  return (
    <div className={cn("page-state", className)} data-state="no-results">
      <div className="page-es page-es-filtered">
        <SearchX className="page-es-glyph" aria-hidden />
        <h2>Nothing matches these filters</h2>
        {filters && <p>{filters}</p>}
        <div className="page-es-actions">
          {onClear && <Button size="sm" variant="outline" onClick={onClear}>Clear filters</Button>}
          {onWiden && <Button size="sm" variant="ghost" onClick={onWiden}>{widenLabel}</Button>}
        </div>
      </div>
    </div>
  )
}

/** Something failed to load. Says what is kept, offers Retry. */
export function PageError({
  title,
  description,
  onRetry,
  requestId,
  detail,
  className,
}: {
  title: string
  description?: React.ReactNode
  onRetry?: () => void
  requestId?: string
  /** Extra technical detail for the footer ("503 from GET /api/v1/…"). */
  detail?: React.ReactNode
  className?: string
}) {
  return (
    <div className={cn("page-state", className)} data-state="error" role="alert">
      <div className="page-ee">
        <header>
          <Tile icon={AlertTriangle} tone="danger" size={40} />
          <div className="min-w-0 flex-1">
            <b>{title}</b>
            {description && <p>{description}</p>}
          </div>
          {onRetry && (
            <Button size="sm" onClick={onRetry}>
              <RefreshCw className="size-3.5" aria-hidden />
              Retry
            </Button>
          )}
        </header>
        {(requestId || detail) && (
          <footer>
            <Info className="size-3.5 shrink-0" aria-hidden />
            <span>
              {requestId && (
                <>
                  Request <span className="font-mono">{requestId}</span>
                </>
              )}
              {requestId && detail && " · "}
              {detail}
            </span>
          </footer>
        )}
      </div>
    </div>
  )
}

/** The person's role does not reach this page. */
export function PageNoAccess({
  area,
  role,
  needed,
  admins,
  primary,
  secondary,
  className,
}: {
  area: string
  role?: string
  needed?: string
  admins?: string[]
  primary?: PageAction
  secondary?: PageAction
  className?: string
}) {
  return (
    <div className={cn("page-state", className)} data-state="no-access">
      <div className="page-es">
        <Tile icon={Lock} />
        <h2>You can’t open {area}</h2>
        <p>
          {role && <>Your role is {role}. </>}
          {needed ? <>{area} needs {needed} or above in this workspace.</> : <>Ask an admin for access.</>}
        </p>
        {(primary || secondary) && (
          <div className="page-es-actions">
            {primary && <ActionButton action={primary} variant="default" />}
            {secondary && <ActionButton action={secondary} variant="outline" />}
          </div>
        )}
        {admins && admins.length > 0 && <span className="page-es-cli">Admins: {admins.join(", ")}</span>}
      </div>
    </div>
  )
}

/** The thing is gone, or the link is wrong. */
export function PageNotFound({
  what = "page",
  description,
  back,
  className,
}: {
  what?: string
  description?: React.ReactNode
  back?: PageAction
  className?: string
}) {
  return (
    <div className={cn("page-state", className)} data-state="not-found">
      <div className="page-es">
        <Tile icon={Search} />
        <h2>This {what} doesn’t exist</h2>
        <p>{description ?? <>It may have been deleted, or the link is wrong.</>}</p>
        {back && (
          <div className="page-es-actions">
            <ActionButton action={back} variant="outline" />
          </div>
        )}
      </div>
    </div>
  )
}

/** One banner while live updates are paused; pair with StaleData. */
export function OfflineBanner({ since, onRetry, className }: { since?: string; onRetry?: () => void; className?: string }) {
  return (
    <div className={cn("page-banner page-banner-warn", className)} role="status" data-state="offline">
      <WifiOff className="size-4 shrink-0" aria-hidden />
      <span>
        <b>Reconnecting…</b> Live updates are paused.{since && <> You see data from {since}.</>}
      </span>
      <span className="flex-1" />
      {onRetry && <Button size="sm" variant="outline" onClick={onRetry}>Retry now</Button>}
    </div>
  )
}

/** Data that may be out of date: dimmed and not clickable until reconnected. */
export function StaleData({ stale, children }: { stale: boolean; children: React.ReactNode }) {
  return (
    <div className={cn(stale && "page-stale")} aria-disabled={stale || undefined} inert={stale ? true : undefined}>
      {children}
    </div>
  )
}

/** Someone saved while you were editing. Both versions side by side. */
export function SaveConflict({
  title = "Someone saved this while you were editing",
  description,
  theirs,
  yours,
  onKeepMine,
  onTakeTheirs,
  className,
}: {
  title?: string
  description?: React.ReactNode
  theirs: React.ReactNode
  yours: React.ReactNode
  onKeepMine: () => void
  onTakeTheirs: () => void
  className?: string
}) {
  return (
    <div className={cn("page-cf", className)} role="alert" data-state="conflict">
      <header>
        <AlertTriangle className="size-4 shrink-0 text-warn" aria-hidden />
        <div className="min-w-0">
          <b>{title}</b>
          {description && <p>{description}</p>}
        </div>
      </header>
      <div className="page-cf-grid">
        <div>
          <small>Theirs (saved)</small>
          {theirs}
        </div>
        <div>
          <small>Yours</small>
          {yours}
        </div>
      </div>
      <footer>
        <Button size="sm" variant="outline" onClick={onTakeTheirs}>Take theirs</Button>
        <Button size="sm" onClick={onKeepMine}>Keep mine</Button>
      </footer>
    </div>
  )
}
