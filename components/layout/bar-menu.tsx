"use client"

import * as React from "react"
import { AnimatePresence, motion } from "motion/react"
import { Slot } from "radix-ui"

import { Button } from "@/components/ui/button"
import { cn } from "@/lib/utils"

// =============================================================================
// The top bar's popover kit.
//
// The bar carried three of these — Activity, Inbox and an informational
// Notifications bell — built three times, by three different means, at three
// different sizes: the inbox as a hand-rolled motion panel (380px, .type-*
// roles, a sectioned list with counts and a two-action footer), the other two
// as Radix dropdowns at 400px and 360px with their own text-[9px] /
// text-[10px] / text-[11px] ladders, their own badge shapes, and no footer
// contract between them. Sitting a centimetre apart in the same strip, that
// read as three products.
//
// The third one is gone — it rendered a table nothing in the product ever
// wrote to (see app-toolbar.tsx) — so the bar is Activity and Inbox, split by
// who is waiting: machines, or a human.
//
// The inbox is the one of the three that was designed rather than grown, so it
// is the source: every class in here is lifted from it unchanged, which makes
// adopting the kit a visual no-op for the inbox and a correction for the other
// two.
//
// What the kit fixes on the way past:
//   · Escape closes. Radix gave activity + notifications that for free and the
//     inbox never had it; now all three do, from one place.
//   · One badge shape and one 99+ cap.
//   · One typographic ladder — .type-row for what the row IS, .type-meta for
//     what it is ABOUT. No component in here hardcodes a pixel size.
//
// Sizes and spacing live here and nowhere else. Changing the popover density
// is this file, not three.
// =============================================================================

/** Badge tone. Severity, not decoration — see BADGE_TONE. */
export type BarMenuBadgeTone = "urgent" | "active" | "live"

const BADGE_TONE: Record<BarMenuBadgeTone, string> = {
  // Someone is parked waiting on a human answer.
  urgent: "bg-warn text-background",
  // Work is in flight right now.
  active: "bg-primary text-primary-foreground",
  // Agent activity — the badge's historical emerald meaning.
  live: "bg-success text-white",
}

export interface BarMenuProps {
  icon: React.ComponentType<{ className?: string }>
  /** Full sentence for screen readers: "Inbox: 3 awaiting a decision, 6 unread". */
  ariaLabel: string
  badge?: { count: number; tone: BarMenuBadgeTone }
  open: boolean
  onOpenChange: (open: boolean) => void
  /**
   * Prefix for the trigger/badge/panel test ids: `<testId>-trigger`,
   * `-badge`, `-popover`, `-backdrop`.
   */
  testId: string
  children: React.ReactNode
}

/**
 * Trigger + panel. Controlled, because Activity ticks its elapsed times only
 * while the panel is open.
 */
export function BarMenu({ icon: Icon, ariaLabel, badge, open, onOpenChange, testId, children }: BarMenuProps) {
  const triggerRef = React.useRef<HTMLButtonElement>(null)
  const count = badge?.count ?? 0

  React.useEffect(() => {
    if (!open) return
    function onKeyDown(e: KeyboardEvent) {
      if (e.key === "Escape") {
        e.stopPropagation()
        onOpenChange(false)
        // Send focus back where it came from — a panel closed by Escape must
        // not drop the keyboard user at the top of the document.
        triggerRef.current?.focus()
      }
    }
    document.addEventListener("keydown", onKeyDown)
    return () => document.removeEventListener("keydown", onKeyDown)
  }, [open, onOpenChange])

  return (
    <div className="relative">
      <Button
        ref={triggerRef}
        variant="ghost"
        size="icon-sm"
        className="relative"
        aria-label={ariaLabel}
        aria-expanded={open}
        aria-haspopup="dialog"
        data-testid={`${testId}-trigger`}
        onClick={() => onOpenChange(!open)}
      >
        <Icon className="h-4 w-4" />
        <AnimatePresence>
          {count > 0 && badge && (
            <motion.span
              initial={{ scale: 0 }}
              animate={{ scale: 1 }}
              exit={{ scale: 0 }}
              data-testid={`${testId}-badge`}
              className={cn(
                "absolute -right-0.5 -top-0.5 flex h-4 min-w-[16px] items-center justify-center rounded-full px-1 text-[9px] font-semibold",
                BADGE_TONE[badge.tone],
              )}
            >
              {count > 99 ? "99+" : count}
            </motion.span>
          )}
        </AnimatePresence>
      </Button>

      <AnimatePresence>
        {open && (
          <>
            <div
              className="fixed inset-0 z-40"
              data-testid={`${testId}-backdrop`}
              onClick={() => onOpenChange(false)}
            />
            <motion.div
              role="dialog"
              aria-label={ariaLabel}
              initial={{ opacity: 0, scale: 0.96, y: -4 }}
              animate={{ opacity: 1, scale: 1, y: 0, transition: { duration: 0.12 } }}
              exit={{ opacity: 0, scale: 0.96, y: -4, transition: { duration: 0.1 } }}
              className="absolute right-0 top-9 z-50 w-[380px] overflow-hidden rounded-lg border border-white/[0.1] bg-card shadow-xl"
              data-testid={`${testId}-popover`}
            >
              {children}
            </motion.div>
          </>
        )}
      </AnimatePresence>
    </div>
  )
}

export interface BarMenuHeaderProps {
  /** What this panel is. Sentence case — it is a name, not a section label. */
  title: string
  /** Optional Pill beside the title: a deadline, a live count, a warning. */
  pill?: React.ReactNode
  /** Right-aligned tally: "10 awaiting you · 6 unread". */
  meta?: React.ReactNode
}

export function BarMenuHeader({ title, pill, meta }: BarMenuHeaderProps) {
  return (
    <div className="flex items-center gap-2 border-b border-white/[0.06] px-3 py-2">
      <span className="type-row font-medium">{title}</span>
      {pill}
      {meta && <span className="type-meta ml-auto text-muted-foreground">{meta}</span>}
    </div>
  )
}

/**
 * The scroll region. Capped so the panel can never grow past the viewport —
 * overflow exits through the footer link, which is what the footer is for.
 */
export function BarMenuBody({ children, className }: { children: React.ReactNode; className?: string }) {
  return <div className={cn("max-h-[420px] overflow-y-auto", className)}>{children}</div>
}

export interface BarMenuSectionProps {
  /** Rendered uppercase — this is a group label, not a heading. */
  label: string
  count?: number
  /** "warn" for the group that is blocking someone. */
  tone?: "warn"
  /** Muted note under the last row: "+6 more in the inbox". */
  overflow?: React.ReactNode
  children: React.ReactNode
}

export function BarMenuSection({ label, count, tone, overflow, children }: BarMenuSectionProps) {
  return (
    <div data-testid={`bar-menu-section-${label.toLowerCase().replace(/\s+/g, "-")}`}>
      <div className="flex items-center gap-2 border-b border-white/[0.04] bg-surface-subtle/60 px-3 py-1">
        <span className={cn("type-meta uppercase tracking-wider", tone === "warn" ? "text-warn" : "text-foreground/40")}>
          {label}
        </span>
        {count != null && (
          <span className="type-meta ml-auto tabular-nums text-muted-foreground-soft">{count}</span>
        )}
      </div>
      <ul>{children}</ul>
      {overflow && <p className="type-meta px-3 py-1.5 text-muted-foreground-soft">{overflow}</p>}
    </div>
  )
}

export interface BarMenuRowProps {
  /** Avatar, status dot or glyph — whatever identifies the subject. */
  leading?: React.ReactNode
  /** What the row IS. One line, truncated. */
  title: React.ReactNode
  /** What the row is ABOUT: subject · category · qualifier. */
  meta?: React.ReactNode
  /** Right column — a time, a deadline, a cost. */
  trailing?: React.ReactNode
  /**
   * Buttons and links under the row. Their presence turns the row body from a
   * <button> into a clickable div: a button inside a button is invalid HTML and
   * swallows the inner click.
   */
  actions?: React.ReactNode
  onClick?: () => void
  testId?: string
}

const ROW_BASE = "flex w-full items-start gap-2.5 px-3 py-2 text-left transition-colors"

export function BarMenuRow({ leading, title, meta, trailing, actions, onClick, testId }: BarMenuRowProps) {
  const body = (
    <>
      {leading && <span className="shrink-0">{leading}</span>}
      <span className="min-w-0 flex-1">
        <span className="type-row block truncate text-foreground">{title}</span>
        {meta && (
          <span className="type-meta flex min-w-0 items-center gap-1.5 text-muted-foreground-soft">{meta}</span>
        )}
      </span>
      {trailing && <span className="shrink-0 text-right">{trailing}</span>}
    </>
  )

  // A row is a <button> when the whole row is the action. It degrades to a div
  // when it carries its own action buttons (a button inside a button is
  // invalid markup and swallows the inner click), and to a plain div when
  // there is nothing to click — a focusable control that does nothing is a
  // keyboard dead end, which is what a read notification used to be.
  return (
    <li>
      {!onClick ? (
        <div data-testid={testId} className={ROW_BASE}>
          {body}
        </div>
      ) : actions ? (
        <div
          data-testid={testId}
          role="button"
          tabIndex={0}
          onClick={onClick}
          onKeyDown={(e) => {
            if (e.key === "Enter" || e.key === " ") {
              e.preventDefault()
              onClick()
            }
          }}
          className={cn(ROW_BASE, "cursor-pointer hover:bg-white/[0.04]")}
        >
          {body}
        </div>
      ) : (
        <button
          type="button"
          data-testid={testId}
          onClick={onClick}
          className={cn(ROW_BASE, "hover:bg-white/[0.04]")}
        >
          {body}
        </button>
      )}
      {/* Indented to the title column (px-3 + a 24px leading slot + gap-2.5)
          so actions hang under the words they act on, not under the avatar. */}
      {actions && <div className="flex items-center gap-2 pb-2 pl-[46px] pr-3">{actions}</div>}
    </li>
  )
}

/**
 * A row's secondary action: "Open trace ↗", "Review →", "Cancel".
 *
 * `asChild` hands the styling to whatever element is passed in — a next/link
 * <Link>, usually. An anchor inside a button is invalid markup and swallows
 * the navigation, so the kit takes the child rather than wrapping it.
 */
export function BarMenuRowAction({
  onClick, danger, disabled, asChild, children, ariaLabel, title,
}: {
  onClick?: () => void
  danger?: boolean
  disabled?: boolean
  asChild?: boolean
  children: React.ReactNode
  ariaLabel?: string
  title?: string
}) {
  const className = cn(
    "type-meta inline-flex items-center gap-1 rounded-md border px-2 py-0.5 transition-colors",
    danger
      ? "border-transparent text-muted-foreground hover:bg-destructive/10 hover:text-destructive"
      : "border-white/[0.08] bg-white/[0.03] text-foreground/85 hover:bg-white/[0.06]",
    disabled && "opacity-50",
  )
  const Comp = asChild ? Slot.Root : "button"
  return (
    <Comp
      {...(asChild ? {} : { type: "button" as const, disabled })}
      onClick={onClick}
      className={className}
      aria-label={ariaLabel}
      title={title}
    >
      {children}
    </Comp>
  )
}

export function BarMenuEmpty({
  icon: Icon,
  message,
}: {
  icon: React.ComponentType<{ className?: string }>
  message: string
}) {
  return (
    <div className="flex flex-col items-center gap-2 p-6 text-center">
      <Icon className="h-6 w-6 text-muted-foreground/30" />
      <span className="type-row text-muted-foreground">{message}</span>
    </div>
  )
}

/** The action strip: secondary on the left, the way out on the right. */
export function BarMenuFooter({ children }: { children: React.ReactNode }) {
  return (
    <div className="flex items-center gap-2 border-t border-white/[0.06] px-2 py-1.5" data-testid="bar-menu-footer">
      {children}
    </div>
  )
}

export function BarMenuFooterAction({
  onClick, disabled, children,
}: {
  onClick: () => void
  disabled?: boolean
  children: React.ReactNode
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      disabled={disabled}
      className="type-meta rounded px-2 py-1 text-muted-foreground hover:text-foreground disabled:opacity-50"
    >
      {children}
    </button>
  )
}

/**
 * The way out of the panel, right-aligned. `asChild` for a next/link <Link>;
 * plain for a button that navigates through the router.
 */
export function BarMenuFooterLink({
  onClick, asChild, children,
}: {
  onClick?: () => void
  asChild?: boolean
  children: React.ReactNode
}) {
  const Comp = asChild ? Slot.Root : "button"
  return (
    <Comp
      {...(asChild ? {} : { type: "button" as const })}
      onClick={onClick}
      className="type-meta ml-auto rounded px-2 py-1 text-primary hover:underline"
    >
      {children}
    </Comp>
  )
}
