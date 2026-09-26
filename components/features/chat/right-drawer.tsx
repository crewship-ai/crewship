"use client"

import { useEffect, useRef, useState, type ReactNode } from "react"
import { AnimatePresence, motion } from "motion/react"
import { useHotkeys } from "react-hotkeys-hook"

import { cn } from "@/lib/utils"
import { spring } from "@/lib/motion"
import { useDrawerStore } from "@/stores/drawer-store"
import { useSessionSafe } from "@/hooks/use-auth"
import { useUserPreference } from "@/hooks/use-user-preference"

import { DRAWER_TAB_LABELS } from "./right-rail"

interface RightDrawerProps {
  children: ReactNode
  className?: string
}

export function RightDrawer(props: RightDrawerProps) {
  const { data: session } = useSessionSafe()
  const userId = session?.user?.id
  if (userId) return <SavedRightDrawer key={userId} userId={userId} {...props} />
  return <DrawerBody {...props} />
}

function SavedRightDrawer({ userId, ...props }: RightDrawerProps & { userId: string }) {
  const [preferredWidth, saveWidth] = useUserPreference<number>(`chat.drawer.width.${userId}`, 340)
  const setWidth = useDrawerStore((s) => s.setWidth)
  useEffect(() => {
    setWidth(Number.isFinite(preferredWidth) ? preferredWidth : 340)
  }, [preferredWidth, setWidth])
  return <DrawerBody {...props} onWidthCommit={saveWidth} />
}

function DrawerBody({ children, className, onWidthCommit }: RightDrawerProps & { onWidthCommit?: (width: number) => void }) {
  // Narrow selectors — one per field actually read.
  const open = useDrawerStore((s) => s.open)
  const mode = useDrawerStore((s) => s.mode)
  const width = useDrawerStore((s) => s.width)
  const setOpen = useDrawerStore((s) => s.setOpen)
  const setWidth = useDrawerStore((s) => s.setWidth)
  const activeTab = useDrawerStore((s) => s.activeTab)
  // Once opened, retain the editor buffer when the rail, Escape or backdrop
  // hides the drawer. Before the first open the panel remains lazy-mounted.
  const [hasOpened, setHasOpened] = useState(open)
  useEffect(() => { if (open) setHasOpened(true) }, [open])
  const dragRef = useRef<{ startX: number; startW: number } | null>(null)

  useHotkeys(
    "esc",
    () => {
      if (open) setOpen(false)
    },
    { enabled: open },
    [open, setOpen],
  )

  useEffect(() => {
    if (!open) return
    const onMove = (e: MouseEvent) => {
      if (!dragRef.current) return
      const dx = dragRef.current.startX - e.clientX
      setWidth(dragRef.current.startW + dx)
    }
    const onUp = () => {
      if (dragRef.current) onWidthCommit?.(useDrawerStore.getState().width)
      dragRef.current = null
      document.body.style.userSelect = ""
    }
    document.addEventListener("mousemove", onMove)
    document.addEventListener("mouseup", onUp)
    return () => {
      document.removeEventListener("mousemove", onMove)
      document.removeEventListener("mouseup", onUp)
      // Always restore — if the component unmounts mid-drag (before
      // mouseup fires), we'd otherwise leave the page un-selectable.
      document.body.style.userSelect = ""
    }
  }, [open, setWidth, onWidthCommit])

  const handleDragStart = (e: React.MouseEvent) => {
    e.preventDefault()
    document.body.style.userSelect = "none"
    dragRef.current = { startX: e.clientX, startW: width }
  }

  return (
    <AnimatePresence>
      {(open || hasOpened) && (
        <>
          {open && mode === "overlay" && (
            <motion.div
              key="drawer-backdrop"
              initial={{ opacity: 0 }}
              animate={{ opacity: 1 }}
              exit={{ opacity: 0 }}
              transition={{ duration: 0.15 }}
              className="absolute inset-0 z-10 bg-background/40 backdrop-blur-[2px]"
              onClick={() => setOpen(false)}
              aria-hidden
            />
          )}
          <motion.aside
            key="drawer"
            id={`drawer-panel-${activeTab}`}
            role="tabpanel"
            // A tabpanel with no accessible name announces as "tab panel" and
            // nothing else — the same problem the rail's unlabelled icons had.
            aria-label={DRAWER_TAB_LABELS[activeTab]}
            initial={{ x: width + 24, opacity: 0 }}
            animate={{ x: 0, opacity: 1 }}
            exit={{ x: width + 24, opacity: 0 }}
            transition={spring.smooth}
            hidden={!open}
            style={{ width, maxWidth: "45%", display: open ? undefined : "none" }}
            className={cn(
              "absolute top-0 right-14 bottom-0 z-20 bg-card border-l shadow-xl flex",
              mode === "push" && "static shadow-none",
              className,
            )}
          >
            <div
              role="separator"
              aria-orientation="vertical"
              aria-label="Resize chat side panel"
              aria-valuemin={280}
              aria-valuemax={520}
              aria-valuenow={width}
              tabIndex={0}
              onKeyDown={(event) => {
                const next = event.key === "ArrowLeft" ? width + 20
                  : event.key === "ArrowRight" ? width - 20
                  : event.key === "Home" ? 280
                  : event.key === "End" ? 520 : null
                if (next === null) return
                event.preventDefault()
                setWidth(next)
                onWidthCommit?.(Math.max(280, Math.min(520, next)))
              }}
              className="group relative w-2 shrink-0 cursor-col-resize bg-border/50 transition-colors hover:bg-primary/20 focus-visible:bg-primary/20 focus-visible:outline-none"
              onMouseDown={handleDragStart}
            ><span aria-hidden="true" className="absolute top-1/2 left-1/2 h-10 w-0.5 -translate-x-1/2 -translate-y-1/2 rounded-full bg-muted-foreground/45 transition-colors group-hover:bg-primary group-focus-visible:bg-primary" /></div>
            <div className="flex-1 min-w-0 flex flex-col overflow-hidden">
              {children}
            </div>
          </motion.aside>
        </>
      )}
    </AnimatePresence>
  )
}
