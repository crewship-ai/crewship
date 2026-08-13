"use client"

// Chrome shared by the three agent folder panes.
//
// Selecting Files / Asks / Memory in the chat tree replaces the CENTRE
// column, not a drawer — so each pane is a page: a header that says what
// you are looking at, one scrolling body, and the three states every
// fetching surface owes the reader (loading, failed, empty).
//
// It lives beside the panes rather than in components/ui because it is
// not a general primitive: the header height and the scroll container are
// tuned to sit where the conversation sits. Everything it renders is the
// existing vocabulary — the detail kit's cards and empty state, the type
// roles from globals.css, the shared Spinner. No new spacing, no new
// colour.

import type { LucideIcon } from "lucide-react"
import { AlertTriangle, RefreshCw } from "lucide-react"

import { Button } from "@/components/ui/button"
import { Spinner } from "@/components/ui/spinner"
import { cn } from "@/lib/utils"

export interface PaneShellProps {
  icon: LucideIcon
  title: string
  /** One muted line under the title — the scope, a count, a caveat. */
  subtitle?: React.ReactNode
  actions?: React.ReactNode
  /** Removes body padding for surfaces that own their own (the file tree). */
  bare?: boolean
  "data-testid"?: string
  children: React.ReactNode
}

export function PaneShell({
  icon: Icon,
  title,
  subtitle,
  actions,
  bare = false,
  "data-testid": testId,
  children,
}: PaneShellProps) {
  return (
    <div data-testid={testId} className="flex h-full min-h-0 flex-col">
      <header className="flex shrink-0 items-start gap-3 border-b border-hairline px-4 py-3">
        <span className="mt-0.5 flex h-7 w-7 shrink-0 items-center justify-center rounded-md bg-white/[0.04]">
          <Icon className="h-4 w-4 text-muted-foreground" />
        </span>
        <div className="min-w-0 flex-1">
          <h2 className="type-row font-medium text-foreground">{title}</h2>
          {subtitle && (
            <p className="type-meta mt-0.5 leading-relaxed text-muted-foreground-soft">{subtitle}</p>
          )}
        </div>
        {actions && <div className="shrink-0">{actions}</div>}
      </header>
      <div className={cn("min-h-0 flex-1 overflow-y-auto", !bare && "p-4")}>{children}</div>
    </div>
  )
}

/**
 * The loading state.
 *
 * Text, not a bare spinner: a pane that swaps the whole centre column has
 * to say what it is fetching, or a slow request reads as a blank page.
 */
export function PaneLoading({ label, "data-testid": testId }: { label: string; "data-testid"?: string }) {
  return (
    <div
      data-testid={testId}
      role="status"
      className="flex items-center justify-center gap-2 px-6 py-16 text-muted-foreground"
    >
      <Spinner className="h-4 w-4" />
      <span className="type-row">{label}</span>
    </div>
  )
}

/**
 * The error state.
 *
 * `detail` carries the status or message verbatim. A pane that says only
 * "something went wrong" costs the reader the one fact that would let them
 * tell a 403 from a 502, and every retry after that is a guess.
 */
export function PaneError({
  title,
  detail,
  onRetry,
  "data-testid": testId,
}: {
  title: string
  detail: string
  onRetry: () => void
  "data-testid"?: string
}) {
  return (
    <div
      data-testid={testId}
      role="alert"
      className="mx-auto flex max-w-md flex-col items-center gap-3 px-6 py-16 text-center"
    >
      <span className="flex h-10 w-10 items-center justify-center rounded-xl bg-destructive/10">
        <AlertTriangle className="h-5 w-5 text-destructive" />
      </span>
      <div className="type-row font-medium text-foreground">{title}</div>
      <p className="type-meta leading-relaxed text-muted-foreground">{detail}</p>
      <Button variant="outline" size="sm" onClick={onRetry}>
        <RefreshCw className="h-3.5 w-3.5" />
        Retry
      </Button>
    </div>
  )
}

/**
 * The "we could not ask" state.
 *
 * Distinct from an empty list on purpose. A tier with no endpoint a
 * workspace member can call must never render as an empty box — that box
 * says "the agent knows nothing", which is a claim this UI has no standing
 * to make. This says what is missing and where the data does live.
 */
export function PaneUnreachable({
  icon: Icon,
  title,
  children,
  "data-testid": testId,
}: {
  icon: LucideIcon
  title: string
  children: React.ReactNode
  "data-testid"?: string
}) {
  return (
    <div
      data-testid={testId}
      className="rounded-xl border border-dashed border-border/70 bg-surface-subtle/40 p-4"
    >
      <div className="flex items-center gap-2">
        <Icon className="h-3.5 w-3.5 text-muted-foreground-soft" />
        <span className="type-section text-foreground/70">{title}</span>
      </div>
      <div className="type-meta mt-2 space-y-1.5 leading-relaxed text-muted-foreground">{children}</div>
    </div>
  )
}
