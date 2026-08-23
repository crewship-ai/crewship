"use client"

import * as React from "react"
import { ArrowLeft, Check, ChevronDown, ChevronRight, Search, TriangleAlert, X } from "lucide-react"

import { cn } from "@/lib/utils"
import { Button } from "@/components/ui/button"
import { Spinner } from "@/components/ui/spinner"
import { ConceptIcon, type SurfaceIconComponent } from "@/components/ui/concept-icon"
import type { AccentName } from "@/lib/concept-accents"
import { Dialog, DialogContent, DialogDescription, DialogTitle } from "@/components/ui/dialog"
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog"

/**
 * CreateSurface — the canonical shell for everything a SubBar action opens.
 *
 * SubBar already unified the DOORS (`New issue`, `Import`, `Add secret`…): one
 * row, one primary, one ghost, identical sizing on every page. What sits BEHIND
 * those doors never got the same treatment, and the audit on /design counts the
 * result: three different modal shells (Radix Dialog, a hand-rolled
 * `fixed inset-0` with `bg-black/50`, and another with
 * `bg-background/70 backdrop-blur-md`), eleven widths, two corner radii, four
 * title sizes and eight ways of drawing the confirm button, across twelve entry
 * points. The blur is the one people name without prompting — two doors out of
 * twelve frost the page behind them, so those two feel like a different
 * application.
 *
 * This is that treatment. Every create/import surface mounts the same Radix
 * Dialog, so overlay, focus trap, Esc, scroll lock and the accessible name are
 * one implementation rather than twelve; the parts below fix the geometry the
 * twelve had each chosen for themselves.
 *
 *   ┌─────────────────────────────────────────────┐
 *   │ ▣ context › Title                       [×] │  Header  — coloured icon
 *   ├─────────────────────────────────────────────┤
 *   │ ① ─── ② ─── ③ ─── ④                        │  Steps   — optional
 *   ├─────────────────────────────────────────────┤
 *   │                                             │
 *   │  Body — the ONLY scrollport                 │  Body
 *   │   · Section / Grid / Field / Choice         │
 *   │   · Disclosure for everything advanced      │
 *   │                                             │
 *   ├─────────────────────────────────────────────┤
 *   │ [pill] [pill] [pill]                        │  Pills   — optional
 *   ├─────────────────────────────────────────────┤
 *   │ ⌘↵ to create · Esc      Cancel   [ Create ] │  Footer  — never scrolls
 *   └─────────────────────────────────────────────┘
 *
 * Rules the shell enforces rather than documents:
 *
 *  · ONE overlay. `bg-black/50`, from the shared DialogOverlay. No blur, ever —
 *    a surface that frosts the page reads as a different product.
 *  · FOUR widths (sm/md/lg/xl), picked once per surface and constant for its
 *    whole life. `New crew` currently grows 680 → 940px between step 1 and
 *    step 2, which moves the footer out from under the cursor mid-flow.
 *  · The FOOTER IS OUTSIDE THE SCROLLPORT. The body scrolls; the primary
 *    action is always reachable without scrolling to find it.
 *  · EXACTLY ONE primary. Everything else is `ghost`, and Cancel is always
 *    present, always leftmost of the action group, always in the same place.
 *  · ⌘↵ submits and Esc cancels on every surface, wired here — not re-added by
 *    hand in each dialog, where nine of twelve forgot it.
 *
 * ── The phone is a second shell, not a narrower first one ────────────────
 *
 * Below `sm` the surface stops being a centred card and becomes a BOTTOM
 * SHEET: anchored to the bottom edge, rounded on top only, capped at 92dvh,
 * entering by sliding up rather than zooming in. That is not decoration. A
 * centred dialog on a phone puts its primary action in the middle of the
 * screen — the hardest place on the device to reach one-handed — and a
 * full-screen takeover throws away the context you were looking at. A sheet
 * puts the action at the thumb and keeps the page visible above it.
 *
 * Three consequences the parts below implement, and which the twelve surfaces
 * being replaced get wrong in eleven cases:
 *
 *  1. `dvh`, not `vh`. Mobile Safari's `vh` counts the collapsed toolbar, so a
 *     `85vh` sheet is taller than the visible viewport and its footer sits
 *     under the browser chrome.
 *  2. 44px touch targets. Every control that is `h-7`/`h-8` on a pointer
 *     device grows on a phone — below ~44px the tap-target guidance in every
 *     platform HIG stops being met, and an `h-8` button here is 29px.
 *
 *     Read that last number twice: this project sets `--spacing: 0.23rem`
 *     (globals.css), not Tailwind's default 0.25rem, so the WHOLE spacing
 *     scale is 92% of what its name suggests. `h-11` is 40.5px, not 44 — it
 *     was measured at 40.4688px on an iPhone 13 viewport before this comment
 *     existed. **`h-12` (44.16px) is the touch-target class in this repo.**
 *     Anyone porting a `h-11` recipe from Tailwind's own docs will land 8%
 *     short and never notice, because 40px still looks fine.
 *
 *  3. `env(safe-area-inset-bottom)`. The footer pads itself past the home
 *     indicator instead of tucking the primary action underneath it.
 *
 * ── Why every mobile class is written twice ──────────────────────────────
 *
 * Once as `max-sm:` (the real breakpoint) and once under
 * `data-[mobile=true]` / `group-data-[mobile=true]/surface` (a preview frame
 * that forces the phone layout at desktop width — see `CreateSurfaceFrame`,
 * which is how /design shows both versions side by side). The pair cannot be
 * generated at runtime: Tailwind's scanner reads source text, so a composed
 * class name is never emitted. Verbose, and the only version that works.
 *
 * Pair it with `sub-bar.tsx`: SubBarPrimary/SubBarSecondary are the door,
 * CreateSurface is the room. Neither should be re-implemented per page.
 */

export type CreateSurfaceSize = "sm" | "md" | "lg" | "xl"

/** Re-exported so a caller writes one import, not three. */
export type { SurfaceIconComponent as SurfaceIcon }

/**
 * Fixed widths. Not a free-form `max-w-*` per caller, which is how the product
 * arrived at 448 / 512 / 560 / 576 / 640 / 672 / 680 / 720 / 768 / 940 / 1100px
 * for what is conceptually the same object.
 *
 *   sm  480  one question — a file to pick, a provider to choose
 *   md  640  a form — the New-issue shape, the default
 *   lg  800  a form beside a preview, or a browsable list
 *   xl  960  a catalogue with tiles; the widest anything is allowed to be
 */
const SIZE_CLASS: Record<CreateSurfaceSize, string> = {
  sm: "sm:max-w-[480px]",
  md: "sm:max-w-[640px]",
  lg: "sm:max-w-[800px]",
  xl: "sm:max-w-[960px]",
}

/**
 * Whether the surface is inside a Radix Dialog.
 *
 * The header wants `DialogTitle`/`DialogDescription` so the modal gets a
 * correct accessible name and Radix stops warning about a missing
 * description — but those primitives THROW outside a `Dialog` root, which is
 * exactly what `CreateSurfaceFrame` is. Rendering plain heading elements there
 * keeps one header component for both shells; the alternative (a second
 * header for previews) is how the twelve surfaces got here in the first place.
 */
const InDialog = React.createContext(true)

/**
 * Wraps a dismissal so the guard sees it.
 *
 * The first cut of the guard only covered Esc and the overlay click, because
 * those are the two the shell owns. The header's × and the footer's Cancel are
 * wired by the CALLER, so they went straight past it — which is precisely the
 * defect the guard exists to prevent, inverted. A test caught it before this
 * shipped, and the lesson is in the shape of the fix: the shell cannot guard a
 * route it does not own, so it has to hand the callers a wrapper.
 *
 * Default is the identity function: outside a CreateSurface, and inside one
 * that is not dirty, `guard(fn)` is exactly `fn()`.
 */
const CloseGuard = React.createContext<(run: () => void) => void>((run) => run())

/**
 * The guarded-dismissal wrapper for a surface's own controls.
 *
 * `CreateSurfaceHeader` uses it for its × already. Reach for it directly when
 * a surface has another way out — a "Cancel" that really means close, a
 * "Discard" link. The footer's Cancel is deliberately NOT guarded on your
 * behalf: half the surfaces overload it as "back out of this panel", and
 * prompting someone about unsaved work for closing a colour picker is worse
 * than not prompting at all.
 */
export function useCreateSurfaceClose() {
  return React.useContext(CloseGuard)
}

/** Geometry shared by the real dialog and the preview frame. */
const SHELL_BASE = "group/surface flex flex-col gap-0 overflow-hidden bg-card"

/** Bottom-sheet geometry, at the phone breakpoint and under a forcing frame. */
const SHELL_SHEET = [
  "max-sm:inset-x-0 max-sm:bottom-0 max-sm:top-auto max-sm:w-full max-sm:max-w-none",
  "max-sm:translate-x-0 max-sm:translate-y-0",
  "max-sm:max-h-[92dvh] max-sm:rounded-b-none max-sm:rounded-t-2xl max-sm:border-x-0 max-sm:border-b-0",
  // Slide up rather than zoom in; cancel the base card's zoom so the two
  // animations do not fight.
  "max-sm:data-[state=open]:slide-in-from-bottom max-sm:data-[state=closed]:slide-out-to-bottom",
  "max-sm:data-[state=open]:zoom-in-100 max-sm:data-[state=closed]:zoom-out-100",
].join(" ")

export interface CreateSurfaceProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  /**
   * The surface has unsaved input.
   *
   * When true, Esc and an overlay click ask before throwing it away. This
   * belongs to the SHELL and not to the caller for the same reason ⌘↵ does:
   * the shell owns those two dismissals, so a per-page guard can only cover
   * the close BUTTON and will be silently bypassed by the two routes people
   * actually use. `page-editor.tsx` is the one surface that got this right
   * today, and it had to hand-roll its whole modal to do it.
   */
  dirty?: boolean
  /** What the confirm says. Default is generic; name the thing when you can. */
  discardLabel?: React.ReactNode
  /** Fixed for the surface's whole life — do not vary it per step. */
  size?: CreateSurfaceSize
  /**
   * ⌘↵ / Ctrl↵. Wired once here so no surface has to remember it. No-op when
   * the form is not submittable — guard inside the handler.
   */
  onSubmit?: () => void
  /** Accessible name when the header title is not the whole story. */
  ariaLabel?: string
  className?: string
  children: React.ReactNode
}

export function CreateSurface({
  open,
  onOpenChange,
  dirty = false,
  discardLabel,
  size = "md",
  onSubmit,
  ariaLabel,
  className,
  children,
}: CreateSurfaceProps) {
  const contentRef = React.useRef<HTMLDivElement>(null)
  const [confirmingDiscard, setConfirmingDiscard] = React.useState(false)
  // What to run once the person confirms. Held in a ref rather than state so
  // confirming does not depend on a render landing first.
  const pendingRef = React.useRef<(() => void) | null>(null)

  const guard = React.useCallback(
    (run: () => void) => {
      if (!dirty) {
        run()
        return
      }
      pendingRef.current = run
      setConfirmingDiscard(true)
    },
    [dirty],
  )

  // Only intercept CLOSING, and only while there is something to lose.
  const requestOpenChange = React.useCallback(
    (next: boolean) => {
      if (next) {
        onOpenChange(true)
        return
      }
      guard(() => onOpenChange(false))
    },
    [guard, onOpenChange],
  )

  const handleKeyDown = React.useCallback(
    (e: React.KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.key === "Enter") {
        e.preventDefault()
        onSubmit?.()
      }
    },
    [onSubmit],
  )

  return (
    <>
    <Dialog open={open} onOpenChange={requestOpenChange}>
      <DialogContent
        ref={contentRef}
        aria-label={ariaLabel}
        // Radix's own opt-out for the missing-description warning. The header
        // renders a description when it has one and nothing when it does not;
        // echoing the title into an sr-only node to silence the warning is what
        // made screen readers say "New project. New project.".
        aria-describedby={undefined}
        showCloseButton={false}
        onKeyDown={handleKeyDown}
        tabIndex={-1}
        // Focus goes to the first real field, not to the close button, so the
        // surface opens ready to type — but if the caller has no field to
        // focus, focus must still land INSIDE the surface, or ⌘↵ never reaches
        // the handler above and the shell's headline promise is silently false
        // until the user clicks. A field's own autoFocus runs after this and
        // wins, so this is a floor, not an override.
        onOpenAutoFocus={(e) => {
          e.preventDefault()
          contentRef.current?.focus({ preventScroll: true })
        }}
        className={cn(SHELL_BASE, "p-0", "sm:max-h-[min(85vh,720px)]", SIZE_CLASS[size], SHELL_SHEET, className)}
      >
        <SheetGrabber />
        <CloseGuard.Provider value={guard}>{children}</CloseGuard.Provider>
      </DialogContent>
    </Dialog>

    {/* An AlertDialog, not a second CreateSurface: this is a decision with two
        answers and no form, which is what AlertDialog is for. Keeping the
        distinction is half of why there were three modal shells. */}
    <AlertDialog open={confirmingDiscard} onOpenChange={setConfirmingDiscard}>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>Discard {discardLabel ?? "this"}?</AlertDialogTitle>
          <AlertDialogDescription>
            You have unsaved input. Closing throws it away — there is no draft.
          </AlertDialogDescription>
        </AlertDialogHeader>
        <AlertDialogFooter>
          <AlertDialogCancel onClick={() => { pendingRef.current = null }}>Keep editing</AlertDialogCancel>
          <AlertDialogAction
            onClick={() => {
              setConfirmingDiscard(false)
              const run = pendingRef.current
              pendingRef.current = null
              run?.()
            }}
          >
            Discard
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
    </>
  )
}

/**
 * The same chrome without a Dialog around it.
 *
 * Two uses. /design renders the phone version of a surface inside a handset
 * frame at desktop width, which a portalled dialog cannot do; and a create
 * flow that has outgrown a modal (the page editor is the standing candidate)
 * can be embedded in a route with its geometry intact rather than forked.
 *
 * `mobile` forces the phone layout regardless of viewport — that is what makes
 * the side-by-side on /design honest, because both columns are this component.
 */
export function CreateSurfaceFrame({
  size = "md",
  mobile = false,
  className,
  children,
}: {
  size?: CreateSurfaceSize
  mobile?: boolean
  className?: string
  children: React.ReactNode
}) {
  return (
    <div
      data-mobile={mobile || undefined}
      className={cn(
        SHELL_BASE,
        "w-full border border-hairline shadow-lg",
        mobile ? "h-full rounded-none" : cn("rounded-lg", SIZE_CLASS[size]),
        className,
      )}
    >
      {mobile && <SheetGrabber />}
      <InDialog.Provider value={false}>{children}</InDialog.Provider>
    </div>
  )
}

/** The sheet's drag affordance. Phone only — a card does not need a handle. */
function SheetGrabber() {
  return (
    <div
      aria-hidden
      className="hidden shrink-0 justify-center pt-2 pb-0.5 max-sm:flex group-data-[mobile=true]/surface:flex"
    >
      <span className="h-1 w-9 rounded-full bg-foreground/20" />
    </div>
  )
}

/* --------------------------------------------------------------------------
 * Header — `context › Title`, a coloured icon, optional back arrow, one close.
 * ------------------------------------------------------------------------ */

export interface CreateSurfaceHeaderProps {
  /**
   * What this is being created INSIDE — a crew, a workspace, a page. Rendered
   * demoted before the title with a chevron, which is the New-issue idiom and
   * the only one of the twelve that answers "where will this land?" before you
   * have filled anything in. Hidden on a phone, where the row has no width to
   * spare and the title is the thing that has to survive.
   */
  context?: React.ReactNode
  title: React.ReactNode
  /** One line under the title. Keep it to what the title cannot say. */
  description?: React.ReactNode
  /** A CONCEPT_ICON key — supplies the glyph AND its colour. */
  concept?: string
  /** An explicit glyph for things that are not product concepts. */
  icon?: SurfaceIconComponent
  /** Override the icon's colour. */
  accent?: AccentName
  /** Renders the back arrow. Absent = no arrow, not a disabled one. */
  onBack?: () => void
  onClose: () => void
  /** Right of the title, left of the close — step counters, a mode switch. */
  meta?: React.ReactNode
  /**
   * Keep `context` visible on a phone.
   *
   * `context` is hidden below `sm` because it is normally a breadcrumb and the
   * row has no width to spare. On New issue it is the CREW SELECTOR, and crew
   * is required — so hiding it shipped a surface whose landing place could not
   * be chosen on a phone, with no caller-side workaround (a child cannot
   * un-hide itself under a `display:none` parent). Set this whenever `context`
   * is interactive.
   */
  keepContext?: boolean
}

export function CreateSurfaceHeader({
  context,
  title,
  description,
  concept,
  icon,
  accent,
  onBack,
  onClose,
  meta,
  keepContext = false,
}: CreateSurfaceHeaderProps) {
  const inDialog = React.useContext(InDialog)
  const guard = React.useContext(CloseGuard)
  const Title = inDialog ? DialogTitle : React.Fragment
  const titleProps = inDialog ? { asChild: true as const } : {}

  return (
    <div className="shrink-0 border-b border-hairline px-4 py-2.5 sm:px-5 sm:py-3">
      <div className="flex items-center gap-2">
        {onBack && (
          <Button
            variant="ghost"
            size="icon-xs"
            onClick={onBack}
            aria-label="Back"
            className="-ml-1 shrink-0 text-muted-foreground hover:text-foreground max-sm:h-12 max-sm:w-12 group-data-[mobile=true]/surface:h-12 group-data-[mobile=true]/surface:w-12"
          >
            <ArrowLeft className="h-4 w-4" />
          </Button>
        )}

        {(concept || icon) && (
          <ConceptIcon concept={concept} icon={icon} accent={accent} variant="chip" size="sm" />
        )}

        {/* One title element spanning the whole path. Two headings side by side
            would make the accessible name "IssuesNew issue". */}
        <Title {...titleProps}>
          <h2 className="flex min-w-0 items-center gap-1.5 text-sm font-medium leading-none">
            {context != null && (
              <>
                <span
                  className={cn(
                    "truncate font-normal text-muted-foreground",
                    !keepContext && "max-sm:hidden group-data-[mobile=true]/surface:hidden",
                  )}
                >
                  {context}
                </span>
                <ChevronRight
                  aria-hidden
                  className={cn(
                    "h-3 w-3 shrink-0 text-muted-foreground-soft",
                    !keepContext && "max-sm:hidden group-data-[mobile=true]/surface:hidden",
                  )}
                />
                {/* A real text node, or the accessible name concatenates.
                 *
                 * The comment above says one <h2> exists so the name is not
                 * "IssuesNew issue" — and then the separator was an icon,
                 * which contributes nothing, so the computed name was exactly
                 * that: "SMONew issue". Measured in the browser, not guessed.
                 * sub-bar.tsx already solved this with a literal " / "; this is
                 * the same fix, hidden from sight because the chevron is the
                 * visible separator. */}
                <span className="sr-only"> › </span>
              </>
            )}
            <span className="truncate text-foreground">{title}</span>
          </h2>
        </Title>

        <div className="flex-1" />

        {meta && (
          <div className="flex shrink-0 items-center gap-2 text-xs tabular-nums text-muted-foreground">
            {meta}
          </div>
        )}

        <Button
          variant="ghost"
          size="icon-xs"
          onClick={() => guard(onClose)}
          aria-label="Close"
          className="-mr-1 shrink-0 text-muted-foreground hover:text-foreground max-sm:h-12 max-sm:w-12 group-data-[mobile=true]/surface:h-12 group-data-[mobile=true]/surface:w-12"
        >
          <X className="h-4 w-4" />
        </Button>
      </div>

      {/* No description means NO node.
       *
       * This used to render `<DialogDescription className="sr-only">{title}`
       * to silence Radix's missing-description warning. The cost was not
       * obvious until eleven surfaces migrated at once: a screen reader read
       * "New project. New project.", and `getByText(title)` matched twice, so
       * two agents had to rewrite existing assertions that were not wrong.
       * The warning is silenced properly instead — `CreateSurface` passes
       * `aria-describedby={undefined}` to DialogContent, which is Radix's own
       * opt-out. */}
      {description != null &&
        (inDialog ? (
          <DialogDescription className="mt-1.5 text-xs leading-relaxed text-muted-foreground">
            {description}
          </DialogDescription>
        ) : (
          <p className="mt-1.5 text-xs leading-relaxed text-muted-foreground">{description}</p>
        ))}
    </div>
  )
}

/* --------------------------------------------------------------------------
 * Steps — chips on a pointer device, a progress bar on a phone.
 * ------------------------------------------------------------------------ */

export interface CreateSurfaceStep {
  id: string
  label: string
}

export function CreateSurfaceSteps({
  steps,
  current,
  onJump,
  ariaLabel = "Steps",
}: {
  steps: CreateSurfaceStep[]
  /** Index into `steps`, 0-based. */
  current: number
  /** Only ever called for a step already completed — you cannot skip forward. */
  onJump?: (index: number) => void
  /** Names the landmark. "Add credential steps", "Wizard progress". */
  ariaLabel?: string
}) {
  const pct = steps.length > 1 ? ((current + 1) / steps.length) * 100 : 100

  // A <nav>, because two migrations wrapped this in one by hand to keep an
  // existing landmark test green — and every chip carries "Step N: Label",
  // because naming them by visible text alone broke
  // e2e/create-crew-wizard.spec.ts and forced six unit assertions to change
  // that were not wrong.
  return (
    <nav aria-label={ariaLabel} className="shrink-0 border-b border-hairline px-4 py-2 sm:px-5">
      {/* Pointer device: the whole path, so you can see what is still coming
          and click back into what you already answered. */}
      <div className="flex items-center gap-1 overflow-x-auto max-sm:hidden group-data-[mobile=true]/surface:hidden [&::-webkit-scrollbar]:hidden [-ms-overflow-style:none] [scrollbar-width:none]">
        {steps.map((step, i) => {
          const done = i < current
          const active = i === current
          return (
            <React.Fragment key={step.id}>
              {i > 0 && <span className="h-px w-3 shrink-0 bg-border" aria-hidden />}
              <button
                type="button"
                aria-label={`Step ${i + 1}: ${step.label}`}
                aria-current={active ? "step" : undefined}
                disabled={!done || !onJump}
                onClick={() => done && onJump?.(i)}
                className={cn(
                  "flex shrink-0 items-center gap-1.5 rounded-full px-2 py-1 text-xs transition-colors",
                  active && "bg-primary/15 font-medium text-primary-hover",
                  done && "cursor-pointer text-muted-foreground hover:text-foreground",
                  !active && !done && "cursor-default text-muted-foreground-soft",
                )}
              >
                <span
                  className={cn(
                    "flex h-4 w-4 shrink-0 items-center justify-center rounded-full text-[10px] font-medium tabular-nums",
                    active ? "bg-primary/25 text-primary-hover" : "bg-muted text-muted-foreground",
                  )}
                >
                  {i + 1}
                </span>
                <span className="whitespace-nowrap">{step.label}</span>
              </button>
            </React.Fragment>
          )
        })}
      </div>

      {/* Phone: five chips do not fit, and a strip that scrolls sideways hides
          exactly the information it exists to give. A bar plus the count says
          the same thing in one line. */}
      <div className="hidden max-sm:block group-data-[mobile=true]/surface:block">
        <div className="flex items-baseline justify-between gap-2">
          <span className="truncate text-xs font-medium text-foreground">{steps[current]?.label}</span>
          <span className="shrink-0 text-[11px] tabular-nums text-muted-foreground">
            {current + 1} / {steps.length}
          </span>
        </div>
        <div
          role="progressbar"
          aria-valuemin={1}
          aria-valuemax={steps.length}
          aria-valuenow={current + 1}
          className="mt-1.5 h-1 w-full overflow-hidden rounded-full bg-muted"
        >
          <span
            className="block h-full rounded-full bg-primary transition-[width] duration-200"
            style={{ width: `${pct}%` }}
          />
        </div>
      </div>
    </nav>
  )
}

/* --------------------------------------------------------------------------
 * Body — the one scrollport.
 * ------------------------------------------------------------------------ */

export interface CreateSurfaceBodyProps extends React.ComponentProps<"div"> {
  /**
   * Off for a body that brings its own padding, or that IS a full-bleed
   * editor. Do not reach for `className="p-0"` — the padding is declared at
   * two breakpoints, so an unprefixed `p-0` loses to `sm:px-5 sm:py-4` in
   * tailwind-merge and you need the `sm:` twin as well. Three separate
   * migrations landed on that trap before this prop existed.
   */
  padded?: boolean
  /**
   * Off for a body that manages its own inner scrollports — a two-pane editor,
   * a virtualised list. The body stays the flex child that shrinks; it just
   * stops being the thing that scrolls.
   */
  scroll?: boolean
}

export function CreateSurfaceBody({
  padded = true,
  scroll = true,
  className,
  children,
  ...props
}: CreateSurfaceBodyProps) {
  return (
    <div
      className={cn(
        // min-h-0 is what actually lets this shrink inside the flex column;
        // without it the body pushes the footer off a short viewport.
        "min-h-0 flex-1",
        scroll ? "overflow-y-auto overscroll-contain" : "overflow-hidden",
        padded && "px-4 py-3.5 sm:px-5 sm:py-4",
        className,
      )}
      {...props}
    >
      {children}
    </div>
  )
}

/**
 * The touch-target class for a caller's own `<input>` / `<select>`.
 *
 * The shell grows its OWN controls at the phone breakpoint, but a body full of
 * bare inputs belongs to the surface, and four migrations each re-derived
 * 44.16px by hand — one of them landed on `h-11` (40.5px) doing it, which is
 * exactly the `--spacing: 0.23rem` trap this file warns about at the top.
 */
export const CREATE_SURFACE_INPUT = "h-8 text-xs max-sm:h-12 max-sm:text-sm"

/* --------------------------------------------------------------------------
 * Structure inside the body — Section, Grid, Field, Choice, ToggleRow,
 * Disclosure. Enough to lay out the product's largest create form (New agent,
 * twenty fields) without a single bespoke wrapper.
 * ------------------------------------------------------------------------ */

/** A titled group of fields. The rule the twelve surfaces lacked: fields that
 *  belong together say so, instead of being separated by a bare `space-y-4`. */
export function CreateSurfaceSection({
  title,
  hint,
  concept,
  icon,
  accent,
  className,
  children,
}: {
  title?: React.ReactNode
  hint?: React.ReactNode
  concept?: string
  icon?: SurfaceIconComponent
  accent?: AccentName
  className?: string
  children: React.ReactNode
}) {
  return (
    <section className={cn("flex flex-col gap-2.5", className)}>
      {title != null && (
        <div className="flex items-center gap-1.5">
          {(concept || icon) && <ConceptIcon concept={concept} icon={icon} accent={accent} size="sm" />}
          <h3 className="text-[11px] font-semibold uppercase tracking-wider text-foreground/75">{title}</h3>
          {/* A hint that truncates to "— a template fills the persona, model and t…"
              is worse than no hint. The section title survives; this does not. */}
          {hint != null && (
            <span className="truncate text-[11px] text-muted-foreground-soft max-sm:hidden group-data-[mobile=true]/surface:hidden">
              — {hint}
            </span>
          )}
        </div>
      )}
      {children}
    </section>
  )
}

/** Two columns on a pointer device, one on a phone. Side-by-side fields at
 *  360px are two half-width fields, which is worse than one full-width one. */
export function CreateSurfaceGrid({
  cols = 2,
  className,
  children,
}: {
  cols?: 2 | 3
  className?: string
  children: React.ReactNode
}) {
  return (
    <div
      className={cn(
        "grid gap-3",
        cols === 2
          ? "sm:grid-cols-2 group-data-[mobile=true]/surface:grid-cols-1"
          : "sm:grid-cols-3 group-data-[mobile=true]/surface:grid-cols-1",
        className,
      )}
    >
      {children}
    </div>
  )
}

/** A labelled field for everything that is not the title. */
export function CreateSurfaceField({
  label,
  hint,
  htmlFor,
  required = false,
  className,
  children,
}: {
  label: React.ReactNode
  hint?: React.ReactNode
  htmlFor?: string
  /** Renders the marker. The FORM still validates — this only says so early. */
  required?: boolean
  className?: string
  children: React.ReactNode
}) {
  return (
    <div className={cn("flex min-w-0 flex-col gap-1.5", className)}>
      <label
        htmlFor={htmlFor}
        className="flex items-center gap-1 text-[11px] font-medium uppercase tracking-wider text-muted-foreground"
      >
        {label}
        {required && (
          <span className="text-destructive" aria-hidden>
            *
          </span>
        )}
      </label>
      {children}
      {hint != null && <span className="text-[11px] leading-relaxed text-muted-foreground-soft">{hint}</span>}
    </div>
  )
}

/** A row of mutually exclusive chips — role, tool profile, lead mode. Beats a
 *  `<select>` for three or four short options: every option is visible, and on
 *  a phone the tap target is the whole chip rather than a 16px caret. */
export function CreateSurfaceChoice<T extends string>({
  value,
  options,
  onChange,
  ariaLabel,
}: {
  value: T
  options: { value: T; label: React.ReactNode; hint?: string }[]
  onChange: (value: T) => void
  ariaLabel?: string
}) {
  return (
    <div role="radiogroup" aria-label={ariaLabel} className="flex flex-wrap gap-1.5">
      {options.map((o) => {
        const active = o.value === value
        return (
          <button
            key={o.value}
            type="button"
            role="radio"
            aria-checked={active}
            title={o.hint}
            onClick={() => onChange(o.value)}
            className={cn(
              "h-8 rounded-md border px-2.5 text-xs font-medium transition-colors",
              "max-sm:h-12 max-sm:flex-1 max-sm:px-3 group-data-[mobile=true]/surface:h-12 group-data-[mobile=true]/surface:flex-1",
              active
                ? "border-primary/40 bg-primary/15 text-primary-hover"
                : "border-hairline bg-foreground/[0.03] text-muted-foreground hover:bg-foreground/[0.07] hover:text-foreground",
            )}
          >
            {o.label}
          </button>
        )
      })}
    </div>
  )
}

/** Label + explanation + a control on the right. The shape every boolean
 *  setting wants, and which four surfaces each drew differently. */
export function CreateSurfaceToggleRow({
  label,
  hint,
  concept,
  icon,
  accent,
  control,
}: {
  label: React.ReactNode
  hint?: React.ReactNode
  concept?: string
  icon?: SurfaceIconComponent
  accent?: AccentName
  control: React.ReactNode
}) {
  return (
    <div className="flex items-center gap-3 rounded-lg border border-hairline bg-foreground/[0.02] px-3 py-2.5">
      {(concept || icon) && <ConceptIcon concept={concept} icon={icon} accent={accent} size="sm" />}
      <div className="min-w-0 flex-1">
        <div className="text-[13px] font-medium text-foreground">{label}</div>
        {hint != null && <div className="mt-0.5 text-[11px] leading-relaxed text-muted-foreground">{hint}</div>}
      </div>
      <div className="shrink-0">{control}</div>
    </div>
  )
}

/**
 * Everything that has a right answer already.
 *
 * The alternative the product currently ships is worse in both directions: New
 * agent hides five real settings behind an "Advanced" that looks like a link,
 * and New crew promotes container internals to a step of their own, so a
 * four-question flow reads as five. A disclosure that names what is inside it
 * and says what the defaults currently are is the honest middle.
 */
export function CreateSurfaceDisclosure({
  label,
  summary,
  defaultOpen = false,
  concept,
  icon,
  accent,
  children,
}: {
  label: React.ReactNode
  /** What is inside, and what it currently says. Shown while collapsed. */
  summary?: React.ReactNode
  defaultOpen?: boolean
  concept?: string
  icon?: SurfaceIconComponent
  accent?: AccentName
  children: React.ReactNode
}) {
  const [open, setOpen] = React.useState(defaultOpen)

  return (
    <div className="overflow-hidden rounded-lg border border-hairline bg-foreground/[0.02]">
      <button
        type="button"
        aria-expanded={open}
        onClick={() => setOpen((o) => !o)}
        className="flex w-full items-center gap-2 px-3 py-2.5 text-left transition-colors hover:bg-foreground/[0.03] max-sm:py-3 group-data-[mobile=true]/surface:py-3"
      >
        {(concept || icon) && <ConceptIcon concept={concept} icon={icon} accent={accent} size="sm" />}
        <span className="shrink-0 text-[13px] font-medium text-foreground">{label}</span>
        {summary != null && !open && (
          <span className="min-w-0 flex-1 truncate text-[11px] text-muted-foreground-soft">{summary}</span>
        )}
        <ChevronDown
          className={cn(
            "ml-auto h-4 w-4 shrink-0 text-muted-foreground transition-transform duration-150",
            open && "rotate-180",
          )}
        />
      </button>
      {open && <div className="flex flex-col gap-3 border-t border-hairline px-3 py-3">{children}</div>}
    </div>
  )
}

/* --------------------------------------------------------------------------
 * Fields — the borderless title/description pair from New issue, which is the
 * one input treatment in the product that does not look like a form.
 * ------------------------------------------------------------------------ */

export function CreateSurfaceTitleInput({ className, ...props }: React.ComponentProps<"input">) {
  return (
    <input
      type="text"
      className={cn(
        "w-full bg-transparent text-base font-medium text-foreground outline-none placeholder:text-muted-foreground/50",
        className,
      )}
      {...props}
    />
  )
}

export function CreateSurfaceDescriptionInput({ className, ...props }: React.ComponentProps<"textarea">) {
  return (
    <textarea
      rows={3}
      className={cn(
        "w-full resize-none bg-transparent text-sm text-foreground/90 outline-none placeholder:text-muted-foreground/40",
        className,
      )}
      {...props}
    />
  )
}

/* --------------------------------------------------------------------------
 * Pills — the metadata row. One height, one radius, one hover.
 * ------------------------------------------------------------------------ */

export function CreateSurfacePills({ className, children }: React.ComponentProps<"div">) {
  return (
    <div
      className={cn(
        "shrink-0 flex items-center gap-1.5 border-t border-hairline px-4 py-2 sm:flex-wrap sm:px-5",
        // A phone wraps six pills onto three rows and eats a third of the
        // sheet. One scrolling row keeps the body where it was.
        "max-sm:flex-nowrap max-sm:overflow-x-auto group-data-[mobile=true]/surface:flex-nowrap group-data-[mobile=true]/surface:overflow-x-auto",
        "[&::-webkit-scrollbar]:hidden [-ms-overflow-style:none] [scrollbar-width:none]",
        className,
      )}
    >
      {children}
    </div>
  )
}

export interface CreateSurfacePillProps extends React.ComponentProps<"button"> {
  icon?: SurfaceIconComponent
  concept?: string
  accent?: AccentName
  /**
   * An arbitrary node before the label, winning over `icon`/`concept`.
   *
   * `SurfaceIconComponent` takes className and style only, so a prop-driven
   * glyph (`<PriorityIcon priority=… />`, `<StatusIcon status=… />`) or a
   * coloured swatch cannot be an icon at all. Three migrations put those in as
   * children and lost the pill's sizing contract; this is where they go.
   */
  leading?: React.ReactNode
  /** Set = the pill carries a value, so it renders at full strength. */
  set?: boolean
  /** Read-only facts (a status the surface does not let you change). */
  readOnly?: boolean
}

export function CreateSurfacePill({
  icon,
  concept,
  accent,
  leading,
  set = false,
  readOnly = false,
  className,
  children,
  ...props
}: CreateSurfacePillProps) {
  return (
    <button
      type="button"
      disabled={readOnly || props.disabled}
      className={cn(
        "flex h-7 shrink-0 items-center gap-1.5 whitespace-nowrap rounded-md border border-hairline bg-foreground/[0.04] px-2.5 text-xs transition-colors",
        "max-sm:h-10 group-data-[mobile=true]/surface:h-10",
        !readOnly && "hover:bg-foreground/[0.08]",
        readOnly && "cursor-default",
        set ? "text-foreground/85" : "text-muted-foreground",
        className,
      )}
      {...props}
    >
      {leading ?? (
        (icon || concept) && (
          <ConceptIcon
            concept={concept}
            icon={icon}
            accent={accent ?? (set ? undefined : "slate")}
            size="sm"
            className="h-3.5 w-3.5"
          />
        )
      )}
      {children}
    </button>
  )
}

/* --------------------------------------------------------------------------
 * Tiles — the "pick one of these" grid. Add integration, Add secret's shape
 * step and New routine's three doors each drew their own; this is the one.
 * ------------------------------------------------------------------------ */

export interface CreateSurfaceTileProps extends Omit<React.ComponentProps<"button">, "title"> {
  icon?: SurfaceIconComponent
  concept?: string
  accent?: AccentName
  /**
   * An arbitrary node in the icon slot, winning over `icon`/`concept`.
   *
   * A brand mark that already carries its own tinted tile (ProviderMark, a
   * CrewIcon with a status ring) renders a tile inside a tile in the wrong
   * colour when forced through ConceptIcon. Two catalogues hand-rolled a copy
   * of this component's class list rather than accept that.
   */
  leading?: React.ReactNode
  title: React.ReactNode
  description?: React.ReactNode
  /** Right-hand annotation — a count, a tier badge, "recommended". */
  meta?: React.ReactNode
  selected?: boolean
}

export function CreateSurfaceTile({
  icon,
  concept,
  accent,
  leading,
  title,
  description,
  meta,
  selected = false,
  className,
  ...props
}: CreateSurfaceTileProps) {
  return (
    <button
      type="button"
      aria-pressed={selected}
      className={cn(
        "group/tile flex w-full items-start gap-3 rounded-lg border p-3 text-left transition-colors",
        "max-sm:p-3.5 group-data-[mobile=true]/surface:p-3.5",
        selected
          ? "border-primary/40 bg-primary/[0.08]"
          : "border-hairline bg-foreground/[0.02] hover:border-border hover:bg-foreground/[0.05]",
        className,
      )}
      {...props}
    >
      {leading ?? (
        (icon || concept) && (
          <ConceptIcon concept={concept} icon={icon} accent={accent} variant="chip" size="md" />
        )
      )}
      <span className="min-w-0 flex-1">
        <span className="flex items-center gap-2">
          <span className="truncate text-[13px] font-medium text-foreground">{title}</span>
          {meta != null && <span className="ml-auto shrink-0 text-[11px] text-muted-foreground">{meta}</span>}
        </span>
        {description != null && (
          <span className="mt-0.5 block text-xs leading-relaxed text-muted-foreground">{description}</span>
        )}
      </span>
    </button>
  )
}

/* --------------------------------------------------------------------------
 * Picker — the avatar / icon / brand chooser.
 *
 * Four of these exist today and all four are liked: the agent avatar picker
 * (style grid + quick-pick faces + seed), the crew icon picker (colour row +
 * searchable grid of 345 icons), the project icon picker (the same grid, plus
 * category chips) and the credential brand picker. They are good, and none of
 * this changes what they contain.
 *
 * What it changes is that they were four components with four containers: two
 * nested Radix dialogs, one popover anchored to a field, and one inline panel.
 * A nested dialog over a create dialog is two overlays deep — on a phone that
 * is a sheet on top of a sheet, and the back gesture dismisses the wrong one.
 *
 * So the picker is a PANEL INSIDE the surface, not a second modal over it. The
 * surface swaps its body, the header grows its back arrow (which the shell
 * already has), and the footer's primary becomes "Use this". One overlay, one
 * Esc, one back affordance, and it works unchanged on a phone.
 *
 * The parts are optional because the four differ in what they need, not in how
 * they look:
 *
 *   preview     ✓ ✓ ✓ ✓   the big live sample, always first
 *   inherit       ✓   ✓   "Follow the crew" / "Use the brand default"
 *   palette       ✓ ✓     the colour row
 *   categories        ✓   the chips above the search
 *   search        ✓ ✓ ✓   with a live count
 *   grid        ✓ ✓ ✓ ✓   the choices
 *   extra       ✓         quick-pick faces + the seed field
 * ------------------------------------------------------------------------ */

export interface CreateSurfacePickerOption {
  id: string
  /** Accessible name for the cell — the grid is icons, so this is not optional. */
  label: string
  /** What the cell draws. */
  render: React.ReactNode
}

export interface CreateSurfacePickerProps {
  /** The big live sample of the current choice. */
  preview: React.ReactNode
  /** One line under the preview saying what the choice affects. */
  previewHint?: React.ReactNode
  /**
   * The "don't choose" row. Present on the two pickers that inherit — an agent
   * follows its crew, a credential follows its brand — because "inherit" is a
   * real answer and a grid with no selected cell cannot express it.
   */
  inherit?: {
    label: React.ReactNode
    hint?: React.ReactNode
    preview?: React.ReactNode
    active: boolean
    onSelect: () => void
  }
  palette?: {
    value: string
    options: { id: string; dot: string }[]
    onChange: (id: string) => void
    label?: React.ReactNode
  }
  categories?: {
    value: string | null
    options: string[]
    onChange: (value: string | null) => void
  }
  search?: {
    value: string
    onChange: (value: string) => void
    placeholder?: string
  }
  options: CreateSurfacePickerOption[]
  value: string | null
  onChange: (id: string) => void
  /** Under the grid — the avatar picker's quick-pick row and seed field. */
  extra?: React.ReactNode
  /** Cells per row on a pointer device. A phone always gets 5. */
  columns?: 5 | 6 | 8
  /**
   * Caption each cell with its label.
   *
   * On for the avatar picker, off for the icon ones, and that asymmetry is
   * the content's, not a preference: twenty-five DiceBear styles are told
   * apart by NAME ("Adventurer" vs "Adventurer Neutral" render nearly the
   * same face at 28px), while 345 icons are told apart by SHAPE and a caption
   * under each would be four screens of six-point text.
   */
  captions?: boolean
}

export function CreateSurfacePicker({
  preview,
  previewHint,
  inherit,
  palette,
  categories,
  search,
  options,
  value,
  onChange,
  extra,
  columns = 6,
  captions = false,
}: CreateSurfacePickerProps) {
  return (
    <div className="flex flex-col gap-4">
      {/* ── Preview ─────────────────────────────────────────────────────── */}
      <div className="flex flex-col items-center gap-1.5 rounded-lg border border-hairline bg-foreground/[0.02] py-4">
        {preview}
        {previewHint != null && (
          <span className="px-4 text-center text-[11px] text-muted-foreground-soft">{previewHint}</span>
        )}
      </div>

      {/* ── Inherit ─────────────────────────────────────────────────────── */}
      {inherit && (
        <button
          type="button"
          aria-pressed={inherit.active}
          onClick={inherit.onSelect}
          className={cn(
            "flex items-center gap-3 rounded-lg border p-2.5 text-left transition-colors",
            inherit.active
              ? "border-primary/40 bg-primary/[0.08]"
              : "border-hairline bg-foreground/[0.02] hover:bg-foreground/[0.05]",
          )}
        >
          {inherit.preview}
          <span className="min-w-0 flex-1">
            <span className="block text-[13px] font-medium text-foreground">{inherit.label}</span>
            {inherit.hint != null && (
              <span className="mt-0.5 block text-[11px] text-muted-foreground">{inherit.hint}</span>
            )}
          </span>
          {inherit.active && <Check className="h-4 w-4 shrink-0 text-primary-hover" />}
        </button>
      )}

      {/* ── Colour ──────────────────────────────────────────────────────── */}
      {palette && (
        <CreateSurfaceField label={palette.label ?? "Colour"}>
          <div className="flex flex-wrap gap-2" role="radiogroup" aria-label="Colour">
            {palette.options.map((c) => (
              <button
                key={c.id}
                type="button"
                role="radio"
                aria-checked={palette.value === c.id}
                aria-label={c.id}
                onClick={() => palette.onChange(c.id)}
                style={{ backgroundColor: c.dot }}
                className={cn(
                  "h-7 w-7 rounded-lg transition-all max-sm:h-9 max-sm:w-9 group-data-[mobile=true]/surface:h-9 group-data-[mobile=true]/surface:w-9",
                  palette.value === c.id
                    ? "ring-2 ring-ring ring-offset-2 ring-offset-card"
                    : "opacity-60 hover:opacity-100",
                )}
              />
            ))}
          </div>
        </CreateSurfaceField>
      )}

      {/* ── Categories ──────────────────────────────────────────────────── */}
      {categories && (
        <div className="flex flex-wrap gap-1.5">
          {categories.options.map((c) => {
            const active = categories.value === c
            return (
              <button
                key={c}
                type="button"
                aria-pressed={active}
                onClick={() => categories.onChange(active ? null : c)}
                className={cn(
                  "h-7 rounded-full border px-2.5 text-[11px] capitalize transition-colors max-sm:h-9 group-data-[mobile=true]/surface:h-9",
                  active
                    ? "border-primary/40 bg-primary/15 text-primary-hover"
                    : "border-hairline bg-foreground/[0.03] text-muted-foreground hover:text-foreground",
                )}
              >
                {c}
              </button>
            )
          })}
        </div>
      )}

      {/* ── Search + count ──────────────────────────────────────────────── */}
      {search && (
        <div className="flex items-center gap-2">
          <div className="relative min-w-0 flex-1">
            <Search className="pointer-events-none absolute left-2.5 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-muted-foreground-soft" />
            <input
              value={search.value}
              onChange={(e) => search.onChange(e.target.value)}
              placeholder={search.placeholder ?? "Search…"}
              aria-label={search.placeholder ?? "Search"}
              className="h-8 w-full rounded-md border border-hairline bg-background pl-8 pr-2 text-xs text-foreground outline-none transition-colors focus:border-primary max-sm:h-12 max-sm:text-sm group-data-[mobile=true]/surface:h-12 group-data-[mobile=true]/surface:text-sm"
            />
          </div>
          <span className="shrink-0 text-[11px] tabular-nums text-muted-foreground-soft">
            {options.length}
          </span>
        </div>
      )}

      {/* ── The grid ────────────────────────────────────────────────────── */}
      {options.length === 0 ? (
        <p className="rounded-lg border border-dashed border-border/60 px-3 py-6 text-center text-xs text-muted-foreground">
          Nothing matches that.
        </p>
      ) : (
        <div
          role="radiogroup"
          aria-label="Choices"
          className={cn(
            // A fixed height, not an unbounded grid: 345 icons inside a body
            // that already scrolls gives you two nested scrollports and no way
            // to reach the footer.
            "grid max-h-[280px] gap-1.5 overflow-y-auto overscroll-contain rounded-lg border border-hairline bg-foreground/[0.02] p-2",
            "grid-cols-5",
            columns === 6 && "sm:grid-cols-6",
            columns === 8 && "sm:grid-cols-8",
          )}
        >
          {options.map((o) => {
            const active = o.id === value
            return (
              <button
                key={o.id}
                type="button"
                role="radio"
                aria-checked={active}
                aria-label={o.label}
                title={o.label}
                onClick={() => onChange(o.id)}
                className={cn(
                  // A fixed minimum, not `aspect-square`: in an 800px dialog a
                  // square cell in a five-column grid is 150px tall, and the
                  // 28px glyph inside it floats in the middle of nothing.
                  "flex min-h-[3.25rem] flex-col items-center justify-center gap-1 rounded-md border p-1.5 transition-colors",
                  active
                    ? "border-primary/50 bg-primary/15"
                    : "border-transparent hover:border-border hover:bg-foreground/[0.06]",
                )}
              >
                {o.render}
                {captions && (
                  <span
                    className={cn(
                      "w-full truncate text-center text-[10px] leading-tight",
                      active ? "text-primary-hover" : "text-muted-foreground",
                    )}
                  >
                    {o.label}
                  </span>
                )}
              </button>
            )
          })}
        </div>
      )}

      {extra}
    </div>
  )
}

/* --------------------------------------------------------------------------
 * Dropzone — Import page, Import skill and Import routine each drew a
 * different dashed box. `htmlFor` + `sr-only`, never `hidden`: display:none
 * takes the input out of the tab order, which makes Import mouse-only.
 * ------------------------------------------------------------------------ */

export function CreateSurfaceDropzone({
  id,
  accept,
  fileName,
  placeholder = "Choose a file",
  icon,
  concept,
  accent,
  onFile,
}: {
  id: string
  accept?: string
  fileName?: string | null
  placeholder?: string
  icon?: SurfaceIconComponent
  concept?: string
  accent?: AccentName
  onFile: (file: File) => void
}) {
  return (
    <label
      htmlFor={id}
      className="flex cursor-pointer items-center gap-2.5 rounded-lg border border-dashed border-border p-3.5 transition-colors hover:border-primary/40 hover:bg-primary/[0.04] focus-within:ring-1 focus-within:ring-ring max-sm:p-5 group-data-[mobile=true]/surface:p-5"
    >
      {(icon || concept) && <ConceptIcon concept={concept} icon={icon} accent={accent} size="md" />}
      <span className={cn("truncate text-xs", fileName ? "text-foreground/85" : "text-muted-foreground")}>
        {fileName || placeholder}
      </span>
      <input
        id={id}
        type="file"
        accept={accept}
        className="sr-only"
        onChange={(e) => {
          const f = e.target.files?.[0]
          if (f) onFile(f)
        }}
      />
    </label>
  )
}

/* --------------------------------------------------------------------------
 * Notice — the refusal / warning block. One tone scale, no per-page hex.
 * ------------------------------------------------------------------------ */

export function CreateSurfaceNotice({
  tone = "info",
  icon: Icon,
  children,
}: {
  tone?: "info" | "warn" | "error" | "ok"
  icon?: SurfaceIconComponent
  children: React.ReactNode
}) {
  return (
    <div
      role={tone === "error" ? "alert" : undefined}
      className={cn(
        "flex items-start gap-2 rounded-md border p-2.5 text-xs leading-relaxed",
        tone === "info" && "border-info/30 bg-info/[0.07] text-foreground/85",
        tone === "warn" && "border-warn/30 bg-warn/[0.07] text-foreground/85",
        tone === "error" && "border-destructive/40 bg-destructive/[0.07] text-foreground/90",
        tone === "ok" && "border-success/30 bg-success/[0.07] text-foreground/85",
      )}
    >
      {Icon && (
        <Icon
          className={cn(
            "mt-px h-3.5 w-3.5 shrink-0",
            tone === "info" && "text-info",
            tone === "warn" && "text-warn",
            tone === "error" && "text-destructive",
            tone === "ok" && "text-success",
          )}
        />
      )}
      <span className="min-w-0">{children}</span>
    </div>
  )
}

/* --------------------------------------------------------------------------
 * Refusal — what the server said when it said no.
 *
 * The thing every one of the twelve surfaces is missing. A create surface
 * spends most of its life in the happy path and then, once, has to tell you
 * that the slug is taken or that the devcontainer config failed a security
 * check — and today that arrives as a toast that has already faded by the time
 * you look up, or as nothing at all.
 *
 * It sits BETWEEN the body and the footer, outside the scrollport, for the
 * same reason the footer does: a refusal you have to scroll to find is a
 * refusal you will not find. And it carries the FIELD LIST, because a 400 that
 * names three fields is a worklist, not a sentence — that idea already exists
 * in the product, in the page importer's unresolved-reference block, and this
 * is it generalised.
 * ------------------------------------------------------------------------ */

export interface CreateSurfaceRefusalProps extends Omit<React.ComponentProps<"div">, "children"> {
  /** One sentence, in the user's terms. Null hides the whole band. */
  message: React.ReactNode | null
  /**
   * Not every band outside the scrollport is a refusal.
   *
   * Three surfaces needed a second and third band here — a dry run reports
   * PASS or fail, a partial save is a warning, and "Name is still empty" is a
   * blocker that must be visible on a phone (where `hint` is not). All three
   * hand-rolled this component's geometry rather than get a tone.
   */
  tone?: "error" | "warn" | "ok" | "info"
  /**
   * Field-level detail from a 400/422, rendered as rows you can act on.
   *
   * `detail` is the third rank — "(used by 2 panels)" — which the page importer
   * had to flatten into `reason` before this existed. A refusal that names a
   * location is going to be common.
   */
  fields?: { field: string; reason: React.ReactNode; detail?: React.ReactNode }[]
  /** Present only when retrying can plausibly work — a 500, not a 400. */
  onRetry?: () => void
  /** Dismiss. Absent means the refusal stays until the input changes. */
  onDismiss?: () => void
}

const BAND_TONE = {
  error: { edge: "border-destructive/30 bg-destructive/[0.07]", glyph: "text-destructive" },
  warn: { edge: "border-warn/30 bg-warn/[0.07]", glyph: "text-warn" },
  ok: { edge: "border-success/30 bg-success/[0.07]", glyph: "text-success" },
  info: { edge: "border-info/30 bg-info/[0.07]", glyph: "text-info" },
} as const

export function CreateSurfaceRefusal({
  message,
  fields,
  onRetry,
  onDismiss,
  tone = "error",
  className,
  ...props
}: CreateSurfaceRefusalProps) {
  if (message == null) return null

  const t = BAND_TONE[tone]
  return (
    <div
      // Only a failure interrupts. A pass or a hint is polite.
      role={tone === "error" ? "alert" : "status"}
      aria-live={tone === "error" ? "assertive" : "polite"}
      className={cn("shrink-0 border-t px-4 py-2.5 sm:px-5", t.edge, className)}
      {...props}
    >
      <div className="flex items-start gap-2">
        <TriangleAlert className={cn("mt-px h-3.5 w-3.5 shrink-0", t.glyph)} />
        <div className="min-w-0 flex-1">
          <p className="text-xs leading-relaxed text-foreground/90">{message}</p>

          {fields != null && fields.length > 0 && (
            <ul className="mt-1.5 flex flex-col gap-0.5">
              {fields.map((f) => (
                <li key={f.field} className="text-[11px] leading-relaxed">
                  <span className={cn("font-mono", t.glyph)}>{f.field}</span>
                  <span className="text-muted-foreground"> — {f.reason}</span>
                  {f.detail != null && (
                    <span className="text-muted-foreground-soft"> {f.detail}</span>
                  )}
                </li>
              ))}
            </ul>
          )}
        </div>

        {onRetry && (
          <Button variant="ghost" size="sm" onClick={onRetry} className="h-7 shrink-0 text-xs">
            Try again
          </Button>
        )}
        {onDismiss && (
          <Button
            variant="ghost"
            size="icon-xs"
            onClick={onDismiss}
            aria-label="Dismiss"
            className="shrink-0 text-muted-foreground hover:text-foreground"
          >
            <X className="h-3.5 w-3.5" />
          </Button>
        )}
      </div>
    </div>
  )
}

/* --------------------------------------------------------------------------
 * Loading — for the surfaces that cannot render until they have fetched.
 *
 * Half the doors need crews, agents, projects or labels before they can draw a
 * single control. Without this they each invent a spinner, and the surface
 * jumps when the data lands because the placeholder was not the shape of what
 * replaced it.
 * ------------------------------------------------------------------------ */

export function CreateSurfaceLoading({ rows = 3 }: { rows?: number }) {
  return (
    <div className="flex flex-col gap-4" aria-busy="true" aria-live="polite">
      <span className="sr-only">Loading</span>
      <div className="h-6 w-2/5 animate-pulse rounded bg-foreground/[0.06]" />
      {Array.from({ length: rows }, (_, i) => (
        <div key={i} className="flex flex-col gap-1.5">
          <div className="h-2.5 w-20 animate-pulse rounded bg-foreground/[0.05]" />
          <div className="h-8 w-full animate-pulse rounded-md bg-foreground/[0.04]" />
        </div>
      ))}
    </div>
  )
}

/* --------------------------------------------------------------------------
 * Footer — hint, then Cancel, then at most one secondary, then THE primary.
 * ------------------------------------------------------------------------ */

export interface CreateSurfaceFooterProps extends Omit<React.ComponentProps<"div">, "children"> {
  /**
   * Left-hand hint. Defaults to the keyboard contract, because it is true on
   * every surface now and was documented on almost none of them. Hidden on a
   * phone, which has no ⌘ and needs the width for the buttons.
   */
  hint?: React.ReactNode
  /** A "Create more" switch or an attachment control — never a third button. */
  aside?: React.ReactNode
  onCancel: () => void
  cancelLabel?: string
  /** At most one. `Back`, `Skip to defaults`, `Preview`. */
  secondary?: React.ReactNode
  /**
   * Optional. A Pick surface whose action IS the tile has no primary — and
   * before this was optional it rendered no footer at all, and therefore no
   * Cancel, contradicting this file's own "Cancel is always present" rule.
   */
  primaryLabel?: React.ReactNode
  onPrimary?: () => void
  primaryDisabled?: boolean
  /**
   * Route the footer's Cancel through the discard guard.
   *
   * Off by default because half the surfaces overload Cancel as "back out of
   * this panel", and prompting about unsaved work for closing a colour picker
   * is worse than not prompting. On for a Cancel that genuinely means close —
   * three migrations wrote a wrapper component to get this, because
   * `useCreateSurfaceClose` cannot be read by the component that renders
   * `CreateSurface`.
   */
  guardCancel?: boolean
  /** Far left, before the hint. Survives to a phone, unlike `hint`. */
  lead?: React.ReactNode
  /** Focus the primary — a wizard landing on its Review step wants this. */
  primaryRef?: React.Ref<HTMLButtonElement>
  /** Renders the spinner and locks both the primary and Cancel. */
  busy?: boolean
  primaryIcon?: SurfaceIconComponent
}

export function CreateSurfaceFooter({
  hint = (
    <>
      <kbd className="font-mono">⌘↵</kbd> to confirm · <kbd className="font-mono">Esc</kbd> to cancel
    </>
  ),
  aside,
  onCancel,
  cancelLabel = "Cancel",
  secondary,
  primaryLabel,
  onPrimary,
  primaryDisabled = false,
  busy = false,
  primaryIcon: PrimaryIcon,
  guardCancel = false,
  lead,
  primaryRef,
  className,
  ...props
}: CreateSurfaceFooterProps) {
  const guard = React.useContext(CloseGuard)
  return (
    <div
      {...props}
      className={cn(
        "shrink-0 flex items-center gap-2 border-t border-hairline px-4 py-2.5 sm:px-5 sm:py-3",
        // Past the home indicator, not under it.
        "max-sm:pb-[max(0.75rem,env(safe-area-inset-bottom))] group-data-[mobile=true]/surface:pb-3.5",
        className,
      )}
    >
      {lead}
      {hint != null && (
        <span className="text-[11px] text-muted-foreground-soft max-sm:hidden group-data-[mobile=true]/surface:hidden">
          {hint}
        </span>
      )}
      <div className="flex-1 max-sm:hidden group-data-[mobile=true]/surface:hidden" />
      {aside}
      <Button
        variant="ghost"
        size="sm"
        onClick={() => (guardCancel ? guard(onCancel) : onCancel())}
        disabled={busy}
        className="h-8 text-xs max-sm:h-12 max-sm:flex-1 max-sm:text-sm group-data-[mobile=true]/surface:h-12 group-data-[mobile=true]/surface:flex-1 group-data-[mobile=true]/surface:text-sm"
      >
        {cancelLabel}
      </Button>
      {secondary}
      {primaryLabel != null && onPrimary != null && (
      <Button
        ref={primaryRef}
        size="sm"
        onClick={onPrimary}
        disabled={primaryDisabled || busy}
        className="h-8 gap-1.5 text-xs max-sm:h-12 max-sm:flex-[2] max-sm:text-sm group-data-[mobile=true]/surface:h-12 group-data-[mobile=true]/surface:flex-[2] group-data-[mobile=true]/surface:text-sm"
      >
        {busy ? <Spinner className="h-3 w-3" /> : PrimaryIcon ? <PrimaryIcon className="h-3 w-3" /> : null}
        {primaryLabel}
      </Button>
      )}
    </div>
  )
}

/** The one shape a footer's `secondary` slot is allowed to take. */
export function CreateSurfaceSecondaryAction({
  icon: Icon,
  className,
  children,
  ...props
}: React.ComponentProps<typeof Button> & { icon?: SurfaceIconComponent }) {
  return (
    <Button
      variant="outline"
      size="sm"
      className={cn(
        "h-8 gap-1.5 text-xs max-sm:h-12 max-sm:flex-1 max-sm:text-sm group-data-[mobile=true]/surface:h-12 group-data-[mobile=true]/surface:flex-1 group-data-[mobile=true]/surface:text-sm",
        className,
      )}
      {...props}
    >
      {Icon && <Icon className="h-3 w-3" />}
      {children}
    </Button>
  )
}
