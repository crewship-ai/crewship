"use client"

import * as React from "react"
import { PanelLeftClose, PanelLeftOpen } from "lucide-react"

import { cn } from "@/lib/utils"
import { useIsMobile } from "@/hooks/use-mobile"
import { useUserPreference } from "@/hooks/use-user-preference"
import { useAppStore } from "@/lib/store"
import { PanelSpaces, type PanelSpacesConfig } from "@/components/layout/panel-spaces"
import { Sheet, SheetContent, SheetHeader, SheetTitle } from "@/components/ui/sheet"

/** Lets the kit's SidebarToolbar know it is the panel's toolbar: it then
 *  takes the 48 px toolbar shape and appends the panel's collapse button. */
export const SecondaryPanelContext = React.createContext<{ toggle: React.ReactNode; claim: () => () => void } | null>(null)

/** Tablet width (Harbor 2 breakpoints, #3358): between the phone and 1280. */
const TABLET_QUERY = "(min-width: 768px) and (max-width: 1279px)"

/**
 * True at tablet width. There the SecondaryPanel starts closed and floats
 * over the content, opened from the top bar's panel button (COM-05).
 */
export function useIsTabletPanel(): boolean {
  const [on, setOn] = React.useState(false)
  React.useEffect(() => {
    if (typeof window === "undefined" || typeof window.matchMedia !== "function") return
    const mql = window.matchMedia(TABLET_QUERY)
    const update = () => setOn(mql.matches)
    update()
    mql.addEventListener?.("change", update)
    return () => mql.removeEventListener?.("change", update)
  }, [])
  return on
}

/**
 * SecondaryPanel — the one second column every section uses (Harbor 2, #3350
 * M3). 272 px wide, 44 px when collapsed; a 48 px toolbar at the height of the
 * top bar (search `/`, Filter, page tools, collapse), then views, places and
 * facets, and an optional sticky footer for bulk actions.
 *
 * - Collapse is remembered per user and section (`panel.<id>.collapsed`), so
 *   it survives a reload and follows the user to another device.
 * - On a phone the same content opens as a bottom sheet from the top bar's
 *   panel button (AppToolbar reads the registration below); there is one phone
 *   pattern instead of four.
 * - On a tablet (768–1279) the panel starts closed and opens from the same
 *   top-bar button, floating over the content (v67 `panelBtn`); its close
 *   button, Escape or a click outside closes it. The desktop collapse
 *   preference does not apply there.
 *
 * Content is composed from components/layout/sidebar-kit.
 */
export function SecondaryPanel<S extends string = string>({
  id,
  label,
  toolbar,
  footer,
  phoneInline = false,
  spaces,
  className,
  children,
}: {
  /** Stable section id: "issues", "inbox"… keys the remembered state. */
  id: string
  /** Accessible name; also the phone sheet's title. */
  label: string
  /** Search, Filter and page tools; the collapse button is added at the end.
   *  Omit it when the content starts with the kit's own SidebarToolbar, which
   *  then becomes the panel toolbar (existing explorers keep their markup). */
  toolbar?: React.ReactNode
  /** Sticky bottom area (bulk actions while several rows are selected). */
  footer?: React.ReactNode
  /** Phone only: show the content as the page itself instead of a sheet —
   *  for sections whose panel IS the list (Inbox), while nothing is open.
   *  List → detail → back (PNL-05). */
  phoneInline?: boolean
  /** Two to nine places that each show a different page body, listed first in
   *  the body (under the toolbar) and switched on keys 1-9 (PNL-02). */
  spaces?: PanelSpacesConfig<S>
  className?: string
  children?: React.ReactNode
}) {
  const spacesNode = spaces ? <PanelSpaces<S> {...spaces} /> : null
  const isMobile = useIsMobile()
  const tablet = useIsTabletPanel() && !isMobile
  // Whether the content brought its own kit toolbar (which then carries the
  // collapse button); otherwise the panel draws a toolbar for it.
  const [claimed, setClaimed] = React.useState(0)
  const claim = React.useCallback(() => {
    setClaimed((n) => n + 1)
    return () => setClaimed((n) => n - 1)
  }, [])
  const [collapsed, setCollapsed] = useUserPreference<boolean>(`panel.${id}.collapsed`, false)
  const register = useAppStore((s) => s.setSecondaryPanel)
  const sheetOpen = useAppStore((s) => s.secondaryPanelOpen)
  const setSheetOpen = useAppStore((s) => s.setSecondaryPanelOpen)

  const inlineOnPhone = isMobile && phoneInline
  React.useEffect(() => {
    if (inlineOnPhone) return
    register({ id, label })
    return () => {
      register(null)
      setSheetOpen(false)
    }
  }, [id, label, register, setSheetOpen, inlineOnPhone])

  if (inlineOnPhone) {
    return (
      <section data-slot="secondary-panel" data-panel-id={id} data-phone="inline" aria-label={label} className={cn("secondary-panel-phone", className)}>
        <SecondaryPanelContext.Provider value={{ toggle: null, claim }}>
          {toolbar ? (
            <>
              <div className="secondary-panel-toolbar">{toolbar}</div>
              <div className="secondary-panel-body">{spacesNode}{children}</div>
            </>
          ) : (
            <div className="secondary-panel-content">{spacesNode}{children}</div>
          )}
        </SecondaryPanelContext.Provider>
        {footer && <div className="secondary-panel-footer">{footer}</div>}
      </section>
    )
  }

  if (isMobile) {
    return (
      <Sheet open={sheetOpen} onOpenChange={setSheetOpen}>
        <SheetContent side="bottom" className="max-h-[85dvh] gap-0 rounded-t-xl p-0" data-slot="secondary-panel-sheet">
          <SheetHeader className="border-b border-border px-4 py-3">
            <SheetTitle className="text-sm">{label}</SheetTitle>
          </SheetHeader>
          <SecondaryPanelContext.Provider value={{ toggle: null, claim }}>
            {toolbar ? (
              <>
                <div className="secondary-panel-toolbar">{toolbar}</div>
                <div className="secondary-panel-body">{spacesNode}{children}</div>
              </>
            ) : (
              <div className="secondary-panel-content">{spacesNode}{children}</div>
            )}
          </SecondaryPanelContext.Provider>
          {footer && <div className="secondary-panel-footer">{footer}</div>}
        </SheetContent>
      </Sheet>
    )
  }

  if (tablet) {
    if (!sheetOpen) return null
    const close = () => setSheetOpen(false)
    const closeButton = (
      <button
        type="button"
        onClick={close}
        aria-label={`Close ${label}`}
        title="Close panel"
        className="secondary-panel-toggle"
      >
        <PanelLeftClose aria-hidden />
      </button>
    )
    return (
      <>
        {/* A click outside closes it; not a control of its own (Escape and
            the close button are the keyboard ways). */}
        <div aria-hidden data-testid="secondary-panel-scrim" className="secondary-panel-scrim" onClick={close} />
        <aside
          data-slot="secondary-panel"
          data-panel-id={id}
          data-collapsed="false"
          data-float="true"
          aria-label={label}
          className={cn("secondary-panel secondary-panel-float", className)}
          onKeyDown={(e) => {
            if (e.key !== "Escape") return
            e.stopPropagation()
            close()
          }}
        >
          <SecondaryPanelContext.Provider value={{ toggle: closeButton, claim }}>
            {toolbar ? (
              <>
                <div className="secondary-panel-toolbar">
                  {toolbar}
                  <span className="ml-auto" />
                  {closeButton}
                </div>
                <div className="secondary-panel-body">{spacesNode}{children}</div>
              </>
            ) : (
              <>
                {claimed === 0 && (
                  <div className="secondary-panel-toolbar">
                    <span className="ml-auto" />
                    {closeButton}
                  </div>
                )}
                <div className="secondary-panel-content">{spacesNode}{children}</div>
              </>
            )}
            {footer && <div className="secondary-panel-footer">{footer}</div>}
          </SecondaryPanelContext.Provider>
        </aside>
      </>
    )
  }

  const toggle = (
    <button
      type="button"
      onClick={() => setCollapsed(!collapsed)}
      aria-label={collapsed ? `Show ${label}` : `Hide ${label}`}
      aria-expanded={!collapsed}
      title={collapsed ? "Show panel" : "Hide panel"}
      className="secondary-panel-toggle"
    >
      {collapsed ? <PanelLeftOpen aria-hidden /> : <PanelLeftClose aria-hidden />}
    </button>
  )

  return (
    <aside
      data-slot="secondary-panel"
      data-panel-id={id}
      data-collapsed={collapsed ? "true" : "false"}
      aria-label={label}
      className={cn("secondary-panel", className)}
    >
      {collapsed ? (
        <div className="secondary-panel-rail">{toggle}</div>
      ) : (
        <SecondaryPanelContext.Provider value={{ toggle, claim }}>
          {toolbar ? (
            <>
              <div className="secondary-panel-toolbar">
                {toolbar}
                <span className="ml-auto" />
                {toggle}
              </div>
              <div className="secondary-panel-body">{spacesNode}{children}</div>
            </>
          ) : (
            <>
              {claimed === 0 && (
                <div className="secondary-panel-toolbar">
                  <span className="ml-auto" />
                  {toggle}
                </div>
              )}
              <div className="secondary-panel-content">{spacesNode}{children}</div>
            </>
          )}
          {footer && <div className="secondary-panel-footer">{footer}</div>}
        </SecondaryPanelContext.Provider>
      )}
    </aside>
  )
}

/** For a phone row that opens something: close the panel sheet first. */
export function useCloseSecondaryPanelSheet(): () => void {
  const setOpen = useAppStore((s) => s.setSecondaryPanelOpen)
  return React.useCallback(() => setOpen(false), [setOpen])
}
