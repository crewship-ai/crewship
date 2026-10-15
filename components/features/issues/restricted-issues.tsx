"use client"

import Link from "next/link"
import { SharedProjectDetails } from "./shared-project-details"
import { useId, useRef, useState } from "react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { z } from "zod"
import { apiFetch } from "@/lib/api-fetch"
import { requireApiPathSegment } from "@/lib/api-path-segment"
import { useWorkspace } from "@/hooks/use-workspace"
import { useSession } from "@/hooks/use-auth"
import { AccessNotice, useAccessContext } from "@/components/features/restricted/access-notice"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Textarea } from "@/components/ui/textarea"

const issueSchema = z.object({ id: z.string(), identifier: z.string(), title: z.string(), status: z.string(), project_id: z.string(), project_name: z.string(), can_read: z.boolean(), can_contribute: z.boolean().default(false), description: z.string().optional() })
const projectSchema = z.object({ id: z.string(), name: z.string(), can_contribute: z.boolean(), intake_ready: z.boolean(), can_manage: z.boolean().default(false) })
const commentSchema = z.object({ id: z.string(), body: z.string(), author: z.string(), created_at: z.string() })
type Issue = z.infer<typeof issueSchema>
type Project = z.infer<typeof projectSchema>

class IssueReadError extends Error {
  constructor(readonly status: number, message: string) { super(message) }
}
const accessLost = (error: unknown) => error instanceof IssueReadError && [401, 403, 404].includes(error.status)

async function issueRequest(path: string, method: string, body: unknown, key: string) {
  const response = await apiFetch(path, { method, headers: { "Content-Type": "application/json", "Idempotency-Key": key }, body: JSON.stringify(body) })
  if (!response.ok) {
    const detail = await response.json().catch(() => null)
    throw new Error(response.status === 404 ? "Your project access changed. Ask your administrator to check it." : typeof detail?.error === "string" ? detail.error : "Your change was not saved. Your draft is kept.")
  }
  return response.json()
}

// Keep an uncertain submission's key for explicit retries. Clear it only
// after success; a new payload or audience is a new operation.
function useIssueRequest() {
  const pending = useRef<{ fingerprint: string; key: string } | null>(null)
  return {
    send: (path: string, method: string, body: unknown) => {
      const fingerprint = JSON.stringify([path, method, body])
      if (pending.current?.fingerprint !== fingerprint) pending.current = { fingerprint, key: crypto.randomUUID() }
      return issueRequest(path, method, body, pending.current.key)
    },
    clear: () => { pending.current = null },
  }
}

/** Guests use dedicated project projections and never mount the trusted board. */
export function RestrictedIssues({ identifier }: { identifier?: string | null }) {
  const { workspaceId } = useWorkspace()
  const { data: session } = useSession()
  const context = useAccessContext()
  const [offset, setOffset] = useState(0)
  const base = workspaceId ? `/api/v1/workspaces/${requireApiPathSegment(workspaceId)}/restricted-issues` : ""
  const query = useQuery({
    queryKey: ["restricted-issues", session?.user.id, workspaceId, identifier, offset],
    enabled: !!workspaceId && !!session?.user.id, retry: false, refetchInterval: 15_000,
    queryFn: async ({ signal }) => {
      const path = identifier ? `${base}/${requireApiPathSegment(identifier)}` : `${base}?limit=100&offset=${offset}`
      const response = await apiFetch(path, { signal })
      if (!response.ok) throw new IssueReadError(response.status, response.status === 404 ? "Access unavailable" : "Issues unavailable")
      const data = await response.json()
      return identifier ? [issueSchema.parse(data)] : z.array(issueSchema).parse(data)
    },
  })
  const projects = useQuery({
    queryKey: ["restricted-issue-projects", session?.user.id, workspaceId],
    enabled: !!base && !!session?.user.id && !identifier, retry: false, refetchInterval: 15_000,
    queryFn: async ({ signal }) => {
      const response = await apiFetch(`/api/v1/workspaces/${requireApiPathSegment(workspaceId!)}/restricted-issue-projects`, { signal })
      if (!response.ok) throw new IssueReadError(response.status, "Project access is unavailable")
      return z.array(projectSchema).parse(await response.json())
    },
  })
  return <main className="mx-auto w-full max-w-4xl space-y-4 p-4 md:p-6">
    <div className="flex items-center justify-between"><h1 className="text-lg font-semibold">{identifier ? "Issue" : "Issues shared with you"}</h1>{identifier ? <Link href="/issues" className="text-sm text-primary">Back to issues</Link> : <Button variant="outline" onClick={() => { void query.refetch(); void projects.refetch() }}>Refresh</Button>}</div>
    <p className="text-sm text-muted-foreground">Your project grants decide which issues you can list, read and contribute to. Only comments explicitly shared with this project appear here.</p>
    {!identifier && !accessLost(projects.error) && workspaceId && projects.data?.some(project => project.can_manage) && <SharedProjectDetails key={`${session?.user.id}:${workspaceId}`} workspaceId={workspaceId} projects={projects.data.filter(project => project.can_manage)} />}
    {!identifier && !accessLost(projects.error) && projects.data?.some(project => project.can_contribute) && <NewIssue key={`${session?.user.id}:${workspaceId}`} base={base} projects={projects.data} disabled={projects.isError} />}
    {query.isPending && <p role="status">Loading shared issues…</p>}
    {query.isError && (!query.data || accessLost(query.error)) && <AccessNotice kind={accessLost(query.error) ? "no-access" : "unavailable"} title="Issues unavailable" description="Your access may have changed. Retry to check your current project grants." context={context} current="issues" onRetry={() => void query.refetch()} />}
    {(!query.isError || !accessLost(query.error)) && query.data?.length === 0 && <AccessNotice kind="no-access" title="No issues shared here" description="There are no issues in the projects shared with you on this page." context={context} current="issues" />}
    {query.isError && query.data && !accessLost(query.error) && <p role="status" className="text-sm text-muted-foreground">Issues could not be refreshed. Your current view and drafts are kept; refresh before posting.</p>}
    {(!query.isError || !accessLost(query.error)) && query.data?.map(issue => <IssueCard key={`${session?.user.id}:${workspaceId}:${issue.id}`} issue={issue} detail={!!identifier} mutationsDisabled={query.isError} base={base} userId={session?.user.id} />)}
    {!identifier && !query.isError && <div className="flex gap-2">
      <Button variant="outline" disabled={offset === 0} onClick={() => setOffset(Math.max(0, offset - 100))}>Previous</Button>
      <Button variant="outline" disabled={(query.data?.length ?? 0) < 100} onClick={() => setOffset(offset + 100)}>Next</Button>
    </div>}
  </main>
}

function NewIssue({ base, projects, disabled }: { base: string; projects: Project[]; disabled: boolean }) {
  const id = useId()
  const client = useQueryClient()
  const [projectId, setProjectId] = useState("")
  const [title, setTitle] = useState("")
  const [description, setDescription] = useState("")
  const request = useIssueRequest()
  const eligible = projects.filter(project => project.can_contribute)
  const selected = eligible.find(project => project.id === projectId)
  const save = useMutation({
    mutationFn: () => request.send(base, "POST", { project_id: projectId, title, description }),
    onSuccess: async () => { request.clear(); setTitle(""); setDescription(""); await client.invalidateQueries({ queryKey: ["restricted-issues"] }) },
  })
  return <details className="rounded-lg border bg-card p-4"><summary className="cursor-pointer font-medium">New issue</summary>
    <form className="mt-3 space-y-3" onSubmit={event => { event.preventDefault(); if (!disabled && selected?.intake_ready && title.trim() && !save.isPending) save.mutate() }}>
      <label htmlFor={`${id}-project`} className="block text-sm">Project</label>
      <select id={`${id}-project`} className="min-h-9 w-full rounded-md border bg-background px-3 coarse:min-h-12" value={projectId} disabled={disabled || save.isPending} onChange={event => setProjectId(event.target.value)}><option value="">Choose a project</option>{eligible.map(project => <option key={project.id} value={project.id}>{project.name}</option>)}</select>
      {selected && !selected.intake_ready && <p role="status" className="text-sm text-muted-foreground">Ask your administrator to configure this project’s issue intake crew.</p>}
      <label htmlFor={`${id}-title`} className="block text-sm">Title</label><Input id={`${id}-title`} value={title} maxLength={4096} disabled={save.isPending} onChange={event => setTitle(event.target.value)} required />
      <label htmlFor={`${id}-description`} className="block text-sm">Description</label><Textarea id={`${id}-description`} value={description} maxLength={65536} disabled={save.isPending} onChange={event => setDescription(event.target.value)} />
      <p className="text-xs text-muted-foreground">Creates an unassigned request in Backlog. An authorized workspace member can review it and start work.</p>
      <Button type="submit" disabled={disabled || !selected?.intake_ready || !title.trim() || save.isPending}>{save.isPending ? "Creating…" : "Create issue"}</Button>
      {save.isError && <p role="alert" className="text-sm">{save.error.message}</p>}
    </form>
  </details>
}

function IssueCard({ issue, detail, base, userId, mutationsDisabled }: { issue: Issue; detail: boolean; base: string; userId?: string; mutationsDisabled: boolean }) {
  const client = useQueryClient()
  const [draft, setDraft] = useState("")
  const [audience, setAudience] = useState<{ id: string; name: string } | null>(null)
  const audienceChanged = !!draft && audience?.id !== issue.project_id
  const commentRequest = useIssueRequest()
  const statusRequest = useIssueRequest()
  const commentId = useId()
  const path = `${base}/${requireApiPathSegment(issue.id)}`
  const comments = useQuery({
    queryKey: ["restricted-issue-comments", userId, base, issue.id, issue.project_id], enabled: detail && issue.can_read,
    retry: false, refetchInterval: 15_000,
    queryFn: async ({ signal }) => {
      const response = await apiFetch(`${path}/comments`, { signal })
      if (!response.ok) throw new IssueReadError(response.status, "Shared comments are unavailable. Refresh to check your access.")
      const parsed = z.array(commentSchema).safeParse(await response.json())
      if (!parsed.success) throw new Error("Shared comments could not be confirmed. Refresh to try again.")
      return parsed.data
    },
  })
  const send = useMutation({ mutationFn: () => commentRequest.send(`${path}/comments`, "POST", { body: draft, project_id: audience?.id }), onSuccess: async () => { commentRequest.clear(); setDraft(""); setAudience(null); await comments.refetch() } })
  const status = useMutation({ mutationFn: (next: string) => statusRequest.send(path, "PATCH", { status: next }), onSuccess: async () => { statusRequest.clear(); await client.invalidateQueries({ queryKey: ["restricted-issues"] }) } })
  return <article className="space-y-3 rounded-lg border bg-card p-4">
    <p className="text-xs text-muted-foreground">{issue.project_name} · {issue.identifier} · {issue.status}</p>
    <h2 className="font-medium">{!detail && issue.can_read ? <Link className="hover:underline" href={`/issues/${encodeURIComponent(issue.identifier)}`}>{issue.title}</Link> : issue.title}</h2>
    {detail && <p className="whitespace-pre-wrap text-sm">{issue.description || "No description."}</p>}
    {!detail && !issue.can_read && <p className="text-xs text-muted-foreground">Ask your administrator for project read access to open details.</p>}
    {detail && issue.can_contribute && ["BACKLOG", "TODO"].includes(issue.status) && <div className="flex gap-2"><Button variant="outline" disabled={status.isPending || mutationsDisabled} onClick={() => status.mutate(issue.status === "BACKLOG" ? "TODO" : "BACKLOG")}>{issue.status === "BACKLOG" ? "Move to To do" : "Move to Backlog"}</Button>{status.isError && <p role="alert" className="text-sm">{status.error.message}</p>}</div>}
    {detail && <section className="space-y-3 border-t pt-3" aria-label="Shared comments"><h3 className="text-sm font-medium">Shared comments</h3>
      {comments.isPending && <p role="status">Loading shared comments…</p>}
      {comments.isError && <div><p role="alert" className="text-sm">{comments.error.message}</p><Button variant="outline" onClick={() => void comments.refetch()}>Retry comments</Button></div>}
      {(!comments.isError || !accessLost(comments.error)) && comments.data?.map(comment => <div key={comment.id} className="rounded border p-3"><p className="text-xs text-muted-foreground">{comment.author}</p><p className="whitespace-pre-wrap text-sm">{comment.body}</p></div>)}
      {!comments.isError && comments.data?.length === 100 && <p className="text-xs text-muted-foreground">Showing the latest 100 shared comments.</p>}
      {!comments.isError && comments.data?.length === 0 && <p className="text-sm text-muted-foreground">No comments have been shared with this project.</p>}
      {issue.can_contribute && <form className="space-y-2" onSubmit={event => { event.preventDefault(); if (draft.trim() && !send.isPending && !audienceChanged && !mutationsDisabled && !comments.isError) send.mutate() }}><label htmlFor={commentId} className="text-sm">Comment shared with this project</label><Textarea id={commentId} value={draft} maxLength={32768} disabled={send.isPending} onChange={event => { if (!draft) setAudience({ id: issue.project_id, name: issue.project_name }); setDraft(event.target.value) }} required />{audienceChanged && <div role="alert" className="text-sm"><p>The project changed. Your draft was written for {audience?.name}. Review the new audience before posting.</p><Button type="button" variant="outline" onClick={() => setAudience({ id: issue.project_id, name: issue.project_name })}>Share this draft with {issue.project_name}</Button></div>}<Button type="submit" disabled={!draft.trim() || send.isPending || audienceChanged || mutationsDisabled || comments.isError}>{send.isPending ? "Posting…" : "Post comment"}</Button>{send.isError && <p role="alert" className="text-sm">{send.error.message}</p>}</form>}
    </section>}
  </article>
}
