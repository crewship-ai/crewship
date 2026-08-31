"use client"

import { useMemo, useState } from "react"
import { Inbox as InboxIcon } from "lucide-react"

import {
  BarMenu,
  BarMenuBody,
  BarMenuEmpty,
  BarMenuFooter,
  BarMenuFooterAction,
  BarMenuFooterLink,
  BarMenuHeader,
  BarMenuRow,
  BarMenuSection,
} from "@/components/layout/bar-menu"
import { Pill } from "@/components/ui/detail"

import type { InboxItem } from "@/hooks/use-inbox"
import { isActionableInboxItem } from "@/components/features/inbox-v2/inbox-v2-derive"

import { ActorAvatar } from "./inbox-actor"
import {
  canRole, categoryOf, decisionMetaFor, expiresIn, remainingLabel, since, subjectOf,
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
//
// The chrome that came out of this — panel, header, section, row, footer —
// now lives in components/layout/bar-menu.tsx, because Activity and
// Notifications sit a centimetre away in the same strip and had each grown
// their own. Adopting the kit here is a visual no-op by construction: the kit's
// classes were lifted from this file unchanged.
// =============================================================================

export interface InboxBellViewProps {
  items: InboxItem[]
  role: WorkspaceRole | null
  /** Deep-link to the row: /inbox?item=<id> opens exactly what was shown. */
  onOpenItem: (id: string) => void
  onOpenInbox: () => void
  /** Marks every unread row read. Undefined hides the affordance entirely. */
  onMarkAllRead?: (ids: string[]) => void | Promise<void>
}

const MAX_PER_SECTION = 4

export function InboxBellView({ items, role, onOpenItem, onOpenInbox, onMarkAllRead }: InboxBellViewProps) {
  const [marking, setMarking] = useState(false)
  const [open, setOpen] = useState(false)

  const { decisions, recent, unread, soonest } = useMemo(() => {
    // "Waiting on you" means an agent is parked until a human answers —
    // the decisions bucket, not everything that happens to have a button.
    // A tripped circuit breaker also wants action, but nothing is standing
    // still because of it, so it belongs in Recent rather than at the top.
    //
    // Blocking rows stay in scope even once read: being looked at is not being
    // answered, and the agent is parked either way.
    const blocking = items.filter(isActionableInboxItem)
    const rest = items.filter((i) => !isActionableInboxItem(i) && i.state === "unread")

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

    // Keep expired deadlines in scope: a gate that ran out is the most urgent
    // thing in the queue, and filtering on `> 0` hid exactly that.
    const deadlines = blocking.map(expiresIn).filter((m): m is number => m != null)

    return {
      decisions: byUrgency,
      recent: [...rest].sort((a, b) => Date.parse(b.created_at) - Date.parse(a.created_at)),
      unread: items.filter((i) => i.state === "unread").length,
      soonest: deadlines.length > 0 ? Math.min(...deadlines) : null,
    }
  }, [items])

  const badge = decisions.length > 0 ? decisions.length : unread
  const urgent = decisions.length > 0

  const closeAndOpenItem = (id: string) => {
    setOpen(false)
    onOpenItem(id)
  }

  return (
    <BarMenu
      icon={InboxIcon}
      ariaLabel={`Inbox: ${decisions.length} awaiting a decision, ${unread} unread`}
      // Severity, not decoration: one badge colour for "someone is waiting on
      // you" and another for "there is unread mail" is the difference the
      // shipped single blue dot throws away.
      badge={{ count: badge, tone: urgent ? "urgent" : "active" }}
      open={open}
      onOpenChange={setOpen}
      testId="bell"
    >
      <BarMenuHeader
        title="Inbox"
        pill={
          soonest != null ? (
            <Pill tone="destructive">
              {soonest > 0 ? `expires in ${remainingLabel(soonest)}` : "one has expired"}
            </Pill>
          ) : undefined
        }
        meta={
          decisions.length > 0
            ? `${decisions.length} awaiting you · ${unread} unread`
            : `${unread} unread`
        }
      />

      <BarMenuBody>
        {decisions.length === 0 && recent.length === 0 && (
          <BarMenuEmpty icon={InboxIcon} message="All caught up" />
        )}

        {decisions.length > 0 && (
          <Section label="Needs a decision" tone="warn" items={decisions} role={role} onOpenItem={closeAndOpenItem} />
        )}

        {recent.length > 0 && (
          <Section label="Recent" items={recent} role={role} onOpenItem={closeAndOpenItem} />
        )}
      </BarMenuBody>

      <BarMenuFooter>
        {onMarkAllRead && unread > 0 && (
          <BarMenuFooterAction
            disabled={marking}
            onClick={async () => {
              setMarking(true)
              try {
                await onMarkAllRead(items.filter((i) => i.state === "unread").map((i) => i.id))
              } finally {
                setMarking(false)
              }
            }}
          >
            {marking ? "Marking…" : `Mark ${unread} read`}
          </BarMenuFooterAction>
        )}
        <BarMenuFooterLink onClick={() => { setOpen(false); onOpenInbox() }}>
          Open inbox →
        </BarMenuFooterLink>
      </BarMenuFooter>
    </BarMenu>
  )
}

function Section({
  label, items, role, tone, onOpenItem,
}: {
  label: string
  items: InboxItem[]
  role: WorkspaceRole | null
  tone?: "warn"
  onOpenItem: (id: string) => void
}) {
  return (
    <BarMenuSection
      label={label}
      count={items.length}
      tone={tone}
      overflow={
        items.length > MAX_PER_SECTION
          ? `+${items.length - MAX_PER_SECTION} more in the inbox`
          : undefined
      }
    >
      {items.slice(0, MAX_PER_SECTION).map((item) => (
        <BellRow key={item.id} item={item} role={role} onOpen={() => onOpenItem(item.id)} />
      ))}
    </BarMenuSection>
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
    <BarMenuRow
      testId={`bell-row-${item.id}`}
      onClick={onOpen}
      leading={<ActorAvatar actor={subject} size={24} />}
      title={item.title}
      meta={
        <>
          <span className="truncate">{subject.label}</span>
          <span>·</span>
          <span className="truncate font-mono">{categoryOf(item)}</span>
          {blocked && <span className="shrink-0">· admin decides</span>}
        </>
      }
      trailing={
        mins != null ? (
          <span className="type-meta font-medium text-destructive">
            {mins > 0 ? `in ${remainingLabel(mins)}` : "expired"}
          </span>
        ) : (
          <span className="type-meta text-muted-foreground-soft">{since(item.created_at)}</span>
        )
      }
    />
  )
}
