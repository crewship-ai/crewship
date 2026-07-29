"use client"

import { useMemo, useState } from "react"
import Link from "next/link"
import { motion } from "motion/react"
import { ArrowUpRight, Search } from "lucide-react"
import type { LucideIcon } from "lucide-react"

import { DetailCard } from "@/components/ui/detail"
import { cn } from "@/lib/utils"

// =============================================================================
// DetailCell — a filterable list inside the standard detail card.
//
// Chrome, type roles and dividers come from components/ui/detail, so this reads
// as the same product as the routine screen rather than a second take on it.
// What it adds on top is the part a routine card does not need: an internal
// scroll (so ten issues and one issue produce the same page height), a bucket
// filter, and free-text search.
//
// The cell never re-implements a destination screen. It shows a slice and ends
// in a link to the screen that owns the data — one place per concept.
// =============================================================================

export type DetailCellTone = "primary" | "success" | "warn" | "danger" | "purple" | "notice" | "gold" | "muted"

// Soft fills, matching the routine screen's chips — a saturated 20px block of
// pure --primary next to 13px text reads as an alert, not as a category.
const TONE_BG: Record<DetailCellTone, string> = {
  primary: "bg-primary/20 text-primary",
  success: "bg-success/20 text-success",
  warn: "bg-warn/20 text-warn",
  danger: "bg-destructive/20 text-destructive",
  purple: "bg-purple/20 text-purple",
  notice: "bg-notice/20 text-notice",
  gold: "bg-gold/20 text-gold",
  muted: "bg-surface-raised text-muted-foreground",
}

export interface DetailCellItem {
  id: string
  icon: LucideIcon
  tone?: DetailCellTone
  title: string
  subtitle?: string
  meta?: string
  /** Filter bucket this row belongs to; matched against the active chip. */
  tag: string
  href?: string
  onSelect?: () => void
  dimmed?: boolean
}

export interface DetailCellFilter {
  id: string
  label: string
}

export interface DetailCellProps {
  title: string
  count?: number | string
  /** Renders the count in the warn tone — something in here needs attention. */
  warn?: boolean
  /** First entry is the neutral "everything" bucket and starts selected. */
  filters: DetailCellFilter[]
  items: DetailCellItem[]
  tall?: boolean
  footerLabel?: string
  footerHref?: string
  /** Used instead of `footerHref` when the destination is in-page. */
  footerOnClick?: () => void
  className?: string
}

const ALL = (filters: DetailCellFilter[]) => filters[0]?.id ?? "all"

export function DetailCell({
  title, count, warn = false, filters, items, tall = false,
  footerLabel, footerHref, footerOnClick, className,
}: DetailCellProps) {
  const [activeFilter, setActiveFilter] = useState(() => ALL(filters))
  const [searchOpen, setSearchOpen] = useState(false)
  const [query, setQuery] = useState("")

  const allId = ALL(filters)

  const visible = useMemo(() => {
    const q = query.trim().toLowerCase()
    return items.filter((item) => {
      if (activeFilter !== allId && item.tag !== activeFilter) return false
      if (!q) return true
      return `${item.title} ${item.subtitle ?? ""}`.toLowerCase().includes(q)
    })
  }, [items, activeFilter, query, allId])

  const narrowed = visible.length !== items.length

  function toggleSearch() {
    setSearchOpen((open) => {
      if (open) setQuery("")
      return !open
    })
  }

  return (
    <DetailCard
      title={title}
      subtitle={String(count ?? items.length)}
      bare
      tone={warn ? "warn" : "default"}
      className={cn("flex flex-col", className)}
      action={
        <button
          type="button"
          onClick={toggleSearch}
          aria-pressed={searchOpen}
          aria-label={`Search ${title.toLowerCase()}`}
          className={cn(
            "grid h-6 w-6 place-items-center rounded-md text-muted-foreground-soft transition-colors",
            "hover:bg-white/[.06] hover:text-foreground",
            searchOpen && "bg-primary/15 text-primary",
          )}
        >
          <Search className="h-3.5 w-3.5" />
        </button>
      }
    >
      <span className="sr-only" data-testid="cell-badge" data-warn={warn ? "true" : "false"} />

      {searchOpen && (
        <div className="border-b border-hairline bg-surface-subtle px-4 py-2">
          <input
            type="search"
            autoFocus
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            placeholder={`Search ${title.toLowerCase()}…`}
            className={cn(
              "type-row w-full rounded-md border border-primary bg-background px-2.5 py-1 text-foreground outline-none",
              "shadow-[0_0_0_3px_color-mix(in_oklch,var(--primary)_18%,transparent)]",
            )}
          />
        </div>
      )}

      <div className="flex gap-1 overflow-x-auto border-b border-hairline px-4 py-2 [scrollbar-width:none] [&::-webkit-scrollbar]:hidden">
        {filters.map((f) => (
          <button
            key={f.id}
            type="button"
            onClick={() => setActiveFilter(f.id)}
            aria-pressed={activeFilter === f.id}
            className={cn(
              "type-meta shrink-0 whitespace-nowrap rounded-full px-2.5 py-1 font-medium transition-colors",
              activeFilter === f.id
                ? "bg-primary/20 text-primary"
                : "text-muted-foreground hover:text-foreground",
            )}
          >
            {f.label}
          </button>
        ))}
      </div>

      <div
        data-testid="cell-body"
        className={cn("divide-y divide-border/40 overflow-y-auto", tall ? "max-h-[340px]" : "max-h-[212px]")}
      >
        {visible.map((item, index) => (
          <CellRow key={item.id} item={item} index={index} />
        ))}
        {visible.length === 0 && (
          <p className="type-row px-4 py-6 text-center text-muted-foreground-soft">Nothing matches this filter.</p>
        )}
      </div>

      <div className="type-meta mt-auto flex items-center gap-2 border-t border-hairline px-4 py-2 text-muted-foreground-soft">
        <span data-testid="cell-count">
          {narrowed ? `${visible.length} of ${items.length}` : `${items.length} items`}
        </span>
        {footerLabel && footerHref && (
          <Link href={footerHref} className="ml-auto inline-flex items-center gap-1 text-primary hover:underline">
            {footerLabel}
            <ArrowUpRight className="h-3 w-3" />
          </Link>
        )}
        {footerLabel && !footerHref && footerOnClick && (
          <button
            type="button"
            onClick={footerOnClick}
            className="ml-auto inline-flex items-center gap-1 text-primary hover:underline"
          >
            {footerLabel}
          </button>
        )}
      </div>
    </DetailCard>
  )
}

function CellRow({ item, index }: { item: DetailCellItem; index: number }) {
  const Icon = item.icon
  const interactive = Boolean(item.onSelect || item.href)

  const content = (
    <>
      <span className={cn("mt-px grid h-5 w-5 shrink-0 place-items-center rounded-md", TONE_BG[item.tone ?? "muted"])}>
        <Icon className="h-3 w-3" />
      </span>
      <span className="min-w-0 flex-1">
        <span className="type-row block truncate text-foreground">{item.title}</span>
        {item.subtitle && (
          <span className="type-meta block truncate font-mono text-muted-foreground">{item.subtitle}</span>
        )}
      </span>
      {item.meta && <span className="type-meta shrink-0 font-mono text-muted-foreground-soft">{item.meta}</span>}
    </>
  )

  const shared = cn(
    "flex w-full items-start gap-2.5 px-4 py-2 text-left transition-colors",
    interactive && "cursor-pointer hover:bg-white/[.03]",
    item.dimmed && "opacity-45",
  )

  const animation = {
    initial: { opacity: 0, y: 3 },
    animate: { opacity: 1, y: 0 },
    transition: { duration: 0.16, delay: Math.min(index, 8) * 0.02 },
  }

  if (item.href) {
    return (
      <motion.div {...animation}>
        <Link href={item.href} className={shared}>{content}</Link>
      </motion.div>
    )
  }

  return (
    <motion.div {...animation}>
      <div
        role={interactive ? "button" : undefined}
        tabIndex={interactive ? 0 : undefined}
        onClick={item.onSelect}
        onKeyDown={(e) => {
          if (!item.onSelect) return
          if (e.key === "Enter" || e.key === " ") {
            e.preventDefault()
            item.onSelect()
          }
        }}
        className={shared}
      >
        {content}
      </div>
    </motion.div>
  )
}
