"use client"

import * as React from "react"
import type { LucideIcon } from "lucide-react"
import { Slot } from "radix-ui"
import { createPortal } from "react-dom"
import { ChevronRight } from "lucide-react"

import { cn } from "@/lib/utils"
import { Button } from "@/components/ui/button"
import { useAppStore } from "@/lib/store"
import { useIsMobile } from "@/hooks/use-mobile"

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
  /**
   * Active section inside a page that has its own internal nav (Admin's
   * console sections, Settings' tabs). Renders as `Title / Section` with the
   * title demoted and the section emphasised, so the row reads as a path
   * rather than two competing headings. Pages without sections omit it and
   * keep the plain full-strength title.
   */
  section?: React.ReactNode
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
  /**
   * The title is the page's h1 (default). A page that shows its own visible
   * h1 (Dashboard greeting, Activity view title) passes false so the page
   * keeps exactly one.
   */
  heading?: boolean
  className?: string
}

/* ---------------------------------------------------------------------------
 * Page actions slot (COM-12). A page rendered below its layout's SubBar (the
 * routine page under the Routines layout) puts its actions in the bar with
 * <SubBarActions>, which portals them after the bar's own `actions`. The bar
 * on screen owns the slot; with none, SubBarActions renders nothing.
 * ------------------------------------------------------------------------ */
let actionsSlot: HTMLElement | null = null
const slotListeners = new Set<() => void>()
function setActionsSlot(el: HTMLElement | null) {
  if (actionsSlot === el) return
  actionsSlot = el
  for (const l of slotListeners) l()
}
function subscribeActionsSlot(l: () => void) {
  slotListeners.add(l)
  return () => {
    slotListeners.delete(l)
  }
}

/** The SubBar's actions container; registers itself as the page actions slot. */
function ActionsSlot({ className, children }: { className?: string; children?: React.ReactNode }) {
  const ref = React.useRef<HTMLDivElement | null>(null)
  React.useLayoutEffect(() => {
    const el = ref.current
    setActionsSlot(el)
    return () => {
      if (actionsSlot === el) setActionsSlot(null)
    }
  }, [])
  return (
    <div ref={ref} data-slot="sub-bar-actions" className={cn("flex shrink-0 items-center gap-1.5 empty:hidden", className)}>
      {children}
    </div>
  )
}

/**
 * Actions for the page's SubBar from anywhere below it, instead of a portal
 * target handed down by a layout. They render after the bar's own actions.
 */
export function SubBarActions({ children }: { children: React.ReactNode }) {
  const slot = React.useSyncExternalStore(subscribeActionsSlot, () => actionsSlot, () => null)
  return slot ? createPortal(children, slot) : null
}

/**
 * Whether anything in the row's actions is displayed. A page may hide an
 * action on a phone (Dashboard's window control below md); a row left with
 * only the hidden heading would be an empty 36 px strip (COM-06).
 */
function useActionsShown(row: React.RefObject<HTMLDivElement | null>, active: boolean): boolean {
  const [shown, setShown] = React.useState(true)
  React.useLayoutEffect(() => {
    const box = row.current?.querySelector<HTMLElement>("[data-slot=sub-bar-actions]")
    if (!active || !box) {
      setShown(true)
      return
    }
    const measure = () => setShown(Array.from(box.children).some((k) => getComputedStyle(k).display !== "none"))
    measure()
    const mo = new MutationObserver(measure)
    mo.observe(box, { childList: true, subtree: true, attributes: true, attributeFilter: ["class", "style", "hidden"] })
    window.addEventListener("resize", measure)
    return () => {
      mo.disconnect()
      window.removeEventListener("resize", measure)
    }
  }, [row, active])
  return shown
}

const ROW = "shrink-0 flex items-center h-9 bg-card border-b border-foreground/[0.08] px-2 sm:px-3"
const SCROLL_X = "overflow-x-auto [&::-webkit-scrollbar]:hidden [-ms-overflow-style:none] [scrollbar-width:none]"

export function SubBar<T extends string = string>({
  icon: Icon,
  title,
  section,
  description,
  meta,
  leading,
  actions,
  tabs,
  activeTab,
  onTabChange,
  tools,
  ariaLabel,
  heading = true,
  className,
}: SubBarProps<T>) {
  const hasRow2 = (tabs != null && tabs.length > 0) || tools != null
  const Heading = heading ? "h1" : "div"
  const slot = useAppStore((s) => s.pageBarSlot)
  const isMobile = useIsMobile()

  // Harbor 2: on desktop and tablet the identity and the actions join the top
  // bar after the area's breadcrumb; a title that only repeats the area's name
  // stays as the page's accessible heading but is not shown twice.
  // AppToolbar writes the area it names onto the slot.
  const areaTitle = slot?.dataset.areaTitle ?? ""
  const repeatsArea = areaTitle !== "" && areaTitle.toLowerCase() === title.toLowerCase()
  // On a phone the top bar already names the area: a title that only repeats
  // it is hidden, and then the row is only worth drawing while an action in
  // it is displayed.
  const identityHidden = isMobile && repeatsArea && section == null && !leading && meta == null
  const rowRef = React.useRef<HTMLDivElement | null>(null)
  const actionsShown = useActionsShown(rowRef, identityHidden)
  const rowEmpty = identityHidden && !actionsShown
  if (slot && !isMobile) {
    return (
      <>
        {createPortal(
          <div className="flex min-w-0 flex-1 items-center gap-2" aria-label={ariaLabel} data-slot="sub-bar">
            {leading && <div className="flex shrink-0 items-center">{leading}</div>}
            <Heading className={cn("flex min-w-0 items-center gap-1.5 whitespace-nowrap text-sm", repeatsArea && section == null && "sr-only")}>
              {!repeatsArea && (
                <>
                  <ChevronRight className="size-3.5 shrink-0 text-muted-foreground-soft" aria-hidden />
                  <span className={section != null ? "text-muted-foreground" : "font-semibold text-foreground"}>{title}</span>
                </>
              )}
              {repeatsArea && section == null && <span>{title}</span>}
              {section != null && (
                <>
                  {repeatsArea && <span className="sr-only">{title}</span>}
                  <ChevronRight className="size-3.5 shrink-0 text-muted-foreground-soft" aria-hidden />
                  <span className="font-semibold text-foreground">{section}</span>
                </>
              )}
            </Heading>
            {description != null && (
              <span className="hidden min-w-0 truncate text-label text-muted-foreground lg:block">{description}</span>
            )}
            {meta}
            <div className="flex-1" />
            {/* Keeps the primary action off the status icons that follow. */}
            <ActionsSlot className="pr-2">{actions}</ActionsSlot>
          </div>,
          slot,
        )}
        {hasRow2 && <SubBarRow2 tabs={tabs} activeTab={activeTab} onTabChange={onTabChange} tools={tools} ariaLabel={ariaLabel} className={className} />}
      </>
    )
  }

  return (
    <div className={cn("shrink-0", className)}>
      {/* A row with nothing displayed in it is not drawn (an empty 36 px
          strip on a phone); its heading stays for assistive technology,
          outside the hidden row. The actions stay mounted, so one that
          shows again brings the row back. */}
      {rowEmpty && heading && <h1 className="sr-only">{title}</h1>}
      {/* ---- Row 1: identity + actions ---- */}
      {/* min-h, not h: with a coarse pointer the actions are 44 px tall. */}
      <div
        ref={rowRef}
        data-slot="sub-bar-row"
        data-empty={rowEmpty || undefined}
        className={cn(ROW, "h-auto gap-2 data-[empty]:hidden", !rowEmpty && "min-h-9", SCROLL_X)}
        aria-label={ariaLabel}
      >
        {leading && <div className="flex items-center shrink-0">{leading}</div>}

        {/* On a phone the top bar already names the area; a title that only
            repeats it stays the page's heading for assistive technology. */}
        {!rowEmpty && <div className="flex items-center gap-2 min-w-0 max-sm:shrink-0">
          {Icon && !(repeatsArea && section == null) && <Icon className="h-3.5 w-3.5 text-foreground/70 shrink-0" />}
          {/* One <h1> spanning the whole path, so the accessible name of the
              page is "Admin Console / Users" rather than a bare section label
              floating next to an unrelated heading. */}
          <Heading className={cn("text-body whitespace-nowrap shrink-0", repeatsArea && section == null && "sr-only")}>
            <span className={section != null ? "font-normal text-muted-foreground" : "font-semibold tracking-[-0.01em] text-foreground"}>
              {title}
            </span>
            {section != null && (
              <>
                {/* whitespace-pre, not a flex gap: the separator has to exist
                    as real text or the accessible name collapses to
                    "Admin ConsoleUsers". */}
                <span className="text-muted-foreground-soft whitespace-pre"> / </span>
                <span className="font-medium text-foreground">{section}</span>
              </>
            )}
          </Heading>
          {description != null && (
            <>
              <span className="text-muted-foreground-soft shrink-0 hidden sm:inline">·</span>
              <span className="text-xs text-muted-foreground truncate hidden sm:block min-w-0">
                {description}
              </span>
            </>
          )}
          {meta}
        </div>}

        <div className="flex-1" />

        <ActionsSlot>{actions}</ActionsSlot>
      </div>

      {hasRow2 && <SubBarRow2 tabs={tabs} activeTab={activeTab} onTabChange={onTabChange} tools={tools} ariaLabel={ariaLabel} />}
    </div>
  )
}

/** Row 2: view tabs and tools. Stays on the page under the top bar. */
function SubBarRow2<T extends string>({ tabs, activeTab, onTabChange, tools, ariaLabel, className }: {
  tabs?: SubBarTab<T>[]
  activeTab?: T
  onTabChange?: (id: T) => void
  tools?: React.ReactNode
  ariaLabel?: string
  className?: string
}) {
  return (
    <div className={cn(ROW, "gap-0", SCROLL_X, className)}>
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
      {tab.badge != null && <span className="text-micro text-muted-foreground/70">{tab.badge}</span>}
      {tab.locked && (
        <span className="text-micro text-warn/70">
          {tab.lockedLabel ?? "soon"}
        </span>
      )}
    </button>
  )
}

/* --------------------------------------------------------------------------
 * Action helpers — the ONLY sanctioned way to render sub-bar buttons.
 * They bake in the Harbor 2 28 px toolbar size (Button size sm) and the
 * variants, so every page's actions look identical.
 * ------------------------------------------------------------------------ */

type ActionProps = React.ComponentProps<typeof Button> & { icon?: LucideIcon }

/** Primary CTA — soft/tinted. At most one per page. */
export function SubBarPrimary({ icon: Icon, className, children, asChild, ...props }: ActionProps) {
  return (
    // Harbor: the page's one primary action is the solid brand button.
    <Button asChild={asChild} variant="default" size="sm" className={cn("gap-1.5 px-3", className)} {...props}>
      {Icon && <Icon className="h-3 w-3" />}
      {asChild ? <Slot.Slottable>{children}</Slot.Slottable> : children}
    </Button>
  )
}

/** Secondary / neutral action (Import, secondary create). */
export function SubBarSecondary({ icon: Icon, className, children, asChild, ...props }: ActionProps) {
  return (
    <Button asChild={asChild} variant="ghost" size="sm" className={cn("gap-1.5", className)} {...props}>
      {Icon && <Icon className="h-3 w-3" />}
      {asChild ? <Slot.Slottable>{children}</Slot.Slottable> : children}
    </Button>
  )
}

/** Utility icon button (settings gear, panel toggles…). */
export function SubBarIconButton({ icon: Icon, className, children, ...props }: ActionProps) {
  return (
    <Button variant="ghost" size="icon-sm" className={className} {...props}>
      {Icon ? <Icon className="h-3.5 w-3.5" /> : children}
    </Button>
  )
}
