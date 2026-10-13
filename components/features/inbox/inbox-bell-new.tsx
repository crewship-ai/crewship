"use client"

import { useMemo, useState } from "react"
import { CheckCircle2, Inbox as InboxIcon, XCircle } from "lucide-react"

import {
  BarMenu,
  BarMenuBody,
  BarMenuChips,
  BarMenuEmpty,
  BarMenuFooter,
  BarMenuFooterAction,
  BarMenuFooterLink,
  BarMenuHeader,
  BarMenuRow,
  BarMenuRowAction,
  BarMenuSection,
  type BarMenuBadgeTone,
  type BarMenuChip,
} from "@/components/layout/bar-menu"
import { Pill } from "@/components/ui/detail"

import type { InboxAttentionClass, InboxItem } from "@/hooks/use-inbox"
import { isBlockingInboxItem } from "@/components/features/inbox-v2/inbox-v2-derive"
import { isDecisionForm } from "@/lib/decision-form"

import { ActorAvatar } from "./inbox-actor"
import {
  canRole, categoryOf, decisionMetaFor, expiresIn, jumpFor, remainingLabel, riskLevelOf, since, subjectOf,
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
//
// Two additions on top of that, both opt-in from the data end:
//   · kind chips (All / Decisions / Input / Review / Repair) over the server's
//     attention_class, filtering both sections at once;
//   · Approve / Deny on the row for the one decision that is a single helper
//     call (plain pipeline waitpoints — inlineDecisionFor), and the row's own
//     Open chat / Open run link for every other decision.
// =============================================================================

export interface InboxBellViewProps {
  items: InboxItem[]
  role: WorkspaceRole | null
  /** Deep-link to the row: /inbox?item=<id> opens exactly what was shown. */
  onOpenItem: (id: string) => void
  onOpenInbox: () => void
  /** Marks every unread row read. Undefined hides the affordance entirely. */
  onMarkAllRead?: (ids: string[]) => void | Promise<void>
  /**
   * Approve or deny a plain pipeline waitpoint from its row (see
   * inlineDecisionFor). Undefined renders no inline buttons at all — the row
   * still opens the item, where KindActions owns every decision.
   */
  onDecideWaitpoint?: (item: InboxItem, approved: boolean) => Promise<void>
  /**
   * Follow a row's own jump target (Open chat / Open run / Open <issue>, from
   * jumpFor). Undefined hides those links.
   */
  onOpenHref?: (href: string) => void
}

const MAX_PER_SECTION = 4

// -----------------------------------------------------------------------------
// Kind chips.
//
// The kind is the server's §12 attention class (attention_class: decision /
// input / review / repair — see attentionBadge in inbox-derive), not the
// producer `kind` and not the bell's own blocking split: "what is being asked
// of me" is the question a chip answers, and the server already computes it.
// A row from a kind that has not adopted the contract has no class, so it is
// counted under All and under no other chip rather than guessed into one.
// -----------------------------------------------------------------------------

export type InboxKindChip = "all" | InboxAttentionClass

const KIND_CHIP_LABEL: Record<InboxAttentionClass, string> = {
  decision: "Decisions",
  input: "Input",
  review: "Review",
  repair: "Repair",
}

const KIND_CHIP_ORDER: InboxAttentionClass[] = ["decision", "input", "review", "repair"]

/**
 * The chips for this set of rows. All always, with its count; a kind chip only
 * when at least one row carries that class — a chip that filters to nothing is
 * a dead click. Kind chips carry no count (the signed-off wireframe): the
 * badge and the header already count, and four more numbers in one strip
 * stop reading as numbers.
 */
export function inboxKindChips(items: InboxItem[]): BarMenuChip<InboxKindChip>[] {
  const present = new Set(items.map((i) => i.attention_class).filter(Boolean))
  return [
    { id: "all", label: "All", count: items.length },
    ...KIND_CHIP_ORDER.filter((k) => present.has(k)).map((k) => ({
      id: k,
      label: KIND_CHIP_LABEL[k],
      tone: k === "decision" ? ("warn" as const) : undefined,
    })),
  ]
}

export function filterInboxByKind(items: InboxItem[], chip: InboxKindChip): InboxItem[] {
  return chip === "all" ? items : items.filter((i) => i.attention_class === chip)
}

/**
 * The toolbar badge. Count: the decisions when any are waiting, else unread.
 * Tone: urgent (warn) only when a blocking decision is waiting — an agent is
 * parked on a human — and the ordinary primary tone for unread mail.
 *
 * "Blocking" is isBlockingInboxItem, the same predicate that fills "Needs a
 * decision", so the badge cannot turn orange over a row the panel files under
 * Recent (a tripped breaker, a missed schedule).
 */
export function inboxBadge(items: InboxItem[]): { count: number; tone: BarMenuBadgeTone } {
  const blocking = items.filter(isBlockingInboxItem).length
  const unread = items.filter((i) => i.state === "unread").length
  return blocking > 0 ? { count: blocking, tone: "urgent" } : { count: unread, tone: "active" }
}

/**
 * Whether a row may be decided inline, from the popover.
 *
 * Only the plain pipeline waitpoint — the one item kind whose decision is a
 * single call with a frontend helper (waitpointDecide, lib/api/waitpoints.ts),
 * the same call KindActions' default waitpoint branch makes on /inbox. Every
 * other decision opens the item instead, because each has a reason not to be
 * a one-click button in a 380px menu:
 *
 *   · autonomy gate (payload.kind=autonomy_gate) — decided in the approvals
 *     queue by approval_id, not by the waitpoint token;
 *   · hire (payload.kind=hire) — approve-hire, and its Deny lives on a twin
 *     row only the /inbox page can pair up;
 *   · a waitpoint with a decision_form — the answer has fields to fill;
 *   · risk_level=destructive — the inbox card carries a "cannot be undone"
 *     marker the row has no room for, and approving blind is the one mistake
 *     this must not make easier;
 *   · escalations, skill/routine proposals, consolidations — varied bodies
 *     (a link, a credential, a free-text answer, a diff to read) and, for
 *     most, raw endpoint calls inside KindActions rather than a helper.
 *
 * Role: waitpoint approve is roleCreate (MANAGER+), via decisionMetaFor's
 * `requires`. A VIEWER or MEMBER gets the row and the "Open" path, not a
 * button the server would 403.
 */
export function inlineDecisionFor(item: InboxItem, role: WorkspaceRole | null): "waitpoint" | null {
  if (item.kind !== "waitpoint" || item.state === "resolved" || !item.source_id) return null
  const sub = item.payload?.kind
  if (sub === "autonomy_gate" || sub === "hire") return null
  if (isDecisionForm(item.payload?.decision_form)) return null
  if (riskLevelOf(item) === "destructive") return null
  const spec = decisionMetaFor(item)
  if (!spec || !canRole(role, spec.requires)) return null
  return "waitpoint"
}

export function InboxBellView({
  items, role, onOpenItem, onOpenInbox, onMarkAllRead, onDecideWaitpoint, onOpenHref,
}: InboxBellViewProps) {
  const [marking, setMarking] = useState(false)
  const [open, setOpen] = useState(false)
  const [pickedKind, setPickedKind] = useState<InboxKindChip>("all")

  const { decisions, recent, unread, soonest } = useMemo(() => {
    // "Waiting on you" means an agent is parked until a human answers —
    // the decisions bucket, not everything that happens to have a button.
    // A tripped circuit breaker also wants action, but nothing is standing
    // still because of it, so it belongs in Recent rather than at the top.
    //
    // That is isBlockingInboxItem, not isActionableInboxItem. The filter used
    // the latter, which answers the broader "is there a source action behind
    // this row?" and says yes to a schedule advisory — so the paragraph above
    // described behaviour the code did not have, and the advisories ranked
    // above real gates.
    //
    // Blocking rows stay in scope even once read: being looked at is not being
    // answered, and the agent is parked either way.
    const blocking = items.filter(isBlockingInboxItem)
    const rest = items.filter((i) => !isBlockingInboxItem(i) && i.state === "unread")

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

  const badge = inboxBadge(items)

  // Chips count what the panel can show — both sections — not the whole
  // active list: "All (n)" disagreeing with the rows beneath it is the first
  // thing anyone would check. A chosen kind whose last row just resolved falls
  // back to All rather than leaving an empty panel behind a chip that is gone.
  const chips = useMemo(() => inboxKindChips([...decisions, ...recent]), [decisions, recent])
  const kind: InboxKindChip = chips.some((c) => c.id === pickedKind) ? pickedKind : "all"
  const shownDecisions = filterInboxByKind(decisions, kind)
  const shownRecent = filterInboxByKind(recent, kind)

  const closeAndOpenItem = (id: string) => {
    setOpen(false)
    onOpenItem(id)
  }
  const closeAndOpenHref = onOpenHref
    ? (href: string) => {
        setOpen(false)
        onOpenHref(href)
      }
    : undefined

  return (
    <BarMenu
      icon={InboxIcon}
      ariaLabel={`Inbox: ${decisions.length} awaiting a decision, ${unread} unread`}
      // Severity, not decoration: one badge colour for "someone is waiting on
      // you" and another for "there is unread mail" is the difference the
      // shipped single blue dot throws away.
      badge={badge}
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

      {/* Only when there is a choice to make: "All" alone filters nothing. */}
      {chips.length > 1 && (
        <BarMenuChips<InboxKindChip>
          ariaLabel="Filter by kind"
          testId="bell-chip"
          value={kind}
          onChange={setPickedKind}
          chips={chips}
        />
      )}

      <BarMenuBody>
        {shownDecisions.length === 0 && shownRecent.length === 0 && (
          <BarMenuEmpty icon={InboxIcon} message="All caught up" />
        )}

        {shownDecisions.length > 0 && (
          <Section
            label="Needs a decision" tone="warn" items={shownDecisions} role={role}
            onOpenItem={closeAndOpenItem} onDecideWaitpoint={onDecideWaitpoint} onOpenHref={closeAndOpenHref}
          />
        )}

        {shownRecent.length > 0 && (
          <Section label="Recent" items={shownRecent} role={role} onOpenItem={closeAndOpenItem} />
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
  label, items, role, tone, onOpenItem, onDecideWaitpoint, onOpenHref,
}: {
  label: string
  items: InboxItem[]
  role: WorkspaceRole | null
  tone?: "warn"
  onOpenItem: (id: string) => void
  /** Decision section only — Recent rows never carry inline actions. */
  onDecideWaitpoint?: (item: InboxItem, approved: boolean) => Promise<void>
  onOpenHref?: (href: string) => void
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
        <BellRow
          key={item.id} item={item} role={role} onOpen={() => onOpenItem(item.id)}
          onDecideWaitpoint={onDecideWaitpoint} onOpenHref={onOpenHref}
        />
      ))}
    </BarMenuSection>
  )
}

function BellRow({
  item, role, onOpen, onDecideWaitpoint, onOpenHref,
}: {
  item: InboxItem
  role: WorkspaceRole | null
  onOpen: () => void
  onDecideWaitpoint?: (item: InboxItem, approved: boolean) => Promise<void>
  onOpenHref?: (href: string) => void
}) {
  const [busy, setBusy] = useState<"approved" | "denied" | null>(null)
  const spec = decisionMetaFor(item)
  const blocked = spec != null && !canRole(role, spec.requires)
  const mins = expiresIn(item)
  const subject = subjectOf(item)

  const inline = onDecideWaitpoint && inlineDecisionFor(item, role) === "waitpoint"
  const jump = onOpenHref ? jumpFor(item) : null
  const decide = async (approved: boolean) => {
    if (!onDecideWaitpoint) return
    setBusy(approved ? "approved" : "denied")
    try {
      await onDecideWaitpoint(item, approved)
    } finally {
      setBusy(null)
    }
  }

  // Inline actions first (the answer), then the row's own destination. Rows
  // with neither stay one <button> — BarMenuRow only degrades to a div when
  // there is something under it.
  const actions = inline || jump ? (
    <>
      {inline && (
        <>
          <BarMenuRowAction onClick={() => decide(true)} disabled={busy !== null} ariaLabel={`Approve ${item.title}`}>
            <CheckCircle2 className="h-3 w-3" />
            {busy === "approved" ? "Approving…" : "Approve"}
          </BarMenuRowAction>
          <BarMenuRowAction danger onClick={() => decide(false)} disabled={busy !== null} ariaLabel={`Deny ${item.title}`}>
            <XCircle className="h-3 w-3" />
            {busy === "denied" ? "Denying…" : "Deny"}
          </BarMenuRowAction>
        </>
      )}
      {jump && onOpenHref && (
        <BarMenuRowAction onClick={() => onOpenHref(jump.href)}>{jump.label}</BarMenuRowAction>
      )}
    </>
  ) : undefined

  return (
    <BarMenuRow
      testId={`bell-row-${item.id}`}
      onClick={onOpen}
      actions={actions}
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
