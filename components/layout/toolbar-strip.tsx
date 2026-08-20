"use client"

import * as React from "react"
import type { LucideIcon } from "lucide-react"

import { cn } from "@/lib/utils"

export interface ToolbarTab<T extends string = string> {
  id: T
  label: string
  icon?: LucideIcon
  /** Optional badge rendered next to the label (e.g., count). */
  badge?: React.ReactNode
  disabled?: boolean
  /**
   * Accessible name for the tab, when the label alone is not the whole story.
   *
   * Defaults to `label`. It exists because the button's `aria-label` overrides
   * everything inside it, badge included: a tab whose badge carries meaning —
   * Pages draws each tab's worst freshness state there — would announce only
   * its label, and a reader who cannot see the glyph would be told nothing
   * about it. Set this to say both.
   */
  ariaLabel?: string
  /**
   * DOM id for the tab button, and the id of the panel it controls.
   *
   * A tab and its panel are two halves of one control, and `role="tab"` alone
   * says only that a button is a tab: it does not say what it revealed. A
   * reader who activates it is left to find the new content by hunting. The
   * pair is what makes the relationship announceable — the button points at
   * the panel with `aria-controls`, and the panel points back at the button
   * with `aria-labelledby`, which is why the button needs an id of its own.
   *
   * Both are optional because a strip whose panels are not separately
   * addressable is better off saying nothing than pointing at an id that does
   * not resolve. Pass both or neither; `domId` alone is harmless, and
   * `ariaControls` alone is a dangling reference.
   *
   * Ids must be unique in the DOCUMENT, not in the strip — see `page-tabs.tsx`
   * for the `useId` scoping that makes two of the same view on one page safe.
   */
  domId?: string
  ariaControls?: string
}

interface ToolbarStripProps<T extends string = string> extends React.ComponentProps<"div"> {
  tabs?: ToolbarTab<T>[]
  activeTab?: T
  onTabChange?: (id: T) => void
  /** Leading slot rendered before tabs (e.g., icon dropdown, search). */
  leading?: React.ReactNode
  /** Trailing slot rendered after the flex spacer (e.g., action buttons). */
  actions?: React.ReactNode
  /** Compact mode tightens vertical padding and hides tab labels on narrow screens. */
  compact?: boolean
  /** aria-label for the toolbar landmark. */
  ariaLabel?: string
}

/**
 * ToolbarStrip — canonical icon+label toolbar matching the orchestration reference.
 * Pattern extracted from `components/features/orchestration/issues-toolbar-strip.tsx`
 * and `orchestration-layout.tsx`. Use for in-page tab switching (board/list, overview/settings/logs).
 *
 * Layout: full-width strip with bottom hairline border, leading slot, tab group,
 * flex spacer, trailing actions slot. Active tab gets `bg-accent text-foreground`;
 * inactive tabs get `text-muted-foreground`.
 *
 * Keyboard: the WAI-ARIA tabs pattern, as in
 * `components/features/admin/keeper-queue-panel.tsx` — ArrowLeft/ArrowRight
 * cycle, Home/End jump to the ends, and a roving `tabIndex` keeps the whole
 * group to one stop in the document's tab order.
 */
export function ToolbarStrip<T extends string = string>({
  tabs,
  activeTab,
  onTabChange,
  leading,
  actions,
  compact = false,
  ariaLabel,
  className,
  ...props
}: ToolbarStripProps<T>) {
  // Focus is moved through refs rather than by querying the DOM for the tab
  // that should get it: a tab id is caller data, and building a selector out
  // of it would need escaping to survive an id with a quote or a bracket in
  // it. The map is keyed by the same ids the caller passed.
  const buttons = React.useRef(new Map<T, HTMLButtonElement>())

  // Roving tabIndex: exactly one tab is in the document's tab order, so Tab
  // steps over the group instead of once per tab, and the arrow keys move
  // within it. Disabled tabs are not focusable, so the roving stop falls to
  // the first enabled one when the active tab is disabled or unset — a group
  // where nothing is reachable by Tab would be worse than no roving at all.
  const rovingId =
    tabs?.find((t) => t.id === activeTab && !t.disabled)?.id ?? tabs?.find((t) => !t.disabled)?.id

  /**
   * ArrowLeft/Right cycle, Home/End jump — the WAI-ARIA tabs pattern.
   *
   * Activation follows focus, the pattern's default and what
   * `keeper-queue-panel.tsx` already does here: it is the right choice when
   * switching is cheap, and the one caller with panels keeps all of them
   * mounted. A strip that has to fetch on select would want the manual variant
   * (move focus, activate on Enter/Space) instead.
   *
   * Only the tablist listens, so a search box or a button in `leading` /
   * `actions` keeps its own arrow keys.
   */
  const onKeyDown = (event: React.KeyboardEvent<HTMLDivElement>) => {
    if (!["ArrowLeft", "ArrowRight", "Home", "End"].includes(event.key)) return
    const order = tabs?.filter((t) => !t.disabled) ?? []
    if (order.length === 0) return
    event.preventDefault()

    const found = order.findIndex((t) => t.id === activeTab)
    const current = found === -1 ? 0 : found
    let next = current
    if (event.key === "ArrowLeft") next = (current - 1 + order.length) % order.length
    if (event.key === "ArrowRight") next = (current + 1) % order.length
    if (event.key === "Home") next = 0
    if (event.key === "End") next = order.length - 1

    const target = order[next]
    if (target.id === activeTab) return
    onTabChange?.(target.id)
    // Move DOM focus with the selection so the visual and the a11y state stay
    // in sync. The button is already mounted — React keys it by tab id — so
    // this needs no wait for the re-render.
    buttons.current.get(target.id)?.focus()
  }

  return (
    <div
      role="toolbar"
      aria-label={ariaLabel}
      className={cn(
        "flex items-center gap-2 px-4 border-b border-border shrink-0 bg-card",
        compact ? "py-1.5" : "py-2",
        className
      )}
      {...props}
    >
      {leading && <div className="flex items-center gap-2 shrink-0">{leading}</div>}

      {tabs && tabs.length > 0 && (
        <div
          className="flex gap-0.5 bg-muted/40 rounded-md p-0.5 shrink-0"
          role="tablist"
          aria-label={ariaLabel ? `${ariaLabel} tabs` : undefined}
          onKeyDown={onKeyDown}
        >
          {tabs.map((tab) => {
            const Icon = tab.icon
            const isActive = tab.id === activeTab
            return (
              <button
                key={tab.id}
                type="button"
                role="tab"
                id={tab.domId}
                aria-selected={isActive}
                aria-controls={tab.ariaControls}
                aria-label={tab.ariaLabel ?? tab.label}
                tabIndex={tab.id === rovingId ? 0 : -1}
                ref={(node) => {
                  if (node) buttons.current.set(tab.id, node)
                  else buttons.current.delete(tab.id)
                }}
                disabled={tab.disabled}
                onClick={() => !tab.disabled && onTabChange?.(tab.id)}
                className={cn(
                  "inline-flex items-center gap-1.5 px-2.5 py-1 rounded text-label font-medium transition-colors",
                  "disabled:opacity-40 disabled:cursor-not-allowed",
                  isActive
                    ? "bg-accent text-foreground shadow-sm"
                    : "text-muted-foreground hover:text-foreground"
                )}
              >
                {Icon && <Icon className="h-3.5 w-3.5" />}
                <span className={cn(compact && "hidden sm:inline")}>{tab.label}</span>
                {tab.badge != null && (
                  <span className="text-micro text-muted-foreground/80">{tab.badge}</span>
                )}
              </button>
            )
          })}
        </div>
      )}

      <div className="flex-1" />

      {actions && <div className="flex items-center gap-2 shrink-0">{actions}</div>}
    </div>
  )
}
