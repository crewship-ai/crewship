"use client"

import * as React from "react"
import type { LucideIcon } from "lucide-react"
import { CheckCircle2, Clock, Info } from "lucide-react"

import { cn } from "@/lib/utils"
import { CrewshipLogo } from "@/components/branding/crewship-logo"
import { ConceptIcon } from "@/components/ui/concept-icon"
import { CreateSurfaceNotice, CreateSurfaceRefusal } from "@/components/layout/create-surface"

/**
 * The auth card (Harbor 2, #3410): one shell for every screen a person meets
 * before they have a session — sign in, create an account, password recovery,
 * first-run setup and invitation acceptance — and for the password prompt of
 * the public pages.
 *
 * It is the kit's small dialog drawn as a page: an icon tile, a sentence-case
 * title and one line of description; the fields; a footer with links on the
 * left and exactly one primary on the right. There is no close button — there
 * is nothing behind it to return to — and no shadow, because the card is the
 * page, not a layer over one.
 *
 * Feedback follows the kit's three ranks, and the card has a slot for each:
 *
 *   · a wrong value      → under its field (`AuthField error`)
 *   · the server said no → `refusal`, one band above the footer
 *   · a redirect state   → `banner`, inside the card above the fields
 *
 * Under 768 px the card loses its frame and fills the screen; the footer sticks
 * to the bottom edge with a full-width primary, the way the kit's dialogs
 * become a bottom sheet.
 */

/** Ground + lockup. Wraps one `AuthCard`. */
export function AuthScreen({
  children,
  lockup = true,
  width = "sm",
  className,
}: {
  children: React.ReactNode
  /** The public pages carry the mark in their bar instead. */
  lockup?: boolean
  /** Match the card's `size`: 480 or 640. */
  width?: "sm" | "md"
  className?: string
}) {
  return (
    <main
      data-slot="auth-screen"
      className={cn(
        // touch-form raises every field, button and link to 44 px under a
        // coarse pointer (globals.css) — these screens are the ones most often
        // met on a phone.
        "touch-form flex min-h-dvh flex-col items-center bg-sidebar text-foreground",
        // Tablet: 64 px from the top. From 1280 the card sits on the centre line.
        "md:px-4 md:pt-[64px] md:pb-8 xl:justify-center xl:pt-8",
        className,
      )}
    >
      <div className={cn("flex w-full flex-1 flex-col md:flex-none", width === "md" ? "md:max-w-[640px]" : "md:max-w-[480px]")}>
        {lockup && <AuthLockup />}
        {children}
      </div>
    </main>
  )
}

/** Logo + wordmark. 28 px on a desk, 22 px on a phone. */
export function AuthLockup({ className }: { className?: string }) {
  return (
    <div
      data-slot="auth-lockup"
      className={cn("flex items-center gap-2 px-4 pt-6 pb-4 md:justify-center md:px-0 md:pt-0 md:pb-5", className)}
    >
      {/* The bare mark cropped to its bounds: inside a tile at this size the
          sails stop being readable. */}
      <CrewshipLogo tight aria-hidden className="h-[22px] w-auto text-foreground md:h-[28px]" />
      <span className="text-default font-semibold tracking-tight">Crewship</span>
    </div>
  )
}

export interface AuthCardProps {
  /** The glyph in the header tile. Brand blue — there is no workspace yet. */
  icon: LucideIcon
  title: React.ReactNode
  /** One sentence. */
  description?: React.ReactNode
  /** sm = 480 (every auth form), md = 640 (the shared-chat reader). */
  size?: "sm" | "md"
  /** Renders the card as a `<form>`; the primary should be `type="submit"`. */
  onSubmit?: (event: React.FormEvent<HTMLFormElement>) => void
  /** Under the header, before the body — a step bar. */
  steps?: React.ReactNode
  /** A state reached by redirect. Use `AuthBanner`. */
  banner?: React.ReactNode
  /** What the server refused, in one sentence. Null hides the band. */
  refusal?: React.ReactNode | null
  /** Left of the footer: text links. */
  links?: React.ReactNode
  /** Right of the footer: the one primary (and at most one ghost before it). */
  actions?: React.ReactNode
  /** Extra attributes for the form (autoComplete, noValidate). */
  formProps?: Omit<React.ComponentProps<"form">, "onSubmit" | "children" | "className">
  className?: string
  children?: React.ReactNode
}

export function AuthCard({
  icon,
  title,
  description,
  size = "sm",
  onSubmit,
  steps,
  banner,
  refusal = null,
  links,
  actions,
  formProps,
  className,
  children,
}: AuthCardProps) {
  const titleId = React.useId()
  const descId = React.useId()
  // No empty padded box when a state has nothing but its header and footer.
  const hasBody = banner != null || React.Children.toArray(children).length > 0
  const inner = (
    <>
      <div data-slot="auth-card-header" className="flex items-start gap-3 border-b border-border px-4 pt-4 pb-3 md:pl-5">
        <ConceptIcon icon={icon} accent="blue" variant="chip" size="md" className="mt-px" />
        <div className="min-w-0 flex-1">
          <h1 id={titleId} className="text-default font-semibold leading-snug text-balance">
            {title}
          </h1>
          {description != null && (
            <p id={descId} className="mt-0.5 text-body text-muted-foreground">
              {description}
            </p>
          )}
        </div>
      </div>
      {steps}
      {hasBody ? (
        <div data-slot="auth-card-body" className="flex flex-1 flex-col gap-4 px-4 py-4 md:flex-none md:px-5">
          {banner}
          {children}
        </div>
      ) : (
        <div className="max-md:flex-1" aria-hidden />
      )}
      <CreateSurfaceRefusal message={refusal} className="md:px-5" />
      {(links != null || actions != null) && (
        <div
          data-slot="auth-card-footer"
          className={cn(
            // The primary comes first in the DOM so Tab reaches it straight
            // after the fields; row-reverse puts it on the right on a desk.
            "flex flex-col items-stretch gap-x-3 gap-y-2 border-t border-border bg-muted px-4 py-3",
            "md:flex-row-reverse md:flex-wrap md:items-center md:rounded-b-xl md:pl-5",
            // Phone: pinned to the bottom edge, primary across the full width,
            // past the home indicator.
            "max-md:sticky max-md:bottom-0 max-md:pb-[max(0.75rem,env(safe-area-inset-bottom))]",
          )}
        >
          {actions != null && (
            <div className="flex items-center gap-2 max-md:flex-col-reverse max-md:items-stretch max-md:[&>*]:w-full">
              {actions}
            </div>
          )}
          {links != null && (
            <div className="flex flex-wrap items-center gap-x-3 gap-y-1 text-body max-md:justify-center md:mr-auto">{links}</div>
          )}
        </div>
      )}
    </>
  )

  const shell = cn(
    "flex flex-col bg-card",
    // A frame on a desk; the whole screen on a phone.
    "max-md:flex-1 md:rounded-xl md:border md:border-border",
    className,
  )

  return onSubmit ? (
    <form
      {...formProps}
      data-slot="auth-card"
      data-size={size}
      aria-labelledby={titleId}
      aria-describedby={description != null ? descId : undefined}
      onSubmit={onSubmit}
      className={shell}
    >
      {inner}
    </form>
  ) : (
    <section
      data-slot="auth-card"
      data-size={size}
      aria-labelledby={titleId}
      aria-describedby={description != null ? descId : undefined}
      className={shell}
    >
      {inner}
    </section>
  )
}

/**
 * A state the person arrived in by redirect: the account was created, the
 * session ended. Polite, never an alert — nothing went wrong on this page.
 */
export function AuthBanner({
  tone,
  icon,
  children,
}: {
  tone: "ok" | "warn" | "info"
  icon?: LucideIcon
  children: React.ReactNode
}) {
  const Icon = icon ?? (tone === "ok" ? CheckCircle2 : tone === "warn" ? Clock : Info)
  return (
    <div role="status" aria-live="polite" data-slot="auth-banner" data-tone={tone}>
      <CreateSurfaceNotice tone={tone} icon={Icon}>
        {children}
      </CreateSurfaceNotice>
    </div>
  )
}

/**
 * The receipt that replaces a form once it went through — "Check your inbox",
 * "Password updated". Same anatomy as the kit's confirmation receipt.
 */
export function AuthReceipt({
  title,
  children,
}: {
  title: React.ReactNode
  children?: React.ReactNode
}) {
  return (
    <div className="flex items-start gap-3">
      <CheckCircle2 aria-hidden className="mt-0.5 size-5 shrink-0 text-success" />
      <div className="min-w-0">
        <p className="text-body font-semibold text-foreground">{title}</p>
        {children != null && <div className="mt-1 text-body text-muted-foreground">{children}</div>}
      </div>
    </div>
  )
}

/** Text link for the footer and the label row. 44 px tall under a coarse pointer. */
export function AuthLink({ className, ...props }: React.ComponentProps<"a">) {
  return (
    <a
      {...props}
      className={cn(
        "inline-flex items-center rounded-sm text-body text-primary-hover underline-offset-4 hover:underline",
        "focus-visible:outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50 coarse:min-h-[44px]",
        className,
      )}
    />
  )
}

/** Height class for the footer's buttons: the kit's 32 px, 44 px under a coarse pointer. */
export const AUTH_BUTTON = "coarse:h-[44px] coarse:text-base"
/** Height class for the fields: the kit's 32 px, 44 px under a coarse pointer. */
export const AUTH_INPUT = "coarse:h-[44px]"

/** Card-shaped skeleton for a screen that must ask the server before it can draw. */
export function AuthCardSkeleton({ delayMs = 1000 }: { delayMs?: number }) {
  // Nothing for the first second: most gates answer well inside it, and a
  // skeleton that flashes for 80 ms reads as a glitch.
  const [shown, setShown] = React.useState(delayMs <= 0)
  React.useEffect(() => {
    if (delayMs <= 0) return
    const t = window.setTimeout(() => setShown(true), delayMs)
    return () => window.clearTimeout(t)
  }, [delayMs])
  return (
    <div
      data-slot="auth-card-skeleton"
      role="status"
      aria-live="polite"
      aria-busy="true"
      className="flex flex-col bg-card max-md:flex-1 md:rounded-xl md:border md:border-border"
    >
      <span className="sr-only">Loading</span>
      {shown && (
        <div className="flex flex-col gap-3 px-4 py-5 md:px-5" aria-hidden>
          <span className="page-sk page-sk-h" />
          <span className="page-sk page-sk-l w-full" />
          <span className="page-sk page-sk-l w-3/4" />
        </div>
      )}
    </div>
  )
}
