"use client"

import { createContext, useContext, useEffect, useRef, useState } from "react"
import { createPortal } from "react-dom"
import Link from "next/link"
import { CheckCircle2, Clock3, Lock, type LucideIcon } from "lucide-react"

import { since } from "@/components/features/inbox/inbox-derive"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { StatusPill } from "@/components/ui/status-pill"
import { entityHref } from "@/lib/entity-links"
import { cn } from "@/lib/utils"

import { EntryAvatar, entryIdentity } from "./inbox-entry-identity"
import { ageLabel, entryAttention, entryKindPill, entryTitle, INBOX_ATTENTION } from "./inbox-v2-derive"
import type { InboxLookup, InboxV2Entry } from "./inbox-v2-types"

/**
 * The one detail layout every inbox kind uses (Harbor 2 v67):
 *
 *   sender + attention  →  title  →  message  →  decision block, its actions
 *   docked bottom-right  →  Where this came from  →  expandable context  →
 *   who decides  →  keyboard hint
 *
 * The decision is always in the same place, so a reader's eye and hand learn
 * one position instead of one per kind.
 */

export function DetailFrame({
  entry, lookup, title, attention, children, className,
}: {
  entry: InboxV2Entry
  lookup: InboxLookup
  /** Overrides the entry's title (a detail read may know a better one). */
  title?: string
  /** Replaces the head's attention pill (the server's badge, when it wrote one). */
  attention?: React.ReactNode
  children: React.ReactNode
  className?: string
}) {
  const ref = useRef<HTMLElement>(null)
  useEffect(() => {
    const element = ref.current
    if (!element) return
    // Markdown can finish rendering after mount. Keep wide code and table
    // regions reachable with a keyboard without moving focus while it arrives.
    const focusable = () => element.querySelectorAll("pre, table").forEach((node) => node.setAttribute("tabindex", "0"))
    focusable()
    const observer = new MutationObserver(focusable)
    observer.observe(element, { childList: true, subtree: true })
    return () => observer.disconnect()
  }, [])
  return (
    <article
      ref={ref}
      data-testid="inbox-detail-frame"
      className={cn(
        "mx-auto flex w-full min-w-0 max-w-[860px] flex-col gap-3.5 px-4 pb-6 pt-5 [overflow-wrap:anywhere] sm:px-6",
        "motion-safe:animate-in motion-safe:fade-in motion-safe:duration-150 [&_button]:max-w-full [&_pre]:max-w-full [&_table]:block [&_table]:overflow-x-auto [&_textarea]:max-w-full",
        className,
      )}
    >
      <DetailHead entry={entry} lookup={lookup} attention={attention} />
      <h1 className="text-title font-semibold leading-tight tracking-tight">{title ?? entryTitle(entry)}</h1>
      {children}
      <KeyHints />
    </article>
  )
}

/** Avatar 32, sender, "{crew} · {age} ago · merged N×", attention and kind. */
export function DetailHead({ entry, lookup, attention: override }: { entry: InboxV2Entry; lookup: InboxLookup; attention?: React.ReactNode }) {
  const { agent, crew, name } = entryIdentity(entry, lookup)
  const attention = INBOX_ATTENTION.find((a) => a.key === entryAttention(entry))
  const kind = entryKindPill(entry)
  const merged = entry.groupedItems?.length ?? 0
  const age = ageLabel(entry.createdAt)
  // "12m ago", "3h ago", but a date stays a date ("2 Oct").
  const meta = [crew?.name ?? agent?.crew?.name, /^\d+[mh]$/.test(age) ? `${age} ago` : age, merged > 1 ? `merged ${merged}×` : null]
    .filter(Boolean).join(" · ")
  return (
    <div data-testid="inbox-detail-head" className="flex flex-wrap items-center gap-2.5">
      <EntryAvatar entry={entry} lookup={lookup} size={32} />
      <div className="min-w-0">
        {agent ? (
          <Link href={entityHref({ kind: "agent", slug: agent.slug })} className="block truncate font-semibold hover:underline">{agent.name}</Link>
        ) : (
          <b className="block truncate font-semibold">{name}</b>
        )}
        <span className="block truncate text-label text-muted-foreground">{meta}</span>
      </div>
      <span className="flex-1" />
      {override ?? (attention ? <StatusPill tone={attention.tone} label={attention.label} /> : <StatusPill tone="muted" label={entry.historical ? "Record" : "Update"} />)}
      <Badge variant="outline" className="text-micro">{kind.label}</Badge>
    </div>
  )
}

/* ---------------------------------------------------------------- the rail */

/**
 * Wide desktop (≥ 1536 px, ODCH-07; v67 `ib-rail`): what v67 marks
 * `data-rail` — "Where this came from" and the expandable run context —
 * leaves the middle column for the right rail, opened. The page provides the
 * rail's slot while the rail is shown; a RailPart renders into it then and
 * in place otherwise, so each part exists once and never twice.
 */
export const WIDE_DETAIL_QUERY = "(min-width: 1536px)"
const RailSlot = createContext<HTMLElement | null>(null)
const InRail = createContext(false)
export const RailSlotProvider = RailSlot.Provider

/** True at the width where the Inbox shows its rail. */
export function useWideDetail(): boolean {
  const [wide, setWide] = useState(false)
  useEffect(() => {
    if (typeof window === "undefined" || typeof window.matchMedia !== "function") return
    const mql = window.matchMedia(WIDE_DETAIL_QUERY)
    const update = () => setWide(mql.matches)
    update()
    mql.addEventListener?.("change", update)
    return () => mql.removeEventListener?.("change", update)
  }, [])
  return wide
}

/** A part of the detail that moves into the rail on a wide desktop. */
export function RailPart({ children }: { children: React.ReactNode }) {
  const slot = useContext(RailSlot)
  if (!slot) return <>{children}</>
  return createPortal(<InRail.Provider value>{children}</InRail.Provider>, slot)
}

export type DecisionTone = "default" | "warn" | "danger"

/**
 * The decision block: a header with the ask and its deadline, the fields and
 * facts the decision rests on, and the dock — note on the left, actions on
 * the right with the primary last.
 */
export function DecisionBlock({
  icon: Icon, heading, badge, expires, tone = "default", children, actions, note, testId = "decision-block",
}: {
  icon: LucideIcon
  heading: string
  badge?: React.ReactNode
  /** "in 3h" / "expired" — rendered in the warn tone with a clock. */
  expires?: string | null
  tone?: DecisionTone
  children?: React.ReactNode
  actions?: React.ReactNode
  note?: React.ReactNode
  testId?: string
}) {
  return (
    <section data-testid={testId} data-tone={tone} aria-label={heading} className="overflow-hidden rounded-xl border border-border bg-card">
      <header
        className={cn(
          "flex flex-wrap items-center gap-2 border-b border-border px-3.5 py-2.5",
          tone === "danger" ? "bg-chip-danger-bg text-chip-danger-fg" : tone === "warn" ? "bg-chip-warn-bg text-chip-warn-fg" : "bg-surface-subtle text-muted-foreground",
        )}
      >
        <Icon className="h-4 w-4 shrink-0" aria-hidden />
        <b className="font-semibold text-foreground">{heading}</b>
        {badge}
        <span className="flex-1" />
        {expires && (
          <span className="inline-flex items-center gap-1 whitespace-nowrap text-label font-semibold text-warn">
            <Clock3 className="h-3.5 w-3.5" aria-hidden />
            {expires === "expired" ? "expired" : `expires ${expires}`}
          </span>
        )}
      </header>
      {children && <div className="grid gap-3 p-3.5">{children}</div>}
      {(actions || note) && <Dock note={note}>{actions}</Dock>}
    </section>
  )
}

/** The action row. Primary is the right-most button; the note sits left. */
export function Dock({ note, children, standalone = false }: { note?: React.ReactNode; children?: React.ReactNode; standalone?: boolean }) {
  return (
    <div
      data-testid="decision-dock"
      className={cn(
        "sticky bottom-0 flex flex-wrap items-center gap-2 border-t border-border bg-surface-subtle px-3.5 py-2.5",
        standalone && "rounded-xl border",
      )}
    >
      {note && <div className="min-w-0 text-label text-muted-foreground">{note}</div>}
      <span className="flex-1" />
      {children}
    </div>
  )
}

/** "Where this came from": the objects that raised this, as chips. */
export function OriginChips({ links }: { links: { label: string; href: string; icon: LucideIcon }[] }) {
  const inRail = useContext(InRail)
  if (links.length === 0) return null
  return (
    <nav
      aria-label="Where this came from"
      className={cn(
        "flex flex-wrap items-center gap-1.5",
        // In the rail (v67 `.ib-rail .ib-ctx`): a card, one chip per line.
        inRail && "flex-col items-stretch rounded-xl border border-border bg-card p-3",
      )}
    >
      <span className="mr-1 text-label text-muted-foreground">Where this came from</span>
      {links.map((l) => (
        <Button key={l.href} asChild size="xs" variant="outline" className={cn("gap-1.5 rounded-full", inRail && "justify-start")}>
          <Link href={l.href}><l.icon className="h-3 w-3" aria-hidden />{l.label}</Link>
        </Button>
      ))}
    </nav>
  )
}

/** Expandable context ("How the run got here", "Additional context"). */
export function DetailExpand({ title, meta, open, children }: { title: string; meta?: React.ReactNode; open?: boolean; children: React.ReactNode }) {
  // v67 opens every <details> it moves into the rail.
  const inRail = useContext(InRail)
  return (
    <details open={inRail || open} className="group rounded-lg border border-border bg-card">
      <summary className="flex cursor-pointer list-none items-center gap-2 px-3 py-2 font-medium [&::-webkit-details-marker]:hidden">
        <span aria-hidden className="text-muted-foreground transition-transform group-open:rotate-90">›</span>
        {title}
        {meta && <span className="ml-auto text-label font-normal text-muted-foreground">{meta}</span>}
      </summary>
      <div className="border-t border-border p-3">{children}</div>
    </details>
  )
}

/** Who decides, when the viewer cannot. */
export function RoleNote({ children }: { children: React.ReactNode }) {
  return (
    <p data-testid="inbox-role-note" className="m-0 flex items-center gap-1.5 text-label text-muted-foreground">
      <Lock className="h-3 w-3 shrink-0" aria-hidden />
      {children}
    </p>
  )
}

/**
 * A History item's receipt (v67): what happened, that the record is kept, and
 * where the run's own record lives. Shown above the item's preserved detail.
 */
export function HistoryReceipt({ outcome, at, activityHref = "/activity" }: { outcome: string; at?: string | null; activityHref?: string }) {
  return (
    <div role="status" data-testid="history-receipt" className="grid justify-items-center gap-1 rounded-xl border border-border bg-card px-4 py-6 text-center">
      <CheckCircle2 className="h-7 w-7 text-success" aria-hidden />
      <h2 className="mt-1 text-heading font-semibold">{outcome}</h2>
      <p className="m-0 text-body text-muted-foreground">Saved {at ? since(at) : "earlier"} · the record stays in History</p>
      <div className="mt-3 flex flex-wrap justify-center gap-2">
        <Button asChild size="sm" variant="outline"><Link href={activityHref}>View in Activity</Link></Button>
      </div>
    </div>
  )
}

/** The outcome word for a receipt, from either source's vocabulary. */
export function outcomeLabel(action: string | null | undefined): string {
  const labels: Record<string, string> = {
    approved: "Approved",
    approve: "Approved",
    supplied: "Credential supplied",
    denied: "Denied",
    deny: "Denied",
    rejected: "Rejected",
    reject: "Rejected",
    retried: "Retry started",
    reenabled: "Schedule re-enabled",
    archived: "Archived",
    dismissed: "Dismissed",
    cancelled: "Cancelled",
    expired: "Expired",
    timeout: "Expired",
    timed_out: "Expired",
    answer: "Answered",
    take_over: "Taken over",
    resolved: "Decision already completed",
  }
  return (action && labels[action]) || "Decision saved"
}

/**
 * Wide desktop (≥ 1536 px, v67 `ib-rail`): who sent this, and the last few
 * things they sent, beside the decision — so the middle column carries only
 * the message and the ask.
 */
export function DetailRail({ entry, lookup, earlier, onOpen, slotRef }: {
  entry: InboxV2Entry
  lookup: InboxLookup
  earlier: InboxV2Entry[]
  onOpen: (entry: InboxV2Entry) => void
  /** Where RailPart content lands (sender card above, Earlier from below). */
  slotRef?: (element: HTMLElement | null) => void
}) {
  const { agent, crew, name } = entryIdentity(entry, lookup)
  return (
    <aside aria-label="About the sender" data-testid="inbox-rail" className="sticky top-0 hidden content-start gap-3 pt-5 2xl:grid">
      <div data-rail-part="sender" className="flex items-center gap-2.5 rounded-xl border border-border bg-card p-3">
        <EntryAvatar entry={entry} lookup={lookup} size={32} />
        <div className="min-w-0">
          <b className="block truncate font-semibold">{agent?.name ?? name}</b>
          <small className="block truncate text-label text-muted-foreground">{crew?.name ?? agent?.crew?.name ?? entryKindPill(entry).label}</small>
        </div>
      </div>
      {/* display: contents, so the moved parts are rail rows like the cards around them. */}
      {slotRef && <div ref={slotRef} data-rail-part="moved" className="contents" />}
      {earlier.length > 0 && (
        <section data-rail-part="earlier" aria-label={`Earlier from ${agent?.name ?? name}`} className="grid gap-1.5 rounded-xl border border-border bg-card p-3">
          <h2 className="m-0 text-label font-semibold text-muted-foreground">Earlier from {agent?.name ?? name}</h2>
          {earlier.map((e) => (
            <button key={e.key} type="button" onClick={() => onOpen(e)} className="row-hover flex w-full justify-between gap-2.5 rounded-md px-1 py-1.5 text-left text-body">
              <span className="min-w-0 truncate">{entryTitle(e)}</span>
              <span className="shrink-0 text-micro tabular-nums text-muted-foreground">{ageLabel(e.createdAt)}</span>
            </button>
          ))}
        </section>
      )}
    </aside>
  )
}

function Kbd({ children }: { children: React.ReactNode }) {
  return (
    <kbd className="inline-flex h-4 min-w-4 items-center justify-center rounded border border-border bg-muted px-1 font-mono text-micro leading-none">
      {children}
    </kbd>
  )
}

function KeyHints() {
  return (
    <div aria-hidden className="hidden flex-wrap items-center gap-1.5 pt-1.5 text-label text-muted-foreground sm:flex">
      <Kbd>J</Kbd><Kbd>K</Kbd> next / previous <Kbd>E</Kbd> archive <Kbd>R</Kbd> reply <Kbd>⌘↵</Kbd> send
    </div>
  )
}
