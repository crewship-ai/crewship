"use client"

import * as React from "react"
import { AlertTriangle, Clock, Rocket } from "lucide-react"
import type { LucideIcon } from "lucide-react"

import { CrewshipLogo } from "@/components/branding/crewship-logo"
import { Button } from "@/components/ui/button"
import { SLOW_LOAD_MS } from "@/components/layout/page-state"
import { cn } from "@/lib/utils"

/**
 * The first-run setup card (Harbor 2, #3409).
 *
 * Setup is a page, not a dialog: there is nothing behind it to return to (the
 * workspace is still empty), so the card wears a dialog's anatomy — header,
 * step bar, one scrolling body, a footer that never scrolls — on the ground
 * colour, with no Cancel and no close button. Skip setup is the only way out.
 *
 *   lockup                      fixed at the top; never moves between steps
 *   ┌───────────────────────────────┐
 *   │ ▣ Title / one line            │ header
 *   │ ① ── ② ── ③                   │ WizardSteps (kit)
 *   │ body — the only scrollport    │
 *   │ hint        Back Skip [Next]  │ footer
 *   └───────────────────────────────┘
 *
 * Widths are the dialog sizes: L 800 for the form steps, XL 960 for the crew
 * step, which holds the Guide chat beside what has been built. The width
 * eases between the two rather than jumping, and the card is top-aligned, so
 * the lockup stays where it was. Below 768 px the card fills the screen and
 * the footer sticks to the bottom with 44 px controls.
 */

export type SetupCardSize = "lg" | "xl"

const SIZE: Record<SetupCardSize, string> = {
  lg: "md:max-w-[800px]",
  xl: "md:max-w-[960px]",
}

/** Ground, lockup and a top-aligned column for the card. */
export function SetupFrame({ children }: { children: React.ReactNode }) {
  return (
    <div data-slot="setup-frame" className="touch-form flex min-h-dvh flex-col bg-surface-subtle">
      <div className="flex h-14 shrink-0 items-center gap-2.5 px-4 md:h-16 md:px-6">
        <CrewshipLogo tight className="h-7 w-auto text-foreground" aria-hidden />
        <span className="text-body font-semibold tracking-tight text-foreground">Crewship</span>
        <span className="text-micro font-semibold uppercase tracking-wider text-muted-foreground-soft">Setup</span>
      </div>
      <div className="flex min-h-0 flex-1 flex-col items-center md:px-4 md:pb-8">{children}</div>
    </div>
  )
}

/** The card. `onSubmit` is wired to ⌘↵ / Ctrl↵ anywhere inside it. */
export function SetupCard({
  size = "lg",
  fill = false,
  onSubmit,
  className,
  children,
  ...props
}: Omit<React.ComponentProps<"section">, "onSubmit"> & {
  size?: SetupCardSize
  /** Take the full available height (the crew step's chat needs a height to fill). */
  fill?: boolean
  onSubmit?: () => void
}) {
  return (
    <section
      data-slot="setup-card"
      data-size={size}
      onKeyDown={(e) => {
        if ((e.metaKey || e.ctrlKey) && e.key === "Enter" && onSubmit) {
          e.preventDefault()
          onSubmit()
        }
      }}
      className={cn(
        "flex w-full flex-1 flex-col overflow-hidden bg-card",
        "md:flex-none md:rounded-xl md:border md:border-hairline md:shadow-sm",
        "transition-[max-width] duration-300 ease-out motion-reduce:transition-none",
        fill ? "md:h-[min(780px,calc(100dvh-6rem))]" : "md:max-h-[calc(100dvh-6rem)]",
        SIZE[size],
        className,
      )}
      {...props}
    >
      {children}
    </section>
  )
}

/** Accent tile, the step's title (the page's h1) and one line under it. */
export function SetupCardHeader({
  title,
  description,
  icon: Icon = Rocket,
}: {
  title: React.ReactNode
  description?: React.ReactNode
  icon?: LucideIcon
}) {
  return (
    <header className="flex shrink-0 items-start gap-3 border-b border-hairline px-4 pb-3 pt-4 md:px-5">
      <span className="page-tile page-tile-40" aria-hidden>
        <Icon />
      </span>
      <div className="min-w-0 flex-1">
        <h1 className="text-heading font-semibold tracking-tight text-foreground">{title}</h1>
        {description != null && <p className="mt-0.5 text-body text-muted-foreground">{description}</p>}
      </div>
    </header>
  )
}

/** The one scrollport. `split` lays out two columns from 768 px. */
export function SetupCardBody({
  className,
  scroll = true,
  children,
  ...props
}: React.ComponentProps<"div"> & { scroll?: boolean }) {
  return (
    <div
      data-slot="setup-card-body"
      className={cn(
        "min-h-0 flex-1",
        scroll ? "overflow-y-auto overscroll-contain px-4 py-4 md:px-5" : "overflow-hidden",
        className,
      )}
      {...props}
    >
      {children}
    </div>
  )
}

/**
 * Never scrolls. Left: why the primary is disabled, or the shortcut; right:
 * the actions. On a phone it sticks to the bottom and its buttons are 44 px.
 */
export function SetupCardFooter({ hint, children }: { hint?: React.ReactNode; children: React.ReactNode }) {
  return (
    <footer
      data-slot="setup-card-footer"
      className={cn(
        "sticky bottom-0 z-10 flex shrink-0 flex-wrap items-center justify-end gap-2 border-t border-hairline bg-muted px-4 py-2.5 md:px-5",
        // 44 px controls on a finger come from `touch-form` on the frame.
        "max-md:pb-[max(0.75rem,env(safe-area-inset-bottom))]",
      )}
    >
      <div className="mr-auto min-w-0 text-label text-muted-foreground-soft max-md:basis-full">{hint}</div>
      {children}
    </footer>
  )
}

/** "⌘↵ continue", hidden where there is no keyboard to press it on. */
export function ShortcutHint({ label }: { label: string }) {
  return (
    <span className="max-md:hidden coarse:hidden">
      <kbd className="rounded-sm border border-hairline bg-card px-1 font-mono text-micro text-muted-foreground">⌘↵</kbd>{" "}
      {label}
    </span>
  )
}

/**
 * Loading. Nothing for the first second (most loads finish inside it), then
 * the card's own shape — header, three step pills, two fields, footer — and
 * after ten seconds a quiet "Still loading…" with Retry. Never a spinner on a
 * full page.
 */
export function SetupCardSkeleton({ onRetry }: { onRetry?: () => void }) {
  const [visible, setVisible] = React.useState(false)
  const [slow, setSlow] = React.useState(false)
  React.useEffect(() => {
    const show = setTimeout(() => setVisible(true), 1000)
    const late = setTimeout(() => setSlow(true), SLOW_LOAD_MS)
    return () => {
      clearTimeout(show)
      clearTimeout(late)
    }
  }, [])
  return (
    <SetupFrame>
      <SetupCard role="status" aria-busy="true" aria-live="polite" data-state="loading" aria-label="Loading setup">
        <div className={cn("flex flex-1 flex-col", !visible && "invisible")} data-testid="setup-skeleton">
          <div className="flex items-start gap-3 border-b border-hairline px-4 pb-3 pt-4 md:px-5">
            <span className="page-sk size-10 shrink-0 rounded-lg" />
            <span className="grid flex-1 gap-2 pt-1">
              <span className="page-sk h-4 w-40" />
              <span className="page-sk h-3 w-64 max-w-full" />
            </span>
          </div>
          <div className="flex items-center gap-3 border-b border-hairline bg-muted px-5 py-2.5">
            {[0, 1, 2].map((i) => (
              <span key={i} className="page-sk h-6 w-24 rounded-full" />
            ))}
          </div>
          <div className="grid flex-1 content-start gap-5 px-4 py-4 md:px-5">
            {[0, 1].map((i) => (
              <span key={i} className="grid gap-2">
                <span className="page-sk h-3 w-28" />
                <span className="page-sk h-8 w-full" />
              </span>
            ))}
          </div>
          <div className="flex justify-end gap-2 border-t border-hairline bg-muted px-5 py-2.5">
            <span className="page-sk h-7 w-20" />
            <span className="page-sk h-7 w-24" />
          </div>
        </div>
        <p className="page-sk-note justify-center pb-3">
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
      </SetupCard>
    </SetupFrame>
  )
}

/**
 * A blocking condition at the top of a step body — the kit's warn banner
 * (`page-banner`), with its one action on the right.
 */
export function SetupBanner({
  action,
  children,
  className,
  ...props
}: React.ComponentProps<"div"> & { action?: React.ReactNode }) {
  return (
    <div className={cn("page-banner page-banner-warn text-label", className)} {...props}>
      <AlertTriangle className="size-4 shrink-0" aria-hidden />
      <span className="min-w-0 flex-1">{children}</span>
      {action}
    </div>
  )
}
