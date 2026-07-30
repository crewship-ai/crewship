"use client"

import { useMemo, useState } from "react"
import { AnimatePresence, motion } from "motion/react"
import { Inbox as InboxIcon } from "lucide-react"

import { Button } from "@/components/ui/button"
import { Pill } from "@/components/ui/detail"
import { cn } from "@/lib/utils"

import type { InboxItem } from "@/hooks/use-inbox"

import { ActorAvatar } from "./inbox-actor"
import {
  bucketOf, canRole, categoryOf, decisionMetaFor, expiresIn, since, subjectOf,
  type WorkspaceRole,
} from "./inbox-derive"

// =============================================================================
// The top-bar inbox popover.
//
// What the shipped one does and why each part is wrong:
//
//   · It lists the five most recent UNREAD items, newest first. A waitpoint
//     that expires in eight minutes but arrived yesterday is invisible behind
//     five newer advisories. Arrival order is the wrong sort for a queue whose
//     entire purpose is "what runs out first".
//   · It reads state=unread, so a blocking escalation you merely OPENED drops
//     out of the bell while the agent is still standing still. The /inbox page
//     fixed exactly this — its default view is active, not unread — and the
//     bell was never brought along.
//   · Every row routes to /inbox rather than to the item, so acting on what the
//     popover showed you starts with finding it again.
//   · Rows carry a kind glyph and no face, so "casey requested GH_TOKEN" is a
//     red circle, and timeout_at — the one number that makes this urgent — is
//     not rendered anywhere.
//
// So: decisions in their own section that nothing can push below, ordered by
// what expires first, blocking items included whether or not they were read,
// the subject's own face, and a row that deep-links to the item.
// =============================================================================

export interface InboxBellViewProps {
  items: InboxItem[]
  role: WorkspaceRole | null
  /** Deep-link to the item. Today /inbox selects the newest match; the
   *  follow-up is ?item=<id> so the row the popover showed opens directly. */
  onOpenItem: (id: string) => void
  onOpenInbox: () => void
}

const MAX_PER_SECTION = 4

export function InboxBellView({ items, role, onOpenItem, onOpenInbox }: InboxBellViewProps) {
  const [open, setOpen] = useState(false)

  const { decisions, recent, unread, soonest } = useMemo(() => {
    // "Waiting on you" means an agent is parked until a human answers —
    // the decisions bucket, not everything that happens to have a button.
    // A tripped circuit breaker also wants action, but nothing is standing
    // still because of it, so it belongs in Recent rather than at the top.
    //
    // Blocking rows stay in scope even once read: being looked at is not being
    // answered, and the agent is parked either way.
    const blocking = items.filter((i) => bucketOf(i) === "decisions")
    const rest = items.filter((i) => bucketOf(i) !== "decisions" && i.state === "unread")

    const byUrgency = [...blocking].sort((a, b) => {
      const ea = expiresIn(a)
      const eb = expiresIn(b)
      if (ea != null && eb != null) return ea - eb
      if (ea != null) return -1
      if (eb != null) return 1
      // No deadline: oldest first. A request that has been ignored longest is
      // the one closest to becoming a problem.
      return Date.parse(a.created_at) - Date.parse(b.created_at)
    })

    const deadlines = blocking.map(expiresIn).filter((m): m is number => m != null && m > 0)

    return {
      decisions: byUrgency,
      recent: [...rest].sort((a, b) => Date.parse(b.created_at) - Date.parse(a.created_at)),
      unread: items.filter((i) => i.state === "unread").length,
      soonest: deadlines.length > 0 ? Math.min(...deadlines) : null,
    }
  }, [items])

  const badge = decisions.length > 0 ? decisions.length : unread
  const urgent = decisions.length > 0

  return (
    <div className="relative">
      <Button
        variant="ghost"
        size="icon-sm"
        className="relative"
        aria-label={`Inbox: ${decisions.length} awaiting a decision, ${unread} unread`}
        aria-expanded={open}
        data-testid="bell-trigger"
        onClick={() => setOpen((v) => !v)}
      >
        <InboxIcon className="h-4 w-4" />
        {badge > 0 && (
          <span
            data-testid="bell-badge"
            className={cn(
              "absolute -right-0.5 -top-0.5 flex h-4 min-w-[16px] items-center justify-center rounded-full px-1 text-[9px] font-semibold",
              // Severity, not decoration: one badge colour for "someone is
              // waiting on you" and another for "there is unread mail" is the
              // difference the shipped single blue dot throws away.
              urgent ? "bg-warn text-background" : "bg-primary text-primary-foreground",
            )}
          >
            {badge > 99 ? "99+" : badge}
          </span>
        )}
      </Button>

      <AnimatePresence>
        {open && (
          <>
            <div className="fixed inset-0 z-40" onClick={() => setOpen(false)} />
            <motion.div
              initial={{ opacity: 0, scale: 0.96, y: -4 }}
              animate={{ opacity: 1, scale: 1, y: 0, transition: { duration: 0.12 } }}
              exit={{ opacity: 0, scale: 0.96, y: -4, transition: { duration: 0.1 } }}
              className="absolute right-0 top-9 z-50 w-[380px] overflow-hidden rounded-lg border border-white/[0.1] bg-card shadow-xl"
              data-testid="bell-popover"
            >
              <div className="flex items-center gap-2 border-b border-white/[0.06] px-3 py-2">
                <span className="type-row font-medium">Inbox</span>
                {soonest != null && (
                  <Pill tone="destructive">expires in {soonest}m</Pill>
                )}
                <span className="type-meta ml-auto text-muted-foreground">
                  {decisions.length > 0
                    ? `${decisions.length} awaiting you · ${unread} unread`
                    : `${unread} unread`}
                </span>
              </div>

              <div className="max-h-[420px] overflow-y-auto">
                {decisions.length === 0 && recent.length === 0 && (
                  <div className="flex flex-col items-center gap-2 p-6 text-center">
                    <InboxIcon className="h-6 w-6 text-muted-foreground/30" />
                    <span className="type-row text-muted-foreground">All caught up</span>
                  </div>
                )}

                {decisions.length > 0 && (
                  <Section
                    label="Needs a decision"
                    count={decisions.length}
                    tone="warn"
                    items={decisions}
                    role={role}
                    onOpenItem={(id) => { setOpen(false); onOpenItem(id) }}
                  />
                )}

                {recent.length > 0 && (
                  <Section
                    label="Recent"
                    count={recent.length}
                    items={recent}
                    role={role}
                    onOpenItem={(id) => { setOpen(false); onOpenItem(id) }}
                  />
                )}
              </div>

              <div className="flex items-center gap-2 border-t border-white/[0.06] px-2 py-1.5">
                <button type="button" className="type-meta rounded px-2 py-1 text-muted-foreground hover:text-foreground">
                  Mark all read
                </button>
                <button
                  type="button"
                  onClick={() => { setOpen(false); onOpenInbox() }}
                  className="type-meta ml-auto rounded px-2 py-1 text-primary hover:underline"
                >
                  Open inbox →
                </button>
              </div>
            </motion.div>
          </>
        )}
      </AnimatePresence>
    </div>
  )
}

function Section({
  label, count, items, role, tone, onOpenItem,
}: {
  label: string
  count: number
  items: InboxItem[]
  role: WorkspaceRole | null
  tone?: "warn"
  onOpenItem: (id: string) => void
}) {
  return (
    <div>
      <div className="flex items-center gap-2 border-b border-white/[0.04] bg-surface-subtle/60 px-3 py-1">
        <span className={cn("type-meta uppercase tracking-wider", tone === "warn" ? "text-warn" : "text-foreground/40")}>
          {label}
        </span>
        <span className="type-meta ml-auto tabular-nums text-muted-foreground-soft">{count}</span>
      </div>
      <ul>
        {items.slice(0, MAX_PER_SECTION).map((item) => (
          <BellRow key={item.id} item={item} role={role} onOpen={() => onOpenItem(item.id)} />
        ))}
      </ul>
      {items.length > MAX_PER_SECTION && (
        <p className="type-meta px-3 py-1.5 text-muted-foreground-soft">
          +{items.length - MAX_PER_SECTION} more in the inbox
        </p>
      )}
    </div>
  )
}

function BellRow({
  item, role, onOpen,
}: {
  item: InboxItem
  role: WorkspaceRole | null
  onOpen: () => void
}) {
  const spec = decisionMetaFor(item)
  const blocked = spec != null && !canRole(role, spec.requires)
  const mins = expiresIn(item)
  const subject = subjectOf(item)

  return (
    <li>
      <button
        type="button"
        onClick={onOpen}
        data-testid={`bell-row-${item.id}`}
        className="flex w-full items-start gap-2.5 px-3 py-2 text-left transition-colors hover:bg-white/[0.04]"
      >
        <ActorAvatar actor={subject} size={24} />
        <span className="min-w-0 flex-1">
          <span className="type-row block truncate text-foreground">{item.title}</span>
          <span className="type-meta flex min-w-0 items-center gap-1.5 text-muted-foreground-soft">
            <span className="truncate">{subject.label}</span>
            <span>·</span>
            <span className="truncate font-mono">{categoryOf(item)}</span>
            {blocked && <span className="shrink-0">· admin decides</span>}
          </span>
        </span>
        <span className="shrink-0 text-right">
          {mins != null && mins > 0 ? (
            <span className="type-meta font-medium text-destructive">in {mins}m</span>
          ) : (
            <span className="type-meta text-muted-foreground-soft">{since(item.created_at)}</span>
          )}
        </span>
      </button>
    </li>
  )
}
