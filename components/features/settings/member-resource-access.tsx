"use client"

import { useState } from "react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { z } from "zod"

import { Button } from "@/components/ui/button"
import { usePageSave } from "@/components/ui/page-save-bar"
import { requireApiPathSegment } from "@/lib/api-path-segment"
import { apiFetch } from "@/lib/api-fetch"
import { nativeSelect } from "@/components/features/settings/shared"

const rightSchema = z.object({ kind: z.enum(["agent", "project", "routine"]), id: z.string().min(1), operation: z.string() })
const policySchema = z.object({
  membership_id: z.string(), revision: z.number().int().positive(),
  mode: z.enum(["trusted", "restricted"]), rights: z.array(rightSchema),
  grant_details: z.array(rightSchema.extend({issuer_id:z.string(),issuer_name:z.string().optional(),created_at:z.string(),live:z.boolean()})).optional(),
})
type Policy = z.infer<typeof policySchema>
type Right = z.infer<typeof rightSchema>
const directorySchema = z.array(z.object({ id: z.string(), name: z.string() }))
const operations = { agent: ["discover", "chat", "run", "delegate"], project: ["list", "read", "write", "delete"], routine: ["run"] }

export function MemberResourceAccess({ workspaceId, memberId, label, role, onSaved }: {
  workspaceId: string; memberId: string; label: string; role: string
  /** A saved mode changes the roster's Restricted marker; reload it. */
  onSaved?: () => void
}) {
  const [open, setOpen] = useState(false)
  const url = `/api/v1/workspaces/${requireApiPathSegment(workspaceId)}/members/${requireApiPathSegment(memberId)}/access?workspace_id=${encodeURIComponent(workspaceId)}`
  const queryKey = ["member-resource-access", workspaceId, memberId]
  const policy = useQuery({
    queryKey, enabled: open, retry: false, staleTime: Infinity, refetchOnWindowFocus: false,
    queryFn: async ({ signal }) => {
      const response = await apiFetch(url, { signal })
      if (!response.ok) throw new Error("Resource policy unavailable")
      const parsed = policySchema.parse(await response.json())
      if (parsed.membership_id !== memberId) throw new Error("Membership changed; reload the roster")
      return parsed
    },
  })
  return <section className="mt-4 space-y-2" aria-label={`Resource access for ${label}`}>
    <Button variant="outline" className="coarse:h-12" onClick={() => setOpen(!open)} aria-expanded={open}>
      {open ? "Close resource access" : "Edit resource access"}
    </Button>
    {open && policy.isPending && <p className="text-xs">Loading resource access…</p>}
    {open && policy.isError && <p role="alert" className="text-xs">Resource policy unavailable. <button type="button" onClick={() => void policy.refetch()}>Reload policy</button></p>}
    {open && policy.data && <PolicyEditor key={`${workspaceId}:${memberId}:${policy.data.revision}`} policy={policy.data} url={url}
      queryKey={queryKey} workspaceId={workspaceId} role={role} label={label} onSaved={onSaved} />}
  </section>
}

function PolicyEditor({ policy, url, queryKey, workspaceId, role, label, onSaved }: {
  policy: Policy; url: string; queryKey: string[]; workspaceId: string; role: string; label: string
  onSaved?: () => void
}) {
  const queryClient = useQueryClient()
  const [mode, setMode] = useState(policy.mode)
  const [rights, setRights] = useState<Right[]>(policy.rights)
  const [reissue, setReissue] = useState<Right[]>([])
  const [kind, setKind] = useState<Right["kind"]>("agent")
  const [resourceId, setResourceId] = useState("")
  const [operation, setOperation] = useState("chat")
  const [conflict, setConflict] = useState(false)
  // Both directories load so every grant resolves its name by its own kind,
  // not by whichever kind the add-grant selector currently shows (#2863).
  const directories = {
    agent: useResourceDirectory(workspaceId, "agent", mode === "restricted"),
    project: useResourceDirectory(workspaceId, "project", mode === "restricted"),
    routine: useResourceDirectory(workspaceId, "routine", mode === "restricted"),
  }
  const directory = directories[kind]
  const save = useMutation({
    mutationFn: async () => {
      const response = await apiFetch(url, { method: "PUT", headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ membership_id: policy.membership_id, revision: policy.revision, mode, rights: mode === "trusted" ? [] : rights,
          ...(mode === "restricted" && reissue.length ? {reissue: reissue.filter(r => rights.some(right => rightKey(right) === rightKey(r)))} : {}) }) })
      if (response.status === 409) { setConflict(true); throw new Error("Policy changed. Reload the current policy before saving.") }
      if (!response.ok) throw new Error("Resource access was not saved")
      const updated = policySchema.parse(await response.json())
      if (updated.membership_id !== policy.membership_id) throw new Error("Membership changed; reload the roster")
      return updated
    },
    // The saved revision remounts this editor (its key), so the draft goes
    // clean. A failure rejects into the page bar, which toasts it.
    onSuccess: updated => {
      queryClient.setQueryData(queryKey, updated)
      if (updated.mode !== policy.mode) onSaved?.()
    },
  })
  // The page's floating Save commits this draft: one change for the mode, one
  // per grant added or removed. A conflict keeps the draft but blocks Save
  // until the current policy is reloaded.
  const pending = changeCount(policy, mode, rights) + (mode === "restricted" ? reissue.filter(r => rights.some(right => rightKey(right) === rightKey(r))).length : 0)
  usePageSave({
    label: `Resource access for ${label}`, count: pending, canSave: !conflict, saving: save.isPending,
    save: () => save.mutateAsync(),
    discard: () => { setMode(policy.mode); setRights(policy.rights); setReissue([]) },
  })
  function addRight() {
    if (!directory.data?.some(resource => resource.id === resourceId)) return
    const right = { kind, id: resourceId, operation }
    if (!rights.some(existing => existing.kind === kind && existing.id === resourceId && existing.operation === operation)) setRights([...rights, right])
  }
  const selectClass = nativeSelect
  const protectedRole = role === "OWNER" || role === "ADMIN"
  return <div className="space-y-3 text-xs">
    <label className="flex flex-wrap items-center gap-2">Access mode
      <select aria-label="Access mode" className={selectClass} value={mode} disabled={save.isPending || conflict}
        onChange={event => setMode(event.target.value as Policy["mode"])}>
        <option value="trusted">Trusted workspace access</option>
        <option value="restricted" disabled={protectedRole}>Guest — explicit resources</option>
      </select>
    </label>
    <p className="text-muted-foreground">{mode === "trusted"
      ? "Trusted access uses the member’s workspace role and capabilities. Saving clears individual resource grants."
      : "Guest restrictions apply to the entire account, across all workspaces. Only listed operations are allowed; conversation privacy checks still apply. Running a routine also requires run access to every agent it uses, and uses its configured credentials."}</p>
    {mode === "restricted" && <>
      <ul className="space-y-1" aria-label="Resource grants">{rights.map((right, index) => <li key={`${right.kind}:${right.id}:${right.operation}`} className="flex flex-wrap items-center gap-2">
        {policy.grant_details?.filter(detail => rightKey(detail) === rightKey(right)).map(detail => <span key={rightKey(detail)} className="text-muted-foreground">
          Issuer: {detail.issuer_name || detail.issuer_id} · {detail.live ? "Active" : "Inactive — issuer no longer has authority"}
          {!detail.live && <Button size="sm" variant="outline" disabled={save.isPending || conflict} onClick={() => setReissue(current => current.some(r => rightKey(r) === rightKey(right)) ? current.filter(r => rightKey(r) !== rightKey(right)) : [...current,right])}>
            {reissue.some(r => rightKey(r) === rightKey(right)) ? "Cancel reissue" : "Reissue as me"}
          </Button>}
        </span>)}
        <span>{right.kind}: {directories[right.kind].data?.find(resource => resource.id === right.id)?.name ?? right.id} · {right.operation}</span>
        <Button size="sm" variant="ghost" className="coarse:h-12" disabled={save.isPending || conflict} onClick={() => setRights(rights.filter((_, row) => row !== index))}
          aria-label={`Remove ${right.kind} ${right.id} ${right.operation}`}>Remove</Button>
      </li>)}</ul>
      {rights.length === 0 && <p>No resource operations are granted.</p>}
      <div className="flex flex-wrap gap-2">
        <select aria-label="Resource kind" className={selectClass} value={kind} disabled={save.isPending || conflict} onChange={event => {
          const next = event.target.value as Right["kind"]; setKind(next); setResourceId(""); setOperation(operations[next][0])
        }}><option value="agent">Agent</option><option value="project">Project</option><option value="routine">Routine</option></select>
        <select aria-label="Resource" className={selectClass} value={resourceId} disabled={save.isPending || conflict || !directory.data} onChange={event => setResourceId(event.target.value)}>
          <option value="">Choose a resource</option>{directory.data?.map(resource => <option key={resource.id} value={resource.id}>{resource.name}</option>)}
        </select>
        <select aria-label="Resource operation" className={selectClass} value={operation} disabled={save.isPending || conflict} onChange={event => setOperation(event.target.value)}>
          {operations[kind].map(value => <option key={value} value={value}>{value}</option>)}
        </select>
        <Button size="sm" className="coarse:h-12" variant="outline" disabled={save.isPending || conflict || !resourceId || rights.length >= 256} onClick={addRight}>Add grant</Button>
      </div>
      {directory.isError && <p role="alert">Resources unavailable. <button type="button" onClick={() => void directory.refetch()}>Reload resources</button></p>}
    </>}
    <EffectiveAccess url={url.replace("/access?", "/access/effective?")} revision={policy.revision} memberId={policy.membership_id} />
    {conflict && <p role="alert">Policy changed. Reload to discard this draft and review the current grants.</p>}
    <div className="flex flex-wrap gap-2">
      <Button size="sm" variant="outline" className="coarse:h-12" disabled={save.isPending} onClick={() => void queryClient.invalidateQueries({ queryKey })}>Reload current policy</Button>
    </div>
  </div>
}

const rightKey = (right: Right) => `${right.kind}:${right.id}:${right.operation}`

/** Edits pending against the saved policy: the mode, then each grant added or removed. */
function changeCount(policy: Policy, mode: Policy["mode"], rights: Right[]): number {
  const before = new Set(policy.mode === "trusted" ? [] : policy.rights.map(rightKey))
  const after = new Set(mode === "trusted" ? [] : rights.map(rightKey))
  let n = mode === policy.mode ? 0 : 1
  for (const key of after) if (!before.has(key)) n++
  for (const key of before) if (!after.has(key)) n++
  return n
}

function useResourceDirectory(workspaceId: string, kind: Right["kind"], enabled: boolean) {
  return useQuery({
    queryKey: ["resource-access-directory", workspaceId, kind], enabled, retry: false,
    queryFn: async ({ signal }) => {
      const path = kind === "routine" ? `workspaces/${requireApiPathSegment(workspaceId)}/pipelines` : kind === "agent" ? "agents" : "projects"
      const response = await apiFetch(`/api/v1/${path}?workspace_id=${encodeURIComponent(workspaceId)}`, { signal })
      if (!response.ok) throw new Error("Resources unavailable")
      return directorySchema.parse(await response.json())
    },
  })
}

const effectiveSchema = z.object({membership_id:z.string(), revision:z.number(), explanation:z.string(), account_restricted:z.boolean(), decisions:z.array(rightSchema.extend({state:z.string(),reason:z.string()}))})
function EffectiveAccess({url,revision,memberId}:{url:string;revision:number;memberId:string}) {
  const query=useQuery({
    queryKey:["member-effective-access",url,revision],retry:false,
    queryFn:async({signal})=>{
      const response=await apiFetch(url,{signal})
      if(!response.ok)throw new Error("Effective access unavailable")
      const result=effectiveSchema.parse(await response.json())
      if(result.membership_id!==memberId||result.revision!==revision)throw new Error("Policy changed; reload current policy")
      return result
    },
  })
  return <details className="rounded-md border p-3">
    <summary className="cursor-pointer">Effective access — saved policy</summary>
    {query.isPending && <p>Checking current access…</p>}
    {query.isError && <p role="status">Effective access unavailable. <button type="button" onClick={()=>void query.refetch()}>Retry access check</button></p>}
    {query.data && <><p className="mt-2 text-muted-foreground">{query.data.explanation}</p><ul className="mt-2 space-y-2">{query.data.decisions.map(row=><li key={rightKey(row)}><span className="font-medium">{row.kind} · {row.id} · {row.operation}: {row.state}</span><p className="text-muted-foreground">{row.reason}</p></li>)}</ul></>}
  </details>
}
