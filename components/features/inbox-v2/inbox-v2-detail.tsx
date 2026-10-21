"use client"

import { MarkdownContent } from "@/components/features/issues/markdown-content"

import { useEffect, useState } from "react"
import Link from "next/link"
import {
  AlertTriangle, Archive, ArrowUpRight, CheckCircle2, ClipboardCheck, History,
  Inbox, MessageSquare, ShieldAlert,
} from "lucide-react"
import { toast } from "@/lib/toast"

import { InboxDetail } from "@/components/features/inbox/inbox-detail"
import type { WorkspaceRole } from "@/components/features/inbox/inbox-derive"
import { canRole, deciderCopy, remainingLabel, since } from "@/components/features/inbox/inbox-derive"
import { PageEmpty } from "@/components/layout/page-state"
import { Button } from "@/components/ui/button"
import { StatusPill } from "@/components/ui/status-pill"
import { Textarea } from "@/components/ui/textarea"
import type { InboxAction, InboxActResult, InboxItem } from "@/hooks/use-inbox"
import { useSession } from "@/hooks/use-auth"
import { entityHref } from "@/lib/entity-links"
import { isAdminTier } from "@/lib/permissions/tiers"
import { formatDateTime } from "@/lib/time"

import { outcomeStatus } from "./inbox-v2-derive"
import { DecisionBlock, DetailExpand, DetailFrame, Dock, OriginChips, RailPart, RoleNote, outcomeLabel } from "./inbox-detail-frame"
import { InboxOverview, type InboxOverviewProps } from "./inbox-v2-overview"
import { EMPTY_INBOX_LOOKUP, type InboxLookup, type InboxV2Confirmation, type InboxV2Entry } from "./inbox-v2-types"

interface Props {
  entry: InboxV2Entry | null
  /** A deep link named a row that is not in any feed once they have settled. */
  selectionMissing?: boolean
  role: WorkspaceRole | null
  detailedInboxItem?: InboxItem
  detailLoading?: boolean
  confirmation: InboxV2Confirmation | null
  nextReview?: { count: number; onOpen: () => void }
  onClearConfirmation: () => void
  onViewReceipt: (entry: InboxV2Entry) => void
  onInboxResolve: (item: InboxItem, action: string) => Promise<void>
  onInboxArchive: (item: InboxItem) => Promise<void>
  onInboxMarkUnread: (item: InboxItem) => Promise<void>
  onInboxRefresh: (item: InboxItem, action?: string) => Promise<void>
  /** Act on a run_needs_human card (#2398) — the same act door the v1 list uses. */
  onInboxAct: (item: InboxItem, action: InboxAction, input?: string) => Promise<InboxActResult>
  onApprovalDecide: (entry: InboxV2Entry, decision: "approved" | "denied", comment: string) => Promise<void>
  onArchiveGroup: (entry: InboxV2Entry) => Promise<void>
  /** Crews and agents by id/slug, so the pane names things instead of ids. */
  lookup?: InboxLookup
  /** Deny for a staged hire, wired through its approvals-queue twin. */
  onDenyHire?: () => Promise<void>
  /** What the pane shows while nothing is open (Harbor 2: the Overview). */
  overview?: InboxOverviewProps
  /** "No longer in your inbox" → History / back to To handle. */
  onOpenHistory?: () => void
  onBackToHandle?: () => void
  /** The ⋯ menu lives in the top bar on the Inbox page; false renders none here. */
  menuInPane?: boolean
}

export function InboxV2Detail(props: Props) {
  const lookup = props.lookup ?? EMPTY_INBOX_LOOKUP
  if (props.confirmation) {
    return <DecisionConfirmation {...props} confirmation={props.confirmation} />
  }
  if (!props.entry) {
    // Saying "not here" is the whole point. This pane used to fall back to
    // the first row in the list, so a stale link armed an Approve button for
    // a decision the user never asked to see.
    if (props.selectionMissing) {
      return (
        <PageEmpty
          icon={Inbox}
          title="No longer in your inbox"
          description="It was handled, archived or moved. Your history keeps the record."
          primary={props.onOpenHistory ? { label: "Open History", icon: History, onClick: props.onOpenHistory } : undefined}
          secondary={props.onBackToHandle ? { label: "Back to To handle", onClick: props.onBackToHandle } : undefined}
        />
      )
    }
    if (props.overview) return <InboxOverview {...props.overview} />
    return null
  }
  const { entry } = props
  if (entry.source === "inbox") {
    const item = props.detailedInboxItem ?? entry.inboxItem
    if (!item) return null
    return (
      <div className="flex w-full flex-col">
        {props.detailLoading && (
          <div className="mx-auto w-full max-w-[860px] px-4 pt-3 text-label text-muted-foreground sm:px-6" role="status">Loading decision evidence…</div>
        )}
        <InboxDetail
          item={item}
          role={props.role}
          menu={props.menuInPane ?? false}
          onResolve={(action) => props.onInboxResolve(item, action)}
          onArchive={() => props.onInboxArchive(item)}
          onMarkUnread={() => { void props.onInboxMarkUnread(item) }}
          onRefresh={(action) => props.onInboxRefresh(item, action)}
          onAct={(action, input) => props.onInboxAct(item, action, input)}
          lookup={lookup}
          onDenyHire={props.onDenyHire}
        />
      </div>
    )
  }
  if (entry.source === "approval") return <ApprovalDetail entry={entry} role={props.role} onDecide={props.onApprovalDecide} lookup={lookup} />
  if (entry.source === "mission") return <MissionDetail entry={entry} lookup={lookup} />
  return <GroupedIncident entry={entry} role={props.role} lookup={lookup} onArchive={props.onArchiveGroup} />
}

/**
 * After a decision (v67 receipt): the outcome, that it was saved and stays in
 * History, View record and — only once this one is complete — the next item
 * to review. The list is beside the pane (on a phone the back arrow is in the
 * top bar), so there is no separate "Back to inbox".
 */
function DecisionConfirmation({
  confirmation, onViewReceipt, nextReview, lookup,
}: Props & { confirmation: InboxV2Confirmation }) {
  const label = outcomeLabel(confirmation.action)
  const saved = since(confirmation.at)
  return (
    <DetailFrame entry={confirmation.entry} lookup={lookup ?? EMPTY_INBOX_LOOKUP}>
      <div role="status" data-testid="inbox-receipt" className="grid justify-items-center gap-1 rounded-xl border border-border bg-card px-4 py-7 text-center">
        <CheckCircle2 className="h-7 w-7 text-success" aria-hidden />
        <h2 className="mt-1 text-heading font-semibold">{label}</h2>
        <p className="m-0 text-body text-muted-foreground">Saved {saved === "just now" ? "just now" : saved} · the record stays in History</p>
        <div className="mt-3 flex flex-wrap justify-center gap-2">
          <Button size="sm" variant={nextReview ? "outline" : "default"} onClick={() => onViewReceipt(confirmation.entry)}>View record</Button>
          {nextReview && <Button size="sm" onClick={nextReview.onOpen}>Next to review · {nextReview.count}</Button>}
        </div>
      </div>
    </DetailFrame>
  )
}

function ApprovalDetail({
  entry, role, onDecide, lookup,
}: {
  entry: InboxV2Entry
  role: WorkspaceRole | null
  onDecide: Props["onApprovalDecide"]
  lookup: InboxLookup
}) {
  const row = entry.approval!
  const crew = row.crew_id ? lookup.crewById.get(row.crew_id) ?? null : null
  const agent = row.agent_id ? lookup.agentById.get(row.agent_id) ?? null : null
  const me = useSession().data?.user?.id ?? null
  const [comment, setComment] = useState("")
  const [busy, setBusy] = useState<"approved" | "denied" | null>(null)
  useEffect(() => setComment(""), [row.id])
  const pending = row.status === "pending"
  const allowed = canRole(role, "manage")
  const destructive = row.kind === "destructive_op"
  const tool = typeof row.payload?.tool === "string" ? row.payload.tool : ""
  const target = typeof row.payload?.target === "string" ? row.payload.target : typeof row.payload?.environment === "string" ? row.payload.environment : ""
  const deadlineMins = row.timeout_at
    ? Math.round((Date.parse(row.timeout_at) - Date.now()) / 60_000)
    : null

  async function decide(status: "approved" | "denied") {
    setBusy(status)
    try {
      await onDecide(entry, status, comment)
    } catch (error) {
      toast.error(error instanceof Error ? error.message : "Decision failed")
    } finally {
      setBusy(null)
    }
  }

  const origin = [
    ...(agent ? [{ label: `Chat with ${agent.name}`, href: entityHref({ kind: "chat", agentSlug: agent.slug }), icon: MessageSquare }] : []),
    ...(row.mission_id ? [{ label: "Open mission", href: `/missions/${encodeURIComponent(row.mission_id)}/timeline`, icon: ArrowUpRight }] : []),
  ]

  return (
    <DetailFrame entry={entry} lookup={lookup}>
      <p className="m-0 whitespace-pre-wrap text-default leading-relaxed text-foreground/90">{row.reason || "No reason supplied."}</p>
      <DecisionBlock
        icon={destructive ? AlertTriangle : ShieldAlert}
        heading={pending ? "Waiting on your decision" : "Decision record"}
        tone={pending && destructive ? "danger" : "default"}
        expires={pending && deadlineMins != null ? deadlineMins > 0 ? `in ${remainingLabel(deadlineMins)}` : "expired" : null}
        note={pending && !allowed ? <RoleNote>{deciderCopy("manage")}</RoleNote> : undefined}
        actions={pending ? (
          <>
            <Button size="sm" variant="outline" disabled={!allowed || busy !== null} onClick={() => void decide("denied")}>
              {busy === "denied" ? "Denying…" : "Deny"}
            </Button>
            <Button size="sm" disabled={!allowed || busy !== null} onClick={() => void decide("approved")}>
              {busy === "approved" ? "Approving…" : "Approve"}
            </Button>
          </>
        ) : undefined}
      >
        <div className="flex flex-wrap gap-1.5">
          <Chip>{row.kind}</Chip>
          {tool && <Chip mono>{tool}</Chip>}
          {target && <Chip>target: {target}</Chip>}
          {destructive && <Chip danger>destructive · cannot be undone by re-running</Chip>}
        </div>
        <dl className="grid grid-cols-[minmax(0,9rem)_minmax(0,1fr)] gap-x-4 gap-y-2 rounded-lg border border-border px-3 py-2.5 text-body">
          <dt className="text-muted-foreground">Requested by</dt>
          <dd className="m-0 min-w-0">
            {agent ? <Link href={entityHref({ kind: "agent", slug: agent.slug })} className="hover:underline">{agent.name}</Link> : entry.subject}
            {crew && <> · <Link href={entityHref({ kind: "crew", slug: crew.slug })} className="hover:underline">{crew.name}</Link></>}
          </dd>
          <dt className="text-muted-foreground">Requested</dt>
          <dd className="m-0">{formatDateTime(row.created_at)}</dd>
          {humanContextEntries(row.payload ?? {}).filter(([key]) => key !== "tool" && key !== "target" && key !== "environment").map(([key, value]) => (
            <ContextRow key={key} label={key.replaceAll("_", " ")} value={humanValue(value)} />
          ))}
        </dl>
        {pending ? (
          <label className="flex flex-col gap-1.5 text-label text-muted-foreground">
            Decision comment
            <Textarea
              aria-label="Decision comment"
              value={comment}
              onChange={(event) => setComment(event.target.value)}
              placeholder="Optional context for the permanent audit record"
              rows={3}
            />
          </label>
        ) : (
          <div className="flex flex-wrap items-center gap-2 text-body">
            <StatusPill status={outcomeStatus(row.status) ?? row.status} />
            {row.decided_by && <span className="text-muted-foreground">{me && row.decided_by === me ? "by you" : "by a person"}</span>}
            {row.decided_at && <span className="text-muted-foreground">· {formatDateTime(row.decided_at)}</span>}
            {row.decision_comment && <p className="m-0 w-full text-muted-foreground">{row.decision_comment}</p>}
          </div>
        )}
      </DecisionBlock>
      <RailPart><OriginChips links={origin} /></RailPart>
    </DetailFrame>
  )
}

function Chip({ children, mono = false, danger = false }: { children: React.ReactNode; mono?: boolean; danger?: boolean }) {
  return (
    <span className={`inline-flex h-6 items-center rounded-full border px-2 text-label ${danger ? "border-destructive/40 font-semibold text-destructive" : "border-border text-foreground/80"} ${mono ? "font-mono" : ""}`}>
      {children}
    </span>
  )
}

function ContextRow({ label, value }: { label: string; value: string }) {
  return (
    <>
      <dt className="text-muted-foreground first-letter:uppercase">{label}</dt>
      <dd className="m-0 min-w-0 break-words">{value}</dd>
    </>
  )
}

const SECRETISH = /(password|secret|token|api[_-]?key|auth|bearer)/i

function humanContextEntries(payload: Record<string, unknown>): [string, unknown][] {
  return Object.entries(payload).filter(([key]) => !SECRETISH.test(key))
}

function humanValue(value: unknown): string {
  if (value == null) return "—"
  if (typeof value === "string" || typeof value === "number" || typeof value === "boolean") return String(value)
  if (Array.isArray(value)) return value.map(humanValue).join(", ")
  if (typeof value === "object") {
    return Object.entries(value as Record<string, unknown>)
      .filter(([key]) => !SECRETISH.test(key))
      .map(([key, nested]) => `${key.replaceAll("_", " ")}: ${humanValue(nested)}`)
      .join(" · ") || "Protected context"
  }
  return String(value)
}

function MissionDetail({ entry, lookup }: { entry: InboxV2Entry; lookup: InboxLookup }) {
  const mission = entry.mission!
  const task = entry.task!
  const review = Boolean(task.needs_review)
  const missionHref = `/missions/${encodeURIComponent(mission.id)}/timeline`
  // "Open review" is the issue's own review panel; a mission without an
  // issue identifier only has its timeline.
  const reviewHref = mission.identifier ? entityHref({ kind: "issue", identifier: mission.identifier }) : null
  return (
    <DetailFrame entry={entry} lookup={lookup}>
      {entry.summary && <p className="m-0 whitespace-pre-wrap text-default leading-relaxed text-foreground/90">{entry.summary}</p>}
      <DecisionBlock
        icon={review ? ClipboardCheck : AlertTriangle}
        heading={review ? "Review requested" : "Mission needs attention"}
        tone={review ? "default" : "danger"}
        actions={
          <>
            {review && reviewHref && (
              <Button asChild size="sm" variant="ghost"><Link href={missionHref}>Open mission</Link></Button>
            )}
            <Button asChild size="sm" className="gap-1.5">
              <Link href={review && reviewHref ? reviewHref : missionHref}>
                {review && reviewHref ? "Open review" : "Open mission"} <ArrowUpRight className="h-3.5 w-3.5" />
              </Link>
            </Button>
          </>
        }
      >
        <dl className="grid grid-cols-[minmax(0,9rem)_minmax(0,1fr)] gap-x-4 gap-y-2 rounded-lg border border-border px-3 py-2.5 text-body">
          <dt className="text-muted-foreground">Mission</dt>
          <dd className="m-0 min-w-0 break-words">{mission.identifier ? `${mission.identifier} · ` : ""}{mission.title}</dd>
          {task.result_summary && <ContextRow label="Result" value={task.result_summary} />}
          {task.error_message && <ContextRow label="Error" value={task.error_message} />}
          <dt className="text-muted-foreground">Status</dt>
          <dd className="m-0"><StatusPill status={task.status} /></dd>
        </dl>
      </DecisionBlock>
    </DetailFrame>
  )
}

const GROUP_PREVIEW = 3

function GroupedIncident({ entry, role, lookup, onArchive }: { entry: InboxV2Entry; role: WorkspaceRole | null; lookup: InboxLookup; onArchive: Props["onArchiveGroup"] }) {
  const [busy, setBusy] = useState(false)
  const items = entry.groupedItems ?? []
  const sender = items[0]?.sender_name
  return (
    <DetailFrame entry={entry} lookup={lookup}>
      <p className="m-0 text-default leading-relaxed text-foreground/90">
        No client decision is required. {items.length} related updates{sender ? ` from ${sender}` : ""}.
      </p>
      <ul className="m-0 grid list-none gap-1 rounded-lg border border-border px-3 py-2.5 text-body" aria-label="Grouped updates">
        {items.slice(0, GROUP_PREVIEW).map((item) => (
          <li key={item.id} className="flex items-center gap-2"><AlertTriangle className="h-3.5 w-3.5 shrink-0 text-muted-foreground" aria-hidden /><span className="min-w-0 truncate">{item.title}</span></li>
        ))}
        {items.length > GROUP_PREVIEW && <li className="text-label text-muted-foreground">+{items.length - GROUP_PREVIEW} more</li>}
      </ul>
      <DetailExpand title={`View ${items.length} related updates`}>
        <ul className="m-0 list-none divide-y divide-border p-0">
          {items.map((item) => <li key={item.id} className="py-3"><p className="m-0 text-body font-medium">{item.title}</p><p className="m-0 text-micro text-muted-foreground">{since(item.created_at)}</p>{item.body_md && <div className="mt-1 text-body text-muted-foreground"><MarkdownContent>{item.body_md}</MarkdownContent></div>}</li>)}
        </ul>
      </DetailExpand>
      <Dock standalone>
        <Button
          size="sm"
          variant="outline"
          disabled={busy}
          onClick={async () => {
            setBusy(true)
            try {
              await onArchive(entry)
            } catch (error) {
              toast.error(error instanceof Error ? error.message : "Archive failed")
            } finally {
              setBusy(false)
            }
          }}
          className="gap-2"
        >
          <Archive className="h-4 w-4" /> {busy ? "Archiving…" : `Archive ${items.length} updates`}
        </Button>
        {/* System Health is the instance console; only an admin tier can open it. */}
        {isAdminTier(role) && <Button asChild size="sm"><Link href="/admin">Open System Health</Link></Button>}
      </Dock>
    </DetailFrame>
  )
}
