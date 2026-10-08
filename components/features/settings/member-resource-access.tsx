"use client"

import { useState } from "react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { z } from "zod"

import { Button } from "@/components/ui/button"
import { usePageSave } from "@/components/ui/page-save-bar"
import { apiFetch } from "@/lib/api-fetch"

const rightSchema = z.object({ kind: z.enum(["agent", "project"]), id: z.string().min(1), operation: z.string() })
const policySchema = z.object({
  membership_id: z.string(), revision: z.number().int().positive(),
  mode: z.enum(["trusted", "restricted"]), rights: z.array(rightSchema),
})
type Policy = z.infer<typeof policySchema>
type Right = z.infer<typeof rightSchema>
const directorySchema = z.array(z.object({ id: z.string(), name: z.string() }))
const operations = { agent: ["discover", "chat", "run", "delegate"], project: ["list", "read", "write", "delete"] }

export function MemberResourceAccess({ workspaceId, memberId, label, role }: {
  workspaceId: string; memberId: string; label: string; role: string
}) {
  const [open, setOpen] = useState(false)
  const url = `/api/v1/workspaces/${encodeURIComponent(workspaceId)}/members/${encodeURIComponent(memberId)}/access?workspace_id=${encodeURIComponent(workspaceId)}`
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
      queryKey={queryKey} workspaceId={workspaceId} role={role} label={label} />}
  </section>
}

function PolicyEditor({ policy, url, queryKey, workspaceId, role, label }: {
  policy: Policy; url: string; queryKey: string[]; workspaceId: string; role: string; label: string
}) {
  const queryClient = useQueryClient()
  const [mode, setMode] = useState(policy.mode)
  const [rights, setRights] = useState<Right[]>(policy.rights)
  const [kind, setKind] = useState<Right["kind"]>("agent")
  const [resourceId, setResourceId] = useState("")
  const [operation, setOperation] = useState("chat")
  const [conflict, setConflict] = useState(false)
  // Both directories load so every grant resolves its name by its own kind,
  // not by whichever kind the add-grant selector currently shows (#2863).
  const directories = {
    agent: useResourceDirectory(workspaceId, "agent", mode === "restricted"),
    project: useResourceDirectory(workspaceId, "project", mode === "restricted"),
  }
  const directory = directories[kind]
  const save = useMutation({
    mutationFn: async () => {
      const response = await apiFetch(url, { method: "PUT", headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ ...policy, mode, rights: mode === "trusted" ? [] : rights }) })
      if (response.status === 409) { setConflict(true); throw new Error("Policy changed. Reload the current policy before saving.") }
      if (!response.ok) throw new Error("Resource access was not saved")
      const updated = policySchema.parse(await response.json())
      if (updated.membership_id !== policy.membership_id) throw new Error("Membership changed; reload the roster")
      return updated
    },
    // The saved revision remounts this editor (its key), so the draft goes
    // clean. A failure rejects into the page bar, which toasts it.
    onSuccess: updated => { queryClient.setQueryData(queryKey, updated) },
  })
  // The page's floating Save commits this draft: one change for the mode, one
  // per grant added or removed. A conflict keeps the draft but blocks Save
  // until the current policy is reloaded.
  const pending = changeCount(policy, mode, rights)
  usePageSave({
    label: `Resource access for ${label}`, count: pending, canSave: !conflict, saving: save.isPending,
    save: () => save.mutateAsync(),
    discard: () => { setMode(policy.mode); setRights(policy.rights) },
  })
  function addRight() {
    if (!directory.data?.some(resource => resource.id === resourceId)) return
    const right = { kind, id: resourceId, operation }
    if (!rights.some(existing => existing.kind === kind && existing.id === resourceId && existing.operation === operation)) setRights([...rights, right])
  }
  const selectClass = "rounded-md border bg-background px-2 text-xs h-8 coarse:h-12"
  const protectedRole = role === "OWNER" || role === "ADMIN"
  return <div className="space-y-3 text-xs">
    <label className="flex flex-wrap items-center gap-2">Access mode
      <select aria-label="Access mode" className={selectClass} value={mode} disabled={save.isPending || conflict}
        onChange={event => setMode(event.target.value as Policy["mode"])}>
        <option value="trusted">Trusted workspace access</option>
        <option value="restricted" disabled={protectedRole}>Restricted to explicit resources</option>
      </select>
    </label>
    <p className="text-muted-foreground">{mode === "trusted"
      ? "Trusted access uses the member’s workspace role and capabilities. Saving clears individual resource grants."
      : "Only listed operations are allowed. Role and conversation privacy checks still apply. Some agent features may be unavailable in restricted mode."}</p>
    {mode === "restricted" && <>
      <ul className="space-y-1" aria-label="Resource grants">{rights.map((right, index) => <li key={`${right.kind}:${right.id}:${right.operation}`} className="flex flex-wrap items-center gap-2">
        <span>{right.kind}: {directories[right.kind].data?.find(resource => resource.id === right.id)?.name ?? right.id} · {right.operation}</span>
        <Button size="sm" variant="ghost" className="coarse:h-12" disabled={save.isPending || conflict} onClick={() => setRights(rights.filter((_, row) => row !== index))}
          aria-label={`Remove ${right.kind} ${right.id} ${right.operation}`}>Remove</Button>
      </li>)}</ul>
      {rights.length === 0 && <p>No resource operations are granted.</p>}
      <div className="flex flex-wrap gap-2">
        <select aria-label="Resource kind" className={selectClass} value={kind} disabled={save.isPending || conflict} onChange={event => {
          const next = event.target.value as Right["kind"]; setKind(next); setResourceId(""); setOperation(operations[next][0])
        }}><option value="agent">Agent</option><option value="project">Project</option></select>
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
      const response = await apiFetch(`/api/v1/${kind === "agent" ? "agents" : "projects"}?workspace_id=${encodeURIComponent(workspaceId)}`, { signal })
      if (!response.ok) throw new Error("Resources unavailable")
      return directorySchema.parse(await response.json())
    },
  })
}
