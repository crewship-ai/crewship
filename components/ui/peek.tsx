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
  className,
  children,
}: {
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
        onKeyDown={onKeyDown}
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
