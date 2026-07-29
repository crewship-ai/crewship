"use client"

import { useMemo, useState } from "react"
import Link from "next/link"
import { motion } from "motion/react"
import { ArrowUpRight, Search } from "lucide-react"
import type { LucideIcon } from "lucide-react"

import { cn } from "@/lib/utils"

// =============================================================================
// DetailCell — the one list primitive the agent and crew detail screens use.
//
// Why one component instead of six bespoke cards: the detail screens grew a
// separate hand-rolled block per relation (issues, routines, credentials,
// skills, memory, channels). They drifted — different empty states, different
// truncation, no filtering, and the page height was decided by whichever
// relation happened to have the most rows. This primitive fixes all four at
// once: it scrolls internally (so ten issues and one issue produce the same
// page height), it filters, and it always ends in a link to the screen that
// actually owns the data.
//
// The cell never re-implements a destination screen. It shows a slice and
// points at `/issues?assignee=…` for the rest — one place per concept.
// =============================================================================

export type DetailCellTone = "primary" | "success" | "warn" | "danger" | "purple" | "notice" | "gold" | "muted"

const TONE_BG: Record<DetailCellTone, string> = {
  primary: "bg-primary",
  success: "bg-success",
  warn: "bg-warn",
  danger: "bg-destructive",
  purple: "bg-purple",
  notice: "bg-notice",
  gold: "bg-gold",
  muted: "bg-surface-raised",
}

export interface DetailCellItem {
  id: string
  icon: LucideIcon
  /** Colour of the leading icon tile. Defaults to `muted`. */
  tone?: DetailCellTone
  title: string
  subtitle?: string
  /** Right-aligned metadata — timestamp, owner, count. */
  meta?: string
  /** Filter bucket this row belongs to; matched against the active chip. */
  tag: string
  href?: string
  onSelect?: () => void
  /** Dim the row without hiding it (disabled skill, unused binding). */
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
  /** Taller scroll viewport, for cells that get a column to themselves. */
  tall?: boolean
  footerLabel?: string
  footerHref?: string
  /** Used instead of `footerHref` when the destination is in-page. */
  footerOnClick?: () => void
  className?: string
}

const ALL = (filters: DetailCellFilter[]) => filters[0]?.id ?? "all"

export function DetailCell({
  title,
  count,
  warn = false,
  filters,
  items,
  tall = false,
  footerLabel,
  footerHref,
  footerOnClick,
  className,
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
    <div
      className={cn(
        "flex flex-col min-w-0 overflow-hidden rounded-[10px] border border-border bg-card",
        className,
      )}
    >
      <div className="flex items-center gap-2 border-b border-border px-3 py-2">
        <span className="text-label font-semibold">{title}</span>
        <span
          data-testid="cell-badge"
          data-warn={warn ? "true" : "false"}
          className={cn(
            "rounded-full px-1.5 py-px font-mono text-micro",
            warn ? "bg-warn/15 text-warn" : "bg-surface-raised text-muted-foreground-soft",
          )}
        >
          {count ?? items.length}
        </span>
        <div className="ml-auto flex items-center gap-1">
          <button
            type="button"
            onClick={toggleSearch}
            aria-pressed={searchOpen}
            aria-label={`Hledat v ${title.toLowerCase()}`}
            className={cn(
              "grid h-6 w-6 place-items-center rounded-md text-muted-foreground-soft transition-colors",
              "hover:bg-white/[.07] hover:text-foreground",
              searchOpen && "bg-primary/15 text-primary",
            )}
          >
            <Search className="h-3.5 w-3.5" />
          </button>
        </div>
      </div>

      {searchOpen && (
        <div className="border-b border-border bg-surface-subtle px-3 py-2">
          <input
            type="search"
            autoFocus
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            placeholder={`Hledat v ${title.toLowerCase()}…`}
            className={cn(
              "w-full rounded-md border border-primary bg-background px-2.5 py-1 text-label text-foreground outline-none",
              "shadow-[0_0_0_3px_color-mix(in_oklch,var(--primary)_18%,transparent)]",
            )}
          />
        </div>
      )}

      <div className="flex gap-1 overflow-x-auto border-b border-border px-3 py-2 [scrollbar-width:none] [&::-webkit-scrollbar]:hidden">
        {filters.map((f) => (
          <button
            key={f.id}
            type="button"
            onClick={() => setActiveFilter(f.id)}
            aria-pressed={activeFilter === f.id}
            className={cn(
              "shrink-0 whitespace-nowrap rounded-full border px-2.5 py-0.5 text-micro transition-colors",
              activeFilter === f.id
                ? "border-primary/40 bg-primary/15 text-primary"
                : "border-border bg-surface-subtle text-muted-foreground hover:text-foreground",
            )}
          >
            {f.label}
          </button>
        ))}
      </div>

      <div
        data-testid="cell-body"
        className={cn(
          "overflow-y-auto p-1.5",
          tall ? "max-h-[340px]" : "max-h-[208px]",
        )}
      >
        {visible.map((item, index) => (
          <CellRow key={item.id} item={item} index={index} />
        ))}
        {visible.length === 0 && (
          <p className="px-2 py-5 text-center text-label text-muted-foreground-soft">
            Nic neodpovídá filtru.
          </p>
        )}
      </div>

      <div className="flex items-center gap-2 border-t border-border px-3 py-1.5 text-micro text-muted-foreground-soft">
        <span data-testid="cell-count">
          {narrowed ? `${visible.length} z ${items.length}` : `${items.length} položek`}
        </span>
        {footerLabel && footerHref && (
          <Link
            href={footerHref}
            className="ml-auto inline-flex items-center gap-1 text-primary hover:underline"
          >
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
    </div>
  )
}

function CellRow({ item, index }: { item: DetailCellItem; index: number }) {
  const Icon = item.icon
  const interactive = Boolean(item.onSelect || item.href)

  const content = (
    <>
      <span
        className={cn(
          "grid h-[22px] w-[22px] shrink-0 place-items-center rounded-md text-white",
          TONE_BG[item.tone ?? "muted"],
        )}
      >
        <Icon className="h-3.5 w-3.5" />
      </span>
      <span className="min-w-0 flex-1">
        <span className="block truncate text-body">{item.title}</span>
        {item.subtitle && (
          <span className="block truncate font-mono text-micro text-muted-foreground-soft">
            {item.subtitle}
          </span>
        )}
      </span>
      {item.meta && (
        <span className="shrink-0 font-mono text-micro text-muted-foreground-soft">{item.meta}</span>
      )}
    </>
  )

  const shared = cn(
    "flex w-full items-center gap-2.5 rounded-md px-2 py-1.5 text-left transition-colors",
    interactive && "cursor-pointer hover:bg-white/[.04]",
    item.dimmed && "opacity-45",
  )

  // Motion carries the stagger the detail screens already use elsewhere; the
  // delay is capped so a long list does not crawl in for a second and a half.
  const animation = {
    initial: { opacity: 0, y: 4 },
    animate: { opacity: 1, y: 0 },
    transition: { duration: 0.18, delay: Math.min(index, 8) * 0.02 },
  }

  if (item.href) {
    return (
      <motion.div {...animation}>
        <Link href={item.href} className={shared}>
          {content}
        </Link>
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
