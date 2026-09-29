"use client"

import * as React from "react"
import Link from "next/link"
import { motion, useReducedMotion } from "motion/react"
import { ArrowLeft, type LucideIcon } from "lucide-react"

import { cn } from "@/lib/utils"
import { useIsMobile } from "@/hooks/use-mobile"
import { SubBar } from "@/components/layout/sub-bar"
import { SIDEBAR_WIDTH } from "@/components/layout/sidebar-kit"

/**
 * A nested page: one level deeper than the page it came from, with its own
 * side panel instead of an extra column.
 *
 * The rule it keeps: at most two navigation columns on screen — the app rail
 * and ONE side panel. Opening a nested page swaps the parent's panel (e.g.
 * the Settings sections) for this page's own, headed by "← <parent>", so the
 * reader knows they went one level in and how to get out. The arrow always
 * goes to the parent, not to browser history. Never nest a nested page.
 *
 * On a phone the panel folds into a header row: the back arrow, the title,
 * and `mobileNav` (usually the page's view tabs).
 *
 * Used by Settings › Crew links and Settings › Audit log.
 */
export function DrillPage({
  parent,
  title,
  icon: Icon,
  description,
  nav,
  mobileNav,
  actions,
  children,
  className,
}: {
  /** Where the back arrow goes, and what it is called. */
  parent: { href: string; label: string; icon?: LucideIcon }
  title: string
  icon: LucideIcon
  description?: string
  /** The side panel's content under the header — use DrillNavSection/DrillNavItem. */
  nav: React.ReactNode
  /** What replaces the side panel on a phone. */
  mobileNav?: React.ReactNode
  /** Page actions, on the sub-bar. */
  actions?: React.ReactNode
  children: React.ReactNode
  className?: string
}) {
  const isMobile = useIsMobile()
  const reduce = useReducedMotion()
  const BackIcon = parent.icon

  return (
    <div className="flex h-[calc(100dvh-var(--app-header-h)-var(--mobile-tab-bar-h))] flex-col">
      <SubBar
        icon={BackIcon}
        title={parent.label}
        section={title}
        ariaLabel={`${parent.label} / ${title}`}
        actions={actions}
        leading={
          isMobile ? (
            <Link href={parent.href} aria-label={`Back to ${parent.label}`} className="-ml-1 grid h-7 w-7 place-items-center rounded-md text-muted-foreground hover:bg-accent hover:text-foreground">
              <ArrowLeft className="h-4 w-4" />
            </Link>
          ) : undefined
        }
      />
      <div className="flex min-h-0 flex-1">
        {!isMobile && (
          <motion.aside
            initial={reduce ? false : { opacity: 0, x: 12 }}
            animate={{ opacity: 1, x: 0 }}
            transition={{ duration: 0.18, ease: [0.2, 0.7, 0.2, 1] }}
            className={cn(SIDEBAR_WIDTH, "flex shrink-0 flex-col border-r border-sidebar-border bg-sidebar")}
            aria-label={`${title} navigation`}
          >
            <div className="border-b border-sidebar-border px-3 pb-3 pt-2.5">
              <Link
                href={parent.href}
                data-slot="drill-back"
                className="-ml-1 inline-flex h-7 items-center gap-1.5 rounded-md px-1.5 text-xs font-medium text-muted-foreground transition-colors hover:bg-accent hover:text-foreground"
              >
                <ArrowLeft className="h-3.5 w-3.5" />
                {parent.label}
              </Link>
              <div className="mt-2 flex items-center gap-2.5">
                <span className="icon-tile inline-flex h-8 w-8 shrink-0 items-center justify-center rounded-lg" aria-hidden>
                  <Icon className="h-4 w-4" />
                </span>
                <div className="min-w-0">
                  <h1 className="truncate text-sm font-semibold tracking-[-0.01em]">{title}</h1>
                  {description && <p className="text-[11.5px] leading-snug text-muted-foreground">{description}</p>}
                </div>
              </div>
            </div>
            <nav className="min-h-0 flex-1 overflow-y-auto pb-4" aria-label={`${title} sections`}>{nav}</nav>
          </motion.aside>
        )}
        <main className={cn("min-h-0 min-w-0 flex-1 overflow-y-auto", className)}>
          {isMobile && mobileNav && (
            <div className="sticky top-0 z-10 border-b border-border bg-background/95 px-3 py-2 backdrop-blur">{mobileNav}</div>
          )}
          {children}
        </main>
      </div>
    </div>
  )
}

/** A labelled group in the nested page's side panel. */
export function DrillNavSection({ label, count, children }: { label: string; count?: React.ReactNode; children: React.ReactNode }) {
  return (
    <div className="px-1.5 pt-3">
      <div className="flex items-center justify-between px-2 pb-1 font-mono text-[10.5px] font-medium uppercase tracking-[0.08em] text-muted-foreground-soft">
        <span>{label}</span>
        {count != null && <span className="tabular-nums">{count}</span>}
      </div>
      <div className="flex flex-col gap-px">{children}</div>
    </div>
  )
}

/** A row in the nested page's side panel: the same shape and selection as SidebarRow. */
export function DrillNavItem({
  selected,
  onSelect,
  icon,
  label,
  meta,
  title,
  muted,
}: {
  selected?: boolean
  onSelect: () => void
  icon?: React.ReactNode
  label: React.ReactNode
  meta?: React.ReactNode
  title?: string
  muted?: boolean
}) {
  return (
    <button
      type="button"
      onClick={onSelect}
      aria-current={selected ? "true" : undefined}
      title={title}
      className={cn(
        "row-interactive row-hover w-full items-center gap-2 rounded-md px-2 py-1.5 text-left text-[13px]",
        selected && "row-selected font-medium",
        muted && !selected && "text-muted-foreground",
      )}
    >
      {icon}
      <span className="min-w-0 flex-1 truncate">{label}</span>
      {meta != null && <span className="shrink-0 font-mono text-[11px] tabular-nums text-muted-foreground">{meta}</span>}
    </button>
  )
}
