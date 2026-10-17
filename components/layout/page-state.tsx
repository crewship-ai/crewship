"use client"

import * as React from "react"
import { AlertTriangle, Clock, Info, Lock, Plus, RefreshCw, Search, SearchX, WifiOff } from "lucide-react"
import type { LucideIcon } from "lucide-react"

import { cn } from "@/lib/utils"
import { Button } from "@/components/ui/button"
import { useAppBannerSlot } from "@/components/layout/app-banners"

/**
 * Page states (Harbor 2 kit, #3366). Every page shows the same eight states
 * the same way, so a person always knows whether something is loading, empty,
 * filtered away, broken, closed to them, gone, offline or contested:
 *
 *   PageLoading      optionally nothing for 1 s (`delay`), then the skeleton;
 *                    after 10 s "Still loading…" with Retry
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

/** How long a load stays blank before the skeleton shows (Harbor 2: under 1 s, nothing). */
export const QUIET_LOAD_MS = 1_000

/**
 * True once `ms` have passed since mount; immediately true for 0.
 * The 1 s / 10 s rule of the loading state, shared with the app frame.
 */
export function useElapsed(ms: number): boolean {
  const [done, setDone] = React.useState(ms <= 0)
  React.useEffect(() => {
    if (ms <= 0) return
    const t = setTimeout(() => setDone(true), ms)
    return () => clearTimeout(t)
  }, [ms])
  return done
}

/**
 * Skeleton rows or cards. After 10 s a note says it is still loading, with
 * Retry when given. `delay` keeps the area blank first: a route that usually
 * answers in under a second should not flash a skeleton (pass QUIET_LOAD_MS).
 */
export function PageLoading({
  variant = "list",
  rows = 7,
  className,
  onRetry,
  delay = 0,
}: {
  variant?: "list" | "cards"
  rows?: number
  className?: string
  onRetry?: () => void
  /** Milliseconds of nothing before the skeleton appears. Default 0. */
  delay?: number
}) {
  const shown = useElapsed(delay)
  const slow = useElapsed(SLOW_LOAD_MS)
  if (!shown) {
    return (
      <div className={cn("page-state page-state-wide", className)} role="status" aria-live="polite" aria-busy="true" data-state="loading" data-quiet="">
        <span className="sr-only">Loading</span>
      </div>
    )
  }
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
  retryLabel = "Retry",
  requestId,
  errorId,
  detail,
  actions,
  className,
}: {
  title: string
  description?: React.ReactNode
  onRetry?: () => void
  /** "Try again" on a crashed page, "Reload" outside the app frame. */
  retryLabel?: string
  requestId?: string
  /** A render error has no request; its digest goes in the footer instead. */
  errorId?: string
  /** Extra technical detail for the footer ("503 from GET /api/v1/…"). */
  detail?: React.ReactNode
  /** Quiet footer links on the right: Copy details, System status, Go to Dashboard. */
  actions?: React.ReactNode
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
              {retryLabel}
            </Button>
          )}
        </header>
        {(requestId || errorId || detail || actions) && (
          <footer>
            <Info className="size-3.5 shrink-0" aria-hidden />
            <span className="min-w-0">
              {requestId && (
                <>
                  Request <span className="font-mono">{requestId}</span>
                </>
              )}
              {errorId && (
                <>
                  {requestId && " · "}
                  Error ID <span className="font-mono">{errorId}</span>
                </>
              )}
              {(requestId || errorId) && detail && " · "}
              {detail}
            </span>
            {actions && <span className="page-ee-actions">{actions}</span>}
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
  title,
  description,
  className,
}: {
  area: string
  role?: string
  needed?: string
  admins?: string[]
  primary?: PageAction
  secondary?: PageAction
  /** Replaces "You can’t open {area}"; the restricted variant words it differently. */
  title?: string
  /**
   * Replaces the role sentence. A restricted session must not learn who the
   * admins are or that a role would help, so it passes its own sentence and
   * no `admins`.
   */
  description?: React.ReactNode
  className?: string
}) {
  return (
    <div className={cn("page-state", className)} data-state="no-access">
      <div className="page-es">
        <Tile icon={Lock} />
        <h2>{title ?? <>You can’t open {area}</>}</h2>
        <p>
          {description ?? (
            <>
              {role && <>Your role is {role}. </>}
              {needed ? <>{area} needs {needed} or above in this workspace.</> : <>Ask an admin for access.</>}
            </>
          )}
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

/**
 * The thing is gone, or the link is wrong. A solid frame, not the dashed one
 * of "empty for the first time", and never a create action: nothing here is
 * the person's to make.
 */
export function PageNotFound({
  what = "page",
  title,
  description,
  back,
  primary,
  secondary,
  className,
}: {
  what?: string
  /** Replaces "This {what} doesn’t exist", e.g. "This routine no longer exists". */
  title?: string
  description?: React.ReactNode
  /** A way back, drawn as the outline button (kept for existing callers). */
  back?: PageAction
  /** The one filled button, e.g. "Go to Dashboard". */
  primary?: PageAction
  /** Beside it, e.g. "Search". */
  secondary?: PageAction
  className?: string
}) {
  return (
    <div className={cn("page-state", className)} data-state="not-found">
      <div className="page-es page-es-solid">
        <Tile icon={Search} />
        <h2>{title ?? <>This {what} doesn’t exist</>}</h2>
        <p>{description ?? <>It may have been deleted, or the link is wrong.</>}</p>
        {(primary || secondary || back) && (
          <div className="page-es-actions">
            {primary && <ActionButton action={primary} variant="default" />}
            {secondary && <ActionButton action={secondary} variant="outline" />}
            {back && <ActionButton action={back} variant="outline" />}
          </div>
        )}
      </div>
    </div>
  )
}

/**
 * One banner while live updates are paused; pair with StaleData.
 *
 * Inside the app shell it joins the shell's banner queue (app-banners.tsx):
 * when the shell already says the connection is down, the page's own banner
 * steps aside instead of saying it a second time. Outside the shell it always
 * renders.
 */
export function OfflineBanner({ since, onRetry, className }: { since?: string; onRetry?: () => void; className?: string }) {
  const visible = useAppBannerSlot("offline", true)
  if (!visible) return null
  return (
    <div className={cn("page-banner page-banner-warn", className)} role="status" data-state="offline">
      <WifiOff className="size-4 shrink-0" aria-hidden />
      <span>
        <b>Reconnecting…</b> Live updates are paused.{since && <> You see data from {since}.</>}
      </span>
      <span className="page-banner-spacer" />
      {onRetry && <Button size="sm" variant="outline" className="page-banner-action" onClick={onRetry}>Retry now</Button>}
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
