"use client"

import * as React from "react"
import Link from "next/link"
import { ChevronRight, Star } from "lucide-react"

import { ConceptIcon, type SurfaceIconComponent } from "@/components/ui/concept-icon"
import { cn } from "@/lib/utils"

/**
 * The Harbor 2 card of the crew and agent pages: a hairline surface, radius
 * 10, no shadow, a header with the concept's tile, the title, a count and one
 * grey link or action. Rows inside run edge to edge with a hairline between
 * them (CrLine), so a card with two rows is two rows tall.
 */
export function CrCard({
  title,
  concept = "crews",
  icon,
  count,
  link,
  action,
  className,
  children,
  ...rest
}: Omit<React.ComponentProps<"section">, "title"> & {
  title: React.ReactNode
  concept?: string
  icon?: SurfaceIconComponent
  /** A number beside the title ("Agents · 3" reads "Agents 3"). */
  count?: React.ReactNode
  /** The card's one link, grey; blue is kept for the main action. */
  link?: { label: string; href?: string; onClick?: () => void }
  /** A header control in place of the link (New agent, Add). */
  action?: React.ReactNode
}) {
  const label = typeof title === "string" ? title : undefined
  return (
    <section
      aria-label={label}
      data-slot="cr-card"
      className={cn("min-w-0 overflow-hidden rounded-card border border-border bg-card", className)}
      {...rest}
    >
      <header className="flex min-h-12 items-center gap-2.5 px-4 pb-2 pt-3.5">
        <ConceptIcon concept={concept} icon={icon} accent="blue" variant="chip" className="h-7 w-7 rounded-md" />
        <h2 className="min-w-0 truncate text-body font-semibold text-foreground">{title}</h2>
        {count != null && <span className="font-mono text-label tabular-nums text-muted-foreground">{count}</span>}
        <span className="flex-1" />
        {link && <CardLink {...link} />}
        {action}
      </header>
      {children}
    </section>
  )
}

const LINK = "inline-flex shrink-0 items-center gap-0.5 text-control font-medium text-muted-foreground transition-colors hover:text-foreground coarse:min-h-11"

function CardLink({ label, href, onClick }: { label: string; href?: string; onClick?: () => void }) {
  const body = (
    <>
      {label}
      <ChevronRight className="size-3.5" aria-hidden />
    </>
  )
  return href ? (
    <Link href={href} className={LINK}>{body}</Link>
  ) : (
    <button type="button" onClick={onClick} className={LINK}>{body}</button>
  )
}

/** One row: a lead (avatar or tile), a title over a muted line, a right side. */
export function CrLine({
  lead,
  title,
  sub,
  right,
  onOpen,
  className,
  ...rest
}: Omit<React.ComponentProps<"div">, "title"> & {
  lead?: React.ReactNode
  title: React.ReactNode
  sub?: React.ReactNode
  right?: React.ReactNode
  /** Makes the title block a button (the right side stays outside it). */
  onOpen?: () => void
}) {
  const text = (
    <>
      <b className="block truncate text-body font-medium text-foreground">{title}</b>
      {sub != null && sub !== "" && <span className="block truncate text-label text-muted-foreground">{sub}</span>}
    </>
  )
  return (
    <div data-slot="cr-line" className={cn("flex min-w-0 items-center gap-3 border-t border-border px-4 py-2.5", className)} {...rest}>
      {lead}
      {onOpen ? (
        <button type="button" onClick={onOpen} className="min-w-0 flex-1 rounded-sm text-left hover:[&>b]:underline focus-visible:outline-2 focus-visible:outline-primary coarse:min-h-11">
          {text}
        </button>
      ) : (
        <span className="min-w-0 flex-1">{text}</span>
      )}
      {right != null && <span className="flex shrink-0 items-center gap-2">{right}</span>}
    </div>
  )
}

/** The one-line state a card wears when it has nothing, or failed. */
export function CrNote({ children, tone = "muted", action, className }: {
  children: React.ReactNode
  tone?: "muted" | "error"
  action?: React.ReactNode
  className?: string
}) {
  return (
    <div
      data-slot="cr-note"
      role={tone === "error" ? "status" : undefined}
      className={cn(
        "flex flex-wrap items-center gap-x-2 gap-y-1 border-t border-border px-4 py-3 text-body",
        tone === "error" ? "text-chip-danger-fg" : "text-muted-foreground",
        className,
      )}
    >
      <span className="min-w-0">{children}</span>
      {action}
    </div>
  )
}

/** Label / value rows (Health, Last 30 days). */
export function CrKv({ rows }: { rows: Array<[React.ReactNode, React.ReactNode]> }) {
  return (
    <dl className="border-t border-border">
      {rows.map(([k, v], i) => (
        <div key={i} className="grid grid-cols-[8.5rem_1fr] items-baseline gap-3 border-t border-border px-4 py-2 first:border-t-0">
          <dt className="text-label text-muted-foreground">{k}</dt>
          <dd className="min-w-0 text-body text-foreground">{v}</dd>
        </div>
      ))}
    </dl>
  )
}

/** A small single-choice segment (Status / A–Z, Environment sections). */
export function CrSegment<T extends string>({ value, options, onChange, label, className }: {
  value: T
  options: Array<{ id: T; label: string }>
  onChange: (id: T) => void
  label: string
  className?: string
}) {
  return (
    <div role="radiogroup" aria-label={label} className={cn("inline-flex shrink-0 items-center gap-0.5 rounded-md border border-border bg-muted p-0.5", className)}>
      {options.map((o) => {
        const on = o.id === value
        return (
          <button
            key={o.id}
            type="button"
            role="radio"
            aria-checked={on}
            onClick={() => onChange(o.id)}
            className={cn(
              "inline-flex h-5 items-center whitespace-nowrap rounded-sm px-2 text-micro font-medium transition-colors coarse:min-h-11",
              on ? "bg-card text-foreground shadow-xs" : "text-muted-foreground hover:text-foreground",
            )}
          >
            {o.label}
          </button>
        )
      })}
    </div>
  )
}

/** The LEAD mark beside an agent's name. */
export function LeadMark({ className }: { className?: string }) {
  return (
    <span className={cn("inline-flex items-center rounded-sm bg-chip-violet-bg px-1 font-mono text-micro font-semibold tracking-wide text-chip-violet-fg", className)}>
      LEAD
    </span>
  )
}

/** The star on a lead's avatar. */
export function LeadStar({ className }: { className?: string }) {
  return (
    <span aria-label="Lead" className={cn("absolute -right-1 -top-1 grid size-3.5 place-items-center rounded-full bg-card text-warn ring-1 ring-border", className)}>
      <Star className="size-2.5 fill-current" aria-hidden />
    </span>
  )
}

/** A neutral fact chip in a page head ("Lead · Scout", "3 agents"). */
export function HeadChip({ icon, children, tone, className }: { icon?: React.ReactNode; children: React.ReactNode; tone?: "warn"; className?: string }) {
  return (
    <span className={cn(
      "inline-flex h-6 items-center gap-1.5 rounded-md border border-border px-2 text-label",
      tone === "warn" ? "border-transparent bg-chip-warn-bg text-chip-warn-fg" : "text-muted-foreground",
      className,
    )}>
      {icon}
      {children}
    </span>
  )
}
