"use client"

import * as React from "react"
import Link from "next/link"
import { ChevronRight } from "lucide-react"

import { ConceptIcon, type SurfaceIconComponent } from "@/components/ui/concept-icon"
import { cn } from "@/lib/utils"

/**
 * The Harbor 2 Dashboard card: a hairline surface, radius 10, no shadow, a
 * header with the concept's tile, the title and one grey link. Rows inside
 * run edge to edge with a hairline between them (DashLine), so a card with
 * two rows is two rows tall rather than a fixed pane with blank space.
 *
 * Local to the Dashboard on purpose: DashboardCard is shared with other
 * areas and keeps its own look until they move.
 */
export function DashCard({
  title,
  concept,
  icon,
  link,
  action,
  meta,
  tools,
  className,
  children,
  ...rest
}: Omit<React.ComponentProps<"section">, "title"> & {
  title: React.ReactNode
  /** Concept key for the header tile (lib/concept-icons). */
  concept?: string
  /** A glyph in place of the concept's own. */
  icon?: SurfaceIconComponent
  /** The card's one link, rendered grey; blue is kept for the main action. */
  link?: { label: string; href: string }
  /** A header control in place of the link (a ghost Dismiss). */
  action?: React.ReactNode
  /** A short muted fact beside the title ("10 runs", "1 stale"). */
  meta?: React.ReactNode
  /** Controls under the header (the Results segment). */
  tools?: React.ReactNode
}) {
  const label = typeof title === "string" ? title : undefined
  return (
    <section
      aria-label={label}
      data-slot="dash-card"
      className={cn("min-w-0 overflow-hidden rounded-card border border-border bg-card", className)}
      {...rest}
    >
      {/* v67 `.card-h` at Compact (a 14.857 px root): a 50 px floor
          (3.3654rem) around a 28 px tile (1.8846rem), 8 px above and below. */}
      <header className="flex min-h-[3.3654rem] items-center gap-2.5 px-4 py-[0.5385rem]">
        {(concept || icon) && (
          <ConceptIcon concept={concept} icon={icon} accent="blue" variant="chip" className="size-[1.8846rem] rounded-md" />
        )}
        <h2 className="shrink-0 text-control font-semibold text-foreground">{title}</h2>
        {meta != null && (
          <span data-slot="dashboard-card-hint" className="min-w-0 truncate text-label tabular-nums text-muted-foreground">
            {meta}
          </span>
        )}
        <span className="flex-1" />
        {link && (
          <Link
            href={link.href}
            className="inline-flex shrink-0 items-center gap-0.5 text-control font-medium text-muted-foreground transition-colors hover:text-foreground coarse:-my-2 coarse:min-h-[44px]"
          >
            {link.label}
            <ChevronRight className="size-3.5" aria-hidden />
          </Link>
        )}
        {action}
      </header>
      {tools && <div className="overflow-x-auto px-4 pb-2.5">{tools}</div>}
      {children}
    </section>
  )
}

/** An inline text link in a card line: foreground ink, underlined so it reads
 *  as a link, and a 44 px target under a coarse pointer. */
export const TEXT_LINK = "text-foreground underline underline-offset-2 hover:no-underline coarse:inline-flex coarse:min-h-[44px] coarse:items-center"

/** A group heading inside a card: "Running now 2". */
export function DashSubHead({ title, count }: { title: string; count?: number }) {
  return (
    <div className="flex items-baseline gap-2 border-t border-border px-4 pb-1.5 pt-3 text-label font-semibold text-muted-foreground">
      {title}
      {count != null && <span className="font-normal tabular-nums">{count}</span>}
    </div>
  )
}

/**
 * One row: a lead (avatar or tile), a title over a muted line, and a right
 * side for a status pill, a time or an action. When `href` is set the title
 * block is the link; the right side stays outside it so an action button is
 * never nested inside another interactive element.
 */
export function DashLine({
  lead,
  title,
  sub,
  right,
  href,
  className,
  ...rest
}: Omit<React.ComponentProps<"div">, "title"> & {
  lead?: React.ReactNode
  title: React.ReactNode
  sub?: React.ReactNode
  right?: React.ReactNode
  href?: string
}) {
  const text = (
    <>
      <b className="block truncate text-body font-medium text-foreground">{title}</b>
      {sub != null && <span className="block truncate text-label text-muted-foreground">{sub}</span>}
    </>
  )
  return (
    <div
      data-slot="dash-line"
      className={cn("flex min-w-0 items-center gap-3 border-t border-border px-4 py-2.5", className)}
      {...rest}
    >
      {lead}
      {href ? (
        <Link href={href} className="min-w-0 flex-1 rounded-sm hover:[&>b]:underline focus-visible:outline-2 focus-visible:outline-primary coarse:min-h-[44px]">
          {text}
        </Link>
      ) : (
        <span className="min-w-0 flex-1">{text}</span>
      )}
      {right != null && <span className="flex shrink-0 items-center gap-2">{right}</span>}
    </div>
  )
}

/** The one-line state a card wears when it has nothing, or failed. */
export function DashNote({
  children,
  tone = "muted",
  action,
  className,
}: {
  children: React.ReactNode
  tone?: "muted" | "ok" | "error"
  action?: React.ReactNode
  className?: string
}) {
  return (
    <div
      data-slot="dash-note"
      role={tone === "error" ? "status" : undefined}
      className={cn(
        "flex flex-wrap items-center gap-x-2 gap-y-1 border-t border-border px-4 py-3 text-body",
        tone === "ok" ? "text-chip-ok-fg" : tone === "error" ? "text-chip-danger-fg" : "text-muted-foreground",
        className,
      )}
    >
      <span className="min-w-0">{children}</span>
      {action}
    </div>
  )
}
