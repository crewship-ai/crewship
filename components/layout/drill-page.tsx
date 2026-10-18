"use client"

import * as React from "react"
import Link from "next/link"
import { AnimatePresence, motion, useReducedMotion } from "motion/react"
import { ArrowLeft, ChevronDown, type LucideIcon } from "lucide-react"

import { cn } from "@/lib/utils"
import { useIsMobile } from "@/hooks/use-mobile"
import { SubBar } from "@/components/layout/sub-bar"
import { SidebarToolbar } from "@/components/layout/sidebar-kit"
import { SidebarRowTile } from "@/components/layout/row-tile"
import { PanelSpaces, type PanelSpacesConfig } from "@/components/layout/panel-spaces"
import { SecondaryPanel, useCloseSecondaryPanelSheet, useIsTabletPanel } from "@/components/layout/secondary-panel"
import { PageSaveBar, PageSaveProvider, usePageSaveGuard } from "@/components/ui/page-save-bar"

/**
 * A nested page: one level deeper than the page it came from, with its own
 * side panel instead of an extra column.
 *
 * The rule it keeps: at most two navigation columns on screen — the app rail
 * and ONE side panel. Opening a nested page swaps the parent's panel (e.g.
 * the Settings sections) for this page's own. The way out is a bordered
 * "← <parent>" control that always goes to the parent, not to browser
 * history. Never nest a nested page.
 *
 * The side panel is the shared SecondaryPanel (Harbor 2, #3350 M3): 272 px,
 * collapse remembered per user as `panel.<panelId>.collapsed`, and on a phone
 * the same content in a bottom sheet opened from the top bar's panel button.
 * Inside it (PNL-02, v67 `pk-back`) the "← <parent>" row comes first, above
 * the page's `toolbar` (search, Filter; the collapse button is added), then
 * the `facets` slot (an inline facet block), then the `spaces` (keys 1-9),
 * then the page's DrillNavSections of DrillNavItems. On a phone, and on a
 * page without a panel, the way back stays at the start of the sub-bar, and
 * `mobileNav` (usually the page's view tabs) stays on the page.
 *
 * The page owns its head (SET-07, PRD SHL-05): the top bar is the path
 * ("Settings / Members"; on a phone just the page, after the arrow back) and
 * the section head - icon tile, title, one sentence, `note` on the right - is
 * drawn here, once, before the page's data arrives. `head={false}` leaves it
 * out for a page that shows only a no-access state.
 *
 * Used by Settings › Crew links, Members, Audit log and Lifecycle hooks, and
 * Admin › People & workspaces, Security and Backups.
 */
export function DrillPage<S extends string = string>({
  parent,
  title,
  icon: Icon,
  description,
  note,
  head = true,
  panelId,
  toolbar,
  facets,
  spaces,
  nav,
  mobileNav,
  actions,
  children,
  className,
}: {
  /** Where the back arrow goes, and what it is called. */
  parent: { href: string; label: string; icon?: LucideIcon }
  title: string
  icon: LucideIcon
  /** The head's one sentence about what the page is for. */
  description?: string
  /** Right of the head: who may change what is here. */
  note?: React.ReactNode
  /** Draw the section head (default). False: a page with only a no-access state. */
  head?: boolean
  /** SecondaryPanel id, `drill-<route segment>` ("drill-crew-links"); it keys
   *  the remembered collapse. Defaults to one derived from `title`. */
  panelId?: string
  /** The panel's toolbar — SidebarSearch, a SidebarFilterPopover. The
   *  collapse button is added here. */
  toolbar?: React.ReactNode
  /** Directly under the toolbar, above the spaces and sections: the inline
   *  facet block (SidebarFacetBlock). Not inside the sections nav. */
  facets?: React.ReactNode
  /** The panel's spaces (By crew / All links / Matrix), on keys 1-9. */
  spaces?: PanelSpacesConfig<S>
  /** The side panel's sections — DrillNavSection / DrillNavItem. Leave it
   *  out for a page whose content is its own map (Lifecycle hooks): no panel. */
  nav?: React.ReactNode
  /** What stays on the page on a phone, above the content. */
  mobileNav?: React.ReactNode
  /** @deprecated The phone "Filters" button is gone (the top bar's panel
   *  button opens the panel sheet); accepted and ignored. */
  filterCount?: number
  /** Page actions, on the sub-bar. */
  actions?: React.ReactNode
  children: React.ReactNode
  className?: string
}) {
  const isMobile = useIsMobile()
  const id = panelId ?? `drill-${slugify(title)}`
  // Runs before the panel reads its remembered state (a child renders after
  // this initializer): the collapse set under the old key carries over once.
  React.useState(() => migrateDrillCollapse(title, id))
  const closeSheet = useCloseSecondaryPanelSheet()
  const hasPanel = nav != null || facets != null || spaces != null
  const tablet = useIsTabletPanel() && !isMobile
  // On a desktop with a panel the way back is the panel's first row; on a
  // phone the panel is a sheet and on a tablet it floats, closed until the top
  // bar's panel button opens it, so there the page keeps it on the sub-bar.
  const backOnSubBar = isMobile || tablet || !hasPanel

  return (
    // One Save for the nested page, as in Settings (components/ui/page-save-bar).
    <PageSaveProvider>
    <div className="flex h-[calc(100dvh-var(--app-header-h)-var(--mobile-tab-bar-h))] flex-col">
      {/* The path: "Settings / Members". On a phone the top bar already names
          the area and the arrow is the way back, so the page is just its title. */}
      <SubBar
        icon={Icon}
        title={isMobile ? title : parent.label}
        section={isMobile ? undefined : title}
        ariaLabel={`${parent.label} / ${title}`}
        actions={actions}
        leading={
          backOnSubBar ? (
            <span className="flex items-center gap-2">
              <DrillBack parent={parent} compact={isMobile} />
              <span className="text-muted-foreground-soft" aria-hidden>/</span>
            </span>
          ) : undefined
        }
      />
      <div className="flex min-h-0 flex-1">
        {hasPanel && (
          <SecondaryPanel id={id} label={`${title} navigation`}>
            {/* v67 pk-back: the way back is the panel's first row, above the
                toolbar. On a phone the sheet has the arrow on the sub-bar. */}
            {!backOnSubBar && (
              <div className="px-2 pt-2" data-slot="drill-back-row">
                <DrillBack parent={parent} />
              </div>
            )}
            {toolbar && <SidebarToolbar>{toolbar}</SidebarToolbar>}
            <div className="min-h-0 flex-1 overflow-y-auto pb-4">
              {facets}
              {spaces && <PanelSpaces<S> {...spaces} />}
              {/* A pick inside the phone sheet closes it: the result is what the
                  person wants to see next. On a desktop this does nothing. */}
              <nav
                aria-label={`${title} sections`}
                onClick={(e) => { if ((e.target as HTMLElement).closest("[data-drill-close]")) closeSheet() }}
              >
                {nav}
              </nav>
            </div>
          </SecondaryPanel>
        )}
        <div className="relative min-h-0 min-w-0 flex-1">
          {/* A <div>, not a <main>: the shell's SidebarInset is the page's one
              main landmark (ADM-08). */}
          <div data-slot="drill-content" className={cn("h-full overflow-y-auto pb-20", className)}>
            {isMobile && mobileNav && (
              <div className="sticky top-0 z-10 border-b border-border bg-background/95 px-3 py-2 backdrop-blur">{mobileNav}</div>
            )}
            {head && <DrillHead icon={Icon} title={title} description={description} note={note} />}
            {children}
          </div>
          <PageSaveBar />
        </div>
      </div>
    </div>
    </PageSaveProvider>
  )
}

/** The section head: tile, title, one sentence, and who may change things. */
function DrillHead({ icon: Icon, title, description, note }: { icon: LucideIcon; title: string; description?: string; note?: React.ReactNode }) {
  return (
    <div data-slot="drill-head" className="flex flex-wrap items-center gap-3.5 px-4 pt-4 md:px-6 md:pt-6">
      <span className="page-tile page-tile-40" aria-hidden><Icon /></span>
      <div className="min-w-0 flex-[1_1_14rem]">
        <h2 className="text-title font-semibold leading-tight tracking-tight text-foreground">{title}</h2>
        {description && <p className="mt-0.5 text-body text-muted-foreground">{description}</p>}
      </div>
      {note != null && <span className="shrink-0 text-label text-muted-foreground" data-slot="drill-head-note">{note}</span>}
    </div>
  )
}

/** The plain "← <parent>" row (v67); the arrow alone when `compact`. */
function DrillBack({ parent, compact = false }: { parent: { href: string; label: string; icon?: LucideIcon }; compact?: boolean }) {
  const BackIcon = parent.icon
  return (
    <Link
      href={parent.href}
      data-slot="drill-back"
      aria-label={`Back to ${parent.label}`}
      className={cn(
        "inline-flex h-7 items-center gap-1.5 rounded-md text-xs font-medium text-muted-foreground transition-colors hover:bg-foreground/[0.04] hover:text-foreground",
        compact ? "w-7 justify-center" : "pl-1.5 pr-2.5",
      )}
    >
      <ArrowLeft className="h-3.5 w-3.5" />
      {!compact && (
        <>
          {BackIcon && <BackIcon className="h-3.5 w-3.5 text-muted-foreground" aria-hidden />}
          {parent.label}
        </>
      )}
    </Link>
  )
}

function slugify(s: string): string {
  return s.toLowerCase().replace(/[^a-z0-9]+/g, "-").replace(/^-|-$/g, "")
}

/**
 * The collapse used to be a per-browser flag, `drill-collapsed:<title>`
 * ("1" / "0"). SecondaryPanel remembers it per user as
 * `panel.<panelId>.collapsed`. When that was never set, the old value becomes
 * its starting point; the old key is then dropped, so this happens once.
 */
export function migrateDrillCollapse(title: string, panelId: string): void {
  try {
    const legacyKey = `drill-collapsed:${title}`
    const legacy = window.localStorage.getItem(legacyKey)
    if (legacy == null) return
    const key = `crewship.pref.panel.${panelId}.collapsed`
    if (window.localStorage.getItem(key) == null) {
      window.localStorage.setItem(key, JSON.stringify(legacy === "1"))
    }
    window.localStorage.removeItem(legacyKey)
  } catch { /* storage blocked */ }
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
  const headClass = "flex w-full items-center gap-1.5 px-2 pb-1 pt-1 type-group-head text-muted-foreground-soft"
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
  tile,
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
  /** An icon on the kit's 24 px row tile (v67); `icon` is for anything else. */
  tile?: LucideIcon
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
  // A pick here swaps what the page shows; with edits pending, ask first.
  const guard = usePageSaveGuard()
  return (
    <button
      type="button"
      onClick={() => guard(onSelect)}
      data-drill-close
      aria-current={pressed === undefined && selected ? "true" : undefined}
      aria-pressed={pressed}
      title={title}
      style={{ animationDelay: `${Math.min(index, 12) * 18}ms` }}
      className={cn(
        // kit-tap: 44px under a coarse pointer (globals.css), desktop density
        // untouched — these rows are the panel's whole navigation on a phone.
        "kit-tap drill-row-in row-interactive row-hover min-h-[30px] w-full items-center gap-2 rounded-md px-2 py-1 text-left text-[13px]",
        selected && "row-selected font-medium",
        muted && !selected && "text-muted-foreground",
      )}
    >
      {tile ? <SidebarRowTile icon={tile} active={selected} /> : icon}
      <span className="min-w-0 flex-1">
        <span className="block truncate">{label}</span>
        {sub != null && <span className="block truncate text-[11px] font-normal text-muted-foreground">{sub}</span>}
      </span>
      {meta != null && <span className="shrink-0 text-[11px] tabular-nums text-muted-foreground">{meta}</span>}
    </button>
  )
}
