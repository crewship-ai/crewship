"use client"

import { useEffect, useRef, useState, type ReactNode } from "react"
import { AnimatePresence, motion } from "motion/react"
import { useHotkeys } from "react-hotkeys-hook"

import { cn } from "@/lib/utils"
import { spring } from "@/lib/motion"
import { useDrawerStore } from "@/stores/drawer-store"

import { DRAWER_TAB_LABELS } from "./right-rail"

interface RightDrawerProps {
  children: ReactNode
  className?: string
}

export function RightDrawer({ children, className }: RightDrawerProps) {
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
  }, [open, setWidth])

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
            style={{ width, display: open ? undefined : "none" }}
            className={cn(
              "absolute top-0 right-14 bottom-0 z-20 bg-background border-l shadow-xl flex",
              mode === "push" && "static shadow-none",
              className,
            )}
          >
            <div
              role="separator"
              aria-orientation="vertical"
              aria-label="Resize chat side panel"
              aria-valuemin={280}
              aria-valuemax={720}
              aria-valuenow={width}
              tabIndex={0}
              onKeyDown={(event) => {
                const next = event.key === "ArrowLeft" ? width + 20
                  : event.key === "ArrowRight" ? width - 20
                  : event.key === "Home" ? 280
                  : event.key === "End" ? 720 : null
                if (next === null) return
                event.preventDefault()
                setWidth(next)
              }}
              className="w-1 cursor-col-resize bg-transparent hover:bg-primary/30 transition-colors shrink-0"
              onMouseDown={handleDragStart}
            />
            <div className="flex-1 min-w-0 flex flex-col overflow-hidden">
              {children}
            </div>
          </motion.aside>
        </>
      )}
    </AnimatePresence>
  )
}
