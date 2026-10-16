"use client"

import { useState } from "react"
import { useQuery, useQueryClient } from "@tanstack/react-query"
import { z } from "zod"
import { Share2 } from "lucide-react"
import { useAbilities } from "@/hooks/use-abilities"
import { useAccessMode } from "@/hooks/use-access-mode"
import { apiFetch } from "@/lib/api-fetch"
import { requireApiPathSegment } from "@/lib/api-path-segment"
import { isResourceRight, resourcePolicySchema, resourceShareReplacement, shareOperations, type ResourcePolicy, type SharedResource } from "@/lib/resource-sharing"
import { Button } from "@/components/ui/button"
import { CreateSurface, CreateSurfaceBody, CreateSurfaceFooter, CreateSurfaceHeader } from "@/components/layout/create-surface"
import { ProjectGuestIntake } from "./project-guest-intake"

const rosterSchema = z.array(z.object({
  id: z.string().min(1), role: z.string(), access_mode: z.enum(["trusted", "restricted"]),
  user: z.object({ id: z.string(), full_name: z.string().nullable(), email: z.string() }),
}))
const policyUrl = (workspaceId: string, memberId: string) => `/api/v1/workspaces/${requireApiPathSegment(workspaceId)}/members/${requireApiPathSegment(memberId)}/access?workspace_id=${encodeURIComponent(workspaceId)}`

export function ResourceShareDialog({ workspaceId, resource }: { workspaceId: string; resource: SharedResource }) {
  const { role, loading } = useAbilities()
  const mode = useAccessMode()
  if (loading || mode !== "trusted" || (role !== "OWNER" && role !== "ADMIN") || !resource.id) return null
  return <ShareDialog key={`${workspaceId}:${resource.kind}:${resource.id}`} workspaceId={workspaceId} resource={resource} />
}

function ShareDialog({ workspaceId, resource }: { workspaceId: string; resource: SharedResource }) {
  const [open, setOpen] = useState(false)
  const [busyMember, setBusyMember] = useState<string | null>(null)
  const client = useQueryClient()
  const queryKey = ["object-resource-sharing", workspaceId, resource.kind, resource.id]
  const query = useQuery({
    queryKey, enabled: open, retry: false,
    // One snapshot per opening. Writes use its revision and never silently retry.
    staleTime: 0, refetchOnWindowFocus: false,
    queryFn: async ({ signal }) => {
      const response = await apiFetch(`/api/v1/workspaces/${requireApiPathSegment(workspaceId)}/members?workspace_id=${encodeURIComponent(workspaceId)}`, { signal })
      if (!response.ok) throw new Error("Sharing is unavailable. Reload to check your access.")
      const members = rosterSchema.parse(await response.json())
      const guests = members.filter(member => member.access_mode === "restricted")
      const policies = new Map<string, ResourcePolicy>()
      // Bound concurrent policy reads even in a large workspace.
      let next = 0
      await Promise.all(Array.from({ length: Math.min(4, guests.length) }, async () => {
        while (next < guests.length) {
          const member = guests[next++]
          const res = await apiFetch(policyUrl(workspaceId, member.id), { signal })
          if (!res.ok) throw new Error("Could not load all shares. Reload before editing.")
          const policy = resourcePolicySchema.parse(await res.json())
          if (policy.membership_id !== member.id || policy.mode !== "restricted") throw new Error("Membership changed. Reload before editing.")
          policies.set(member.id, policy)
        }
      }))
      return { members, policies }
    },
  })
  const requestOpen = (next: boolean) => { if (!busyMember) setOpen(next) }
  return <>
    <Button variant="outline" size="sm" className="coarse:h-12" aria-haspopup="dialog" aria-expanded={open} onClick={() => setOpen(true)}><Share2 className="mr-1.5 h-3.5 w-3.5" />Share</Button>
    <CreateSurface open={open} onOpenChange={requestOpen} size="md" restoreOpenerFocus>
      <CreateSurfaceHeader title={`Share ${resource.name}`} onClose={() => requestOpen(false)} description={<>
        {resource.kind === "routine" ? "View shares the stored definition, including its prompts and defaults, without permission to execute. View runs and outputs additionally shares past and future routine run history and outputs; inherited private conversation audiences still apply. Inputs are not shown directly; outputs may contain them. It does not share transcripts or execution authority. Edit draft additionally shares the collaborative unpublished draft and permits changes with View. It does not publish or grant execution. Existing Run shares do not automatically expose the definition. Run uses the routine’s configured credentials and also requires access to every agent it uses." : "List shows the project in Issues. Read opens its issues and explicitly shared comments. Contribute also permits requests, shared comments and arranging intake work; it requires Read. Manage with Read explicitly shares the project description and permits editing its name, description and milestones. Only empty milestones can be removed. Delete issues also requires Read and Manage, and explicitly permits permanent removal of Backlog or Cancelled issues without active work or sub-issues."}
      </>} />
      <CreateSurfaceBody className="space-y-4">
      <p className="text-sm text-muted-foreground">Trusted members use their workspace role. Individual shares apply to Guest accounts; changing account mode is managed in Settings.</p>
      {open && resource.kind === "project" && <ProjectGuestIntake workspaceId={workspaceId} projectId={resource.id} disabled={busyMember !== null} onBusy={busy => setBusyMember(busy ? "__intake__" : null)} />}
      {query.isPending && <p role="status">Loading shares…</p>}
      {query.isError && <div role="alert"><p>{query.error.message}</p></div>}
      {!query.isError && query.data && <>
        {query.data.members.filter(member => member.access_mode === "trusted").length > 0 && <details><summary>Trusted workspace members</summary><ul>{query.data.members.filter(member => member.access_mode === "trusted").map(member => <li key={member.id}>{member.user.full_name || member.user.email} · {member.role}</li>)}</ul></details>}
        {query.data.policies.size === 0 && <p>No Guest members. Invite a Guest from Settings to share individual resources.</p>}
        {query.data.members.filter(member => member.access_mode === "restricted").map(member => {
          const policy = query.data.policies.get(member.id)!
          return <ShareMember key={`${member.id}:${policy.revision}`} workspaceId={workspaceId} resource={resource} policy={policy}
            label={member.user.full_name || member.user.email} disabled={busyMember !== null && busyMember !== member.id} onBusy={busy => setBusyMember(busy ? member.id : null)}
            onSaved={updated => client.setQueryData<typeof query.data>(queryKey, current => current ? { ...current, policies: new Map(current.policies).set(member.id, updated) } : current)} />
        })}
      </>}
      <Button variant="outline" disabled={busyMember !== null} onClick={() => void query.refetch()}>Reload shares</Button>
      <p className="text-xs text-muted-foreground">Reloading discards edits for members whose saved access changed.</p>
      </CreateSurfaceBody>
      <CreateSurfaceFooter hint={null} onCancel={() => requestOpen(false)} cancelLabel="Close" busy={busyMember !== null} />
    </CreateSurface>
  </>
}

function ShareMember({ workspaceId, resource, policy, label, onSaved, disabled, onBusy }: {
  workspaceId: string; resource: SharedResource; policy: ResourcePolicy; label: string; onSaved: (policy: ResourcePolicy) => void; disabled: boolean; onBusy: (busy: boolean) => void
}) {
  const client = useQueryClient()
  const original = policy.rights.filter(right => isResourceRight(right, resource)).map(right => right.operation)
  const [selected, setSelected] = useState(original)
  const [reissue, setReissue] = useState<string[]>([])
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState("")
  const [conflict, setConflict] = useState(false)
  const changed = shareOperations[resource.kind].some(operation => selected.includes(operation) !== original.includes(operation)) || reissue.length > 0
  async function save() {
    setSaving(true); onBusy(true); setError("")
    try {
      const response = await apiFetch(policyUrl(workspaceId, policy.membership_id), { method: "PUT", headers: { "Content-Type": "application/json" }, body: JSON.stringify(resourceShareReplacement(policy, resource, selected.filter(op => (shareOperations[resource.kind] as readonly string[]).includes(op)), reissue)) })
      if (response.status === 409) { setConflict(true); throw new Error("Access changed. Reload shares and review the current policy before saving.") }
      if (!response.ok) throw new Error("Sharing was not saved. Your edits are kept.")
      const updated = resourcePolicySchema.parse(await response.json())
      if (updated.membership_id !== policy.membership_id) throw new Error("Membership changed. Reload shares.")
      client.setQueryData(["member-resource-access", workspaceId, policy.membership_id], updated)
      await client.invalidateQueries({ queryKey: ["own-access", workspaceId] })
      await client.invalidateQueries({ queryKey: ["object-resource-sharing", workspaceId], refetchType: "none" })
      onSaved(updated)
    } catch (err) { setError(err instanceof Error ? err.message : "Sharing was not saved") }
    finally { setSaving(false); onBusy(false) }
  }
  return <fieldset disabled={saving || conflict || disabled} className="space-y-2 rounded-lg border p-3">
    <legend className="px-1 text-sm font-medium">{label}</legend>
    {shareOperations[resource.kind].map(operation => {
      const detail = policy.grant_details?.find(right => isResourceRight(right, resource) && right.operation === operation)
      return <div key={operation} className="space-y-1">
        <label className="flex min-h-9 items-center gap-2 coarse:min-h-12"><input type="checkbox" checked={selected.includes(operation)} onChange={event => {
          setSelected(event.target.checked ? [...selected, operation] : selected.filter(op => op !== operation))
          if (!event.target.checked) setReissue(reissue.filter(op => op !== operation))
        }} />{operation === "results" ? "View runs and outputs" : operation === "view" ? "View definition" : operation === "edit" ? "Edit draft" : operation === "run" ? "Run" : operation === "list" ? "List in Issues" : operation === "contribute" ? "Contribute to issues" : operation === "delete_issues" ? "Delete issues" : operation === "manage" ? "Manage project" : "Read issues"}</label>
        {detail && <p className="text-xs text-muted-foreground">{detail.live ? "Active" : "Inactive — issuer no longer has authority"} · Issued by {detail.issuer_name || detail.issuer_id}</p>}
        {detail && !detail.live && selected.includes(operation) && <label className="flex min-h-9 items-center gap-2 text-xs coarse:min-h-12"><input type="checkbox" checked={reissue.includes(operation)} onChange={event => setReissue(event.target.checked ? [...reissue, operation] : reissue.filter(op => op !== operation))} />Reissue {operation} as me</label>}
      </div>
    })}
    <Button variant="outline" size="sm" className="coarse:h-12" disabled={!changed || saving || conflict} onClick={() => void save()}>{saving ? "Saving…" : `Save access for ${label}`}</Button>
    {error && <p role="alert" className="text-sm">{error}</p>}
  </fieldset>
}
