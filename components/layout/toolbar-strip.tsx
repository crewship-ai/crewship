"use client"

import * as React from "react"
import type { LucideIcon } from "lucide-react"

import { cn } from "@/lib/utils"

export interface ToolbarTab<T extends string = string> {
  id: T
  label: string
  icon?: LucideIcon
  /** Optional badge rendered next to the label (e.g., count). */
  badge?: React.ReactNode
  disabled?: boolean
}

interface ToolbarStripProps<T extends string = string> extends React.ComponentProps<"div"> {
  tabs?: ToolbarTab<T>[]
  activeTab?: T
  onTabChange?: (id: T) => void
  /** Leading slot rendered before tabs (e.g., icon dropdown, search). */
  leading?: React.ReactNode
  /** Trailing slot rendered after the flex spacer (e.g., action buttons). */
  actions?: React.ReactNode
  /** Compact mode tightens vertical padding and hides tab labels on narrow screens. */
  compact?: boolean
  /** aria-label for the toolbar landmark. */
  ariaLabel?: string
}

/**
 * ToolbarStrip — canonical icon+label toolbar matching the orchestration reference.
 * Pattern extracted from `components/features/orchestration/issues-toolbar-strip.tsx`
 * and `orchestration-layout.tsx`. Use for in-page tab switching (board/list, overview/settings/logs).
 *
 * Layout: full-width strip with bottom hairline border, leading slot, tab group,
 * flex spacer, trailing actions slot. Active tab gets `bg-accent text-foreground`;
 * inactive tabs get `text-muted-foreground`.
 */
export function ToolbarStrip<T extends string = string>({
  tabs,
  activeTab,
  onTabChange,
  leading,
  actions,
  compact = false,
  ariaLabel,
  className,
  ...props
}: ToolbarStripProps<T>) {
  return (
    <div
      role="toolbar"
      aria-label={ariaLabel}
      className={cn(
        "flex items-center gap-2 px-4 border-b border-border shrink-0 bg-card",
        compact ? "py-1.5" : "py-2",
        className
      )}
      {...props}
    >
      {leading && <div className="flex items-center gap-2 shrink-0">{leading}</div>}

      {tabs && tabs.length > 0 && (
        <div
          className="flex gap-0.5 bg-muted/40 rounded-md p-0.5 shrink-0"
          role="tablist"
          aria-label={ariaLabel ? `${ariaLabel} tabs` : undefined}
        >
          {tabs.map((tab) => {
            const Icon = tab.icon
            const isActive = tab.id === activeTab
            return (
              <button
                key={tab.id}
                type="button"
                role="tab"
                aria-selected={isActive}
                aria-label={tab.label}
                disabled={tab.disabled}
                onClick={() => !tab.disabled && onTabChange?.(tab.id)}
                className={cn(
                  "inline-flex items-center gap-1.5 px-2.5 py-1 rounded text-label font-medium transition-colors",
                  "disabled:opacity-40 disabled:cursor-not-allowed",
                  isActive
                    ? "bg-accent text-foreground shadow-sm"
                    : "text-muted-foreground hover:text-foreground"
                )}
              >
                {Icon && <Icon className="h-3.5 w-3.5" />}
                <span className={cn(compact && "hidden sm:inline")}>{tab.label}</span>
                {tab.badge != null && (
                  <span className="text-micro text-muted-foreground/80">{tab.badge}</span>
                )}
              </button>
            )
          })}
        </div>
      )}

      <div className="flex-1" />

      {actions && <div className="flex items-center gap-2 shrink-0">{actions}</div>}
    </div>
  )
}
