"use client"

import { useId, useState } from "react"
import { SharedProjectMilestones } from "./shared-project-milestones"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { z } from "zod"
import { useSession } from "@/hooks/use-auth"
import { apiFetch } from "@/lib/api-fetch"
import { requireApiPathSegment } from "@/lib/api-path-segment"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Textarea } from "@/components/ui/textarea"

const schema = z.object({ id: z.string(), name: z.string(), description: z.string(), version: z.string().regex(/^[a-f0-9]{64}$/), can_manage: z.boolean() })
type Details = z.infer<typeof schema>
class ProjectReadError extends Error { constructor(readonly status: number) { super("Project details are unavailable. Refresh to check your access.") } }

export function SharedProjectDetails({ workspaceId, projects }: { workspaceId: string; projects: { id: string; name: string }[] }) {
  const { data: session } = useSession()
  const [selected, setSelected] = useState("")
  const id = useId()
  const visible = projects.some(project => project.id === selected)
  return <details className="rounded-lg border bg-card p-4"><summary className="cursor-pointer font-medium">Project details</summary>
    <div className="mt-3 space-y-3"><label htmlFor={id} className="block text-sm">Shared project</label><select id={id} className="min-h-10 w-full rounded border bg-background p-2 coarse:min-h-12" value={visible ? selected : ""} onChange={event => setSelected(event.target.value)}><option value="">Choose a project</option>{projects.map(project => <option key={project.id} value={project.id}>{project.name}</option>)}</select>
      {visible && session?.user.id && <ProjectDetails key={`${session.user.id}:${workspaceId}:${selected}`} workspaceId={workspaceId} projectId={selected} userId={session.user.id} />}
    </div>
  </details>
}

function ProjectDetails({ workspaceId, projectId, userId }: { workspaceId: string; projectId: string; userId: string }) {
  const client = useQueryClient()
  const key = ["restricted-project-details", userId, workspaceId, projectId]
  const path = `/api/v1/workspaces/${requireApiPathSegment(workspaceId)}/restricted-projects/${requireApiPathSegment(projectId)}`
  const query = useQuery({ queryKey: key, retry: false, refetchInterval: 15_000,
    queryFn: async ({ signal }) => {
      const response = await apiFetch(path, { signal })
      if (!response.ok) throw new ProjectReadError(response.status)
      const data = schema.parse(await response.json())
      if (data.id !== projectId) throw new Error("Project access could not be confirmed.")
      return data
    },
  })
  const lost = query.error instanceof ProjectReadError && [401, 403, 404].includes(query.error.status)
  return <section aria-label="Shared project details" className="space-y-3">
    <Button variant="outline" onClick={() => void query.refetch()}>Refresh project details</Button>
    {query.isPending && <p role="status">Loading project details…</p>}
    {query.isError && <p role="alert">{query.error.message}</p>}
    {!lost && query.data && (query.data.can_manage
      ? <ProjectDetailsForm value={query.data} path={path} disabled={query.isError || query.isFetching} onConflict={() => void query.refetch()} onSaved={value => { client.setQueryData(key, value); void client.invalidateQueries({ queryKey: ["restricted-issue-projects", userId, workspaceId] }); void client.invalidateQueries({ queryKey: ["restricted-issues", userId, workspaceId] }) }} />
      : <div><h3 className="font-medium">{query.data.name}</h3><p className="whitespace-pre-wrap text-sm">{query.data.description || "No project description."}</p></div>)}
    {!lost && query.data && <SharedProjectMilestones key={`${userId}:${workspaceId}:${projectId}`} workspaceId={workspaceId} projectId={projectId} userId={userId} />}
  </section>
}

function ProjectDetailsForm({ value, path, disabled, onSaved, onConflict }: { value: Details; path: string; disabled: boolean; onSaved: (value: Details) => void; onConflict: () => void }) {
  const id = useId()
  const [name, setName] = useState(value.name)
  const [description, setDescription] = useState(value.description)
  const [version, setVersion] = useState(value.version)
  const [conflict, setConflict] = useState(false)
  const changed = conflict || value.version !== version
  const save = useMutation({
    mutationFn: async () => {
      const response = await apiFetch(path, { method: "PUT", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ name, description, version }) })
      if (!response.ok) {
        if (response.status === 409) { setConflict(true); onConflict() }
        throw new Error(response.status === 409 ? "Project details changed. Your draft is kept; load current details before editing again." : "Project details were not saved. Your draft is kept.")
      }
      const data = schema.parse(await response.json())
      if (data.id !== value.id) throw new Error("Saved project could not be confirmed. Refresh before retrying.")
      return data
    },
    onSuccess: current => { setName(current.name); setDescription(current.description); setVersion(current.version); setConflict(false); onSaved(current) },
  })
  return <form className="space-y-3" onSubmit={event => { event.preventDefault(); if (!disabled && !changed && name.trim() && !save.isPending && value.can_manage) save.mutate() }}>
    <label htmlFor={`${id}-name`} className="block text-sm">Project name</label><Input id={`${id}-name`} value={name} maxLength={4096} disabled={save.isPending} onChange={event => setName(event.target.value)} required />
    <label htmlFor={`${id}-description`} className="block text-sm">Project description</label><Textarea id={`${id}-description`} value={description} maxLength={65536} disabled={save.isPending} onChange={event => setDescription(event.target.value)} />
    {changed && <div role="status"><p className="text-sm">Project details changed since this draft started. Load current details to review them before saving.</p><Button type="button" variant="outline" disabled={disabled || save.isPending} onClick={() => { setName(value.name); setDescription(value.description); setVersion(value.version); setConflict(false); save.reset() }}>Load current details</Button></div>}
    <Button type="submit" disabled={disabled || changed || !name.trim() || save.isPending}>{save.isPending ? "Saving…" : "Save project details"}</Button>
    {save.isError && <p role="alert">{save.error.message}</p>}
  </form>
}
