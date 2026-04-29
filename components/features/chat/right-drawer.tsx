"use client"

import { useEffect, useRef, type ReactNode } from "react"
import { AnimatePresence, motion } from "motion/react"
import { useHotkeys } from "react-hotkeys-hook"

import { cn } from "@/lib/utils"
import { spring } from "@/lib/motion"
import { useDrawerStore } from "@/stores/drawer-store"

interface RightDrawerProps {
  children: ReactNode
  className?: string
}

export function RightDrawer({ children, className }: RightDrawerProps) {
  const { open, mode, width, setOpen, setWidth, activeTab } = useDrawerStore()
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
      {open && (
        <>
          {mode === "overlay" && (
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
            initial={{ x: width + 24, opacity: 0 }}
            animate={{ x: 0, opacity: 1 }}
            exit={{ x: width + 24, opacity: 0 }}
            transition={spring.smooth}
            style={{ width }}
            className={cn(
              "absolute top-0 right-12 bottom-0 z-20 bg-background border-l shadow-xl flex",
              mode === "push" && "static shadow-none",
              className,
            )}
          >
            <div
              role="separator"
              aria-orientation="vertical"
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
