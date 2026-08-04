"use client"

// The issue detail, as one scrolling surface of cards.
//
// Same shape as the routine detail (components/features/routines/
// routine-card-detail.tsx) on purpose: identity card → figures band →
// two-column body → one full-width card at the foot. Two screens that show
// two different nouns should still read as one product, and today they do
// not — the issue detail invented its own KPI tile, its own property row and
// its own header while the detail kit sat unused next door.
//
// What went, and why:
//
//   KPI tiles    four 32px numerals, three of them zero, and the project
//                name silently cut to 14 characters so "File Operations"
//                rendered as "File Operation". The kit's StatStrip says
//                the same six things in one band you can glance at.
//   Double       every rail card was titled twice — a DetailCard headed
//   headers      PROPERTIES wrapping a panel that drew its own PROPERTIES
//                header. The panels here draw rows, not headers.
//   Bottom       Comments and Activity were cards on this page AND tabs in
//   drawer       the drawer under it. They are one card at the foot now,
//                behind a switch, the way Triggers/Versions is.
//
// Ordered by what an operator asks, in order: what is it → how is it going
// → what is the work → who owns it and what can it reach → what happened.

import * as React from "react"
import Link from "next/link"
import {
  ArrowUpRight,
  CheckCircle2,
  CircleDot,
  Clock,
  Flag,
  FolderKanban,
  GitBranch,
  Link2,
  MessageSquare,
  Tag,
  UserCircle2,
  Users,
  XCircle,
} from "lucide-react"

import { cn } from "@/lib/utils"
import { formatDate, formatDurationDecimal, relTime, timeAgo } from "@/lib/time"
import { Appear, DetailCard, EntityChip, Pill, StatStrip } from "@/components/ui/detail"
import { TintedCard, TintedFacts, type TintTone } from "./tinted-card"
import { AgentAvatar } from "@/components/ui/agent-avatar"
import { MarkdownContent } from "@/components/features/issues/markdown-content"
import { MentionChip } from "@/components/features/issues/mention-chip"
import { CommentComposer } from "./comment-composer"
import {
  isMentionActivity,
  mentionDirectory,
  mentionTargetFromActivityDetails,
  type MentionAgent,
  type MentionDirectory,
} from "@/lib/mentions"
import { StatusIcon, statusLabel } from "@/components/features/issues/status-icon"
import { PriorityIcon, priorityLabel } from "@/components/features/issues/priority-icon"
import { LabelBadge } from "@/components/features/issues/label-badge"
import { getCrewIconDef } from "@/lib/entities"
import { issueFacts, issuePriorityTone, issueStatusTone } from "@/lib/issue-facts"
import type {
  IssueActivity,
  IssueComment,
  IssueRelation,
  Mission,
  Project,
} from "@/lib/types/mission"

/** `GET /api/v1/crews/{crewId}/issues/{identifier}/runs` (issue_handler_runs.go). */
export interface IssueRun {
  id: string
  status: string
  agent_name?: string
  task?: string
  started_at?: string
  ended_at?: string
  duration_ms: number
  result_summary?: string
  error_message?: string
}

interface Props {
  issue: Mission
  comments: IssueComment[]
  activities: IssueActivity[]
  relations: IssueRelation[]
  /** Newest first, as the endpoint returns them. */
  runs?: IssueRun[]
  /** The issue's project, already resolved by the caller. */
  project?: Project | null
  /**
   * Start / Stop / Approve / Request changes, rendered top-right of the
   * identity card. Passed in rather than rebuilt here: the host owns the
   * handlers, the RBAC guards and the busy states, and a second copy of
   * that wiring is a second thing to keep correct.
   */
  actions?: React.ReactNode
  /**
   * Agents that may be mentioned here, and that mentions already in the
   * comments resolve against. Without it a mention is just the text somebody
   * typed — see lib/mentions.ts.
   */
  agents?: readonly MentionAgent[]
  /**
   * Posts a comment. Absent, the card stays read-only and no composer is
   * drawn: the endpoint, the workspace and the refetch belong to the host,
   * the same way the action buttons do.
   */
  onSubmitComment?: (body: string) => boolean | Promise<boolean>
  /** Initial for the composer's author bubble. */
  viewerInitial?: string
}

type FootTab = "comments" | "history"

export function IssueCardDetail({
  issue,
  comments,
  activities,
  relations,
  runs = [],
  project,
  actions,
  agents,
  onSubmitComment,
  viewerInitial,
}: Props) {
  const [footTab, setFootTab] = React.useState<FootTab>("comments")

  const mentions = React.useMemo(() => mentionDirectory(agents ?? []), [agents])

  const labels = issue.labels ?? []
  const facts = React.useMemo(
    () => issueFacts(issue, { comments: comments.length }),
    [issue, comments.length],
  )

  return (
    <div className="flex flex-col gap-4 p-4">
      {/* Identity, as a card that scrolls with the page rather than a fixed
          header band. The title is the first thing on the page — it used to
          sit under a row of status chrome and a breadcrumb saying the same
          words. */}
      <Appear order={0}>
        <DetailCard>
          <div className="flex flex-col gap-3">
            <div className="flex flex-wrap items-start justify-between gap-3">
              <div className="flex min-w-0 items-start gap-3">
                <div
                  className={cn(
                    "flex h-10 w-10 shrink-0 items-center justify-center rounded-xl border border-border/60",
                    "bg-surface-raised",
                  )}
                >
                  <StatusIcon status={issue.status} className="h-5 w-5" />
                </div>
                <div className="min-w-0">
                  <h1 className="truncate text-lg font-semibold tracking-tight">{issue.title}</h1>
                  <div className="mt-1 flex flex-wrap items-center gap-x-2 gap-y-1 text-[11px] text-muted-foreground">
                    <span className="font-mono">{issue.identifier ?? issue.id.slice(0, 8)}</span>
                    {issue.crew_name && (
                      <>
                        <span aria-hidden>·</span>
                        <span>{issue.crew_name}</span>
                      </>
                    )}
                    {issue.assignee_name && (
                      <>
                        <span aria-hidden>·</span>
                        <span className="inline-flex items-center gap-1.5 rounded-full border border-border/60 py-0.5 pl-0.5 pr-2">
                          <AgentAvatar
                            seed={issue.assignee_id ?? issue.assignee_name}
                            className="h-4 w-4"
                            alt=""
                          />
                          <span className="font-medium">{issue.assignee_name}</span>
                        </span>
                      </>
                    )}
                  </div>
                </div>
              </div>
              {actions && <div className="flex shrink-0 items-center gap-1.5">{actions}</div>}
            </div>

            {issue.description && (
              <p className="max-w-[80ch] text-[13px] leading-relaxed text-foreground/85">
                {firstParagraph(issue.description)}
              </p>
            )}

            <div className="flex flex-wrap items-center gap-1.5">
              <Pill tone={issueStatusTone(issue.status)}>
                <StatusIcon status={issue.status} className="h-3 w-3" />
                {statusLabel[issue.status] ?? issue.status}
              </Pill>
              {issue.priority && issue.priority !== "none" && (
                <Pill tone={issuePriorityTone(issue.priority)}>
                  <PriorityIcon priority={issue.priority} className="h-3 w-3" />
                  {priorityLabel[issue.priority]}
                </Pill>
              )}
              {/* The full name. The old KPI tile cut it to 14 characters, which
                  turned "File Operations" into a project that does not exist. */}
              {project && (
                <Pill tone="default">
                  <FolderKanban className="h-3 w-3" />
                  {project.name}
                </Pill>
              )}
              {issue.routine_name && (
                <Pill tone="purple">
                  <GitBranch className="h-3 w-3" />
                  {issue.routine_name}
                </Pill>
              )}
              {(issue.sub_issues_count ?? 0) > 0 && (
                <Pill tone="default">
                  {issue.sub_issues_count} sub-{issue.sub_issues_count === 1 ? "issue" : "issues"}
                </Pill>
              )}
            </div>
          </div>
        </DetailCard>
      </Appear>

      <Appear order={1}>
        <StatStrip items={facts} />
      </Appear>

      <div className="grid grid-cols-1 gap-4 xl:grid-cols-3 2xl:grid-cols-4">
        <div className="flex flex-col gap-4 xl:col-span-2 2xl:col-span-3">
          <Appear order={2}>
            <DetailCard title="Description" subtitle={issue.description ? undefined : "empty"}>
              {issue.description ? (
                <MarkdownContent>{issue.description}</MarkdownContent>
              ) : (
                <p className="text-[12px] text-muted-foreground">
                  No description. The issue is whatever its title says.
                </p>
              )}
            </DetailCard>
          </Appear>

          <Appear order={3}>
            <DetailCard
              title="Links"
              icon={Link2}
              subtitle={relations.length > 0 ? String(relations.length) : undefined}
            >
              {relations.length === 0 ? (
                <p className="text-[12px] text-muted-foreground">
                  Nothing blocks this and nothing hangs off it.
                </p>
              ) : (
                <ul className="space-y-2">
                  {relations.map((rel) => (
                    <li key={rel.id} className="flex items-center gap-2.5 text-[12px]">
                      <span className="w-[86px] shrink-0 text-[10px] uppercase tracking-wider text-muted-foreground-soft">
                        {rel.relation_type.replace(/_/g, " ")}
                      </span>
                      <Link
                        href={`/issues/${encodeURIComponent(rel.target_identifier ?? rel.target_id)}`}
                        className="inline-flex min-w-0 items-center gap-1.5 hover:underline"
                      >
                        <span className="font-mono text-[10px] text-muted-foreground">
                          {rel.target_identifier ?? rel.target_id.slice(0, 8)}
                        </span>
                        <span className="truncate text-foreground/85">
                          {rel.target_title ?? "Untitled"}
                        </span>
                      </Link>
                    </li>
                  ))}
                </ul>
              )}
            </DetailCard>
          </Appear>
        </div>

        <div className="flex flex-col gap-4">
          {/* The one tinted card on the page. An issue that has run is
              first of all a thing that either worked or did not, and the
              wash says which before a word of it is read. */}
          <Appear order={4}>
            <LatestRun run={runs[0] ?? null} issue={issue} />
          </Appear>

          {/* One header, not two. The old rail wrapped a panel that drew its
              own PROPERTIES header inside a card titled PROPERTIES. */}
          <Appear order={5}>
            {/* Only what the figures band above does not already carry.
                Due and Estimate live there; repeating them here is the
                duplication this redesign is supposed to remove. */}
            <DetailCard title="Properties">
              <dl className="space-y-0.5">
                <Row icon={CircleDot} label="Status">
                  <span className="inline-flex items-center gap-1.5">
                    <StatusIcon status={issue.status} className="h-3.5 w-3.5" />
                    {statusLabel[issue.status] ?? issue.status}
                  </span>
                </Row>
                <Row icon={Flag} label="Priority">
                  <span className="inline-flex items-center gap-1.5">
                    <PriorityIcon priority={issue.priority ?? "none"} className="h-3.5 w-3.5" />
                    {priorityLabel[issue.priority ?? "none"]}
                  </span>
                </Row>
                <Row icon={UserCircle2} label="Assignee">
                  {issue.assignee_name ? (
                    <span className="inline-flex items-center gap-1.5">
                      <AgentAvatar
                        seed={issue.assignee_id ?? issue.assignee_name}
                        className="h-4 w-4"
                        alt=""
                      />
                      {issue.assignee_name}
                    </span>
                  ) : (
                    <Muted>Unassigned</Muted>
                  )}
                </Row>
                <Row icon={Users} label="Crew">
                  {issue.crew_name ?? <Muted>Unassigned</Muted>}
                </Row>
              </dl>
            </DetailCard>
          </Appear>

          <Appear order={6}>
            <DetailCard title="Routine" icon={GitBranch} tone="purple">
              {issue.routine_name ? (
                <div className="space-y-2">
                  <EntityChip
                    icon={GitBranch}
                    label={issue.routine_name}
                    note={issue.routine_slug ?? undefined}
                    tone="purple"
                    href={`/routines?routine=${encodeURIComponent(issue.routine_slug ?? "")}`}
                  />
                  <p className="text-[11px] text-muted-foreground">
                    Starting this issue runs that routine.
                  </p>
                </div>
              ) : (
                <p className="text-[12px] text-muted-foreground">
                  No routine bound. Starting this issue hands it to{" "}
                  {issue.assignee_name ?? "whoever is assigned"} directly.
                </p>
              )}
            </DetailCard>
          </Appear>

          <Appear order={7}>
            <DetailCard title="Project" icon={FolderKanban}>
              {project ? (
                <div className="space-y-2.5">
                  <EntityChip
                    icon={getCrewIconDef(project.icon || "folder").icon}
                    label={project.name}
                    href={`/issues?project=${encodeURIComponent(project.id)}`}
                  />
                  <div className="flex items-center gap-2 text-[11px] text-muted-foreground">
                    <div className="h-1.5 flex-1 overflow-hidden rounded-full bg-white/[0.06]">
                      <div
                        className="h-full rounded-full bg-primary/60"
                        style={{ width: `${Math.min(project.progress, 100)}%` }}
                      />
                    </div>
                    <span className="tabular-nums">
                      {project.done_count}/{project.issue_count}
                    </span>
                  </div>
                </div>
              ) : (
                <p className="text-[12px] text-muted-foreground">Not filed under a project.</p>
              )}
            </DetailCard>
          </Appear>

          <Appear order={8}>
            <DetailCard title="Labels" icon={Tag} subtitle={labels.length > 0 ? String(labels.length) : undefined}>
              {labels.length > 0 ? (
                <div className="flex flex-wrap gap-1.5">
                  {labels.map((l) => (
                    <LabelBadge key={l.id} label={l} />
                  ))}
                </div>
              ) : (
                <p className="text-[12px] text-muted-foreground">No labels.</p>
              )}
            </DetailCard>
          </Appear>

        </div>
      </div>

      {/* Comments and history behind one switch, at the foot, full width —
          the same arrangement Triggers/Versions uses on the routine card.
          Both used to be cards here AND tabs in a drawer underneath. */}
      <Appear order={9}>
        <DetailCard
          title={footTab === "comments" ? "Comments" : "History"}
          icon={MessageSquare}
          subtitle={String(footTab === "comments" ? comments.length : activities.length)}
          action={
            <div className="flex items-center gap-0.5 rounded-md border border-border/60 p-0.5">
              {(["comments", "history"] as const).map((t) => (
                <button
                  key={t}
                  type="button"
                  onClick={() => setFootTab(t)}
                  aria-pressed={footTab === t}
                  className={cn(
                    "rounded px-1.5 py-0.5 text-[10px] font-medium capitalize transition-colors",
                    footTab === t
                      ? "bg-primary/15 text-primary"
                      : "text-muted-foreground hover:text-foreground",
                  )}
                >
                  {t}
                </button>
              ))}
            </div>
          }
        >
          {footTab === "comments" ? (
            <div className="space-y-4">
            {comments.length === 0 ? (
              <p className="text-[12px] text-muted-foreground">
                Nobody has said anything about this issue yet.
              </p>
            ) : (
              <ul className="space-y-4">
                {comments.map((c) => (
                  <li key={c.id} className="flex gap-3">
                    {c.author_type === "agent" ? (
                      <AgentAvatar
                        seed={c.author_id}
                        className="mt-0.5 h-7 w-7 shrink-0 rounded-full"
                        alt=""
                      />
                    ) : (
                      <span className="mt-0.5 flex h-7 w-7 shrink-0 items-center justify-center rounded-full bg-primary/20 text-[11px] font-semibold text-primary">
                        {(c.author_name ?? "?").charAt(0).toUpperCase()}
                      </span>
                    )}
                    <div className="min-w-0 flex-1">
                      <div className="flex items-baseline gap-2">
                        <span className="text-[13px] font-medium text-foreground/90">
                          {c.author_name ?? c.author_type}
                        </span>
                        <span className="text-[11px] text-muted-foreground">
                          {timeAgo(c.created_at)}
                        </span>
                      </div>
                      <div className="mt-1 text-[13px] leading-relaxed text-foreground/85">
                        <MarkdownContent compact mentions={mentions}>
                          {c.body}
                        </MarkdownContent>
                      </div>
                    </div>
                  </li>
                ))}
              </ul>
            )}

            {/* The composer is the whole point of the next step: an agent
                becomes a participant when it is @mentioned here. */}
            {onSubmitComment && (
              <div className="border-t border-border/60 pt-4">
                <CommentComposer
                  agents={agents ?? []}
                  onSubmit={onSubmitComment}
                  authorInitial={viewerInitial ?? "U"}
                />
              </div>
            )}
            </div>
          ) : activities.length === 0 ? (
            <p className="text-[12px] text-muted-foreground">
              No recorded changes since this issue was opened.
            </p>
          ) : (
            <ul className="space-y-2 text-[12px]">
              {activities.map((a) => (
                <ActivityRow key={a.id} activity={a} mentions={mentions} />
              ))}
            </ul>
          )}
        </DetailCard>
      </Appear>

      {/* Metadata spans the width rather than sitting at the bottom of the
          rail: a rail taller than the main column leaves dead space beside it
          that a two-column grid has nothing to put in. */}
      <Appear order={10}>
        <DetailCard title="Metadata">
          <dl className="grid grid-cols-2 gap-x-4 gap-y-2 text-[11px] sm:grid-cols-3 xl:grid-cols-6">
            <Fact label="created" value={formatDate(issue.created_at)} />
            <Fact label="updated" value={relTime(issue.updated_at)} />
            <Fact label="author" value={issue.created_by?.name ?? issue.created_by?.id ?? "—"} />
            <Fact label="via" value={issue.authored_via ?? "ui"} />
            <Fact label="crew" value={issue.crew_slug ?? issue.crew_name ?? "—"} />
            <Fact label="id" value={issue.id} mono />
          </dl>
        </DetailCard>
      </Appear>
    </div>
  )
}

/**
 * How the last attempt ended.
 *
 * The one tinted card on the page. An issue that has run is first of all a
 * thing that either worked or did not, and the wash says which before a word
 * of it is read — the treatment the routine detail uses for its Last run, and
 * the reason it is worth copying.
 *
 * Reads `assignments` through the issue's tasks, so "nothing has run" is the
 * honest answer for an issue nobody started, not a card full of dashes.
 */
function LatestRun({ run, issue }: { run: IssueRun | null; issue: Mission }) {
  if (!run) {
    return (
      <DetailCard title="Runs">
        <p className="text-[12px] text-muted-foreground">
          {issue.status === "BACKLOG" || issue.status === "TODO"
            ? "Not started yet — nothing has run."
            : "No agent run recorded against this issue."}
        </p>
      </DetailCard>
    )
  }

  const tone = runTint(run.status)
  const Icon = tone === "success" ? CheckCircle2 : tone === "destructive" ? XCircle : Clock

  return (
    <TintedCard
      tone={tone}
      icon={Icon}
      title={`Last run · ${run.status.toLowerCase()}`}
      subtitle={run.id}
    >
      <TintedFacts
        items={[
          { label: "started", value: run.started_at ? relTime(run.started_at) : "—" },
          {
            label: "duration",
            value: run.duration_ms > 0 ? formatDurationDecimal(run.duration_ms) : "—",
          },
          { label: "agent", value: run.agent_name || "—" },
        ]}
      />
      {run.error_message && (
        <p className="line-clamp-2 text-[11px] text-destructive/90">{run.error_message}</p>
      )}
      <Link
        href={`/activity?mission=${encodeURIComponent(issue.id)}`}
        className="inline-flex items-center gap-1 text-[11px] text-primary hover:underline"
      >
        Open full trace
        <ArrowUpRight className="h-3 w-3" />
      </Link>
    </TintedCard>
  )
}

function runTint(status: string): TintTone {
  const s = status.toLowerCase()
  if (s === "completed" || s === "succeeded" || s === "success") return "success"
  if (s === "failed" || s === "error" || s === "cancelled") return "destructive"
  if (s === "in_progress" || s === "running") return "info"
  return "neutral"
}

/* ------------------------------------------------------------------ *
 *  Pieces                                                             *
 * ------------------------------------------------------------------ */

/**
 * One line of history.
 *
 * `mentioned` is rendered specially — "Pavel mentioned @Robin", with the chip
 * — and everything else keeps the generic shape it already had.
 *
 * **The backend does not emit a `mentioned` activity today.** Nothing in
 * `internal/api` logs one; the kinds that exist are `created`,
 * `status_changed`, `assignee_changed`, `priority_changed`,
 * `review_approved`, `review_changes_requested`. This renderer is written
 * ahead of it so the row does not have to be designed twice, and it accepts
 * every plausible `details` shape (see `mentionTargetFromActivityDetails`)
 * rather than betting on one. Until then it is dead code that costs nothing;
 * the day the activity lands, the row is already right.
 */
function ActivityRow({
  activity: a,
  mentions,
}: {
  activity: IssueActivity
  mentions: MentionDirectory
}) {
  const actor = <span className="font-medium">{a.actor_name ?? a.actor_type}</span>
  const when = (
    <span className="ml-auto shrink-0 text-[10px] text-muted-foreground-soft">
      {timeAgo(a.created_at)}
    </span>
  )

  if (isMentionActivity(a.action)) {
    const targetId = mentionTargetFromActivityDetails(a.details)
    const agent = targetId ? mentions.get(targetId) : undefined
    return (
      <li className="flex items-baseline gap-2">
        <span className="flex flex-wrap items-baseline gap-1.5 text-foreground/85">
          {actor} <span>mentioned</span>{" "}
          {agent ? (
            <MentionChip agent={agent} />
          ) : (
            // An id nothing resolves is still worth showing: it says the
            // mention happened, and names what it pointed at.
            <span className="font-mono text-[10px] text-muted-foreground">
              @{targetId ?? a.details ?? "someone"}
            </span>
          )}
        </span>
        {when}
      </li>
    )
  }

  return (
    <li className="flex items-baseline gap-2">
      <span className="text-foreground/85">
        {actor} {a.action.replace(/_/g, " ")}
      </span>
      {a.details && (
        <span className="truncate font-mono text-[10px] text-muted-foreground">{a.details}</span>
      )}
      {when}
    </li>
  )
}

/** A property row: label left, value right, no border ladder. */
function Row({
  icon: Icon,
  label,
  children,
}: {
  icon: React.ComponentType<{ className?: string }>
  label: string
  children: React.ReactNode
}) {
  return (
    <div className="flex items-center gap-2 py-1 text-[12px]">
      <Icon className="h-3.5 w-3.5 shrink-0 text-muted-foreground-soft" />
      <dt className="w-[70px] shrink-0 text-muted-foreground">{label}</dt>
      <dd className="min-w-0 flex-1 truncate text-foreground/85">{children}</dd>
    </div>
  )
}

function Muted({ children }: { children: React.ReactNode }) {
  return <span className="text-muted-foreground-soft">{children}</span>
}

function Fact({ label, value, mono }: { label: string; value: string; mono?: boolean }) {
  return (
    <div className="min-w-0">
      <dt className="text-[10px] uppercase tracking-wider text-muted-foreground-soft">{label}</dt>
      <dd className={cn("truncate text-foreground/85", mono && "font-mono text-[10px]")}>{value}</dd>
    </div>
  )
}

/**
 * The identity card carries a summary, not the whole brief — the full text
 * is one card below. Cutting at the first blank line keeps the summary the
 * author's own sentence rather than an arbitrary character count.
 */
function firstParagraph(md: string): string {
  const [first] = md.trim().split(/\n\s*\n/)
  return first ?? md
}
