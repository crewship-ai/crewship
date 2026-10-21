"use client"

import { useId, useState } from "react"
import { useQuery, useQueryClient } from "@tanstack/react-query"
import { z } from "zod"
import { apiFetch } from "@/lib/api-fetch"
import { requireApiPathSegment } from "@/lib/api-path-segment"
import { Button } from "@/components/ui/button"

const intakeSchema = z.object({ project_id: z.string(), crew_id: z.string(), crew_name: z.string(), revision: z.number().int().nonnegative() })
const crewsSchema = z.array(z.object({ id: z.string(), name: z.string() }))

/** Administrator-owned routing; it confers no agent execution authority. */
export function ProjectGuestIntake({ workspaceId, projectId, disabled, onBusy }: { workspaceId: string; projectId: string; disabled: boolean; onBusy: (busy: boolean) => void }) {
  const path = `/api/v1/workspaces/${requireApiPathSegment(workspaceId)}/projects/${requireApiPathSegment(projectId)}/guest-intake`
  const query = useQuery({
    queryKey: ["project-guest-intake", workspaceId, projectId], retry: false, refetchOnWindowFocus: false,
    queryFn: async ({ signal }) => {
      const [intakeResponse, crewsResponse] = await Promise.all([apiFetch(path, { signal }), apiFetch(`/api/v1/crews?workspace_id=${encodeURIComponent(workspaceId)}`, { signal })])
      if (!intakeResponse.ok || !crewsResponse.ok) throw new Error("Could not load issue intake. Reload before editing.")
      const parsedIntake = intakeSchema.safeParse(await intakeResponse.json())
      const parsedCrews = crewsSchema.safeParse(await crewsResponse.json())
      if (!parsedIntake.success || !parsedCrews.success) throw new Error("Could not confirm issue intake. Reload before editing.")
      if (parsedIntake.data.project_id !== projectId) throw new Error("Project changed. Reload before editing.")
      return { intake: parsedIntake.data, crews: parsedCrews.data }
    },
  })
  return <section className="space-y-2 rounded-lg border p-3" aria-label="Guest issue intake">
    <h3 className="text-sm font-medium">Where client requests arrive</h3>
    <p className="text-xs text-muted-foreground">Choose the crew for new Guest issues in this project. Requests start unassigned in Backlog; this does not let Guests run agents or routines.</p>
    {query.isPending && <p role="status">Loading issue intake…</p>}
    {query.isError && <p role="alert" className="text-sm">{query.error.message}</p>}
    {!query.isError && query.data && <IntakeEditor key={query.data.intake.revision} path={path} data={query.data} disabled={disabled} onBusy={onBusy} workspaceId={workspaceId} projectId={projectId} />}
    <Button variant="outline" size="sm" disabled={disabled} onClick={() => void query.refetch()}>Reload intake</Button>
  </section>
}

function IntakeEditor({ path, data, disabled, onBusy, workspaceId, projectId }: { path: string; data: { intake: z.infer<typeof intakeSchema>; crews: z.infer<typeof crewsSchema> }; disabled: boolean; onBusy: (busy: boolean) => void; workspaceId: string; projectId: string }) {
  const id = useId()
  const client = useQueryClient()
  const [crewId, setCrewId] = useState(data.intake.crew_id)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState("")
  const [conflict, setConflict] = useState(false)
  async function save() {
    setSaving(true); onBusy(true); setError("")
    try {
      const response = await apiFetch(path, { method: "PUT", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ crew_id: crewId, revision: data.intake.revision }) })
      if (response.status === 409) { setConflict(true); throw new Error("Issue intake changed. Reload intake before saving.") }
      if (!response.ok) { const body = await response.json().catch(() => null); throw new Error(typeof body?.error === "string" ? body.error : "Issue intake was not saved. Your choice is kept.") }
      const parsed = intakeSchema.safeParse(await response.json())
      if (!parsed.success) { setConflict(true); throw new Error("Could not confirm the saved intake. Reload before editing again.") }
      const intake = parsed.data
      if (intake.project_id !== projectId || intake.revision <= data.intake.revision) throw new Error("Project changed. Reload intake.")
      client.setQueryData(["project-guest-intake", workspaceId, projectId], { ...data, intake })
    } catch (err) { setError(err instanceof Error ? err.message : "Issue intake was not saved") }
    finally { setSaving(false); onBusy(false) }
  }
  return <div className="space-y-2"><label htmlFor={id} className="text-sm">Issue intake crew</label>
    <select id={id} className="min-h-9 w-full rounded-md border bg-background px-3 coarse:min-h-[44px]" value={crewId} disabled={disabled || saving || conflict} onChange={event => setCrewId(event.target.value)}><option value="">Choose a crew</option>{data.crews.map(crew => <option key={crew.id} value={crew.id}>{crew.name}</option>)}</select>
    <Button size="sm" variant="outline" disabled={disabled || saving || conflict || !crewId || crewId === data.intake.crew_id} onClick={() => void save()}>{saving ? "Saving…" : "Save intake crew"}</Button>
    {error && <p role="alert" className="text-sm">{error}</p>}
  </div>
}
