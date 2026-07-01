"use client"

import * as React from "react"
import type { LucideIcon } from "lucide-react"

import { cn } from "@/lib/utils"
import { Button } from "@/components/ui/button"

/**
 * SubBar — the canonical page sub-bar (the strip directly under the global
 * top bar), unified across every page for the 1.0 cleanup.
 *
 * Layout = "Option 1 / two rows" (see .claude/context/wireframes/subbar-*.html):
 *
 *   Row 1  [icon] Title · <live description>            …flex…   [actions]
 *   Row 2  [ tab  tab  tab ]                             …flex…   [tools]
 *
 * Rules that keep it consistent:
 *  · Identity (icon + title + description) is MANDATORY — no bar is ever empty
 *    and every bar says what you're looking at.
 *  · Row 2 renders ONLY when the page has `tabs` or `tools`; single-view pages
 *    (e.g. Crews) stay a single row.
 *  · Tabs are token-based blue underline (`text-primary-hover`), never
 *    hardcoded `blue-400`.
 *  · Actions use the shared <Button> via the SubBar* helpers below: exactly one
 *    `soft` primary per page, everything else `ghost`. Zero raw <button>s, zero
 *    hardcoded hex.
 *
 * Responsive: the identity description hides on narrow viewports, the tab row
 * scrolls horizontally, and action labels can be collapsed by the caller with
 * `hidden sm:inline` spans. Same DOM at every breakpoint — no per-page mobile
 * layout.
 */

export interface SubBarTab<T extends string = string> {
  id: T
  label: string
  icon?: LucideIcon
  /** Small count/badge rendered after the label. */
  badge?: React.ReactNode
  /** Locked tabs render a "soon" affordance and never activate. */
  locked?: boolean
  /** Text shown next to the lock (defaults to "soon"). */
  lockedLabel?: string
  disabled?: boolean
}

export interface SubBarProps<T extends string = string> {
  icon?: LucideIcon
  title: string
  /** Live, "·"-separated context (counts / status). Muted, hidden on mobile. */
  description?: React.ReactNode
  /** Extra inline nodes after the description (status badges, Live dot, …). */
  meta?: React.ReactNode
  /** Leading control before the identity (mobile explorer toggle, collapse…). */
  leading?: React.ReactNode
  /** Row-1 right side. Compose with SubBarPrimary/Secondary/IconButton. */
  actions?: React.ReactNode

  /** Row-2 tabs. Presence (or `tools`) is what makes the second row render. */
  tabs?: SubBarTab<T>[]
  activeTab?: T
  onTabChange?: (id: T) => void
  /** Row-2 right side — Filter / Sort / view controls. */
  tools?: React.ReactNode

  ariaLabel?: string
  className?: string
}

const ROW = "shrink-0 flex items-center h-9 bg-card border-b border-white/[0.08] px-2 sm:px-3"
const SCROLL_X = "overflow-x-auto [&::-webkit-scrollbar]:hidden [-ms-overflow-style:none] [scrollbar-width:none]"

export function SubBar<T extends string = string>({
  icon: Icon,
  title,
  description,
  meta,
  leading,
  actions,
  tabs,
  activeTab,
  onTabChange,
  tools,
  ariaLabel,
  className,
}: SubBarProps<T>) {
  const hasRow2 = (tabs != null && tabs.length > 0) || tools != null

  return (
    <div className={cn("shrink-0", className)}>
      {/* ---- Row 1: identity + actions ---- */}
      <div className={cn(ROW, "gap-2", SCROLL_X)} aria-label={ariaLabel}>
        {leading && <div className="flex items-center shrink-0">{leading}</div>}

        <div className="flex items-center gap-2 min-w-0">
          {Icon && <Icon className="h-3.5 w-3.5 text-foreground/70 shrink-0" />}
          <h1 className="text-body font-medium text-foreground whitespace-nowrap shrink-0">{title}</h1>
          {description != null && (
            <>
              <span className="text-muted-foreground-soft shrink-0 hidden sm:inline">·</span>
              <span className="text-xs text-muted-foreground truncate hidden sm:block min-w-0">
                {description}
              </span>
            </>
          )}
          {meta}
        </div>

        <div className="flex-1" />

        {actions && <div className="flex items-center gap-1.5 shrink-0">{actions}</div>}
      </div>

      {/* ---- Row 2: tabs + tools (only when present) ---- */}
      {hasRow2 && (
        <div className={cn(ROW, "gap-0", SCROLL_X)}>
          {tabs && tabs.length > 0 && (
            <div role="tablist" aria-label={ariaLabel ? `${ariaLabel} views` : "Views"} className="flex items-center h-full">
              {tabs.map((tab) => (
                <SubBarTabButton
                  key={tab.id}
                  tab={tab}
                  active={tab.id === activeTab}
                  onSelect={() => !tab.locked && !tab.disabled && onTabChange?.(tab.id)}
                />
              ))}
            </div>
          )}
          <div className="flex-1" />
          {tools && <div className="flex items-center gap-1.5 shrink-0">{tools}</div>}
        </div>
      )}
    </div>
  )
}

function SubBarTabButton<T extends string>({
  tab,
  active,
  onSelect,
}: {
  tab: SubBarTab<T>
  active: boolean
  onSelect: () => void
}) {
  const Icon = tab.icon
  return (
    <button
      type="button"
      role="tab"
      aria-selected={active}
      aria-disabled={tab.locked || tab.disabled || undefined}
      onClick={onSelect}
      title={tab.locked ? `${tab.label} — coming soon` : undefined}
      className={cn(
        "flex items-center gap-1.5 px-2.5 h-full text-xs font-medium border-b-2 transition-colors duration-100 relative top-px whitespace-nowrap shrink-0",
        tab.locked
          ? "border-transparent text-muted-foreground/40 cursor-not-allowed"
          : active
            ? "border-primary-hover text-primary-hover"
            : "border-transparent text-muted-foreground hover:text-foreground/80",
      )}
    >
      {Icon && <Icon className="h-3 w-3 opacity-75" />}
      {tab.label}
      {tab.badge != null && <span className="text-[11px] text-muted-foreground/70">{tab.badge}</span>}
      {tab.locked && (
        <span className="text-[9px] uppercase tracking-wider text-amber-400/70 font-mono">
          {tab.lockedLabel ?? "soon"}
        </span>
      )}
    </button>
  )
}

/* --------------------------------------------------------------------------
 * Action helpers — the ONLY sanctioned way to render sub-bar buttons.
 * They bake in the h-7 toolbar sizing + Style-B variants so every page's
 * actions look identical.
 * ------------------------------------------------------------------------ */

type ActionProps = React.ComponentProps<typeof Button> & { icon?: LucideIcon }

/** Primary CTA — soft/tinted. At most one per page. */
export function SubBarPrimary({ icon: Icon, className, children, ...props }: ActionProps) {
  return (
    <Button variant="soft" size="sm" className={cn("h-7 gap-1.5 text-xs", className)} {...props}>
      {Icon && <Icon className="h-3 w-3" />}
      {children}
    </Button>
  )
}

/** Secondary / neutral action (Import, secondary create). */
export function SubBarSecondary({ icon: Icon, className, children, ...props }: ActionProps) {
  return (
    <Button variant="ghost" size="sm" className={cn("h-7 gap-1.5 text-xs", className)} {...props}>
      {Icon && <Icon className="h-3 w-3" />}
      {children}
    </Button>
  )
}

/** Utility icon button (settings gear, panel toggles…). */
export function SubBarIconButton({ icon: Icon, className, children, ...props }: ActionProps) {
  return (
    <Button variant="ghost" size="icon-sm" className={cn("h-7 w-7", className)} {...props}>
      {Icon ? <Icon className="h-3.5 w-3.5" /> : children}
    </Button>
  )
}
