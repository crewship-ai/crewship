"use client"

// The issue detail, wired.
//
// One component owns every fetch and every write for a single issue, and both
// places an issue can be opened render it:
//
//   /issues/<identifier>   the canonical deep link — the dashboard, ⌘K, the
//                          inbox, activity, crew tabs and sub-issue links all
//                          point here
//   /issues?issue=<ident>  the centre pane, beside the explorer and the board
//
// Before this they were two different screens with different chrome, a
// different rail and different capabilities, and which one a reader got
// depended on whether they had clicked a row or followed a link. Everything
// below the header is now the same file in both.
//
// The card itself (issue-card-detail.tsx) stays a renderer: it draws, it does
// not fetch. That split is why the design preview could argue the layout
// against real data without ever being able to write.

import * as React from "react"
import { toast } from "sonner"

import { apiFetch } from "@/lib/api-fetch"
import { LABEL_PRESET_COLORS } from "@/lib/colors"
import { useRealtimeEvent } from "@/hooks/use-realtime"
import { usePipelines } from "@/hooks/use-pipelines"
import { useAutomations } from "@/hooks/use-automations"
import { usePipelineRunRecords } from "@/hooks/use-pipeline-run-records"
import { Skeleton } from "@/components/ui/skeleton"
import { RunActivityTimeline, RUN_WORK_ENTRY_TYPES } from "@/components/features/activity/run-activity-timeline"
import { IssueCardDetail, type IssueRun } from "@/components/features/issues/issue-card-detail"
import {
  IssueWorkflowActions,
  type IssueCardEdit,
  type WorkflowAction,
} from "@/components/features/issues/issue-card-editors"
import type {
  CodeLinkAttachResult,
  CodeLinkEdit,
} from "@/components/features/issues/issue-code-links-card"
import { codeLinkProblemMessage } from "@/lib/code-links"
import type { MentionAgent } from "@/lib/mentions"
import type {
  IssueActivity,
  IssueCodeLink,
  IssueComment,
  IssueLabel,
  IssueRelation,
  Milestone,
  Mission,
  Project,
  RelationType,
} from "@/lib/types/mission"

interface Props {
  workspaceId: string
  /** The issue's identifier — ENG-4. What both URLs carry. */
  identifier: string
  /**
   * Whether the reader may write. False renders the same card read-only
   * rather than a different, smaller one.
   */
  editable?: boolean
  /** Initial for the comment composer's author bubble. */
  viewerInitial?: string
  /** The host's list needs to know when a write landed. */
  onChanged?: () => void
  /** Called once when the identifier resolves to nothing. */
  onNotFound?: () => void
}

/** The workspace lists the pickers choose from. Fetched once per workspace. */
interface Roster {
  agents: MentionAgent[]
  labels: IssueLabel[]
  projects: Project[]
}

const EMPTY_ROSTER: Roster = { agents: [], labels: [], projects: [] }

export function IssueDetailSurface({
  workspaceId,
  identifier,
  editable = true,
  viewerInitial,
  onChanged,
  onNotFound,
}: Props) {
  const [issue, setIssue] = React.useState<Mission | null>(null)
  const [loading, setLoading] = React.useState(true)
  const [error, setError] = React.useState<string | null>(null)

  const [comments, setComments] = React.useState<IssueComment[]>([])
  const [activities, setActivities] = React.useState<IssueActivity[]>([])
  const [relations, setRelations] = React.useState<IssueRelation[]>([])
  const [runs, setRuns] = React.useState<IssueRun[]>([])
  const [subIssues, setSubIssues] = React.useState<Mission[]>([])
  const [codeLinks, setCodeLinks] = React.useState<IssueCodeLink[]>([])

  const [roster, setRoster] = React.useState<Roster>(EMPTY_ROSTER)
  const [milestones, setMilestones] = React.useState<Milestone[]>([])
  const [busy, setBusy] = React.useState(false)

  const { pipelines } = usePipelines(workspaceId)
  // The workspace's rules, narrowed to this issue inside the card — the
  // predicate belongs next to the type it reads, not here.
  const { automations } = useAutomations(workspaceId)
  // Recent runs of the routine bound to this issue, purely so the card can say
  // HOW they started. Null slug short-circuits inside the hook, so an issue
  // with nothing bound makes no request.
  const { records: routineRuns } = usePipelineRunRecords(workspaceId, issue?.routine_slug ?? null)

  const crewId = issue?.crew_id
  const projectId = issue?.project_id
  const qs = `workspace_id=${encodeURIComponent(workspaceId)}`
  const base = crewId
    ? `/api/v1/crews/${encodeURIComponent(crewId)}/issues/${encodeURIComponent(identifier)}`
    : null

  /* ---------------------------------------------------------------- *
   *  Reads                                                            *
   * ---------------------------------------------------------------- */

  // Sequencing guards, one per fetch group. Switching issues is a click in
  // the explorer or an arrow key on the board, and each switch is five
  // parallel requests deep — so a response for the issue you have already
  // left arriving after the one you are looking at is the ordinary case, not
  // an exotic one. Without these it wins, and you read ENG-1's comments under
  // ENG-2's title. `useIssueDetail` carried the same guards before this
  // component took the work off it.
  const issueReq = React.useRef(0)
  const subReq = React.useRef(0)

  const fetchIssue = React.useCallback(async () => {
    if (!workspaceId || !identifier) return
    const mine = ++issueReq.current
    try {
      const res = await apiFetch(`/api/v1/issues/${encodeURIComponent(identifier)}?${qs}`)
      if (mine !== issueReq.current) return
      if (!res.ok) {
        setError(res.status === 404 ? "Issue not found" : "Failed to load issue")
        onNotFound?.()
        return
      }
      const body = await res.json()
      // Re-checked after the parse too. The headers can arrive before the
      // reader navigates and the body after — the check above is at the
      // wrong await for that, and it is the longer of the two gaps.
      if (mine !== issueReq.current) return
      setIssue(body)
      setError(null)
    } catch {
      if (mine !== issueReq.current) return
      setError("Failed to load issue")
    } finally {
      // Not in the stale arm: clearing `loading` for an abandoned request
      // ends the skeleton for an issue that has not arrived, and the error
      // branch then renders "Issue not found" over an issue that exists.
      if (mine === issueReq.current) setLoading(false)
    }
  }, [workspaceId, identifier, qs, onNotFound])

  // The issue's own sub-resources. Grouped because they all key on the same
  // crew + identifier and all go stale together on any write.
  const fetchSubResources = React.useCallback(async () => {
    if (!base) return
    const mine = ++subReq.current
    const get = (path: string) =>
      apiFetch(`${base}/${path}?${qs}`)
        .then((r) => (r.ok ? r.json() : []))
        .catch(() => [])
    const [cs, as, rs, rn, sub, cl] = await Promise.all([
      get("comments"),
      get("activity"),
      get("relations"),
      get("runs"),
      get("subtasks"),
      // The pull requests attached to this issue. Same crew + identifier as
      // its siblings, and it goes stale on the same writes.
      get("code-links"),
    ])
    if (mine !== subReq.current) return
    setComments(Array.isArray(cs) ? cs : [])
    setActivities(Array.isArray(as) ? as : [])
    setRelations(Array.isArray(rs) ? rs : [])
    setRuns(Array.isArray(rn) ? rn : [])
    setSubIssues(Array.isArray(sub) ? sub : (sub?.subtasks ?? []))
    setCodeLinks(Array.isArray(cl) ? cl : [])
  }, [base, qs])

  React.useEffect(() => {
    // A new identifier is a different issue: clear first, or the previous
    // one's comments render under this one's title while the fetch is out.
    // The sub-resource guard is bumped here rather than only inside its own
    // fetch, because the previous issue's five requests are already in the
    // air and nothing will start a new group until the new crew_id resolves.
    subReq.current++
    setIssue(null)
    setComments([])
    setActivities([])
    setRelations([])
    setRuns([])
    setSubIssues([])
    setCodeLinks([])
    setMilestones([])
    setLoading(true)
    setError(null)
    void fetchIssue()
    // fetchIssue already depends on workspaceId + identifier.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [workspaceId, identifier])

  React.useEffect(() => {
    void fetchSubResources()
  }, [fetchSubResources])

  React.useEffect(() => {
    if (!workspaceId) return
    let cancelled = false
    const get = (path: string) =>
      apiFetch(`/api/v1/${path}?${qs}`)
        .then((r) => (r.ok ? r.json() : []))
        .catch(() => [])
    Promise.all([get("agents"), get("labels"), get("projects")]).then(([a, l, p]) => {
      if (cancelled) return
      setRoster({
        // The roster is workspace-wide on purpose. It feeds the assignee
        // picker AND the @-mention directory, and the properties panel used
        // to narrow it to the issue's crew — which quietly made half the
        // workspace unassignable from that screen while the other screen
        // offered everybody.
        agents: Array.isArray(a) ? a : (a?.agents ?? []),
        labels: Array.isArray(l) ? l : [],
        projects: Array.isArray(p) ? p : [],
      })
    })
    return () => {
      cancelled = true
    }
  }, [workspaceId, qs])

  // Milestones belong to the project, so they reset with it — carrying the
  // previous project's list would offer a milestone this issue cannot hold.
  React.useEffect(() => {
    setMilestones([])
    if (!projectId || !workspaceId) return
    let cancelled = false
    apiFetch(`/api/v1/projects/${encodeURIComponent(projectId)}/milestones?${qs}`)
      .then((r) => (r.ok ? r.json() : []))
      .then((d) => !cancelled && setMilestones(Array.isArray(d) ? d : (d?.milestones ?? [])))
      .catch(() => {})
    return () => {
      cancelled = true
    }
  }, [projectId, workspaceId, qs])

  useRealtimeEvent(
    "mission.updated",
    React.useCallback(
      (payload: unknown) => {
        const id = (payload as { id?: string } | null)?.id
        if (id && issue?.id && id !== issue.id) return
        void fetchIssue()
        void fetchSubResources()
      },
      [fetchIssue, fetchSubResources, issue?.id],
    ),
  )

  /* ---------------------------------------------------------------- *
   *  Writes                                                           *
   * ---------------------------------------------------------------- */

  const refresh = React.useCallback(async () => {
    await fetchIssue()
    await fetchSubResources()
    onChanged?.()
  }, [fetchIssue, fetchSubResources, onChanged])

  const patch = React.useCallback(
    async (body: Record<string, unknown>): Promise<boolean> => {
      if (!base) return false
      setBusy(true)
      try {
        const res = await apiFetch(`${base}?${qs}`, {
          method: "PATCH",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify(body),
        })
        if (!res.ok) {
          const b = await res.json().catch(() => null)
          toast.error(b?.detail ?? "Failed to update issue")
          return false
        }
        await refresh()
        return true
      } catch {
        toast.error("Failed to update issue")
        return false
      } finally {
        setBusy(false)
      }
    },
    [base, qs, refresh],
  )

  const postComment = React.useCallback(
    async (body: string): Promise<boolean> => {
      if (!base) return false
      try {
        const res = await apiFetch(`${base}/comments?${qs}`, {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ body }),
        })
        if (!res.ok) {
          const b = await res.json().catch(() => null)
          toast.error(b?.detail ?? "Failed to add comment")
          return false
        }
        // Re-read rather than append: the server owns the id, the timestamp
        // and — once mentions are parsed there — what the body became.
        const fresh = await apiFetch(`${base}/comments?${qs}`).then((r) => (r.ok ? r.json() : null))
        if (Array.isArray(fresh)) setComments(fresh)
        return true
      } catch {
        toast.error("Failed to add comment")
        return false
      }
    },
    [base, qs],
  )

  const createLabel = React.useCallback(
    async (name: string) => {
      try {
        // color is required by POST /labels — a create without one 400s.
        const color =
          LABEL_PRESET_COLORS[Math.floor(Math.random() * LABEL_PRESET_COLORS.length)].value
        const res = await apiFetch(`/api/v1/labels?${qs}`, {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ name, color }),
        })
        if (!res.ok) {
          toast.error("Failed to create label")
          return
        }
        const created: IssueLabel = await res.json()
        setRoster((r) => ({ ...r, labels: [...r.labels, created] }))
        await patch({ labels: [...(issue?.labels ?? []).map((l) => l.id), created.id] })
      } catch {
        toast.error("Failed to create label")
      }
    },
    [qs, patch, issue?.labels],
  )

  const addRelation = React.useCallback(
    async (target: string, type: RelationType): Promise<boolean> => {
      if (!base) return false
      try {
        const res = await apiFetch(`${base}/relations?${qs}`, {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ target_identifier: target, relation_type: type }),
        })
        if (!res.ok) {
          const b = await res.json().catch(() => null)
          toast.error(b?.detail ?? "Failed to add link")
          return false
        }
        await fetchSubResources()
        return true
      } catch {
        toast.error("Failed to add link")
        return false
      }
    },
    [base, qs, fetchSubResources],
  )

  const removeRelation = React.useCallback(
    async (relationId: string) => {
      try {
        const res = await apiFetch(`/api/v1/relations/${encodeURIComponent(relationId)}?${qs}`, {
          method: "DELETE",
        })
        if (!res.ok) {
          toast.error("Failed to remove link")
          return
        }
        await fetchSubResources()
      } catch {
        toast.error("Failed to remove link")
      }
    },
    [qs, fetchSubResources],
  )

  /* ---- git links ------------------------------------------------- *
   *
   * The four routes in internal/api/issue_code_links.go. They answer RFC 7807
   * with a `detail` written for the moment it is read — the 412 names the
   * credential to add and the account label to put on it, which is the whole
   * fix — so every branch below carries that sentence through rather than
   * substituting one of ours. `codeLinkProblemMessage` is the one place that
   * decides what to do when a response carries none.
   */

  const attachCodeLink = React.useCallback(
    async (url: string): Promise<CodeLinkAttachResult> => {
      if (!base) return { ok: false, message: "This issue has no crew to attach a link to." }
      try {
        const res = await apiFetch(`${base}/code-links?${qs}`, {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ url }),
        })
        if (!res.ok) {
          const b = await res.json().catch(() => null)
          return {
            ok: false,
            message: codeLinkProblemMessage(b, "Could not attach that pull request."),
          }
        }
        await fetchSubResources()
        return { ok: true }
      } catch {
        return { ok: false, message: "Could not reach the server." }
      }
    },
    [base, qs, fetchSubResources],
  )

  const removeCodeLink = React.useCallback(
    async (linkId: string) => {
      if (!base) return
      try {
        const res = await apiFetch(
          `${base}/code-links/${encodeURIComponent(linkId)}?${qs}`,
          { method: "DELETE" },
        )
        if (!res.ok) {
          const b = await res.json().catch(() => null)
          toast.error(codeLinkProblemMessage(b, "Could not remove that link."))
          return
        }
        await fetchSubResources()
      } catch {
        toast.error("Could not remove that link.")
      }
    },
    [base, qs, fetchSubResources],
  )

  const refreshCodeLink = React.useCallback(
    async (linkId: string) => {
      if (!base) return
      try {
        const res = await apiFetch(
          `${base}/code-links/${encodeURIComponent(linkId)}/refresh?${qs}`,
          { method: "POST" },
        )
        if (!res.ok) {
          const b = await res.json().catch(() => null)
          toast.error(codeLinkProblemMessage(b, "Could not refresh that link."))
        }
        // Re-read either way. A failed refresh keeps the state it had and
        // records the reason on the row (last_sync_error), and re-reading is
        // what puts that on the card — otherwise the only trace is a toast
        // that is gone in five seconds.
        await fetchSubResources()
      } catch {
        toast.error("Could not refresh that link.")
      }
    },
    [base, qs, fetchSubResources],
  )

  const runRoutine = React.useCallback(async () => {
    if (!issue?.routine_slug) return
    try {
      const res = await apiFetch(
        `/api/v1/workspaces/${encodeURIComponent(workspaceId)}/pipelines/${encodeURIComponent(issue.routine_slug)}/run`,
        {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({
            inputs: {},
            triggered_via: "issue",
            // The identifier, so /activity's Runs view can join the run back
            // to the issue that started it.
            triggered_by_id: issue.identifier ?? issue.id,
          }),
        },
      )
      if (!res.ok) {
        const b = await res.json().catch(() => null)
        toast.error(b?.detail ?? "Failed to start routine")
        return
      }
      toast.success(`Routine ${issue.routine_slug} started — see /activity`)
      await refresh()
    } catch {
      toast.error("Failed to start routine")
    }
  }, [issue?.routine_slug, issue?.identifier, issue?.id, workspaceId, refresh])

  const runWorkflow = React.useCallback(
    async (action: WorkflowAction, comment?: string) => {
      if (!base) return
      // Reopen is a status change, not a verb the server exposes — it was
      // implemented as a PATCH in the inline detail and has no endpoint.
      if (action === "reopen") {
        await patch({ status: "BACKLOG" })
        return
      }
      const review = action === "approve" || action === "request_changes"
      const url = review ? `${base}/review?${qs}` : `${base}/${action}?${qs}`
      setBusy(true)
      try {
        const res = await apiFetch(url, {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: review
            ? JSON.stringify({
                action: action === "approve" ? "approve" : "request_changes",
                ...(comment ? { comment } : {}),
              })
            : undefined,
        })
        if (!res.ok) {
          const b = await res.json().catch(() => null)
          toast.error(b?.detail ?? `Failed to ${action.replace("_", " ")} issue`)
          return
        }
        toast.success(
          action === "start"
            ? "Issue started"
            : action === "stop"
              ? "Issue stopped"
              : action === "approve"
                ? "Issue approved"
                : "Changes requested",
        )
        await refresh()
      } catch {
        toast.error(`Failed to ${action.replace("_", " ")} issue`)
      } finally {
        setBusy(false)
      }
    },
    [base, qs, patch, refresh],
  )

  /* ---------------------------------------------------------------- *
   *  Render                                                           *
   * ---------------------------------------------------------------- */

  const edit: IssueCardEdit | undefined = React.useMemo(() => {
    if (!editable || !issue) return undefined
    return {
      agents: roster.agents,
      labels: roster.labels,
      projects: roster.projects,
      routines: pipelines.map((p) => ({ id: p.id, name: p.name, slug: p.slug })),
      milestones,
      patch,
      createLabel,
      addRelation,
      removeRelation,
      runRoutine: issue.routine_slug ? runRoutine : undefined,
      busy,
    }
  }, [
    editable,
    issue,
    roster,
    pipelines,
    milestones,
    patch,
    createLabel,
    addRelation,
    removeRelation,
    runRoutine,
    busy,
  ])

  const codeLinkEdit: CodeLinkEdit | undefined = React.useMemo(
    () =>
      editable
        ? { attach: attachCodeLink, remove: removeCodeLink, refresh: refreshCodeLink }
        : undefined,
    [editable, attachCodeLink, removeCodeLink, refreshCodeLink],
  )

  if (loading) return <DetailSkeleton />

  if (error || !issue) {
    return (
      <div className="grid h-full place-items-center p-6 text-center">
        <p className="text-[13px] text-muted-foreground">{error ?? "Issue not found"}</p>
      </div>
    )
  }

  const project = roster.projects.find((p) => p.id === issue.project_id) ?? null

  return (
    <IssueCardDetail
      issue={issue}
      comments={comments}
      activities={activities}
      relations={relations}
      runs={runs}
      subIssues={subIssues}
      codeLinks={codeLinks}
      codeLinkEdit={codeLinkEdit}
      automations={automations}
      routineRuns={routineRuns}
      project={project}
      agents={roster.agents}
      onSubmitComment={editable ? postComment : undefined}
      viewerInitial={viewerInitial}
      edit={edit}
      actions={
        editable ? (
          <IssueWorkflowActions issue={issue} onAction={runWorkflow} busy={busy} />
        ) : undefined
      }
      runActivity={
        <RunActivityTimeline
          workspaceId={workspaceId}
          params={{ mission_id: issue.id, entry_type: RUN_WORK_ENTRY_TYPES.join(",") }}
          // Every other section of this page is a DetailCard; the timeline
          // defaults to a bare rail because the routine panel and the
          // activity bar want it that way, so this screen has to ask.
          card
          // An un-started issue has nothing to show and should stay clean;
          // once it is moving, keep the section up even before the first
          // entry lands so there is immediate "run starting…" feedback.
          hideWhenEmpty={issue.status === "BACKLOG" || issue.status === "TODO"}
          forceRunning={issue.status === "IN_PROGRESS"}
        />
      }
    />
  )
}

function DetailSkeleton() {
  return (
    <div className="flex flex-col gap-4 p-4">
      <Skeleton className="h-[132px] w-full rounded-xl" />
      <Skeleton className="h-[64px] w-full rounded-xl" />
      <div className="grid grid-cols-1 gap-4 xl:grid-cols-3 2xl:grid-cols-4">
        <Skeleton className="h-[320px] w-full rounded-xl xl:col-span-2 2xl:col-span-3" />
        <Skeleton className="h-[320px] w-full rounded-xl" />
      </div>
    </div>
  )
}
