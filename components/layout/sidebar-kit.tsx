"use client"

import * as React from "react"
import { ChevronDown, Filter as FilterIcon, PanelLeftClose, PanelLeftOpen, Search, X } from "lucide-react"
import type { LucideIcon } from "lucide-react"

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
        "flex items-center gap-1.5 h-8 px-2.5 flex-1 min-w-0 rounded-md",
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
        "inline-flex items-center gap-1.5 h-8 px-2.5 shrink-0 rounded-md border text-[11px] whitespace-nowrap transition-colors",
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
        "inline-flex items-center justify-center h-8 w-8 shrink-0 rounded-md border text-muted-foreground/70",
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
        "inline-flex h-8 w-8 shrink-0 items-center justify-center rounded-md text-muted-foreground/70",
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
        "inline-flex items-center gap-1.5 rounded-md px-1.5 py-0.5 text-[10px]",
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
              "flex flex-1 items-center gap-1.5 px-3 py-1.5 hover:bg-white/[0.02] transition-colors",
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
        "mx-1.5 gap-2 rounded-md px-2 py-1.5 text-xs",
        indent && "ml-6",
        className,
      )}
      {...rest}
    >
      {children}
    </ListRow>
  )
}
