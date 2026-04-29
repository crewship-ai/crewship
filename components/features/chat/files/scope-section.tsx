"use client"

import { useState, type ReactNode } from "react"
import { motion, AnimatePresence } from "motion/react"
import { ChevronRight, type LucideIcon } from "lucide-react"

import { cn } from "@/lib/utils"
import { spring } from "@/lib/motion"

interface ScopeSectionProps {
  icon: LucideIcon
  title: string
  count?: number
  defaultOpen?: boolean
  badge?: ReactNode
  children: ReactNode
}

export function ScopeSection({
  icon: Icon,
  title,
  count,
  defaultOpen = true,
  badge,
  children,
}: ScopeSectionProps) {
  const [open, setOpen] = useState(defaultOpen)
  return (
    <section className="border-b last:border-b-0">
      <button
        type="button"
        onClick={() => setOpen((v) => !v)}
        className={cn(
          "flex w-full items-center gap-1.5 px-2.5 py-1.5 text-xs font-medium",
          "text-muted-foreground hover:text-foreground transition-colors",
        )}
        aria-expanded={open}
      >
        <motion.span animate={{ rotate: open ? 90 : 0 }} transition={spring.snappy}>
          <ChevronRight className="h-3 w-3" />
        </motion.span>
        <Icon className="h-3.5 w-3.5" />
        <span className="uppercase tracking-wide">{title}</span>
        {typeof count === "number" && (
          <span className="rounded-full border bg-muted/40 px-1.5 py-px text-[10px] tabular-nums">
            {count}
          </span>
        )}
        {badge && <span className="ml-auto">{badge}</span>}
      </button>
      <AnimatePresence initial={false}>
        {open && (
          <motion.div
            initial={{ height: 0, opacity: 0 }}
            animate={{ height: "auto", opacity: 1 }}
            exit={{ height: 0, opacity: 0 }}
            transition={spring.smooth}
            className="overflow-hidden"
          >
            <div className="pb-1">{children}</div>
          </motion.div>
        )}
      </AnimatePresence>
    </section>
  )
}
