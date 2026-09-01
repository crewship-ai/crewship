"use client"

import { useEffect, useState } from "react"
import Link from "next/link"
import {
  AlertTriangle, Archive, ArrowUpRight, Check, CheckCircle2, Clock3,
  ExternalLink, ShieldAlert, X,
} from "lucide-react"
import { toast } from "sonner"

import { InboxDetail } from "@/components/features/inbox/inbox-detail"
import type { WorkspaceRole } from "@/components/features/inbox/inbox-derive"
import { canRole, remainingLabel, since } from "@/components/features/inbox/inbox-derive"
import { Button } from "@/components/ui/button"
import { DetailCard, Pill } from "@/components/ui/detail"
import { Textarea } from "@/components/ui/textarea"
import type { InboxItem } from "@/hooks/use-inbox"
import { formatDateTime } from "@/lib/time"
import { cn } from "@/lib/utils"

import type { InboxV2Confirmation, InboxV2Entry } from "./inbox-v2-types"

interface Props {
  entry: InboxV2Entry | null
  /** A deep link named a row that is not in any feed once they have settled. */
  selectionMissing?: boolean
  role: WorkspaceRole | null
  detailedInboxItem?: InboxItem
  detailLoading?: boolean
  confirmation: InboxV2Confirmation | null
  onClearConfirmation: () => void
  onViewReceipt: (entry: InboxV2Entry) => void
  onInboxResolve: (item: InboxItem, action: string) => Promise<void>
  onInboxArchive: (item: InboxItem) => Promise<void>
  onInboxMarkUnread: (item: InboxItem) => Promise<void>
  onInboxRefresh: (item: InboxItem, action?: string) => Promise<void>
  onApprovalDecide: (entry: InboxV2Entry, decision: "approved" | "denied", comment: string) => Promise<void>
  onArchiveGroup: (entry: InboxV2Entry) => Promise<void>
}

export function InboxV2Detail(props: Props) {
  if (props.confirmation) {
    return <DecisionConfirmation {...props} confirmation={props.confirmation} />
  }
  if (!props.entry) {
    // Saying "not here" is the whole point. This pane used to fall back to
    // the first row in the list, so a stale link armed an Approve button for
    // a decision the user never asked to see.
    if (props.selectionMissing) {
      return (
        <div className="flex h-full flex-col items-center justify-center gap-2 p-8 text-center">
          <AlertTriangle className="h-6 w-6 text-muted-foreground" />
          <p className="text-sm font-medium">That item is no longer in your inbox</p>
          <p className="max-w-sm text-xs text-muted-foreground">
            It may have been decided elsewhere, archived, or belong to another workspace.
            Nothing was opened — pick an item from the list to continue.
          </p>
        </div>
      )
    }
    return (
      <div className="flex h-full items-center justify-center p-8 text-center text-sm text-muted-foreground">
        Select an item to see its context and actions.
      </div>
    )
  }
  const { entry } = props
  if (entry.source === "inbox") {
    const item = props.detailedInboxItem ?? entry.inboxItem
    if (!item) return null
    return (
      <div className="mx-auto w-full max-w-4xl p-4 lg:p-6">
        {props.detailLoading && (
          <div className="mb-2 text-[11px] text-muted-foreground">Loading decision evidence…</div>
        )}
        <InboxDetail
          item={item}
          role={props.role}
          onResolve={(action) => props.onInboxResolve(item, action)}
          onArchive={() => props.onInboxArchive(item)}
          onMarkUnread={() => { void props.onInboxMarkUnread(item) }}
          onRefresh={(action) => props.onInboxRefresh(item, action)}
        />
      </div>
    )
  }
  if (entry.source === "approval") return <ApprovalDetail entry={entry} role={props.role} onDecide={props.onApprovalDecide} />
  if (entry.source === "mission") return <MissionDetail entry={entry} />
  return <GroupedIncident entry={entry} onArchive={props.onArchiveGroup} />
}

function DecisionConfirmation({
  confirmation, onClearConfirmation, onViewReceipt,
}: Props & { confirmation: InboxV2Confirmation }) {
  const positive = !["denied", "rejected", "cancelled"].includes(confirmation.action)
  const label = outcomeLabel(confirmation.action)
  return (
    <div className="flex min-h-full items-start justify-center p-6 lg:p-12">
      <DetailCard className={cn("w-full max-w-xl", positive ? "border-success/35 bg-success/[.06]" : "border-border")}>
        <div className="flex flex-col items-center gap-5 py-6 text-center">
          <span className={cn(
            "flex h-14 w-14 items-center justify-center rounded-full border",
            positive ? "border-success/50 bg-success/10 text-success" : "border-destructive/50 bg-destructive/10 text-destructive",
          )}>
            {positive ? <CheckCircle2 className="h-7 w-7" /> : <X className="h-7 w-7" />}
          </span>
          <div>
            <h2 className="text-lg font-semibold">{label}</h2>
            <p className="mt-1 text-sm text-muted-foreground">{confirmation.entry.title}</p>
            <p className="mt-2 text-xs text-muted-foreground-soft">Saved {since(confirmation.at)} · the record stays in History</p>
          </div>
          <div className="flex flex-wrap justify-center gap-2">
            <Button onClick={() => onViewReceipt(confirmation.entry)} className="gap-2">
              View record <ExternalLink className="h-3.5 w-3.5" />
            </Button>
            <Button variant="outline" onClick={onClearConfirmation}>Back to inbox</Button>
          </div>
        </div>
      </DetailCard>
    </div>
  )
}

function ApprovalDetail({
  entry, role, onDecide,
}: {
  entry: InboxV2Entry
  role: WorkspaceRole | null
  onDecide: Props["onApprovalDecide"]
}) {
  const row = entry.approval!
  const [comment, setComment] = useState("")
  const [busy, setBusy] = useState<"approved" | "denied" | null>(null)
  useEffect(() => setComment(""), [row.id])
  const pending = row.status === "pending"
  const allowed = canRole(role, "manage")
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

  return (
    <div className="mx-auto flex w-full max-w-4xl flex-col gap-3 p-4 lg:p-6">
      <DetailCard tone={pending ? "warn" : "default"} className={pending ? "bg-warn/[.05]" : undefined}>
        <div className="flex flex-col gap-4">
          <div className="flex flex-wrap items-center gap-2">
            <ShieldAlert className="h-4 w-4 text-warn" />
            <span className="text-sm font-semibold">{pending ? "Waiting on your decision" : "Decision record"}</span>
            <Pill tone={row.kind === "destructive_op" ? "destructive" : "warn"}>{row.kind.replaceAll("_", " ")}</Pill>
            {deadlineMins != null && pending && (
              <span className="ml-auto text-xs font-medium text-destructive">
                {deadlineMins > 0 ? `expires in ${remainingLabel(deadlineMins)}` : "expired"}
              </span>
            )}
          </div>
          <div>
            <h2 className="text-xl font-semibold">{entry.title}</h2>
            <p className="mt-1 text-xs text-muted-foreground">
              Requested by {entry.subject} · {formatDateTime(row.created_at)}
            </p>
          </div>
          <div>
            <div className="text-[10px] font-semibold uppercase tracking-wider text-muted-foreground">What you are approving</div>
            <p className="mt-1 whitespace-pre-wrap text-sm leading-relaxed">{row.reason || "No reason supplied."}</p>
          </div>
          {pending && (
            <>
              <Textarea
                value={comment}
                onChange={(event) => setComment(event.target.value)}
                placeholder="Optional context for the permanent audit record"
                rows={3}
              />
              <div className="flex flex-wrap gap-2">
                <Button disabled={!allowed || busy !== null} onClick={() => void decide("approved")} className="gap-2 bg-success/20 text-success hover:bg-success/30">
                  <Check className="h-4 w-4" /> {busy === "approved" ? "Approving…" : "Approve"}
                </Button>
                <Button disabled={!allowed || busy !== null} variant="outline" onClick={() => void decide("denied")} className="gap-2">
                  <X className="h-4 w-4" /> {busy === "denied" ? "Denying…" : "Deny"}
                </Button>
                {!allowed && <span className="self-center text-xs text-muted-foreground">OWNER or ADMIN decides this</span>}
              </div>
            </>
          )}
          {!pending && (
            <div className="rounded-lg border border-border/60 bg-muted/20 px-3 py-2 text-sm">
              <span className="font-semibold capitalize">{row.status}</span>
              {row.decided_by && <> by <span className="font-mono text-xs">{row.decided_by}</span></>}
              {row.decided_at && <> · {formatDateTime(row.decided_at)}</>}
              {row.decision_comment && <p className="mt-2 text-muted-foreground">{row.decision_comment}</p>}
            </div>
          )}
        </div>
      </DetailCard>

      <DetailCard title="Impact and context" subtitle="captured from the source request">
        <HumanContext payload={row.payload ?? {}} />
      </DetailCard>

      <DetailCard bare>
        <div className="flex flex-wrap gap-2 px-4 py-3">
          {row.crew_id && <Button asChild size="sm" variant="ghost"><Link href={`/crews/${encodeURIComponent(row.crew_id)}`}>Open crew <ArrowUpRight className="ml-1 h-3 w-3" /></Link></Button>}
          {row.mission_id && <Button asChild size="sm" variant="ghost"><Link href={`/missions/${encodeURIComponent(row.mission_id)}/timeline`}>Open mission <ArrowUpRight className="ml-1 h-3 w-3" /></Link></Button>}
        </div>
      </DetailCard>
    </div>
  )
}

function HumanContext({ payload }: { payload: Record<string, unknown> }) {
  const entries = Object.entries(payload).filter(([key]) => !/(password|secret|token|api[_-]?key|auth|bearer)/i.test(key))
  if (entries.length === 0) return <p className="text-sm text-muted-foreground">No additional context.</p>
  return (
    <dl className="grid gap-x-6 gap-y-3 sm:grid-cols-2">
      {entries.map(([key, value]) => (
        <div key={key} className="min-w-0">
          <dt className="text-[10px] font-semibold uppercase tracking-wider text-muted-foreground">{key.replaceAll("_", " ")}</dt>
          <dd className="mt-0.5 break-words text-sm">{humanValue(value)}</dd>
        </div>
      ))}
    </dl>
  )
}

function humanValue(value: unknown): string {
  if (value == null) return "—"
  if (typeof value === "string" || typeof value === "number" || typeof value === "boolean") return String(value)
  if (Array.isArray(value)) return value.map(humanValue).join(", ")
  if (typeof value === "object") {
    return Object.entries(value as Record<string, unknown>)
      .filter(([key]) => !/(password|secret|token|api[_-]?key|auth|bearer)/i.test(key))
      .map(([key, nested]) => `${key.replaceAll("_", " ")}: ${humanValue(nested)}`)
      .join(" · ") || "Protected context"
  }
  return String(value)
}

function MissionDetail({ entry }: { entry: InboxV2Entry }) {
  const mission = entry.mission!
  const task = entry.task!
  const review = task.needs_review
  return (
    <div className="mx-auto flex w-full max-w-4xl flex-col gap-3 p-4 lg:p-6">
      <DetailCard tone={review ? "warn" : "default"}>
        <div className="flex flex-col gap-4">
          <div className="flex items-center gap-2">
            {review ? <Clock3 className="h-4 w-4 text-warn" /> : <AlertTriangle className="h-4 w-4 text-destructive" />}
            <span className="text-sm font-semibold">{review ? "Review requested" : "Mission needs attention"}</span>
          </div>
          <div>
            <h2 className="text-xl font-semibold">{entry.title}</h2>
            <p className="mt-1 text-xs text-muted-foreground">{mission.title} · {entry.subject} · {since(entry.createdAt)}</p>
          </div>
          {entry.summary && <p className="whitespace-pre-wrap text-sm leading-relaxed">{entry.summary}</p>}
          <Button asChild className="w-fit gap-2">
            <Link href={`/missions/${encodeURIComponent(mission.id)}/timeline`}>
              {review ? "Open review" : "Open mission"} <ArrowUpRight className="h-3.5 w-3.5" />
            </Link>
          </Button>
        </div>
      </DetailCard>
    </div>
  )
}

function GroupedIncident({ entry, onArchive }: { entry: InboxV2Entry; onArchive: Props["onArchiveGroup"] }) {
  const [busy, setBusy] = useState(false)
  return (
    <div className="mx-auto flex w-full max-w-3xl flex-col gap-3 p-4 lg:p-6">
      <DetailCard className="border-destructive/25 bg-destructive/[.035]">
        <div className="flex flex-col gap-4">
          <div className="flex items-center gap-2 text-destructive">
            <AlertTriangle className="h-4 w-4" />
            <span className="text-sm font-semibold">Grouped system incident</span>
          </div>
          <div>
            <h2 className="text-xl font-semibold">{entry.title}</h2>
            <p className="mt-1 text-sm text-muted-foreground">{entry.groupedItems?.length ?? 0} related updates grouped into one item.</p>
          </div>
          <div className="rounded-lg border border-border/60 bg-background/40 p-3 text-sm">
            <p className="font-medium">No client decision is required.</p>
            <p className="mt-1 text-muted-foreground">The underlying service issue belongs in System Health. These notices remain available here without competing with approvals.</p>
          </div>
          <div className="flex flex-wrap gap-2">
            <Button
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
              <Archive className="h-4 w-4" /> {busy ? "Archiving…" : `Archive ${entry.groupedItems?.length ?? 0} updates`}
            </Button>
            <Button asChild variant="ghost" className="gap-2"><Link href="/admin">Open System Health <ExternalLink className="h-3.5 w-3.5" /></Link></Button>
          </div>
        </div>
      </DetailCard>
    </div>
  )
}

function outcomeLabel(action: string): string {
  const labels: Record<string, string> = {
    approved: "Approved",
    approve: "Approved",
    denied: "Denied",
    rejected: "Rejected",
    retried: "Retry started",
    reenabled: "Schedule re-enabled",
    archived: "Archived",
    dismissed: "Dismissed",
    cancelled: "Cancelled",
    resolved: "Decision already completed",
  }
  return labels[action] ?? "Decision saved"
}
