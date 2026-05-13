"use client"

import * as React from "react"
import { motion } from "motion/react"
import { cn } from "@/lib/utils"
import { prefersReducedMotion, spring } from "@/lib/motion"

interface TabBarContextValue {
  value: string
  onValueChange: (value: string) => void
  layoutId: string
}

const TabBarContext = React.createContext<TabBarContextValue | null>(null)

function useTabBar() {
  const ctx = React.useContext(TabBarContext)
  if (!ctx) {
    throw new Error("TabBar.Item must be rendered inside <TabBar>")
  }
  return ctx
}

interface TabBarProps {
  value: string
  onValueChange: (value: string) => void
  /** Unique id for the animated indicator. Required if multiple TabBars share a page. */
  layoutId?: string
  className?: string
  children: React.ReactNode
  ariaLabel?: string
}

// TabBar — purpose-built tab strip with a Framer Motion `layoutId`
// underline that slides between active items (Linear/n8n style).
// NOT a wrapper around shadcn Tabs — Radix's data-state styling model
// fights `layoutId`. Use shadcn Tabs only when you need its keyboard
// orchestration. For simple visual tabs (Inbox filter, Routines, etc.)
// this primitive is the canonical choice.
let tabBarCounter = 0
function TabBar({
  value,
  onValueChange,
  layoutId,
  className,
  children,
  ariaLabel,
}: TabBarProps) {
  const generatedId = React.useMemo(() => `tab-indicator-${++tabBarCounter}`, [])
  const resolvedLayoutId = layoutId ?? generatedId

  return (
    <TabBarContext.Provider
      value={{ value, onValueChange, layoutId: resolvedLayoutId }}
    >
      <div
        role="tablist"
        aria-label={ariaLabel}
        className={cn("flex items-center gap-0 border-b border-white/[0.06]", className)}
      >
        {children}
      </div>
    </TabBarContext.Provider>
  )
}

interface TabBarItemProps {
  value: string
  count?: number | null
  className?: string
  children: React.ReactNode
}

function TabBarItem({ value, count, className, children }: TabBarItemProps) {
  const { value: active, onValueChange, layoutId } = useTabBar()
  const isActive = active === value
  const reduced = prefersReducedMotion()

  return (
    <button
      type="button"
      role="tab"
      aria-selected={isActive}
      onClick={() => onValueChange(value)}
      className={cn(
        "relative flex items-center justify-center gap-1.5 px-3 py-2 text-xs font-medium transition-colors",
        isActive ? "text-foreground" : "text-muted-foreground hover:text-foreground/80",
        className,
      )}
    >
      <span>{children}</span>
      {count !== null && count !== undefined && (
        <span className="rounded bg-white/[0.06] px-1.5 py-0.5 text-[10px] tabular-nums text-foreground/50">
          {count}
        </span>
      )}
      {isActive && (
        <motion.span
          layoutId={layoutId}
          aria-hidden
          transition={reduced ? { duration: 0 } : spring.smooth}
          className="absolute inset-x-0 -bottom-px h-0.5 bg-primary"
        />
      )}
    </button>
  )
}

TabBar.Item = TabBarItem

export { TabBar }
