"use client"

import * as React from "react"
import Link from "next/link"
import { AnimatePresence, motion, useReducedMotion } from "motion/react"
import { ArrowLeft, ChevronDown, SlidersHorizontal, type LucideIcon } from "lucide-react"

import { cn } from "@/lib/utils"
import { useIsMobile } from "@/hooks/use-mobile"
import { SubBar } from "@/components/layout/sub-bar"
import { SIDEBAR_WIDTH, SidebarCollapseButton, SidebarToolbar } from "@/components/layout/sidebar-kit"
import { Sheet, SheetContent, SheetDescription, SheetTitle } from "@/components/ui/sheet"

/**
 * A nested page: one level deeper than the page it came from, with its own
 * side panel instead of an extra column.
 *
 * The rule it keeps: at most two navigation columns on screen — the app rail
 * and ONE side panel. Opening a nested page swaps the parent's panel (e.g.
 * the Settings sections) for this page's own, headed by "← <parent>", so the
 * reader knows they went one level in and how to get out. The arrow always
 * goes to the parent, not to browser history. Never nest a nested page.
 *
 * The panel is built like every other explorer sidebar (sidebar-kit): a
 * toolbar (search, Filter) with the collapse button, then collapsible
 * DrillNavSections of DrillNavItems. On a phone the panel becomes a bottom
 * sheet behind a "Filters" button in the sub-bar; `mobileNav` (usually the
 * page's view tabs) stays on the page.
 *
 * Used by Settings › Crew links and Settings › Audit log.
 */
export function DrillPage({
  parent,
  title,
  icon: Icon,
  description,
  toolbar,
  nav,
  mobileNav,
  filterCount = 0,
  actions,
  children,
  className,
}: {
  /** Where the back arrow goes, and what it is called. */
  parent: { href: string; label: string; icon?: LucideIcon }
  title: string
  icon: LucideIcon
  description?: string
  /** The panel's toolbar — SidebarSearch, a SidebarFilterPopover. The
   *  collapse button is added here. */
  toolbar?: React.ReactNode
  /** The side panel's sections — DrillNavSection / DrillNavItem. */
  nav: React.ReactNode
  /** What stays on the page on a phone, above the content. */
  mobileNav?: React.ReactNode
  /** Active filters, for the phone's "Filters" button. */
  filterCount?: number
  /** Page actions, on the sub-bar. */
  actions?: React.ReactNode
  children: React.ReactNode
  className?: string
}) {
  const isMobile = useIsMobile()
  const reduce = useReducedMotion()
  const [collapsed, setCollapsed] = usePersistentFlag(`drill-collapsed:${title}`)
  const [sheetOpen, setSheetOpen] = React.useState(false)
  const BackIcon = parent.icon

  const filtersButton = isMobile && (
    <button
      type="button"
      onClick={() => setSheetOpen(true)}
      data-slot="drill-filters"
      className={cn(
        "inline-flex h-8 items-center gap-1.5 rounded-md border px-2.5 text-xs font-medium",
        filterCount > 0 ? "border-primary/30 bg-primary/10 text-primary-hover" : "border-border text-muted-foreground",
      )}
    >
      <SlidersHorizontal className="h-3.5 w-3.5" />
      Filters
      {filterCount > 0 && (
        <span className="rounded-full bg-primary-hover px-1.5 text-[10px] font-bold tabular-nums text-background">{filterCount}</span>
      )}
    </button>
  )

  return (
    <div className="flex h-[calc(100dvh-var(--app-header-h)-var(--mobile-tab-bar-h))] flex-col">
      <SubBar
        icon={BackIcon}
        title={parent.label}
        section={title}
        ariaLabel={`${parent.label} / ${title}`}
        actions={isMobile ? <>{actions}{filtersButton}</> : actions}
        leading={
          isMobile ? (
            <Link href={parent.href} aria-label={`Back to ${parent.label}`} className="-ml-1 grid h-7 w-7 place-items-center rounded-md text-muted-foreground hover:bg-accent hover:text-foreground">
              <ArrowLeft className="h-4 w-4" />
            </Link>
          ) : undefined
        }
      />
      <div className="flex min-h-0 flex-1">
        {!isMobile && (
          <motion.aside
            initial={reduce ? false : { opacity: 0, x: 16 }}
            animate={{ opacity: 1, x: 0 }}
            transition={{ duration: 0.22, ease: [0.2, 0.7, 0.2, 1] }}
            data-collapsed={collapsed || undefined}
            className={cn(
              "flex shrink-0 flex-col border-r border-sidebar-border bg-sidebar transition-[width] duration-200",
              collapsed ? "w-11" : SIDEBAR_WIDTH,
            )}
            aria-label={`${title} navigation`}
          >
            {collapsed ? (
              <div className="flex flex-col items-center gap-2 py-2">
                <SidebarCollapseButton collapsed onToggle={() => setCollapsed(false)} />
                <Link href={parent.href} aria-label={`Back to ${parent.label}`} title={`Back to ${parent.label}`} className="grid h-8 w-8 place-items-center rounded-md text-muted-foreground hover:bg-accent hover:text-foreground">
                  <ArrowLeft className="h-3.5 w-3.5" />
                </Link>
                <span className="icon-tile inline-flex h-8 w-8 items-center justify-center rounded-lg" title={title} aria-hidden>
                  <Icon className="h-4 w-4" />
                </span>
              </div>
            ) : (
              <>
                <DrillHeader parent={parent} title={title} icon={Icon} description={description} />
                <SidebarToolbar>
                  {toolbar ?? <span className="flex-1" />}
                  <SidebarCollapseButton collapsed={false} onToggle={() => setCollapsed(true)} />
                </SidebarToolbar>
                <nav className="min-h-0 flex-1 overflow-y-auto pb-4" aria-label={`${title} sections`}>{nav}</nav>
              </>
            )}
          </motion.aside>
        )}
        <main className={cn("min-h-0 min-w-0 flex-1 overflow-y-auto", className)}>
          {isMobile && mobileNav && (
            <div className="sticky top-0 z-10 border-b border-border bg-background/95 px-3 py-2 backdrop-blur">{mobileNav}</div>
          )}
          {children}
        </main>
      </div>
      {isMobile && (
        <Sheet open={sheetOpen} onOpenChange={setSheetOpen}>
          <SheetContent side="bottom" className="max-h-[85dvh] gap-0 rounded-t-2xl bg-sidebar p-0" data-slot="drill-sheet">
            <div className="mx-auto mt-2 h-1 w-9 rounded-full bg-border" aria-hidden />
            <SheetTitle className="px-4 pt-2 text-sm">{title}</SheetTitle>
            <SheetDescription className="sr-only">Filters and sections for {title}</SheetDescription>
            {toolbar && <SidebarToolbar>{toolbar}</SidebarToolbar>}
            {/* A pick inside the sheet closes it: on a phone the result is what
                the person wants to see next. */}
            <nav
              className="min-h-0 flex-1 overflow-y-auto pb-6"
              aria-label={`${title} sections`}
              onClick={(e) => { if ((e.target as HTMLElement).closest("[data-drill-close]")) setSheetOpen(false) }}
            >
              {nav}
            </nav>
          </SheetContent>
        </Sheet>
      )}
    </div>
  )
}

function DrillHeader({ parent, title, icon: Icon, description }: { parent: { href: string; label: string }; title: string; icon: LucideIcon; description?: string }) {
  return (
    <div className="border-b border-sidebar-border px-3 pb-3 pt-2.5">
      <Link
        href={parent.href}
        data-slot="drill-back"
        className="-ml-1 inline-flex h-7 items-center gap-1.5 rounded-md px-1.5 text-xs font-medium text-muted-foreground transition-colors hover:bg-accent hover:text-foreground"
      >
        <ArrowLeft className="h-3.5 w-3.5" />
        {parent.label}
      </Link>
      <div className="mt-2 flex items-center gap-2.5">
        <span className="icon-tile inline-flex h-8 w-8 shrink-0 items-center justify-center rounded-lg" aria-hidden>
          <Icon className="h-4 w-4" />
        </span>
        <div className="min-w-0">
          <h1 className="truncate text-sm font-semibold tracking-[-0.01em]">{title}</h1>
          {description && <p className="text-[11.5px] leading-snug text-muted-foreground">{description}</p>}
        </div>
      </div>
    </div>
  )
}

/** A per-browser flag (the collapsed panel), remembered across visits. */
function usePersistentFlag(key: string): [boolean, (v: boolean) => void] {
  const [value, setValue] = React.useState(false)
  React.useEffect(() => {
    try { setValue(window.localStorage.getItem(key) === "1") } catch { /* storage blocked */ }
  }, [key])
  const set = React.useCallback((v: boolean) => {
    setValue(v)
    try { window.localStorage.setItem(key, v ? "1" : "0") } catch { /* storage blocked */ }
  }, [key])
  return [value, set]
}

/**
 * A labelled group in the side panel. Collapsible like SidebarSection, with
 * the body folding by height.
 */
export function DrillNavSection({
  label,
  count,
  collapsible = true,
  defaultOpen = true,
  children,
}: {
  label: string
  count?: React.ReactNode
  collapsible?: boolean
  defaultOpen?: boolean
  children: React.ReactNode
}) {
  const [open, setOpen] = React.useState(defaultOpen)
  const reduce = useReducedMotion()
  const bodyId = React.useId()
  const head = (
    <>
      {collapsible && <ChevronDown className={cn("h-3 w-3 shrink-0 transition-transform duration-200", !open && "-rotate-90")} aria-hidden />}
      <span className="min-w-0 truncate">{label}</span>
      {count != null && <span className="ml-auto tabular-nums">{count}</span>}
    </>
  )
  const headClass = "flex w-full items-center gap-1.5 px-2 pb-1 pt-1 font-mono text-[10.5px] font-medium uppercase tracking-[0.08em] text-muted-foreground-soft"
  return (
    <div className="px-1.5 pt-2.5" data-slot="drill-section">
      {collapsible ? (
        <button type="button" onClick={() => setOpen(!open)} aria-expanded={open} aria-controls={bodyId} className={cn(headClass, "rounded-md hover:text-foreground")}>
          {head}
        </button>
      ) : (
        <div className={headClass}>{head}</div>
      )}
      <AnimatePresence initial={false}>
        {open && (
          <motion.div
            id={bodyId}
            key="body"
            initial={reduce ? false : { height: 0, opacity: 0 }}
            animate={{ height: "auto", opacity: 1 }}
            exit={reduce ? undefined : { height: 0, opacity: 0 }}
            transition={{ duration: 0.2, ease: [0.2, 0.7, 0.2, 1] }}
            className="overflow-hidden"
          >
            <div className="flex flex-col gap-px">{children}</div>
          </motion.div>
        )}
      </AnimatePresence>
    </div>
  )
}

/**
 * A row in the side panel: the same shape and selection as SidebarRow. Rows
 * rise in one after another (`index`), so a filter that changes the list reads
 * as a change rather than a flash. Picking one closes the phone's sheet.
 */
export function DrillNavItem({
  selected,
  onSelect,
  icon,
  label,
  sub,
  meta,
  title,
  muted,
  index = 0,
  pressed,
}: {
  selected?: boolean
  onSelect: () => void
  icon?: React.ReactNode
  label: React.ReactNode
  /** A second, quieter line. */
  sub?: React.ReactNode
  meta?: React.ReactNode
  title?: string
  muted?: boolean
  index?: number
  /** For toggles (a facet value) rather than a place: aria-pressed. */
  pressed?: boolean
}) {
  return (
    <button
      type="button"
      onClick={onSelect}
      data-drill-close
      aria-current={pressed === undefined && selected ? "true" : undefined}
      aria-pressed={pressed}
      title={title}
      style={{ animationDelay: `${Math.min(index, 12) * 18}ms` }}
      className={cn(
        // kit-tap: 44px under a coarse pointer (globals.css), desktop density
        // untouched — these rows are the panel's whole navigation on a phone.
        "kit-tap drill-row-in row-interactive row-hover w-full items-center gap-2 rounded-md px-2 py-1.5 text-left text-[13px]",
        selected && "row-selected font-medium",
        muted && !selected && "text-muted-foreground",
      )}
    >
      {icon}
      <span className="min-w-0 flex-1">
        <span className="block truncate">{label}</span>
        {sub != null && <span className="block truncate text-[11px] font-normal text-muted-foreground">{sub}</span>}
      </span>
      {meta != null && <span className="shrink-0 font-mono text-[11px] tabular-nums text-muted-foreground">{meta}</span>}
    </button>
  )
}
