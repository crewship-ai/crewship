"use client"

// The issue detail. One scrolling surface of cards, and — since #1752's
// successor — the ONLY issue detail: /issues/<identifier> and the centre pane
// of /issues both render this file. Before that the same issue had two
// screens with different chrome, a different rail and different capabilities,
// and which one you got depended on whether you clicked a row or followed a
// link.
//
// Read-only by default. Pass `edit` and every property becomes a picker; see
// issue-card-editors.tsx for why that union is wider than any one of the
// screens it replaced.
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
  Bot,
  ChevronRight,
  CircleDot,
  Clock,
  Flag,
  FolderKanban,
  GitBranch,
  Hash,
  Link2,
  ListChecks,
  MessageSquare,
  Milestone as MilestoneIcon,
  Tag,
  UserCircle2,
  Users,
  Zap,
} from "lucide-react"

import { cn } from "@/lib/utils"
import { formatDate, relTime, timeAgo } from "@/lib/time"
import { Appear, DetailCard, EntityChip, StatStrip } from "@/components/ui/detail"
import { Badge } from "@/components/ui/badge"
import { StatusPill } from "@/components/ui/status-pill"
import { formatStatus } from "@/lib/format-status"
import { AgentAvatar } from "@/components/ui/agent-avatar"
import { UserAvatar } from "@/components/ui/user-avatar"
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
import { TiptapEditor } from "@/components/features/issues/tiptap-editor"
import {
  AddRelationPicker,
  AssigneePicker,
  DueDatePicker,
  EditRow,
  EstimatePicker,
  LabelsPicker,
  MilestonePicker,
  PriorityPicker,
  ProjectPicker,
  RemoveRelationButton,
  RoutinePicker,
  RunRoutineButton,
  StatusPicker,
  TitleEditor,
  type IssueCardEdit,
} from "@/components/features/issues/issue-card-editors"
import {
  IssueCodeLinksCard,
  type CodeLinkEdit,
} from "@/components/features/issues/issue-code-links-card"
import { getCrewIconDef } from "@/lib/entities"
import { RELATION_TYPE_LABELS } from "@/components/features/issues/issue-constants"
import { entityHref } from "@/lib/entity-links"
import { IssueRunsCard, issueRunLinks, issueRunStatus, type IssueRun } from "@/components/features/issues/issue-runs-card"
import { issueFacts, issuePriorityTone } from "@/lib/issue-facts"
import { automationsForIssue, type Automation } from "@/lib/automations"
import { runProvenance } from "@/lib/run-provenance"
import { AutomationList } from "@/components/features/automations/automation-list"
import type { PipelineRunRecord } from "@/hooks/use-pipeline-run-records"
import type {
  IssueActivity,
  IssueCodeLink,
  IssueComment,
  IssueRelation,
  Mission,
  Project,
} from "@/lib/types/mission"

export type { IssueRun } from "@/components/features/issues/issue-runs-card"

interface Props {
  issue: Mission
  comments: IssueComment[]
  olderComments?: React.ReactNode
  activities: IssueActivity[]
  relations: IssueRelation[]
  /** Newest first, as the endpoint returns them. */
  runs?: IssueRun[]
  /**
   * Sub-resources whose fetch failed ("comments", "history", "runs", …).
   * Their cards say so instead of claiming emptiness — a 500 on comments is
   * not "nobody has said anything" (S6).
   */
  unavailable?: readonly string[]
  /** The issue's project, already resolved by the caller. */
  project?: Project | null
  /**
   * Start / Stop / Approve / Request changes, rendered top-right of the
   * identity card. Passed in rather than rebuilt here: the host owns the
   * handlers, the RBAC guards and the busy states, and a second copy of
   * that wiring is a second thing to keep correct.
   */
  actions?: React.ReactNode
  /** Who is working on it and what to do next (issue-session-card.tsx). */
  session?: React.ReactNode
  /** Work & control (issue-work-card.tsx), first in the aside. */
  workPanel?: React.ReactNode
  filesPanel?: React.ReactNode
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
  onSubmitComment?: (body: string, options?: { guestVisible: boolean; expectedProjectId: string }) => boolean | Promise<boolean>
  /** Initial for the composer's author bubble. */
  commentDraft?: string
  onCommentDraft?: (value: string) => void
  viewerInitial?: string
  /**
   * Makes the card writable. Absent, every property renders as text — which
   * is what a reader without permission, and what a screenshot, should get.
   */
  edit?: IssueCardEdit
  /** Children of this issue, resolved by the host from `/subtasks`. */
  subIssues?: Mission[]
  /**
   * Pull requests / merge requests attached to this issue, newest first as
   * `/code-links` returns them. Their title, author and branch names are
   * forge-supplied and untrusted — see issue-code-links-card.tsx.
   */
  codeLinks?: IssueCodeLink[]
  /**
   * Attach / remove / refresh. Honoured only alongside `edit`: a card that
   * renders every property read-only must not carry a live delete.
   */
  codeLinkEdit?: CodeLinkEdit
  /**
   * The live agent-work timeline. Passed in rather than rendered here: it
   * reads the journal, which needs the workspace and a poller, and the card
   * is not the thing that should own either.
   */
  runActivity?: React.ReactNode
  /**
   * Every automation rule in the workspace. Narrowed HERE, to the ones whose
   * event type and matcher put this issue in scope — the host has no reason to
   * know that predicate, and two callers narrowing it separately is two places
   * for the answer to drift.
   */
  automations?: Automation[]
  /**
   * Recent runs of the routine bound to this issue, newest first, as
   * `/pipelines/{slug}/run-records` returns them. Absent when nothing is
   * bound. Used only to say HOW those runs started.
   */
  routineRuns?: PipelineRunRecord[]
}

type FootTab = "all" | "comments" | "history"

export function IssueCardDetail({
  issue,
  comments,
  olderComments,
  activities,
  relations,
  runs = [],
  unavailable = [],
  project,
  actions,
  agents,
  onSubmitComment,
  viewerInitial,
  commentDraft, onCommentDraft,
  edit,
  subIssues = [],
  codeLinks = [],
  codeLinkEdit,
  runActivity,
  session,
  workPanel,
  filesPanel,
  automations = [],
  routineRuns = [],
}: Props) {
  const [footTab, setFootTab] = React.useState<FootTab>("all")

  const mentions = React.useMemo(() => mentionDirectory(agents ?? []), [agents])

  // The rules an event from THIS issue could set off. See lib/automations.ts
  // for why only mission_ids and crew_ids are treated as exclusions.
  const myAutomations = React.useMemo(
    () => automationsForIssue(automations, { missionId: issue.id, crewId: issue.crew_id }),
    [automations, issue.id, issue.crew_id],
  )
  // How the bound routine's recent runs started, grouped. Only meaningful
  // when something IS bound — otherwise these runs belong to no routine this
  // issue has any relationship with.
  const runSources = React.useMemo(
    () => (issue.routine_slug ? groupRunSources(routineRuns) : []),
    [issue.routine_slug, routineRuns],
  )

  const labels = issue.labels ?? []
  const facts = React.useMemo(
    () => issueFacts(issue, { comments: comments.length }),
    [issue, comments.length],
  )

  const timeline = React.useMemo(() => {
    // Comments and history in one stream, newest first, whichever order the
    // server listed them in (the API answers history oldest-first since the
    // seq-cursored resync).
    type Item = { kind: "comment"; at: string; c: IssueComment } | { kind: "activity"; at: string; seq?: number; a: IssueActivity }
    const items: Item[] = [
      ...comments.map((c) => ({ kind: "comment" as const, at: String(c.created_at ?? ""), c })),
      ...activities.map((a) => ({ kind: "activity" as const, at: String(a.created_at ?? ""), seq: (a as { seq?: number }).seq, a })),
    ]
    const byTime = (x: Item, y: Item) => y.at.localeCompare(x.at)
    const history = items.filter((i) => i.kind === "activity").sort((x, y) => {
      const sx = x.kind === "activity" ? x.seq : undefined
      const sy = y.kind === "activity" ? y.seq : undefined
      if (typeof sx === "number" && typeof sy === "number" && sx !== sy) return sy - sx
      return byTime(x, y)
    })
    return { all: [...items].sort(byTime), history }
  }, [comments, activities])

  const editable = (action: string) => Boolean(edit && allowsEdit(edit, action))
  const sub = subIssues
  const subDone = sub.filter((s) => ["DONE", "COMPLETED"].includes(String(s.status))).length
  const subOpen = sub.length - subDone

  const comment = (c: IssueComment) => (
    <li key={`c-${c.id}`} className="flex gap-3">
      {c.author_type === "agent" ? (
        <AgentAvatar seed={c.author_id} className="mt-0.5 size-7 shrink-0 rounded-full" alt="" />
      ) : (
        <span className="mt-0.5 flex size-7 shrink-0 items-center justify-center rounded-full bg-primary/20 text-label font-semibold text-primary">
          {(c.author_name ?? "?").charAt(0).toUpperCase()}
        </span>
      )}
      <div className="min-w-0 flex-1">
        <div className="flex items-baseline gap-2">
          <span className="text-body font-medium text-foreground">{c.author_name ?? c.author_type}</span>
          <span className="text-label text-muted-foreground">{timeAgo(c.created_at)}</span>
          {c.guest_visible && <span className="text-label text-muted-foreground">Shared with Guests</span>}
        </div>
        <div className="mt-1 text-body leading-relaxed text-foreground/85">
          <MarkdownContent compact mentions={mentions}>
            {c.body}
          </MarkdownContent>
        </div>
      </div>
    </li>
  )

  return (
    <div className="mx-auto grid w-full max-w-[1280px] grid-cols-1 gap-5 px-4 py-5 sm:px-6 xl:grid-cols-[minmax(0,1fr)_320px]" data-slot="issue-full">
      {/* ── Main column: what the issue is and what is happening on it ── */}
      <div className="flex min-w-0 flex-col gap-4">
        <Appear order={0}>
          <div className="flex flex-col gap-1.5">
            <div className="flex flex-wrap items-center gap-x-2 gap-y-1 text-label text-muted-foreground">
              <span className="font-mono">{issue.identifier ?? issue.id.slice(0, 8)}</span>
              <StatusIcon status={issue.status} className="size-3.5" />
              <span>{statusLabel[issue.status] ?? formatStatus(issue.status).label}</span>
              {issue.priority && issue.priority !== "none" && (
                <StatusPill
                  tone={issuePriorityTone(issue.priority)}
                  icon={<PriorityIcon priority={issue.priority} className="size-3" />}
                  label={priorityLabel[issue.priority]}
                />
              )}
              {project && (
                <>
                  <span aria-hidden>·</span>
                  <Link href={`/issues?project=${encodeURIComponent(project.id)}`} className="inline-flex items-center gap-1 hover:text-foreground hover:underline">
                    <FolderKanban className="size-3" aria-hidden />
                    {project.name}
                  </Link>
                </>
              )}
              {issue.crew_name && (
                <>
                  <span aria-hidden>·</span>
                  <CrewLink issue={issue} />
                </>
              )}
              {(issue.sub_issues_count ?? 0) > 0 && (
                <Badge variant="secondary">
                  {issue.sub_issues_count} sub-{issue.sub_issues_count === 1 ? "issue" : "issues"}
                </Badge>
              )}
              {actions && <span className="ml-auto flex items-center gap-1.5">{actions}</span>}
            </div>
            {editable("edit_fields") && edit ? (
              <TitleEditor title={issue.title} onDraftChange={edit.onTitleDraft} onSave={(next) => void edit.patch({ title: next })} />
            ) : (
              <h1 className="break-words text-title font-semibold tracking-tight">{issue.title}</h1>
            )}
          </div>
        </Appear>

        {session && <Appear order={1}>{session}</Appear>}

        <Appear order={2}>
          <DetailCard title="Brief" subtitle={issue.description ? "what the agent works from" : "empty"}>
            {editable("edit_fields") && edit ? (
              <DescriptionEditor key={issue.id} description={issue.description ?? ""} onChange={(md) => void edit.patch({ description: md })} />
            ) : issue.description ? (
              <MarkdownContent>{issue.description}</MarkdownContent>
            ) : (
              <p className="text-label text-muted-foreground">No description. The issue is whatever its title says.</p>
            )}
          </DetailCard>
        </Appear>

        <Appear order={3}>
          <DetailCard title="Sub-issues" icon={ListChecks} subtitle={sub.length > 0 ? `${subDone}/${sub.length}` : undefined}>
            {sub.length === 0 ? (
              <p className="text-label text-muted-foreground">Break bigger work into sub-issues. A parent can’t move to Done while a sub-issue is open.</p>
            ) : (
              <>
                <ul className="flex flex-col">
                  {sub.map((s) => (
                    <li key={s.id}>
                      <Link href={`/issues/${encodeURIComponent(s.identifier ?? s.id)}`} className="row-hover flex items-center gap-2 rounded-md px-1 py-1.5 text-body">
                        <StatusIcon status={s.status} className="size-3.5 shrink-0" />
                        <span className="shrink-0 font-mono text-label text-muted-foreground">{s.identifier ?? s.id.slice(0, 8)}</span>
                        <span className="truncate text-foreground/85">{s.title}</span>
                      </Link>
                    </li>
                  ))}
                </ul>
                {subOpen > 0 && (
                  <p className="mt-2 text-label text-muted-foreground">
                    The parent can’t move to Done or In review while {subOpen} sub-issue{subOpen === 1 ? " is" : "s are"} open.
                  </p>
                )}
              </>
            )}
          </DetailCard>
        </Appear>

        <Appear order={4}>
          <DetailCard
            title="Relations"
            icon={Link2}
            subtitle={relations.length > 0 ? String(relations.length) : undefined}
            action={editable("link") && edit ? <AddRelationPicker edit={edit} /> : undefined}
          >
            {relations.length === 0 ? (
              <p className="text-label text-muted-foreground">Nothing blocks this and nothing hangs off it.</p>
            ) : (
              <ul className="space-y-2">
                {relations.map((rel) => (
                  <li key={rel.id} className="group flex items-center gap-2.5 text-body">
                    <span className="w-[6.5rem] shrink-0 text-label text-muted-foreground">
                      {RELATION_TYPE_LABELS[rel.relation_type as keyof typeof RELATION_TYPE_LABELS] ?? rel.relation_type.replace(/_/g, " ")}
                    </span>
                    <Link href={`/issues/${encodeURIComponent(rel.target_identifier ?? rel.target_id)}`} className="inline-flex min-w-0 flex-1 items-center gap-1.5 hover:underline">
                      <span className="font-mono text-label text-muted-foreground">{rel.target_identifier ?? rel.target_id.slice(0, 8)}</span>
                      <span className="truncate text-foreground/85">{rel.target_title ?? "Untitled"}</span>
                    </Link>
                    {editable("link") && edit && (
                      <RemoveRelationButton identifier={rel.target_identifier ?? rel.target_id.slice(0, 8)} onRemove={() => void edit.removeRelation(rel.id)} />
                    )}
                  </li>
                ))}
              </ul>
            )}
          </DetailCard>
        </Appear>

        {/* What code resolves this: a pull request is the answer to the
            issue, not another issue. */}
        <Appear order={5}>
          <IssueCodeLinksCard links={codeLinks} edit={editable("link") ? codeLinkEdit : undefined} />
        </Appear>

        {/* Live agent work while the issue is running; nothing when quiet. */}
        {runActivity && <Appear order={6}>{runActivity}</Appear>}

        <Appear order={7}>
          <IssueRunsCard issue={issue} runs={runs} unavailable={unavailable.includes("runs")} />
        </Appear>

        <span id="issue-conversation" className="scroll-mt-16" />
        <Appear order={8}>
          <DetailCard
            title="Timeline"
            icon={MessageSquare}
            subtitle={String(footTab === "comments" ? comments.length : footTab === "history" ? activities.length : comments.length + activities.length)}
            action={
              <div role="radiogroup" aria-label="Show" className="flex items-center gap-0.5 rounded-md border border-border p-0.5">
                {(
                  [
                    ["all", "All"],
                    ["comments", "Comments"],
                    ["history", "Activity"],
                  ] as const
                ).map(([t, label]) => (
                  <button
                    key={t}
                    type="button"
                    role="radio"
                    onClick={() => setFootTab(t)}
                    aria-checked={footTab === t}
                    className={cn(
                      "kit-tap rounded-sm px-2 py-0.5 text-label font-medium transition-colors",
                      footTab === t ? "bg-primary/15 text-primary-hover" : "text-muted-foreground hover:text-foreground",
                    )}
                  >
                    {label}
                  </button>
                ))}
              </div>
            }
          >
            <div className="space-y-4">
              {footTab !== "history" && olderComments}
              {footTab === "comments" ? (
                comments.length === 0 ? (
                  <p className="text-label text-muted-foreground">
                    {unavailable.includes("comments") ? "Could not load the comments — try again above." : "Nobody has said anything about this issue yet."}
                  </p>
                ) : (
                  <ul className="space-y-4">{comments.map(comment)}</ul>
                )
              ) : footTab === "history" ? (
                timeline.history.length === 0 ? (
                  <p className="text-label text-muted-foreground">
                    {unavailable.includes("history") ? "Could not load the history — try again above." : "No recorded changes since this issue was opened."}
                  </p>
                ) : (
                  <ul className="space-y-2 text-label">
                    {timeline.history.map((i) => (i.kind === "activity" ? <ActivityRow key={`a-${i.a.id}`} activity={i.a} mentions={mentions} /> : null))}
                  </ul>
                )
              ) : timeline.all.length === 0 ? (
                <p className="text-label text-muted-foreground">
                  {unavailable.includes("comments") || unavailable.includes("history") ? "Could not load the timeline — try again above." : "Nothing yet."}
                </p>
              ) : (
                <ul className="space-y-3">
                  {timeline.all.map((i) => (i.kind === "comment" ? comment(i.c) : <ActivityRow key={`a-${i.a.id}`} activity={i.a} mentions={mentions} />))}
                </ul>
              )}

              {/* The composer is how an agent becomes a participant: it is
                  @mentioned here. */}
              {onSubmitComment && footTab !== "history" && (
                <div className="border-t border-border pt-4">
                  <CommentComposer
                    agents={agents ?? []}
                    onSubmit={onSubmitComment}
                    draftValue={commentDraft}
                    onDraftChange={onCommentDraft}
                    allowGuestSharing={Boolean(project)}
                    guestProject={project}
                    authorInitial={viewerInitial ?? "U"}
                  />
                </div>
              )}
            </div>
          </DetailCard>
        </Appear>
      </div>

      {/* ── Aside: who holds it and its properties ──
          It follows the reader on wide screens. Three parts, all load-bearing:
          xl: (below, the columns stack and a sticky column fights the scroll),
          self-start (a stretched grid item can never stick), and max-h +
          overflow (a taller rail would park its tail below the fold). */}
      <aside
        data-testid="issue-detail-rail"
        aria-label="Issue details"
        className={cn("flex min-w-0 flex-col gap-4", "xl:sticky xl:top-4 xl:self-start xl:max-h-[calc(100dvh-9rem)] xl:overflow-y-auto")}
      >
        {workPanel && <Appear order={1}>{workPanel}</Appear>}

        <Appear order={2}>
          <DetailCard title="Properties">
            <dl className="space-y-0.5">
              <EditRow icon={CircleDot} label="Status">
                {editable("change_status") && edit ? (
                  <StatusPicker issue={issue} edit={edit} />
                ) : (
                  <span className="inline-flex items-center gap-1.5 text-body">
                    <StatusIcon status={issue.status} className="size-3.5" />
                    {statusLabel[issue.status] ?? issue.status}
                  </span>
                )}
              </EditRow>
              <EditRow icon={Flag} label="Priority">
                {editable("edit_fields") && edit ? (
                  <PriorityPicker issue={issue} edit={edit} />
                ) : (
                  <span className="inline-flex items-center gap-1.5 text-body">
                    <PriorityIcon priority={issue.priority ?? "none"} className="size-3.5" />
                    {priorityLabel[issue.priority ?? "none"]}
                  </span>
                )}
              </EditRow>
              {issue.owner || issue.delegate || !issue.assignee_name || edit ? (
                <>
                  <EditRow icon={UserCircle2} label="Owner" hint="accountable person">
                    {issue.owner ? (
                      <span className="inline-flex items-center gap-1.5 text-body">
                        <UserAvatar name={issue.owner.name} email="" className="size-4" />
                        {issue.owner.name ?? "Owner"}
                      </span>
                    ) : (
                      <Muted>No human owner</Muted>
                    )}
                  </EditRow>
                  <EditRow icon={Bot} label="Delegate" hint={`agent doing the work${issue.crew_name ? ` · ${issue.crew_name} crew` : ""}`}>
                    {editable("assign") && edit ? (
                      <AssigneePicker issue={issue} edit={edit} />
                    ) : issue.delegate ? (
                      <span className="inline-flex items-center gap-1.5 text-body">
                        <AgentAvatar seed={issue.delegate.id} className="size-4" alt="" />
                        {issue.delegate.name ?? "Delegate"}
                      </span>
                    ) : (
                      <Muted>Not delegated</Muted>
                    )}
                  </EditRow>
                </>
              ) : (
                // A row this client fetched before the owner/delegate
                // backfill reached it: neither typed field is present.
                <EditRow icon={UserCircle2} label="Assignee">
                  <AssigneeLink issue={issue} />
                </EditRow>
              )}
              <EditRow icon={FolderKanban} label="Project">
                {editable("edit_fields") && edit ? (
                  <ProjectPicker issue={issue} edit={edit}>
                    <FolderKanban className="size-3.5 shrink-0 text-muted-foreground-soft" />
                    <span className="truncate text-body">{project?.name ?? <Muted>Add to project</Muted>}</span>
                  </ProjectPicker>
                ) : project ? (
                  <EntityChip icon={getCrewIconDef(project.icon || "folder").icon} label={project.name} href={`/issues?project=${encodeURIComponent(project.id)}`} />
                ) : (
                  <Muted>Not filed under a project</Muted>
                )}
              </EditRow>
              {(edit || issue.milestone_id) && (
                <EditRow icon={MilestoneIcon} label="Milestone">
                  {editable("edit_fields") && edit ? <MilestonePicker issue={issue} edit={edit} /> : <span className="text-body">{issue.milestone_id ?? "Not set"}</span>}
                </EditRow>
              )}
              <EditRow icon={Tag} label="Labels">
                <span className="flex min-w-0 flex-wrap items-center gap-1.5">
                  {labels.map((l) => (
                    <LabelBadge key={l.id} label={l} />
                  ))}
                  {labels.length === 0 && !editable("edit_fields") && <Muted>No labels</Muted>}
                  {editable("edit_fields") && edit && <LabelsPicker issue={issue} edit={edit} />}
                </span>
              </EditRow>
              {edit && (
                <EditRow icon={Clock} label="Due">
                  {editable("edit_fields") && edit ? <DueDatePicker issue={issue} edit={edit} /> : <span className="font-mono text-body">{issue.due_date ?? "Not set"}</span>}
                </EditRow>
              )}
              {edit && (
                <EditRow icon={Hash} label="Estimate">
                  {editable("edit_fields") && edit ? <EstimatePicker issue={issue} edit={edit} /> : <span className="font-mono text-body">{issue.estimate ?? "Not set"}</span>}
                </EditRow>
              )}
              <EditRow icon={Users} label="Crew">
                {issue.crew_name ? <CrewLink issue={issue} /> : <Muted>Unassigned</Muted>}
              </EditRow>
            </dl>
          </DetailCard>
        </Appear>

        {filesPanel}

        <Appear order={3}>
          <RelatedCard issue={issue} runs={runs} />
        </Appear>

        {/* What the rail does not need every day: the routine's detail, the
            rules watching this issue, and the raw facts. */}
        <Appear order={4}>
          <details className="group/tech rounded-card border border-border bg-card" data-slot="issue-technical">
            <summary className="flex cursor-pointer list-none items-center gap-2 px-4 py-2.5 text-label font-semibold text-muted-foreground">
              <ChevronRight className="size-3.5 transition-transform group-open/tech:rotate-90" aria-hidden />
              Technical details
            </summary>
            <div className="flex flex-col gap-4 border-t border-border p-3">
              <DetailCard
                title="Routine"
                icon={GitBranch}
                tone="purple"
                action={
                  edit?.runRoutine && issue.routine_slug && issue.work_mode !== "human" ? (
                    <RunRoutineButton onRun={() => void edit.runRoutine!()} busy={edit.busy} />
                  ) : undefined
                }
              >
                <div className="space-y-2">
                  {editable("change_workflow") && edit && (
                    <RoutinePicker issue={issue} edit={edit}>
                      <GitBranch className="size-3.5 shrink-0 text-purple" />
                      <span className="truncate text-body">{issue.routine_name ?? <Muted>No routine bound</Muted>}</span>
                    </RoutinePicker>
                  )}
                  {issue.routine_name && !editable("change_workflow") && (
                    <EntityChip
                      icon={GitBranch}
                      label={issue.routine_name}
                      note={issue.routine_slug ?? undefined}
                      tone="purple"
                      href={issue.routine_slug ? entityHref({ kind: "routine", slug: issue.routine_slug }) : undefined}
                    />
                  )}
                  {issue.work_mode === "human" ? (
                    <p className="text-label text-muted-foreground">Agent execution is paused while a person handles this issue.</p>
                  ) : issue.routine_name ? (
                    <p className="text-label text-muted-foreground">Starting this issue runs that routine.</p>
                  ) : (
                    <p className="text-label text-muted-foreground">
                      No routine bound. Starting this issue hands it to {issue.delegate?.name ?? issue.assignee_name ?? "whoever is delegated"} directly.
                    </p>
                  )}
                  <RunSourceBreakdown sources={runSources} />
                </div>
              </DetailCard>

              {myAutomations.length > 0 && (
                <DetailCard title="Automations" icon={Zap} subtitle={String(myAutomations.length)}>
                  <div data-testid="issue-automations" className="space-y-2.5">
                    <AutomationList
                      automations={myAutomations}
                      note={
                        // Hedged: the matcher narrows further on things that
                        // are only known when the event happens.
                        <p className="text-label text-muted-foreground">
                          These could fire on a change to this issue. A rule may narrow further on the agent, severity or payload of the event itself.
                        </p>
                      }
                    />
                  </div>
                </DetailCard>
              )}

              <StatStrip items={facts} />

              <DetailCard title="Metadata">
                <dl className="grid grid-cols-2 gap-x-4 gap-y-2 text-label">
                  <Fact label="created" value={formatDate(issue.created_at)} />
                  <Fact label="updated" value={relTime(issue.updated_at)} />
                  {issue.created_by && (
                    <div className="min-w-0" data-testid="issue-created-by">
                      <dt className="font-mono text-micro uppercase tracking-[0.08em] text-muted-foreground-soft">author</dt>
                      <dd className="truncate text-foreground/85">
                        Created by {issue.created_by.name || issue.created_by.id}
                        {issue.created_by.type === "agent" && (
                          <span
                            data-testid="issue-creator-agent-badge"
                            className="ml-1.5 inline-flex items-center rounded border border-primary/30 bg-primary/10 px-1 py-px text-micro font-medium uppercase tracking-wide text-primary"
                          >
                            agent
                          </span>
                        )}
                      </dd>
                    </div>
                  )}
                  <Fact label="via" value={issue.authored_via ?? "ui"} />
                  <Fact label="crew" value={issue.crew_slug ?? issue.crew_name ?? "—"} />
                  <Fact label="id" value={issue.id} mono />
                </dl>
              </DetailCard>
            </div>
          </details>
        </Appear>
      </aside>
    </div>
  )
}

/** One way the bound routine gets started, and how often it did. */
interface RunSource {
  /** "issue" | "manual" | "schedule" | "automation" | … */
  label: string
  /** Which rule / schedule / issue, when the run named one. */
  source?: string
  count: number
}

/**
 * Groups a routine's recent runs by how each was started.
 *
 * Grouped, not listed: the reader is asking "does this thing run without me",
 * which is a question about SOURCES, and eight rows of run ids in a 280px rail
 * answers a question nobody asked.
 *
 * The label comes from runProvenance, not from `triggered_via`. Every deferred
 * run — cron and automation alike — is stored as "schedule", so counting the
 * enum would fold a rule into the cron bucket and report that a schedule did
 * something a rule did.
 */
function groupRunSources(runs: PipelineRunRecord[]): RunSource[] {
  const byKey = new Map<string, RunSource>()
  for (const r of runs) {
    const { label, source } = runProvenance(r)
    // An automation is keyed by its rule; a schedule and a manual start are
    // keyed by kind alone. Keying a schedule by its id would produce a row per
    // cron entry, which is detail this card is not for.
    const key = label === "automation" ? `automation:${source ?? ""}` : label
    const existing = byKey.get(key)
    if (existing) {
      existing.count += 1
      continue
    }
    byKey.set(key, { label, source: label === "automation" ? source : undefined, count: 1 })
  }
  return [...byKey.values()].sort((a, b) => b.count - a.count || a.label.localeCompare(b.label))
}

/** The grouped sources, or nothing at all when there is nothing to group. */
function RunSourceBreakdown({ sources }: { sources: RunSource[] }) {
  if (sources.length === 0) return null
  return (
    <div data-testid="issue-routine-provenance" className="space-y-1 pt-1">
      <p className="font-mono text-[10px] uppercase tracking-[0.08em] text-muted-foreground-soft">
        Recent runs started by
      </p>
      <ul className="space-y-1">
        {sources.map((s) => (
          <li
            key={`${s.label}:${s.source ?? ""}`}
            data-testid={`issue-routine-source-${s.label}`}
            className="flex items-baseline gap-2 text-[11px]"
          >
            <span className="text-foreground/85">{s.label}</span>
            {s.source && (
              <span className="min-w-0 flex-1 truncate text-muted-foreground">{s.source}</span>
            )}
            <span className={cn("tabular-nums text-muted-foreground", !s.source && "ml-auto")}>
              {s.count}
            </span>
          </li>
        ))}
      </ul>
    </div>
  )
}

/** The crew, as a link to its page — the name used to be plain text in four places. */
function CrewLink({ issue }: { issue: Mission }) {
  const inner = (
    <span className="inline-flex items-center gap-1.5">
      <Users className="h-3.5 w-3.5 text-muted-foreground" aria-hidden />
      {issue.crew_name}
    </span>
  )
  if (!issue.crew_slug) return inner
  return (
    <Link href={entityHref({ kind: "crew", slug: issue.crew_slug })} className="hover:underline">
      {inner}
    </Link>
  )
}

/** The assignee: an agent links to its page; a person has none. */
function AssigneeLink({ issue }: { issue: Mission }) {
  const inner = (
    <span className="inline-flex items-center gap-1.5 rounded-full border border-border/60 py-0.5 pl-0.5 pr-2">
      <AgentAvatar seed={issue.assignee_id ?? issue.assignee_name ?? ""} className="h-4 w-4" alt="" />
      <span className="font-medium">{issue.assignee_name}</span>
    </span>
  )
  if (issue.assignee_type !== "agent" || !issue.assignee_slug) return inner
  return (
    <Link href={entityHref({ kind: "agent", slug: issue.assignee_slug })} className="hover:underline">
      {inner}
    </Link>
  )
}

/**
 * Where the issue leads. The links README §5 asks of an issue, in one card,
 * each through entityHref so none can point at a route that does not exist.
 * The run page and the journal carry the same links back.
 */
function RelatedCard({ issue, runs }: { issue: Mission; runs: IssueRun[] }) {
  const current = runs.find((r) => r.status === "RUNNING") ?? runs[0]
  const links = issueRunLinks(issue, current)
  const runWord = current ? issueRunStatus(current).label : ""
  return (
    <DetailCard title="Related" icon={ArrowUpRight}>
      <dl className="space-y-0.5">
        <Row icon={Users} label="Crew">
          {issue.crew_name ? <CrewLink issue={issue} /> : <Muted>Unassigned</Muted>}
        </Row>
        <Row icon={UserCircle2} label={issue.work_mode === "human" ? "Worker" : "Agent"}>
          {issue.work_mode === "human" ? issue.worker_name || "Human worker" : issue.delegate?.name ? issue.delegate.name : issue.assignee_name ? <AssigneeLink issue={issue} /> : <Muted>Unassigned</Muted>}
        </Row>
        <Row icon={Clock} label={current?.status === "RUNNING" ? "Current run" : "Latest run"}>
          {current ? (
            links.run ? (
              <Link href={links.run} className="inline-flex items-center gap-1 hover:underline">
                {current.agent_name ?? "run"} · {runWord}
                <ArrowUpRight className="h-3 w-3" />
              </Link>
            ) : (
              <span>
                {current.agent_name ?? "run"} · {runWord}
              </span>
            )
          ) : (
            <Muted>none yet</Muted>
          )}
        </Row>
        <Row icon={GitBranch} label="Journal">
          <Link href={links.journal} className="inline-flex items-center gap-1 hover:underline">
            trace {issue.identifier ?? "this issue"}
            <ArrowUpRight className="h-3 w-3" />
          </Link>
        </Row>
        <Row icon={GitBranch} label="Activity">
          <Link href={links.activity} className="inline-flex items-center gap-1 hover:underline">
            all runs
            <ArrowUpRight className="h-3 w-3" />
          </Link>
        </Row>
      </dl>
    </DetailCard>
  )
}

/**
 * The description, as a WYSIWYG editor that saves on blur.
 *
 * The draft is local so a keystroke does not PATCH; the write happens once,
 * when focus leaves, and only if the markdown actually changed — the shipped
 * page's `descDraft !== issue.description` guard, kept because without it
 * every click-through of an issue rewrote its description with itself.
 */
function DescriptionEditor({
  description,
  onChange,
}: {
  description: string
  /** Each edit goes to the page's draft; nothing saves on its own. The
   *  editor's own autosave only runs through onBlur, which is not passed. */
  onChange: (markdown: string) => void
}) {
  return (
    <TiptapEditor
      content={description}
      onChange={(md) => {
        if (md !== description) onChange(md)
      }}
      placeholder="Click to add description…"
      editable
    />
  )
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
      <dt className="font-mono text-[10px] uppercase tracking-[0.08em] text-muted-foreground-soft">{label}</dt>
      <dd className={cn("truncate text-foreground/85", mono && "font-mono text-[10px]")}>{value}</dd>
    </div>
  )
}

function allowsEdit(edit: IssueCardEdit, action: string): boolean {
  return !edit.allowedActions || edit.allowedActions.some((a) => a === action)
}
