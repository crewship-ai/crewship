"use client"

import * as React from "react"
import { Slot } from "radix-ui"

import { cn } from "@/lib/utils"
import { useIsMobile } from "@/hooks/use-mobile"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuSub,
  DropdownMenuSubContent,
  DropdownMenuSubTrigger,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { Sheet, SheetContent, SheetTitle, SheetTrigger } from "@/components/ui/sheet"

/**
 * A menu that is a popover on a tablet or desktop and a bottom sheet on a
 * phone (< 768 px) - the kit's one answer for "this menu, on a phone"
 * (Harbor 2 chrome-states G12, v67 `pop-user`). The same children serve both:
 * write the rows once with MenuSheetItem / MenuSheetSeparator / MenuSheetSub.
 *
 *  - Popover: a Radix menu (role menu / menuitem, arrow keys, typeahead).
 *  - Sheet: a bottom dialog named by `label`, the rows 44 px high, a handle
 *    on top, scrolling inside 85% of the screen. Rows are links and buttons,
 *    not menu items: a sheet is read as a list, not navigated with arrows.
 *    A sub-menu opens out as a labelled group instead of a flyout.
 *
 * Picking a row closes the menu unless its `onSelect` calls preventDefault.
 */

type Mode = { sheet: boolean; close: () => void }
const ModeContext = React.createContext<Mode>({ sheet: false, close: () => {} })

export function MenuSheet({
  open,
  onOpenChange,
  label,
  trigger,
  children,
  popoverSide = "bottom",
  popoverAlign = "end",
  popoverOffset = 4,
  popoverClassName,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  /** The sheet's accessible name ("User menu"). */
  label: string
  /** The one element that opens the menu; it receives the trigger behaviour. */
  trigger: React.ReactElement
  children: React.ReactNode
  popoverSide?: "top" | "right" | "bottom" | "left"
  popoverAlign?: "start" | "center" | "end"
  popoverOffset?: number
  popoverClassName?: string
}) {
  const isMobile = useIsMobile()
  const close = React.useCallback(() => onOpenChange(false), [onOpenChange])
  const mode = React.useMemo<Mode>(() => ({ sheet: isMobile, close }), [isMobile, close])

  if (isMobile) {
    return (
      <ModeContext.Provider value={mode}>
        <Sheet open={open} onOpenChange={onOpenChange}>
          <SheetTrigger asChild>{trigger}</SheetTrigger>
          <SheetContent
            side="bottom"
            showCloseButton={false}
            data-menu-sheet
            className="menu-sheet max-h-[85dvh] gap-0 rounded-t-xl p-0"
            aria-describedby={undefined}
          >
            <div className="mx-auto mb-1 mt-3 h-1.5 w-12 shrink-0 rounded-full bg-border" aria-hidden />
            <SheetTitle className="sr-only">{label}</SheetTitle>
            <div className="min-h-0 flex-1 overflow-y-auto px-1 pb-[max(0.5rem,env(safe-area-inset-bottom))]">{children}</div>
          </SheetContent>
        </Sheet>
      </ModeContext.Provider>
    )
  }
  return (
    <ModeContext.Provider value={mode}>
      <DropdownMenu open={open} onOpenChange={onOpenChange}>
        <DropdownMenuTrigger asChild>{trigger}</DropdownMenuTrigger>
        <DropdownMenuContent align={popoverAlign} side={popoverSide} sideOffset={popoverOffset} className={popoverClassName}>
          {children}
        </DropdownMenuContent>
      </DropdownMenu>
    </ModeContext.Provider>
  )
}

const SHEET_ROW =
  "flex min-h-[44px] w-full items-center gap-3 rounded-md px-3 py-2 text-left text-sm outline-none transition-colors hover:bg-accent focus-visible:bg-accent [&_svg]:shrink-0"

type SelectEvent = { preventDefault: () => void }

/**
 * One row. `asChild` makes the child (a Link, an anchor) the row. `checked`
 * turns it into a switch row (menuitemcheckbox in the popover, role switch in
 * the sheet). `className` styles the popover row only.
 */
export function MenuSheetItem({
  asChild,
  checked,
  className,
  onSelect,
  children,
  ...rest
}: {
  asChild?: boolean
  checked?: boolean
  className?: string
  onSelect?: (event: SelectEvent) => void
  children: React.ReactNode
} & Omit<React.ComponentProps<"button">, "onSelect" | "className" | "children">) {
  const { sheet, close } = React.useContext(ModeContext)
  if (!sheet) {
    return (
      <DropdownMenuItem
        asChild={asChild}
        className={className}
        onSelect={onSelect}
        data-menu-row
        {...(checked !== undefined ? { role: "menuitemcheckbox", "aria-checked": checked } : {})}
        {...(rest as object)}
      >
        {children}
      </DropdownMenuItem>
    )
  }
  const pick = (e: React.MouseEvent<HTMLElement>) => {
    rest.onClick?.(e as React.MouseEvent<HTMLButtonElement>)
    let prevented = false
    onSelect?.({ preventDefault: () => { prevented = true } })
    if (!prevented) close()
  }
  const Comp = asChild ? Slot.Root : "button"
  return (
    <Comp
      {...(asChild ? {} : { type: "button" as const })}
      {...rest}
      {...(checked !== undefined ? { role: "switch", "aria-checked": checked } : {})}
      data-menu-row
      className={SHEET_ROW}
      onClick={pick}
    >
      {children}
    </Comp>
  )
}

export function MenuSheetSeparator() {
  const { sheet } = React.useContext(ModeContext)
  return sheet ? <div role="separator" className="mx-3 my-1 h-px bg-border" /> : <DropdownMenuSeparator className="my-0" />
}

/**
 * A group the popover shows as a flyout and the sheet opens out in place under
 * its label. `trigger` is the flyout's row content (icon + label).
 */
export function MenuSheetSub({
  label,
  trigger,
  triggerClassName,
  contentClassName,
  children,
}: {
  label: string
  trigger: React.ReactNode
  triggerClassName?: string
  contentClassName?: string
  children: React.ReactNode
}) {
  const { sheet } = React.useContext(ModeContext)
  if (sheet) {
    return (
      <div role="group" aria-label={label}>
        <div className="flex min-h-9 items-center gap-3 px-3 pt-2 text-micro font-semibold text-muted-foreground-soft" aria-hidden>
          {label}
        </div>
        {children}
      </div>
    )
  }
  return (
    <DropdownMenuSub>
      <DropdownMenuSubTrigger className={triggerClassName}>{trigger}</DropdownMenuSubTrigger>
      <DropdownMenuSubContent className={contentClassName}>{children}</DropdownMenuSubContent>
    </DropdownMenuSub>
  )
}

/** A padded block inside the menu (a header, the theme row) - popover and sheet. */
export function MenuSheetBlock({ className, children }: { className?: string; children: React.ReactNode }) {
  return <div className={cn("p-1", className)}>{children}</div>
}
