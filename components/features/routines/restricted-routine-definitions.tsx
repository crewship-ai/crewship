"use client"

import { useState } from "react"
import { useQuery } from "@tanstack/react-query"
import { z } from "zod"
import { useSession } from "@/hooks/use-auth"
import { apiFetch } from "@/lib/api-fetch"
import { requireApiPathSegment } from "@/lib/api-path-segment"
import { RoutineReadableSummary } from "./routine-readable-summary"
import { SharedRoutineDraft } from "./shared-routine-draft"
import { Button } from "@/components/ui/button"

const itemSchema = z.object({ id: z.string(), slug: z.string(), name: z.string(), status: z.string(), can_view: z.boolean(), can_edit: z.boolean().default(false), can_run: z.boolean(), runtime_ready: z.boolean() })
const definitionSchema = itemSchema.extend({ definition: z.record(z.string(), z.unknown()), definition_hash: z.string(), version: z.number().int() })

/** An explicit View share exposes the definition, independently of execution. */
export function RestrictedRoutineDefinitions({ workspaceId }: { workspaceId: string }) {
  const { data: session } = useSession()
  const userId = session?.user.id
  const [selected, setSelected] = useState("")
  const base = `/api/v1/workspaces/${requireApiPathSegment(workspaceId)}/restricted-routine-library`
  const library = useQuery({
    queryKey: ["restricted-routine-library", userId, workspaceId], enabled: !!userId, retry: false, refetchInterval: 15_000,
    queryFn: async ({ signal }) => {
      const response = await apiFetch(base, { signal })
      if ([401, 403, 404].includes(response.status)) return null
      if (!response.ok) throw new Error("Could not refresh shared definitions. Showing the last successful check.")
      return z.array(itemSchema).parse(await response.json())
    },
  })
  const visible = library.data?.filter(item => item.can_view) ?? []
  const allowed = visible.some(item => item.id === selected)
  const detail = useQuery({
    queryKey: ["restricted-routine-definition", userId, workspaceId, selected], enabled: !!userId && allowed,
    retry: false, refetchInterval: 15_000,
    queryFn: async ({ signal }) => {
      const response = await apiFetch(`${base}/${requireApiPathSegment(selected)}`, { signal })
      if ([401, 403, 404].includes(response.status)) return null
      if (!response.ok) throw new Error("Could not refresh this definition. Showing the last successful check.")
      const parsed = definitionSchema.parse(await response.json())
      if (parsed.id !== selected || !parsed.can_view) throw new Error("Definition access could not be confirmed.")
      return parsed
    },
  })
  if (library.isPending || (!library.isError && library.data !== null && visible.length === 0)) return null
  return <section className="space-y-3 rounded-lg border bg-card p-4" aria-label="Shared routine definitions">
    <h2 className="font-medium">Shared definitions</h2>
    <p className="text-sm text-muted-foreground">View lets you inspect a definition. It does not grant execution or access to another person’s private results.</p>
    {library.isError && <p role="status">{library.error.message}</p>}
    {library.data === null ? <p role="alert">Shared definitions are no longer available. Refresh to check your access.</p> : <label className="block space-y-2"><span>Routine definition</span><select aria-label="Routine definition" className="min-h-10 w-full rounded border bg-background p-2 coarse:min-h-12" value={allowed ? selected : ""} onChange={event => setSelected(event.target.value)}><option value="">Choose a definition</option>{visible.map(item => <option key={item.id} value={item.id}>{item.name}</option>)}</select></label>}
    <Button variant="outline" onClick={() => { void library.refetch(); if (allowed) void detail.refetch() }}>Refresh definitions</Button>
    {allowed && detail.isPending && <p role="status">Loading definition…</p>}
    {allowed && detail.data === null && <p role="alert">This definition is no longer available. Refresh to check your access.</p>}
    {allowed && detail.isError && <p role="alert">{detail.error.message}</p>}
    {allowed && detail.data && <div><p className="text-xs text-muted-foreground">Version {detail.data.version} · {detail.data.status}</p><RoutineReadableSummary definition={detail.data.definition} /><details><summary className="cursor-pointer text-sm">Definition source</summary><pre className="mt-2 max-h-96 overflow-auto whitespace-pre-wrap rounded bg-muted p-3 text-xs">{JSON.stringify(detail.data.definition, null, 2)}</pre></details>{detail.data.can_edit && visible.some(item => item.id === selected && item.can_edit) && userId && <SharedRoutineDraft key={`${workspaceId}:${userId}:${selected}`} url={`${base}/${requireApiPathSegment(selected)}/draft`} userId={userId} />}</div>}
  </section>
}
