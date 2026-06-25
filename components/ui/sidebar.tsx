"use client"

import * as React from "react"
import { PanelLeftIcon } from "lucide-react"

import { useIsMobile } from "@/hooks/use-mobile"
import { cn } from "@/lib/utils"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from "@/components/ui/sheet"
import { TooltipProvider } from "@/components/ui/tooltip"

import {
  SidebarContent,
  SidebarFooter,
  SidebarGroup,
  SidebarGroupAction,
  SidebarGroupContent,
  SidebarGroupLabel,
  SidebarHeader,
  SidebarSeparator,
} from "./sidebar-sections"
import {
  SidebarMenu,
  SidebarMenuAction,
  SidebarMenuBadge,
  SidebarMenuButton,
  SidebarMenuItem,
  SidebarMenuSkeleton,
  SidebarMenuSub,
  SidebarMenuSubButton,
  SidebarMenuSubItem,
} from "./sidebar-menu"


const SIDEBAR_COOKIE_NAME = "sidebar_state"
const SIDEBAR_COOKIE_MAX_AGE = 60 * 60 * 24 * 7
const SIDEBAR_WIDTH = "13.25rem"
const SIDEBAR_WIDTH_MOBILE = "18rem"
const SIDEBAR_WIDTH_ICON = "4rem"
const SIDEBAR_KEYBOARD_SHORTCUT = "b"
const SIDEBAR_MODE_KEY = "crewship_sidebar_mode"


type SidebarMode = "hover" | "collapsed" | "pinned"

type SidebarContextProps = {
  state: "expanded" | "collapsed"
  open: boolean
  setOpen: (open: boolean) => void
  openMobile: boolean
  setOpenMobile: (open: boolean) => void
  isMobile: boolean
  toggleSidebar: () => void
  hoverExpanded: boolean
  setHoverExpanded: (expanded: boolean) => void
  isPopoverOpen: boolean
  setPopoverOpen: (open: boolean) => void
  sidebarMode: SidebarMode
  setSidebarMode: (mode: SidebarMode) => void
}

const SidebarContext = React.createContext<SidebarContextProps | null>(null)


function useSidebar() {
  const context = React.useContext(SidebarContext)
  if (!context) {
    throw new Error("useSidebar must be used within a SidebarProvider.")
  }

  return context
}


function SidebarProvider({
  defaultOpen: _defaultOpen = true,
  open: openProp,
  onOpenChange: setOpenProp,
  className,
  style,
  children,
  ...props
}: React.ComponentProps<"div"> & {
  defaultOpen?: boolean
  open?: boolean
  onOpenChange?: (open: boolean) => void
}) {
  const isMobile = useIsMobile()
  const [openMobile, setOpenMobile] = React.useState(false)
  const [hoverExpanded, setHoverExpanded] = React.useState(false)

  // Tracks popovers/dropdowns anchored inside the sidebar (e.g. the
  // workspace switcher). While one is open the sidebar must NOT collapse
  // a hover-expanded panel out from under it: those menus portal outside
  // the sidebar element, so moving the cursor onto them fires the
  // sidebar's mouseleave and would otherwise yank the panel shut
  // mid-interaction. Ref-counted so concurrent popovers don't clobber
  // each other's open state.
  const popoverCountRef = React.useRef(0)
  const [isPopoverOpen, setIsPopoverOpen] = React.useState(false)
  const setPopoverOpen = React.useCallback((open: boolean) => {
    popoverCountRef.current = Math.max(0, popoverCountRef.current + (open ? 1 : -1))
    setIsPopoverOpen(popoverCountRef.current > 0)
  }, [])

  // Sidebar mode: hover (default), collapsed, pinned — persisted in localStorage
  const [sidebarMode, _setSidebarMode] = React.useState<SidebarMode>(() => {
    if (typeof window === "undefined") return "hover"
    const stored = localStorage.getItem(SIDEBAR_MODE_KEY)
    if (stored === "collapsed" || stored === "pinned" || stored === "hover") return stored
    return "hover"
  })

  const setSidebarMode = React.useCallback((mode: SidebarMode) => {
    _setSidebarMode(mode)
    localStorage.setItem(SIDEBAR_MODE_KEY, mode)
  }, [])

  // Derive open state from mode (pinned = open, hover/collapsed = closed)
  const [_open, _setOpen] = React.useState(sidebarMode === "pinned")
  const open = openProp ?? _open
  const setOpen = React.useCallback(
    (value: boolean | ((value: boolean) => boolean)) => {
      const openState = typeof value === "function" ? value(open) : value
      if (setOpenProp) {
        setOpenProp(openState)
      } else {
        _setOpen(openState)
      }

      document.cookie = `${SIDEBAR_COOKIE_NAME}=${openState}; path=/; max-age=${SIDEBAR_COOKIE_MAX_AGE}`
    },
    [setOpenProp, open]
  )

  // Sync open state when mode changes
  React.useEffect(() => {
    if (isMobile) return
    _setOpen(sidebarMode === "pinned")
  }, [sidebarMode, isMobile])

  // Helper to toggle the sidebar.
  const toggleSidebar = React.useCallback(() => {
    return isMobile ? setOpenMobile((open) => !open) : setOpen((open) => !open)
  }, [isMobile, setOpen, setOpenMobile])

  // Adds a keyboard shortcut to toggle the sidebar.
  React.useEffect(() => {
    const handleKeyDown = (event: KeyboardEvent) => {
      if (
        event.key === SIDEBAR_KEYBOARD_SHORTCUT &&
        (event.metaKey || event.ctrlKey)
      ) {
        event.preventDefault()
        // Cycle modes: hover → pinned → collapsed → hover
        if (!isMobile) {
          setSidebarMode(
            sidebarMode === "hover" ? "pinned" : sidebarMode === "pinned" ? "collapsed" : "hover"
          )
        } else {
          toggleSidebar()
        }
      }
    }

    window.addEventListener("keydown", handleKeyDown)
    return () => window.removeEventListener("keydown", handleKeyDown)
  }, [toggleSidebar, sidebarMode, setSidebarMode, isMobile])

  const state = open ? "expanded" : "collapsed"

  const contextValue = React.useMemo<SidebarContextProps>(
    () => ({
      state,
      open,
      setOpen,
      isMobile,
      openMobile,
      setOpenMobile,
      toggleSidebar,
      hoverExpanded,
      setHoverExpanded,
      isPopoverOpen,
      setPopoverOpen,
      sidebarMode,
      setSidebarMode,
    }),
    [state, open, setOpen, isMobile, openMobile, setOpenMobile, toggleSidebar, hoverExpanded, setHoverExpanded, isPopoverOpen, setPopoverOpen, sidebarMode, setSidebarMode]
  )

  return (
    <SidebarContext.Provider value={contextValue}>
      <TooltipProvider delayDuration={0}>
        <div
          data-slot="sidebar-wrapper"
          style={
            {
              "--sidebar-width": SIDEBAR_WIDTH,
              "--sidebar-width-icon": SIDEBAR_WIDTH_ICON,
              ...style,
            } as React.CSSProperties
          }
          className={cn(
            "group/sidebar-wrapper has-data-[variant=inset]:bg-sidebar flex h-svh w-full overflow-hidden",
            className
          )}
          {...props}
        >
          {children}
        </div>
      </TooltipProvider>
    </SidebarContext.Provider>
  )
}


function Sidebar({
  side = "left",
  variant = "sidebar",
  collapsible = "offcanvas",
  className,
  children,
  ...props
}: React.ComponentProps<"div"> & {
  side?: "left" | "right"
  variant?: "sidebar" | "floating" | "inset"
  collapsible?: "offcanvas" | "icon" | "none"
}) {
  const ctx = React.useContext(SidebarContext)!
  const { isMobile, state, openMobile, setOpenMobile, hoverExpanded, sidebarMode, isPopoverOpen } = ctx
  const hoverTimeoutRef = React.useRef<ReturnType<typeof setTimeout> | null>(null)
  // Whether the cursor is currently over the sidebar, and whether a
  // sidebar-anchored popover is open. Both are read inside event-handler
  // closures, so they live in refs to stay current without re-binding.
  const pointerInsideRef = React.useRef(false)
  const popoverOpenRef = React.useRef(isPopoverOpen)

  const scheduleCollapse = React.useCallback(() => {
    if (hoverTimeoutRef.current) {
      clearTimeout(hoverTimeoutRef.current)
      hoverTimeoutRef.current = null
    }
    hoverTimeoutRef.current = setTimeout(() => {
      ctx.setHoverExpanded(false)
    }, 250)
  }, [ctx])

  const handleMouseEnter = React.useCallback(() => {
    pointerInsideRef.current = true
    // Only hover-expand in "hover" mode
    if (sidebarMode !== "hover" || state !== "collapsed" || isMobile) return
    if (hoverTimeoutRef.current) {
      clearTimeout(hoverTimeoutRef.current)
      hoverTimeoutRef.current = null
    }
    hoverTimeoutRef.current = setTimeout(() => {
      ctx.setHoverExpanded(true)
    }, 80)
  }, [state, isMobile, ctx, sidebarMode])

  const handleMouseLeave = React.useCallback(() => {
    pointerInsideRef.current = false
    if (hoverTimeoutRef.current) {
      clearTimeout(hoverTimeoutRef.current)
      hoverTimeoutRef.current = null
    }
    // Keep the panel open while a popover anchored in the sidebar is up —
    // the cursor "left" only because it moved onto the portalled menu.
    if (popoverOpenRef.current) return
    scheduleCollapse()
  }, [scheduleCollapse])

  // Pin the hover panel open while a popover is open; once it closes,
  // collapse unless the cursor has returned inside the sidebar.
  React.useEffect(() => {
    popoverOpenRef.current = isPopoverOpen
    if (isPopoverOpen) {
      if (hoverTimeoutRef.current) {
        clearTimeout(hoverTimeoutRef.current)
        hoverTimeoutRef.current = null
      }
    } else if (!pointerInsideRef.current) {
      scheduleCollapse()
    }
  }, [isPopoverOpen, scheduleCollapse])

  React.useEffect(() => {
    return () => {
      if (hoverTimeoutRef.current) clearTimeout(hoverTimeoutRef.current)
    }
  }, [])

  // Reset hover state when sidebar is pinned open
  React.useEffect(() => {
    if (state === "expanded") ctx.setHoverExpanded(false)
  }, [state, ctx])

  if (collapsible === "none") {
    return (
      <div
        data-slot="sidebar"
        className={cn(
          "bg-sidebar text-sidebar-foreground flex h-full w-(--sidebar-width) flex-col",
          className
        )}
        {...props}
      >
        {children}
      </div>
    )
  }

  if (isMobile) {
    return (
      <Sheet open={openMobile} onOpenChange={setOpenMobile} {...props}>
        <SheetContent
          data-sidebar="sidebar"
          data-slot="sidebar"
          data-mobile="true"
          className="bg-sidebar text-sidebar-foreground w-(--sidebar-width) p-0 [&>button]:hidden"
          style={
            {
              "--sidebar-width": SIDEBAR_WIDTH_MOBILE,
            } as React.CSSProperties
          }
          side={side}
        >
          <SheetHeader className="sr-only">
            <SheetTitle>Sidebar</SheetTitle>
            <SheetDescription>Displays the mobile sidebar.</SheetDescription>
          </SheetHeader>
          <div className="flex h-full w-full flex-col">{children}</div>
        </SheetContent>
      </Sheet>
    )
  }

  // When collapsed + hoverExpanded, render content as expanded but gap stays at icon width
  const visualState = hoverExpanded ? "expanded" : state
  const collapsibleAttr = visualState === "collapsed" ? collapsible : ""

  return (
    <div
      className="group peer text-sidebar-foreground hidden md:block"
      data-state={visualState}
      data-collapsible={collapsibleAttr}
      data-variant={variant}
      data-side={side}
      data-slot="sidebar"
      data-hover={hoverExpanded ? "true" : undefined}
      onMouseEnter={handleMouseEnter}
      onMouseLeave={handleMouseLeave}
    >
      {/* Gap: stays at icon width when collapsed, even during hover (no content shift) */}
      <div
        data-slot="sidebar-gap"
        className={cn(
          "relative bg-transparent transition-[width] duration-200 ease-linear",
          state === "collapsed"
            ? (variant === "floating" || variant === "inset"
                ? "w-[calc(var(--sidebar-width-icon)+(--spacing(4)))]"
                : "w-(--sidebar-width-icon)")
            : "w-(--sidebar-width)",
          state === "collapsed" && collapsible === "offcanvas" && "w-0",
        )}
      />
      {/* Container: expands as overlay on hover when collapsed */}
      <div
        data-slot="sidebar-container"
        className={cn(
          "fixed inset-y-0 z-10 hidden h-svh md:flex",
          // Transition: smooth cubic-bezier for polished feel
          "transition-[left,right,width,box-shadow] duration-200 ease-[cubic-bezier(0.25,0.1,0.25,1)]",
          side === "left"
            ? "left-0 group-data-[collapsible=offcanvas]:left-[calc(var(--sidebar-width)*-1)]"
            : "right-0 group-data-[collapsible=offcanvas]:right-[calc(var(--sidebar-width)*-1)]",
          variant === "floating" || variant === "inset"
            ? "p-2"
            : "",
          className
        )}
        style={{
          width: hoverExpanded
            ? "var(--sidebar-width)"
            : (visualState === "collapsed" && collapsible === "icon")
              ? "var(--sidebar-width-icon)"
              : "var(--sidebar-width)",
          zIndex: hoverExpanded ? 50 : 10,
          boxShadow: hoverExpanded ? "4px 0 24px rgba(0,0,0,0.35)" : "none",
        }}
        {...props}
      >
        <div
          data-sidebar="sidebar"
          data-slot="sidebar-inner"
          className={cn(
            "bg-sidebar flex h-full w-full flex-col overflow-hidden",
            "group-data-[variant=floating]:border-sidebar-border group-data-[variant=floating]:rounded-lg group-data-[variant=floating]:border group-data-[variant=floating]:shadow-sm",
          )}
        >
          {children}
        </div>
      </div>
    </div>
  )
}


function SidebarTrigger({
  className,
  onClick,
  ...props
}: React.ComponentProps<typeof Button>) {
  const { toggleSidebar } = useSidebar()

  return (
    <Button
      data-sidebar="trigger"
      data-slot="sidebar-trigger"
      variant="ghost"
      size="icon"
      className={cn("size-7", className)}
      onClick={(event) => {
        onClick?.(event)
        toggleSidebar()
      }}
      {...props}
    >
      <PanelLeftIcon />
      <span className="sr-only">Toggle Sidebar</span>
    </Button>
  )
}


function SidebarRail({ className, ...props }: React.ComponentProps<"button">) {
  const { toggleSidebar } = useSidebar()

  return (
    <button
      data-sidebar="rail"
      data-slot="sidebar-rail"
      aria-label="Toggle Sidebar"
      tabIndex={-1}
      onClick={toggleSidebar}
      title="Toggle Sidebar"
      className={cn(
        "hover:after:bg-sidebar-border absolute inset-y-0 z-20 hidden w-4 -translate-x-1/2 transition-all ease-linear group-data-[side=left]:-right-4 group-data-[side=right]:left-0 after:absolute after:inset-y-0 after:left-1/2 after:w-[2px] sm:flex",
        "in-data-[side=left]:cursor-w-resize in-data-[side=right]:cursor-e-resize",
        "[[data-side=left][data-state=collapsed]_&]:cursor-e-resize [[data-side=right][data-state=collapsed]_&]:cursor-w-resize",
        "hover:group-data-[collapsible=offcanvas]:bg-sidebar group-data-[collapsible=offcanvas]:translate-x-0 group-data-[collapsible=offcanvas]:after:left-full",
        "[[data-side=left][data-collapsible=offcanvas]_&]:-right-2",
        "[[data-side=right][data-collapsible=offcanvas]_&]:-left-2",
        className
      )}
      {...props}
    />
  )
}


function SidebarInset({ className, ...props }: React.ComponentProps<"main">) {
  return (
    <main
      data-slot="sidebar-inset"
      className={cn(
        "bg-card relative flex w-full flex-1 flex-col min-h-0 border-l border-white/[0.1]",
        "md:peer-data-[variant=inset]:m-2 md:peer-data-[variant=inset]:ml-0 md:peer-data-[variant=inset]:rounded-xl md:peer-data-[variant=inset]:shadow-sm md:peer-data-[variant=inset]:peer-data-[state=collapsed]:ml-2",
        className
      )}
      {...props}
    />
  )
}


function SidebarInput({
  className,
  ...props
}: React.ComponentProps<typeof Input>) {
  return (
    <Input
      data-slot="sidebar-input"
      data-sidebar="input"
      className={cn("bg-background h-8 w-full shadow-none", className)}
      {...props}
    />
  )
}


export {
  Sidebar,
  SidebarContent,
  SidebarFooter,
  SidebarGroup,
  SidebarGroupAction,
  SidebarGroupContent,
  SidebarGroupLabel,
  SidebarHeader,
  SidebarInput,
  SidebarInset,
  SidebarMenu,
  SidebarMenuAction,
  SidebarMenuBadge,
  SidebarMenuButton,
  SidebarMenuItem,
  SidebarMenuSkeleton,
  SidebarMenuSub,
  SidebarMenuSubButton,
  SidebarMenuSubItem,
  SidebarProvider,
  SidebarRail,
  SidebarSeparator,
  SidebarTrigger,
  useSidebar,
}
