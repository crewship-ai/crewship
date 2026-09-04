"use client"

import * as React from "react"
import { Check, ChevronDown, Filter as FilterIcon, PanelLeftClose, PanelLeftOpen, Search, X } from "lucide-react"
import type { LucideIcon } from "lucide-react"
import { AnimatePresence, motion } from "motion/react"

import { cn } from "@/lib/utils"
import { ListRow } from "@/components/ui/list-row"

/**
 * sidebar-kit — the canonical building blocks for every in-page LEFT sidebar
 * (the explorer / filter / nav rail), unified for the 1.0 cleanup.
 *
 * Design (see .claude/context/wireframes/sidebar-*.html, "Style A"):
 *  · Page identity lives in the SUB-BAR, never repeated here — a sidebar
 *    starts straight at its toolbar (no page title, no "EXPLORER" label).
 *  · Toolbar primitive: [🔍 Search] [⧩ Filter (n)]? [⋮ View]? — Search is
 *    always present (contextual placeholder; on nav pages it's a live
 *    command-finder); Filter only on faceted pages; View only where sort/group
 *    applies.
 *  · Section headers = uppercase micro-label + trailing count (+ optional
 *    collapse chevron). Same on explorers and on Settings/Admin nav.
 *  · Rows go through the shared ListRow so selection is the tokenized brand
 *    accent-bar — never hardcoded blue.
 *  · Width unified to 280px by the parent; collapsed 44px.
 */

export const SIDEBAR_WIDTH = "w-[280px]"
export const SIDEBAR_WIDTH_COLLAPSED = "w-11"

/* ---------------------------------------------------------------- toolbar */

export function SidebarToolbar({ className, children, ...props }: React.ComponentProps<"div">) {
  return (
    <div className={cn("shrink-0 flex items-center gap-1.5 px-2 py-2", className)} {...props}>
      {children}
    </div>
  )
}

/** Unified search box. Controlled — pass value + onValueChange. */
export function SidebarSearch({
  value,
  onValueChange,
  placeholder = "Search…",
  className,
  inputClassName,
  autoFocus,
  onKeyDown,
  "aria-label": ariaLabel,
}: {
  value: string
  onValueChange: (v: string) => void
  placeholder?: string
  className?: string
  inputClassName?: string
  autoFocus?: boolean
  onKeyDown?: React.KeyboardEventHandler<HTMLInputElement>
  "aria-label"?: string
}) {
  return (
    <div
      className={cn(
        "kit-tap flex items-center gap-1.5 h-8 px-2.5 flex-1 min-w-0 rounded-md",
        "bg-white/[0.04] border border-white/[0.08]",
        "focus-within:border-primary/40 transition-colors",
        className,
      )}
    >
      <Search className="h-3.5 w-3.5 text-muted-foreground/50 shrink-0" />
      <input
        type="text"
        value={value}
        autoFocus={autoFocus}
        onChange={(e) => onValueChange(e.target.value)}
        onKeyDown={onKeyDown}
        placeholder={placeholder}
        aria-label={ariaLabel ?? placeholder}
        className={cn(
          "flex-1 min-w-0 bg-transparent text-xs text-foreground outline-none",
          "placeholder:text-muted-foreground/40",
          inputClassName,
        )}
      />
      {value && (
        <button
          type="button"
          onClick={() => onValueChange("")}
          aria-label="Clear search"
          className="shrink-0 text-muted-foreground/50 hover:text-foreground transition-colors"
        >
          <X className="h-3 w-3" />
        </button>
      )}
    </div>
  )
}

/**
 * Filter trigger — the single entry point for a page's facet filters.
 * `activeCount` drives the active styling + count badge. Wrap in your own
 * Popover/Dropdown; this is just the styled, consistent trigger.
 */
export function SidebarFilterButton({
  activeCount = 0,
  active,
  icon: Icon = FilterIcon,
  children = "Filter",
  className,
  ...props
}: React.ComponentProps<"button"> & { activeCount?: number; active?: boolean; icon?: LucideIcon }) {
  const on = active ?? activeCount > 0
  return (
    <button
      type="button"
      className={cn(
        "kit-tap inline-flex items-center gap-1.5 h-8 px-2.5 shrink-0 rounded-md border text-[11px] whitespace-nowrap transition-colors",
        on
          ? "bg-primary/10 border-primary/30 text-primary-hover"
          : "bg-white/[0.04] border-white/[0.08] text-muted-foreground/70 hover:text-foreground",
        className,
      )}
      {...props}
    >
      <Icon className="h-3 w-3" />
      {children}
      {activeCount > 0 && (
        <span className="ml-0.5 rounded-full bg-primary-hover px-1.5 min-w-[15px] text-center text-[9px] font-bold text-background tabular-nums">
          {activeCount}
        </span>
      )}
    </button>
  )
}

/* ------------------------------------------------------- filter popover */

const FILTER_PANEL_ANIM = {
  initial: { opacity: 0, scale: 0.95, y: -4 },
  animate: { opacity: 1, scale: 1, y: 0, transition: { duration: 0.12 } },
  exit: { opacity: 0, scale: 0.95, y: -4, transition: { duration: 0.1 } },
}

/**
 * The filter panel itself — trigger, dismiss layer, anchored panel and its
 * animation as one component.
 *
 * The kit used to export only `SidebarFilterButton`, so each surface wrote the
 * panel by hand and they drifted (#1776). Two of the behaviours here are the
 * reason it is worth sharing at all, and both were fixed once, for Issues,
 * where nobody else could inherit them:
 *
 *  · **A pick never closes the panel.** Credentials closed on every pick, which
 *    makes reaching a second facet a matter of reopening the menu.
 *  · **A pick never touches a sibling facet.** That is the consumer's job, but
 *    the panel is what made it feel wrong: with each pick clearing the last one,
 *    the count badge could only ever read 0 or 1 — a switch wearing a filter's
 *    clothes.
 *
 * Open state is owned here; pass `open`/`onOpenChange` only if the surface
 * genuinely needs to drive it.
 */
export function SidebarFilterPopover({
  label,
  activeCount = 0,
  onClear,
  open: openProp,
  onOpenChange,
  icon,
  triggerLabel,
  panelClassName,
  className,
  children,
}: {
  /** Accessible name for the panel, e.g. "Filter issues". */
  label: string
  activeCount?: number
  /** Renders "Clear all" in the header while something is active. */
  onClear?: () => void
  open?: boolean
  onOpenChange?: (open: boolean) => void
  icon?: LucideIcon
  triggerLabel?: React.ReactNode
  panelClassName?: string
  className?: string
  children: React.ReactNode
}) {
  const [uncontrolled, setUncontrolled] = React.useState(false)
  const open = openProp ?? uncontrolled
  const setOpen = React.useCallback(
    (next: boolean) => {
      if (openProp === undefined) setUncontrolled(next)
      onOpenChange?.(next)
    },
    [openProp, onOpenChange],
  )

  // Ties the trigger to the panel it opens, so a screen reader can move
  // between them.
  const panelId = React.useId()

  // Escape closes. The dismiss layer below only catches pointers, and a filter
  // panel you can open from the keyboard has to be closable from it too.
  React.useEffect(() => {
    if (!open) return
    const onKeyDown = (e: KeyboardEvent) => {
      if (e.key === "Escape") setOpen(false)
    }
    document.addEventListener("keydown", onKeyDown)
    return () => document.removeEventListener("keydown", onKeyDown)
  }, [open, setOpen])

  return (
    <div className={cn("relative shrink-0", className)}>
      <SidebarFilterButton
        activeCount={activeCount}
        icon={icon}
        aria-expanded={open}
        // Without these the trigger reads as an ordinary action button:
        // nothing tells a screen reader that activating it reveals more
        // controls, or which region those controls live in. aria-expanded
        // alone says "expanded" without naming what expanded.
        aria-haspopup="dialog"
        aria-controls={open ? panelId : undefined}
        onClick={() => setOpen(!open)}
      >
        {triggerLabel}
      </SidebarFilterButton>
      <AnimatePresence>
        {open && (
          <>
            <div
              data-testid="sidebar-filter-dismiss"
              className="fixed inset-0 z-40"
              onClick={() => setOpen(false)}
            />
            <motion.div
              {...FILTER_PANEL_ANIM}
              id={panelId}
              role="group"
              aria-label={label}
              className={cn(
                "absolute right-0 top-9 z-50 min-w-[200px] max-h-[360px] overflow-y-auto",
                "rounded-lg border border-white/[0.1] bg-card py-1 shadow-xl",
                panelClassName,
              )}
            >
              <div className="flex items-center gap-2 border-b border-white/[0.06] px-3 py-1.5">
                <span className="text-[9px] font-semibold uppercase tracking-wider text-foreground/40">
                  Filters
                </span>
                {activeCount > 0 && onClear && (
                  <button
                    type="button"
                    onClick={onClear}
                    className="ml-auto text-[10px] text-muted-foreground/80 hover:text-foreground"
                  >
                    Clear all
                  </button>
                )}
              </div>
              {children}
            </motion.div>
          </>
        )}
      </AnimatePresence>
    </div>
  )
}

/**
 * One facet inside the panel: a header, an explicit reset row, and its values.
 *
 * The reset row is what makes "drop the crew but keep the priority" a single
 * click. Without it the only way back to "all crews" is a control that also
 * clears the agent — which is how a filter panel teaches people not to trust it.
 */
export function SidebarFacet({
  label,
  resetLabel,
  resetActive,
  onReset,
  first = false,
  children,
}: {
  label: React.ReactNode
  resetLabel: string
  /** True when this facet has no selection — i.e. the reset row IS the state. */
  resetActive: boolean
  onReset: () => void
  /** Suppresses the leading divider on the first facet in a panel. */
  first?: boolean
  children: React.ReactNode
}) {
  return (
    <>
      {!first && <div className="mt-1 border-t border-white/[0.06]" />}
      <div className="px-3 py-1 text-[9px] font-semibold uppercase tracking-wider text-foreground/40">
        {label}
      </div>
      <button
        type="button"
        onClick={onReset}
        aria-pressed={resetActive}
        className={cn(
          "kit-tap w-full px-3 py-1.5 text-left text-xs hover:bg-white/[0.06]",
          resetActive ? "text-primary" : "text-muted-foreground/80",
        )}
      >
        {resetLabel}
      </button>
      {children}
    </>
  )
}

/** One value inside a facet. Toggles itself; never touches its neighbours. */
export function SidebarFacetOption({
  active,
  onToggle,
  className,
  children,
}: {
  active: boolean
  onToggle: () => void
  className?: string
  children: React.ReactNode
}) {
  return (
    <button
      type="button"
      onClick={onToggle}
      aria-pressed={active}
      className={cn(
        "kit-tap flex w-full items-center gap-2 px-3 py-1.5 text-left text-xs hover:bg-white/[0.06]",
        active ? "text-primary" : "text-muted-foreground/80",
        className,
      )}
    >
      {children}
      {active && <Check className="ml-auto h-3 w-3 shrink-0" />}
    </button>
  )
}

/**
 * View trigger — sort/group controls (kept separate from Filter so the two
 * never read as "two filters"). Icon-only by default. Wrap in a menu.
 */
export function SidebarViewButton({
  className,
  children,
  "aria-label": ariaLabel = "View: sort & group",
  ...props
}: React.ComponentProps<"button">) {
  return (
    <button
      type="button"
      aria-label={ariaLabel}
      className={cn(
        "kit-tap inline-flex items-center justify-center h-8 w-8 shrink-0 rounded-md border text-muted-foreground/70",
        "bg-white/[0.04] border-white/[0.08] hover:text-foreground transition-colors",
        className,
      )}
      {...props}
    >
      {children ?? <span className="text-base leading-none">⋮</span>}
    </button>
  )
}

/**
 * Collapse toggle — lives in the toolbar next to search on every sidebar
 * (never a separate empty strip or a floating button). When the sidebar is
 * collapsed, render this on its own in the narrow rail to expand it again.
 */
export function SidebarCollapseButton({
  collapsed,
  onToggle,
  className,
  ...props
}: React.ComponentProps<"button"> & { collapsed: boolean; onToggle: () => void }) {
  return (
    <button
      type="button"
      onClick={onToggle}
      aria-label={collapsed ? "Expand sidebar" : "Collapse sidebar"}
      title={collapsed ? "Expand" : "Collapse"}
      className={cn(
        "kit-tap inline-flex h-8 w-8 shrink-0 items-center justify-center rounded-md text-muted-foreground/70",
        "hover:text-foreground hover:bg-white/[0.04] transition-colors",
        className,
      )}
      {...props}
    >
      {collapsed ? <PanelLeftOpen className="h-3.5 w-3.5" /> : <PanelLeftClose className="h-3.5 w-3.5" />}
    </button>
  )
}

/** Removable active-filter chip, shown under the toolbar when filters apply. */
export function SidebarActiveChip({
  onRemove,
  className,
  children,
}: {
  onRemove?: () => void
  className?: string
  children: React.ReactNode
}) {
  return (
    <span
      className={cn(
        "kit-tap inline-flex items-center gap-1.5 rounded-md px-1.5 py-0.5 text-[10px]",
        "bg-primary/10 border border-primary/25 text-primary-hover",
        className,
      )}
    >
      {children}
      {onRemove && (
        <button type="button" onClick={onRemove} aria-label="Remove filter" className="opacity-70 hover:opacity-100">
          <X className="h-2.5 w-2.5" />
        </button>
      )}
    </span>
  )
}

export function SidebarActiveChips({ className, children }: React.ComponentProps<"div">) {
  const has = React.Children.toArray(children).some(Boolean)
  if (!has) return null
  return <div className={cn("flex flex-wrap gap-1.5 px-2 pb-2", className)}>{children}</div>
}

/* --------------------------------------------------------------- sections */

/** Section header (+ optional collapse). Its children render below when open. */
export function SidebarSection({
  label,
  count,
  collapsible = false,
  collapsed = false,
  onToggle,
  actions,
  className,
  headerClassName,
  children,
}: {
  label: React.ReactNode
  count?: React.ReactNode
  collapsible?: boolean
  collapsed?: boolean
  onToggle?: () => void
  actions?: React.ReactNode
  className?: string
  headerClassName?: string
  children?: React.ReactNode
}) {
  // Header content WITHOUT actions — this is what goes inside the toggle
  // <button> when collapsible. `actions` (which may itself contain buttons)
  // must never nest inside that button, so for the collapsible variant it's
  // rendered as a sibling; the non-collapsible variant is a <div>, so keeping
  // actions inline there is safe (and preserves the "label · meta" layout).
  const headerInner = (
    <>
      {collapsible && (
        <ChevronDown
          className={cn(
            "h-3 w-3 text-muted-foreground/60 shrink-0 transition-transform duration-150",
            collapsed && "-rotate-90",
          )}
        />
      )}
      <span className="text-[10px] font-semibold uppercase tracking-wider text-foreground/50">{label}</span>
      {count != null && (
        <span className="ml-auto text-[10px] tabular-nums text-muted-foreground/50">{count}</span>
      )}
      {!collapsible && actions}
    </>
  )
  return (
    <div className={cn("shrink-0", className)}>
      {collapsible ? (
        <div className="flex items-center">
          <button
            type="button"
            onClick={onToggle}
            aria-expanded={!collapsed}
            className={cn(
              "kit-tap flex flex-1 items-center gap-1.5 px-3 py-1.5 hover:bg-white/[0.02] transition-colors",
              headerClassName,
            )}
          >
            {headerInner}
          </button>
          {actions && <div className="flex shrink-0 items-center pr-2">{actions}</div>}
        </div>
      ) : (
        <div className={cn("flex items-center gap-1.5 px-3 py-1.5 select-none", headerClassName)}>{headerInner}</div>
      )}
      {!collapsed && children}
    </div>
  )
}

/* ------------------------------------------------------------------- rows */

/**
 * Canonical sidebar row — routes through ListRow so selection is the tokenized
 * brand accent-bar (never hardcoded blue). Compose the inner content freely
 * (icon/dot + label + count/trailing); this bakes in the standard padding.
 */
export function SidebarRow({
  selected,
  onSelect,
  indent,
  className,
  children,
  ...rest
}: {
  selected?: boolean
  onSelect?: () => void
  /** Indent one level (nested tree rows, e.g. agents under a crew). */
  indent?: boolean
  className?: string
  children: React.ReactNode
} & Omit<React.ComponentProps<typeof ListRow>, "selected" | "onSelect" | "className" | "children">) {
  return (
    <ListRow
      selected={selected}
      onSelect={onSelect}
      className={cn(
        // py-1 rather than py-1.5: with the role line gone from every
        // unselected row, the portrait is what sets the height, and the extra
        // padding was only ever there to keep two lines of text from touching.
        "kit-tap mx-1.5 gap-2 rounded-md px-2 py-1 type-nav",
        indent && "ml-6",
        className,
      )}
      {...rest}
    >
      {children}
    </ListRow>
  )
}
