"use client"

/**
 * One work item, whole
 * (docs/prd/WEBHOOKS-AGENT-PARALLELISM-IMPLEMENTATION-1-0.md §9).
 *
 * The shape of this screen is an argument about identity. A work item is one
 * accepted piece of work; a RUN is one attempt at it; a session is the
 * conversation. All three are routinely conflated, and the log panels this
 * product already has conflate exactly the pair that matters — they filter by
 * agent slug, so two concurrent runs of one agent interleave into a single
 * scroll and the reader cannot tell whose line is whose.
 *
 * So attempts render as SEPARATE streams, one card per run_id, each with its
 * own lease, its own runtime locator, its own exit evidence and its own cost.
 * Two attempts of this item share an agent and a session and differ only in
 * run_id; nothing on this screen groups on anything but run_id.
 */

import * as React from "react"
import { Ban, Coins, FileDigit, Radio, ScrollText } from "lucide-react"

import { Button } from "@/components/ui/button"
import { DetailCard, StatStrip, type StatItem } from "@/components/ui/detail"
import { Skeleton } from "@/components/ui/skeleton"
import { cn } from "@/lib/utils"
import { relTime } from "@/lib/time"
import {
  WORK_STATE_LABEL,
  attemptCount,
  isTerminalWorkState,
  ledgerEvents,
  queuedReason,
  replayAvailability,
  totalCostUSD,
  useCancelWorkItem,
  useReplayWorkItem,
  useWorkItem,
  workRunStreams,
  type WorkCancelResponse,
  type WorkItem,
  type WorkItemDetail as WorkItemDetailData,
  type WorkRunStream,
} from "@/hooks/use-work-items"
import { useWebhookDelivery } from "@/hooks/use-webhook-deliveries"
import { NeedsReconciliationNotice, WorkStatePill } from "./work-state-pill"
import { formatCost } from "./work-items-list"
import { ReplayButton, ReplayUnavailableReason, WorkReplayDialog } from "./work-replay-dialog"

export interface WorkItemDetailProps {
  workspaceId: string
  workItemId: string
  /** Lets the caller follow replay_of / a freshly minted replay. */
  onNavigate?: (workItemId: string) => void
  className?: string
}

export function WorkItemDetail({ workspaceId, workItemId, onNavigate, className }: WorkItemDetailProps) {
  const { item, loading, notFound, error } = useWorkItem(workspaceId, workItemId)

  if (loading) {
    return (
      <div className={cn("space-y-3 p-3", className)}>
        <Skeleton className="h-8 w-48" />
        <Skeleton className="h-16 w-full" />
        <Skeleton className="h-40 w-full" />
      </div>
    )
  }
  if (notFound) {
    return (
      <div className={cn("p-6 text-center text-[12px] text-muted-foreground", className)}>
        This work item is not in this workspace&apos;s ledger.
      </div>
    )
  }
  if (error || !item) {
    return (
      <div className={cn("p-6 text-center text-[12px] text-destructive", className)}>
        {error?.message ?? "Could not read this work item."}
      </div>
    )
  }
  return <WorkItemDetailBody workspaceId={workspaceId} item={item} onNavigate={onNavigate} className={className} />
}

export function WorkItemDetailBody({
  workspaceId,
  item,
  onNavigate,
  className,
}: {
  workspaceId: string
  item: WorkItemDetailData
  onNavigate?: (workItemId: string) => void
  className?: string
}) {
  const [replayOpen, setReplayOpen] = React.useState(false)
  const [cancelResult, setCancelResult] = React.useState<WorkCancelResponse | null>(null)
  const [actionError, setActionError] = React.useState<string | null>(null)

  // The delivery behind a webhook-sourced item is what says whether the raw
  // payload still exists — which is the ONLY honest way to know, before the
  // click, whether a replay can work at all.
  const delivery = useWebhookDelivery(
    workspaceId,
    item.source === "webhook" && item.source_ref ? item.source_ref : null,
  )
  const availability = replayAvailability(item, delivery)

  const cancel = useCancelWorkItem(workspaceId, {
    onSettled: (response) => { setCancelResult(response); setActionError(null) },
    onError: (err) => setActionError(err instanceof Error ? err.message : "Cancel failed."),
  })
  const replay = useReplayWorkItem(workspaceId, {
    onCreated: (created: WorkItem) => {
      setReplayOpen(false)
      setActionError(null)
      onNavigate?.(created.id)
    },
    onError: (err) => setActionError(err instanceof Error ? err.message : "Replay failed."),
  })

  const streams = workRunStreams(item)
  const itemEvents = ledgerEvents(item)
  const cost = totalCostUSD(item.attempts)
  const reason = queuedReason(item)
  const terminal = isTerminalWorkState(item.state)
  const liveAttempt = item.attempts.find((a) => !a.ended_at) ?? null

  const stats: StatItem[] = [
    { label: "Source", value: item.source, mono: true },
    { label: "Class", value: item.class, mono: true },
    { label: "Attempts", value: attemptCount(item), mono: true },
    { label: "Generation", value: item.generation, mono: true },
    { label: "Priority", value: item.priority, mono: true },
    { label: "Cost", value: formatCost(cost), mono: true },
  ]

  return (
    <div className={cn("space-y-3 p-3", className)}>
      <div className="flex flex-wrap items-center gap-2">
        <WorkStatePill state={item.state} data-testid="work-detail-state" />
        <span className="font-mono text-[11px] text-muted-foreground/70">{item.id}</span>
        {item.replay_of && (
          <button
            type="button"
            onClick={() => onNavigate?.(item.replay_of as string)}
            className="rounded-full bg-primary/10 px-2 py-0.5 text-[10px] text-primary coarse:min-h-12"
          >
            replay of {item.replay_of.slice(0, 10)}…
          </button>
        )}
      </div>

      <NeedsReconciliationNotice state={item.state} runtimeLocator={liveAttempt?.runtime_locator} />

      <StatStrip items={stats} />

      <DetailCard title="Acceptance" icon={FileDigit}>
        <dl className="grid grid-cols-1 gap-x-4 gap-y-2 text-[11px] sm:grid-cols-2">
          <Fact label="Agent" value={item.agent_id} mono empty="no agent" />
          {/* Session and agent are listed separately and are never merged into
              one "who" — they are different identities and §9's rule about run
              streams is the same rule one level down. */}
          <Fact label="Session" value={item.session_id} mono empty={item.class === "chat" ? "no session" : "background work"} />
          <Fact label="Crew" value={item.crew_id} mono empty="—" />
          <Fact label="Authorized by" value={item.authorized_by_user_id} mono empty="—" />
          <Fact label="Target revision" value={item.target_revision} mono empty="none recorded" />
          <Fact
            label="Input fingerprint"
            value={item.input_sha256 ? `sha256:${item.input_sha256.slice(0, 16)}…` : ""}
            mono
            empty="not recorded"
          />
          <Fact label="Eligible" value={`${relTime(item.eligible_at)} · ${item.eligible_at}`} />
          <Fact label="Deadline" value={item.deadline_at ?? ""} empty="never expires on its own" />
          <Fact label="Accepted" value={`${relTime(item.created_at)}`} />
          <Fact label="Finished" value={item.terminal_at ?? ""} empty={terminal ? "—" : "not finished"} />
          {item.source === "webhook" && item.source_ref ? (
            <Fact label="Delivery" value={item.source_ref} mono empty="—" />
          ) : null}
          {item.replay_reason ? <Fact label="Replay reason" value={item.replay_reason} /> : null}
        </dl>
        {reason ? (
          <p className="mt-3 rounded-md bg-surface-subtle/60 px-2.5 py-2 text-[11px] leading-relaxed text-muted-foreground">
            {reason}
          </p>
        ) : null}
      </DetailCard>

      <div className="space-y-2">
        <div className="flex items-baseline gap-2">
          <span className="type-section text-foreground/70">Runs</span>
          <span className="type-meta font-mono text-muted-foreground">
            {streams.length} {streams.length === 1 ? "stream" : "streams"}
          </span>
        </div>
        {/* One card per run_id. Two attempts of this item share an agent and a
            session; only run_id tells them apart, so only run_id groups them. */}
        {streams.length === 0 ? (
          <p className="rounded-lg border border-hairline px-3 py-3 text-[11px] text-muted-foreground">
            Nothing has been claimed yet, so there is no run to show.
          </p>
        ) : (
          streams.map((stream) => <RunStreamCard key={stream.runId} stream={stream} />)
        )}
      </div>

      {itemEvents.length > 0 && (
        <DetailCard title="Ledger" subtitle="not attributable to one run" icon={ScrollText} bare>
          <ol className="divide-y divide-hairline">
            {itemEvents.map((event) => (
              <li key={event.seq} className="flex flex-wrap items-baseline gap-x-2 gap-y-0.5 px-3 py-2 text-[11px]">
                <span className="font-mono text-muted-foreground/60">#{event.seq}</span>
                <span className="text-foreground/80">
                  {event.from_state === event.to_state
                    ? WORK_STATE_LABEL[event.to_state as keyof typeof WORK_STATE_LABEL] ?? event.to_state
                    : `${event.from_state || "—"} → ${event.to_state || "—"}`}
                </span>
                {event.reason ? <span className="text-muted-foreground">{event.reason}</span> : null}
                <span className="ml-auto font-mono text-muted-foreground/60">{relTime(event.at)}</span>
              </li>
            ))}
          </ol>
        </DetailCard>
      )}

      <div className="flex flex-wrap items-center gap-2">
        <Button
          variant="outline"
          size="sm"
          onClick={() => cancel.mutate({ workItemId: item.id })}
          disabled={terminal || cancel.isPending}
          title={terminal ? `This work already finished as ${item.state}.` : undefined}
          data-testid="cancel-button"
        >
          <Ban className="h-3 w-3" />
          {cancel.isPending ? "Cancelling…" : "Cancel"}
        </Button>
        <ReplayButton availability={availability} onClick={() => setReplayOpen(true)} />
      </div>
      <ReplayUnavailableReason availability={availability} />

      {cancelResult && (
        <div
          data-testid="cancel-outcome"
          data-cancel-outcome={cancelResult.outcome}
          className={cn(
            "rounded-lg border px-3 py-2 text-[11px] leading-relaxed",
            cancelResult.outcome === "cancelled"
              ? "border-border/60 bg-surface-subtle/60 text-muted-foreground"
              : "border-warn/30 bg-warn/[0.06] text-warn",
          )}
        >
          {cancelResult.detail}
        </div>
      )}
      {actionError && (
        <div className="rounded-lg border border-destructive/40 bg-destructive/5 px-3 py-2 text-[11px] text-destructive">
          {actionError}
        </div>
      )}

      <WorkReplayDialog
        open={replayOpen}
        onOpenChange={setReplayOpen}
        item={item}
        availability={availability}
        pending={replay.isPending}
        error={replay.error instanceof Error ? replay.error.message : null}
        onConfirm={({ reason: why, targetRevision }) =>
          replay.mutate({ workItemId: item.id, reason: why, targetRevision })
        }
      />
    </div>
  )
}

function RunStreamCard({ stream }: { stream: WorkRunStream }) {
  const attempt = stream.attempt
  const live = attempt ? !attempt.ended_at : false
  return (
    <DetailCard
      title={attempt ? `Attempt ${attempt.attempt}` : "Run"}
      subtitle={stream.runId}
      icon={Radio}
      tone={live ? "blue" : "default"}
      data-testid={`run-stream-${stream.runId}`}
      bare
    >
      {attempt && (
        <dl className="grid grid-cols-1 gap-x-4 gap-y-2 border-b border-hairline px-3 py-2.5 text-[11px] sm:grid-cols-2">
          <Fact label="Lease owner" value={attempt.lease_owner} mono empty="—" />
          <Fact label="Lease expires" value={attempt.lease_expires_at} mono empty="—" />
          <Fact label="Runtime" value={attempt.runtime_locator} mono empty="not recorded" />
          <Fact label="Heartbeat" value={relTime(attempt.heartbeat_at)} empty="—" />
          <Fact label="Started" value={`${relTime(attempt.started_at)}${attempt.start_reason ? ` · ${attempt.start_reason}` : ""}`} />
          <Fact
            label="Ended"
            value={attempt.ended_at ? `${relTime(attempt.ended_at)}${attempt.end_reason ? ` · ${attempt.end_reason}` : ""}` : ""}
            empty="still open"
          />
          <Fact label="Exit evidence" value={attempt.exit_evidence} mono empty="—" />
          <Fact label="Cost" value={formatCost(attempt.cost_usd)} mono />
        </dl>
      )}
      {stream.events.length === 0 ? (
        <p className="px-3 py-2.5 text-[11px] text-muted-foreground">No transitions recorded for this run.</p>
      ) : (
        <ol className="divide-y divide-hairline">
          {stream.events.map((event) => (
            <li key={event.seq} className="flex flex-wrap items-baseline gap-x-2 gap-y-0.5 px-3 py-2 text-[11px]">
              <span className="font-mono text-muted-foreground/60">#{event.seq}</span>
              <span className="text-foreground/80">
                {event.from_state || "—"} → {event.to_state || "—"}
              </span>
              {event.reason ? <span className="text-muted-foreground">{event.reason}</span> : null}
              <span className="ml-auto font-mono text-muted-foreground/60">{relTime(event.at)}</span>
            </li>
          ))}
        </ol>
      )}
      {attempt && attempt.cost_usd > 0 && (
        <p className="flex items-center gap-1.5 border-t border-hairline px-3 py-2 text-[11px] text-muted-foreground">
          <Coins className="h-3 w-3" />
          This attempt cost {formatCost(attempt.cost_usd)}. A retry is a new attempt and costs again.
        </p>
      )}
    </DetailCard>
  )
}

function Fact({
  label, value, mono, empty = "—",
}: {
  label: string
  value: React.ReactNode
  mono?: boolean
  empty?: string
}) {
  const missing = value === "" || value === null || value === undefined
  return (
    <div className="min-w-0">
      <dt className="type-meta uppercase tracking-wide text-muted-foreground-soft">{label}</dt>
      <dd className={cn("mt-0.5 truncate text-foreground/85", mono && !missing && "font-mono")}>
        {missing ? <span className="text-muted-foreground/60">{empty}</span> : value}
      </dd>
    </div>
  )
}
