"use client"

import * as React from "react"
import type { LucideIcon } from "lucide-react"
import { Check, CheckCircle2, Clock, Eye, EyeOff, Info } from "lucide-react"

import { cn } from "@/lib/utils"
import { AnimatedMark } from "@/components/branding/animated-mark"
import { CrewshipLogo } from "@/components/branding/crewship-logo"
import { Input } from "@/components/ui/input"
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

/**
 * Copy for the sail panel, one per screen. Branding, not a document section:
 * it is a `<p>`, never a heading, so the form's h1 stays the first heading a
 * screen reader meets.
 */
export const AUTH_PANELS = {
  login: ["Self-hosted", "Command the whole fleet.", "Every agent in its own container, on hardware you own."],
  signup: ["Self-hosted", "Bring your own hardware.", "Every agent runs in its own container, on machines you own."],
  recovery: ["Account recovery", "Back on deck in a minute.", "A reset link goes to the email on your account. Nothing else changes until you use it."],
  reset: ["Account recovery", "Choose a new password.", "Every other session signs out as soon as the new password is saved."],
  bootstrap: ["First run", "You’re the first aboard.", "Nothing has been created yet. This account owns the workspace and invites the rest."],
  join: ["Workspace invitation", "Join your workspace.", "Accept the invitation using the account it was sent to."],
} as const satisfies Record<string, readonly [string, string, string]>
export type AuthPanel = keyof typeof AUTH_PANELS

/** "split" inside a screen with the sail panel; the card then drops its frame. */
const AuthLayout = React.createContext<"card" | "split">("card")

/**
 * Ground + lockup. Wraps one `AuthCard`.
 *
 * With `panel` (every screen under app/(auth), approved 2026-10-18, #3583) the
 * form sits in a column on the left and the animated sail mark fills the right
 * 56 %. Below `lg` the panel becomes a 12rem banner above the form — tall
 * enough to read as the mark, short enough that the form stays above the fold
 * on a phone — and drops its copy, which would run through the sails.
 * Without `panel` (the public pages' password prompt) it is the plain card.
 */
export function AuthScreen({
  children,
  lockup = true,
  width = "sm",
  panel,
  replayKey,
  className,
}: {
  children: React.ReactNode
  /** The public pages carry the mark in their bar instead. */
  lockup?: boolean
  /** Match the card's `size`: 480 or 640. */
  width?: "sm" | "md"
  /** The sail panel and its copy. */
  panel?: AuthPanel
  /** Changing this sends a light sweep across the sails — use it for a step change. */
  replayKey?: string | number
  className?: string
}) {
  if (panel) {
    const [eyebrow, headline, blurb] = AUTH_PANELS[panel]
    return (
      <main
        data-slot="auth-screen"
        data-layout="split"
        className={cn(
          "grid min-h-dvh grid-rows-[12rem_1fr] bg-background text-foreground lg:grid-cols-[minmax(0,44fr)_minmax(0,56fr)] lg:grid-rows-1",
          className,
        )}
      >
        <div data-slot="auth-panel" className="auth-panel relative overflow-hidden lg:order-last">
          <AnimatedMark replayKey={replayKey} />
          <div className="auth-panel-shade pointer-events-none absolute inset-0" />
          <div className="absolute inset-x-10 bottom-14 z-10 hidden max-w-md text-white lg:block">
            <p className="text-micro font-semibold uppercase tracking-widest text-white/70">{eyebrow}</p>
            <p className="mt-3 text-display font-extrabold tracking-tight text-balance">{headline}</p>
            <p className="mt-3 max-w-[36ch] text-body leading-relaxed text-white/80">{blurb}</p>
          </div>
        </div>
        {/* touch-form raises every field, button and link to 44 px under a
            coarse pointer (globals.css). */}
        <div className="touch-form flex min-w-0 flex-col p-6 lg:p-10">
          {lockup && <AuthLockup className="justify-start px-0 pt-0 pb-0 md:justify-start" />}
          <div className="flex flex-1 items-center py-8">
            <div className={cn("mx-auto w-full", width === "md" ? "max-w-[640px]" : "max-w-[352px]")}>
              <AuthLayout value="split">{children}</AuthLayout>
            </div>
          </div>
        </div>
      </main>
    )
  }
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
  const split = React.useContext(AuthLayout) === "split"
  // No empty padded box when a state has nothing but its header and footer.
  const hasBody = banner != null || React.Children.toArray(children).length > 0
  // Beside the sail panel the form is the page: no frame, a large title,
  // the primary across the column and the links under it.
  const inner = split ? (
    <>
      {steps}
      <div data-slot="auth-card-header">
        <h1 id={titleId} className="text-title font-bold tracking-tight text-balance lg:text-display">
          {title}
        </h1>
        {description != null && (
          <p id={descId} className="mt-2 text-body text-muted-foreground">
            {description}
          </p>
        )}
      </div>
      {hasBody && (
        <div data-slot="auth-card-body" className="mt-6 flex flex-col gap-4">
          {banner}
          {children}
        </div>
      )}
      <CreateSurfaceRefusal message={refusal} className="mt-4 rounded-md px-3" />
      {(links != null || actions != null) && (
        <div data-slot="auth-card-footer" className="mt-6 flex flex-col gap-3">
          {actions != null && <div className="flex flex-col-reverse gap-2 [&>*]:w-full">{actions}</div>}
          {links != null && (
            <div className="flex flex-wrap items-center justify-center gap-x-3 gap-y-1 text-body text-muted-foreground">{links}</div>
          )}
        </div>
      )}
    </>
  ) : (
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

  const shell = split
    ? cn("flex flex-col", className)
    : cn(
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
        "focus-visible:outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50 coarse:min-h-12",
        className,
      )}
    />
  )
}

/** Height class for the footer's buttons: the kit's 32 px, 44 px under a coarse pointer. */
export const AUTH_BUTTON = "coarse:h-12 coarse:text-base"
/** Height class for the fields: the kit's 32 px, 44 px under a coarse pointer. */
export const AUTH_INPUT = "coarse:h-12"

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
  const split = React.useContext(AuthLayout) === "split"
  return (
    <div
      data-slot="auth-card-skeleton"
      role="status"
      aria-live="polite"
      aria-busy="true"
      className={split ? "flex flex-col" : "flex flex-col bg-card max-md:flex-1 md:rounded-xl md:border md:border-border"}
    >
      <span className="sr-only">Loading</span>
      {shown && (
        <div className={cn("flex flex-col gap-3", split ? "py-2" : "px-4 py-5 md:px-5")} aria-hidden>
          <span className="page-sk page-sk-h" />
          <span className="page-sk page-sk-l w-full" />
          <span className="page-sk page-sk-l w-3/4" />
        </div>
      )}
    </div>
  )
}

/**
 * A password input with the details people expect on a sign-in page: show /
 * hide (the caret stays where it was), a "Caps Lock is on" line while it is,
 * and on a new password the one rule the server has — 8 characters — ticked as
 * it is typed. Everything else is the kit `Input`.
 */
export function AuthPasswordInput({
  rule = false,
  className,
  onKeyDown,
  onKeyUp,
  onBlur,
  ...props
}: Omit<React.ComponentProps<typeof Input>, "type"> & {
  /** Show "At least 8 characters" under the field and tick it once met. */
  rule?: boolean
}) {
  const [shown, setShown] = React.useState(false)
  const [caps, setCaps] = React.useState(false)
  const ref = React.useRef<HTMLInputElement>(null)
  const readCaps = (e: React.KeyboardEvent<HTMLInputElement>) => setCaps(e.getModifierState?.("CapsLock") ?? false)
  const met = String(props.value ?? "").length >= 8
  return (
    <>
      <div className="relative">
        <Input
          {...props}
          ref={ref}
          type={shown ? "text" : "password"}
          className={cn(AUTH_INPUT, "pr-10 coarse:pr-[44px]", className)}
          onKeyDown={(e) => {
            readCaps(e)
            onKeyDown?.(e)
          }}
          onKeyUp={(e) => {
            readCaps(e)
            onKeyUp?.(e)
          }}
          onBlur={(e) => {
            setCaps(false)
            onBlur?.(e)
          }}
        />
        <button
          type="button"
          aria-label={shown ? "Hide password" : "Show password"}
          aria-pressed={shown}
          aria-controls={props.id}
          disabled={props.disabled}
          // Keep focus (and the caret) in the field.
          onMouseDown={(e) => e.preventDefault()}
          onClick={() => {
            const input = ref.current
            const at = input?.selectionStart ?? null
            setShown((v) => !v)
            requestAnimationFrame(() => {
              input?.focus()
              if (at != null) input?.setSelectionRange(at, at)
            })
          }}
          className={cn(
            "absolute inset-y-0 right-0 grid w-9 place-items-center rounded-r-md text-muted-foreground hover:text-foreground",
            "focus-visible:outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50 disabled:opacity-50 coarse:w-[44px]",
          )}
        >
          {shown ? <EyeOff aria-hidden className="size-4" /> : <Eye aria-hidden className="size-4" />}
        </button>
      </div>
      {rule && (
        <p
          data-slot="password-rule"
          data-met={met}
          className={cn("flex items-center gap-1.5 text-label", met ? "text-success" : "text-muted-foreground")}
        >
          <Check aria-hidden className={cn("size-3.5", !met && "opacity-40")} />
          At least 8 characters
        </p>
      )}
      {caps && (
        <p data-slot="caps-lock" className="flex items-center gap-1.5 text-label text-warn">
          <Info aria-hidden className="size-3.5" />
          Caps Lock is on
        </p>
      )}
    </>
  )
}

/** The step row on First run: where the account sits in setup. */
export function AuthSteps({ steps, current }: { steps: readonly string[]; current: number }) {
  return (
    <ol aria-label="Setup steps" className="mb-6 flex flex-wrap items-center gap-x-1 gap-y-2 text-label text-muted-foreground">
      {steps.map((label, i) => (
        <li
          key={label}
          aria-current={i === current ? "step" : undefined}
          className={cn("flex items-center gap-1.5 pr-1", i === current && "font-semibold text-foreground")}
        >
          {i > 0 && <span aria-hidden className="mr-1 h-px w-3 bg-border" />}
          <span
            aria-hidden
            className={cn(
              "grid size-4.5 place-items-center rounded-full border text-micro font-semibold",
              i === current ? "border-primary bg-primary text-primary-foreground" : "border-border",
            )}
          >
            {i + 1}
          </span>
          {label}
        </li>
      ))}
    </ol>
  )
}
