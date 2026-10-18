"use client"

import * as React from "react"
import { ChevronDown, ChevronUp, Maximize2 } from "lucide-react"

import { cn } from "@/lib/utils"
import { useIsMobile } from "@/hooks/use-mobile"
import { Button } from "@/components/ui/button"
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet"

/**
 * Peek — the one side sheet for looking at an item without leaving the list
 * (Harbor 2 kit, #3366). 480 px from the right; a bottom sheet on a phone.
 * J / K move to the next / previous item, Enter opens the full page, Esc
 * closes. Lists pass `onPrev` / `onNext` when the item has neighbours.
 */
export function Peek({
  open,
  onOpenChange,
  title,
  subtitle,
  onPrev,
  onNext,
  onOpenFull,
  actions,
  footer,
  initialFocus,
  returnFocus = false,
  className,
  children,
}: {
  /**
   * Where focus lands when the Peek opens. Default: Radix's first focusable
   * control (the Previous / Next / Open buttons). `"content"` focuses the
   * sheet itself, so J / K / Enter work at once without a control looking
   * selected; `"none"` leaves focus where it was (a list that keeps its own
   * roving focus); a ref focuses that element.
   */
  initialFocus?: "content" | "none" | React.RefObject<HTMLElement | null>
  /**
   * On close, focus returns to the element that had it when the Peek opened
   * (a list row, a search field). A Peek has no trigger, so without this
   * Radix drops focus to the page. If the person has already moved focus
   * somewhere else by then, it is left where they put it.
   */
  returnFocus?: boolean
  open: boolean
  onOpenChange: (open: boolean) => void
  title: React.ReactNode
  subtitle?: React.ReactNode
  onPrev?: () => void
  onNext?: () => void
  /** Opens the item's own page (Enter). */
  onOpenFull?: () => void
  actions?: React.ReactNode
  footer?: React.ReactNode
  className?: string
  children?: React.ReactNode
}) {
  const isMobile = useIsMobile()
  const contentRef = React.useRef<HTMLDivElement>(null)

  // Layout effect: runs before Radix moves focus into the sheet, so the opener
  // is still the active element. The cleanup runs on close or unmount.
  React.useLayoutEffect(() => {
    if (!open || !returnFocus) return
    const opener = document.activeElement instanceof HTMLElement ? document.activeElement : null
    return () => {
      if (!opener || opener === document.body) return
      window.setTimeout(() => {
        const now = document.activeElement
        if (opener.isConnected && (!now || now === document.body)) opener.focus({ preventScroll: true })
      }, 0)
    }
  }, [open, returnFocus])

  const onKeyDown = (e: React.KeyboardEvent) => {
    const t = e.target as HTMLElement
    if (t.closest("input, textarea, select, [contenteditable=true]")) return
    if (e.metaKey || e.ctrlKey || e.altKey) return
    if (e.key === "j" && onNext) { e.preventDefault(); onNext() }
    else if (e.key === "k" && onPrev) { e.preventDefault(); onPrev() }
    else if (e.key === "Enter" && onOpenFull && t === e.currentTarget) { e.preventDefault(); onOpenFull() }
  }

  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent
        side={isMobile ? "bottom" : "right"}
        data-slot="peek"
        ref={contentRef}
        tabIndex={-1}
        onKeyDown={onKeyDown}
        onOpenAutoFocus={initialFocus === undefined ? undefined : (e) => {
          e.preventDefault()
          if (initialFocus === "none") return
          const target = initialFocus === "content" ? contentRef.current : initialFocus.current
          target?.focus({ preventScroll: true })
        }}
        className={cn(
          "flex flex-col gap-0 p-0",
          isMobile ? "max-h-[92dvh] rounded-t-xl" : "w-[480px] max-w-[calc(100vw-32px)] sm:max-w-[480px]",
          className,
        )}
      >
        <SheetHeader className="flex-row items-start gap-2 border-b border-border px-4 py-3 text-left">
          <div className="min-w-0 flex-1">
            <SheetTitle className="truncate text-sm font-semibold">{title}</SheetTitle>
            {subtitle ? (
              <SheetDescription className="truncate text-label">{subtitle}</SheetDescription>
            ) : (
              <SheetDescription className="sr-only">Details</SheetDescription>
            )}
          </div>
          <div className="mr-8 flex shrink-0 items-center gap-1">
            {onPrev && (
              <Button variant="ghost" size="icon-sm" onClick={onPrev} aria-label="Previous (K)" aria-keyshortcuts="k">
                <ChevronUp className="size-4" />
              </Button>
            )}
            {onNext && (
              <Button variant="ghost" size="icon-sm" onClick={onNext} aria-label="Next (J)" aria-keyshortcuts="j">
                <ChevronDown className="size-4" />
              </Button>
            )}
            {onOpenFull && (
              <Button variant="ghost" size="icon-sm" onClick={onOpenFull} aria-label="Open full page (Enter)" aria-keyshortcuts="Enter">
                <Maximize2 className="size-4" />
              </Button>
            )}
          </div>
        </SheetHeader>
        {actions && <div className="flex flex-wrap items-center gap-2 border-b border-border px-4 py-2">{actions}</div>}
        <div className="min-h-0 flex-1 overflow-auto px-4 py-4">{children}</div>
        {footer && <div className="border-t border-border px-4 py-3">{footer}</div>}
      </SheetContent>
    </Sheet>
  )
}
