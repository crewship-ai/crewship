"use client"

import { useState } from "react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { z } from "zod"
import { resourceRightSchema, resourcePolicySchema } from "@/lib/resource-sharing"

import { Button } from "@/components/ui/button"
import { StatusPill } from "@/components/ui/status-pill"
import { usePageSave } from "@/components/ui/page-save-bar"
import { requireApiPathSegment } from "@/lib/api-path-segment"
import { apiFetch } from "@/lib/api-fetch"
import { ResourcePresetEditor } from "./resource-preset-editor"
import { useRestrictedProjection } from "@/hooks/use-restricted-projection"
import { nativeSelect } from "@/components/features/settings/shared"
import { kindName, operationName } from "./members/resource-labels"
import { RadioOption } from "./members/member-bits"

const rightSchema = resourceRightSchema
const policySchema = resourcePolicySchema
type Policy = z.infer<typeof policySchema>
type Right = z.infer<typeof rightSchema>
const directorySchema = z.array(z.object({ id: z.string(), name: z.string() }))
const operations = { crew: ["view", "use"], agent: ["discover", "chat", "run", "delegate"], project: ["list", "read", "contribute", "manage", "delete_issues", "write", "delete"], routine: ["view", "results", "run", "edit", "publish"] }

/**
 * Reach: where a person's role and abilities apply — the whole workspace, or
 * only what is shared with them (a Guest). The saved policy loads when the
 * person's sheet shows this card; every edit is a draft that waits for the
 * page's Save, and a conflict (409) keeps the draft until the current policy
 * is reloaded.
 */
export function MemberResourceAccess({ workspaceId, memberId, label, role, onSaved }: {
  workspaceId: string; memberId: string; label: string; role: string
  /** A saved mode changes the roster's Guest marker; reload it. */
  onSaved?: () => void
}) {
  const [reloadKey, setReloadKey] = useState(0)
  const projection = useRestrictedProjection<Policy>(JSON.stringify([workspaceId, memberId]))
  const url = `/api/v1/workspaces/${requireApiPathSegment(workspaceId)}/members/${requireApiPathSegment(memberId)}/access?workspace_id=${encodeURIComponent(workspaceId)}`
  const queryKey = ["member-resource-access", workspaceId, memberId]
  const policy = useQuery({
    queryKey, retry: false, staleTime: 0, refetchOnWindowFocus: false,
    queryFn: async ({ signal }) => {
      const read = projection.read(signal)
      const response = await apiFetch(url, { signal })
      if (!response.ok) { read.reject(response.status); throw new Error("Resource policy unavailable") }
      const parsed = policySchema.parse(await response.json())
      if (parsed.membership_id !== memberId) { read.reject(404); throw new Error("Membership changed; reload the roster") }
      read.accept(parsed)
      return parsed
    },
  })
  return <section className="space-y-3 px-4 py-3" aria-label={`Reach for ${label}`} data-slot="member-reach">
    {policy.isPending && <p className="text-label text-muted-foreground">Loading reach…</p>}
    {policy.isError && <p role="alert" className="text-label">Resource policy unavailable. <button type="button" className="font-medium text-primary-hover hover:underline" onClick={() => void policy.refetch()}>Reload policy</button></p>}
    {projection.value && <PolicyEditor key={`${workspaceId}:${memberId}:${projection.value.revision}:${reloadKey}`} policy={projection.value} url={url}
      queryKey={queryKey} workspaceId={workspaceId} role={role} label={label} onSaved={onSaved} onPolicySaveStarted={projection.captureSave} onReload={async () => { const result = await policy.refetch(); if (!result.isError) setReloadKey(key => key + 1) }} />}
  </section>
}

function PolicyEditor({ policy, url, queryKey, workspaceId, role, label, onSaved, onPolicySaveStarted, onReload }: {
  policy: Policy; url: string; queryKey: string[]; workspaceId: string; role: string; label: string
  onSaved?: () => void; onPolicySaveStarted: () => (policy: Policy) => boolean; onReload: () => Promise<void>
}) {
  const queryClient = useQueryClient()
  const [presetBusy, setPresetBusy] = useState(false)
  const [mode, setMode] = useState(policy.mode)
  const [rights, setRights] = useState<Right[]>(policy.rights)
  const [reissue, setReissue] = useState<Right[]>([])
  const [sharing, setSharing] = useState(false)
  const [kind, setKind] = useState<keyof typeof operations>(policy.mode === "trusted" ? "crew" : "agent")
  const [resourceId, setResourceId] = useState("")
  const [operation, setOperation] = useState(policy.mode === "trusted" ? "manage" : "chat")
  const [conflict, setConflict] = useState(false)
  const trustedEligible = role === "MEMBER" || role === "MANAGER"
  const canAddTrusted = policy.mode === "trusted" && mode === "trusted" && trustedEligible
  const availableOperations: readonly string[] = mode === "trusted" ? ["manage"] : operations[kind]
  // Each directory loads so every grant resolves its name by its own kind,
  // not by whichever kind the add-grant selector currently shows (#2863).
  const directories = {
    crew: useResourceDirectory(workspaceId, "crew", mode === "restricted" || canAddTrusted),
    agent: useResourceDirectory(workspaceId, "agent", mode === "restricted"),
    project: useResourceDirectory(workspaceId, "project", mode === "restricted"),
    routine: useResourceDirectory(workspaceId, "routine", mode === "restricted"),
  }
  const directory = directories[kind]
  const save = useMutation({
    onMutate: () => onPolicySaveStarted(),
    mutationFn: async () => {
      if (invalidPublish) throw new Error("Publish requires Edit on the same routine")
      const response = await apiFetch(url, { method: "PUT", headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ membership_id: policy.membership_id, revision: policy.revision, mode, rights: mode === "trusted" && policy.mode !== "trusted" ? [] : rights,
          ...(reissue.length ? {reissue: reissue.filter(r => rights.some(right => rightKey(right) === rightKey(r)))} : {}) }) })
      if (response.status === 409) { setConflict(true); throw new Error("Policy changed. Reload the current policy before saving.") }
      if (!response.ok) throw new Error("Resource access was not saved")
      const updated = policySchema.parse(await response.json())
      if (updated.membership_id !== policy.membership_id) throw new Error("Membership changed; reload the roster")
      return updated
    },
    // The saved revision remounts this editor (its key), so the draft goes
    // clean. A failure rejects into the page bar, which toasts it.
    onSuccess: (updated, _variables, confirm) => {
      if (!confirm?.(updated)) return
      queryClient.setQueryData(queryKey, updated)
      if (updated.mode !== policy.mode) onSaved?.()
    },
  })
  // The page's floating Save commits this draft: one change for the mode, one
  // per grant added or removed. A conflict keeps the draft but blocks Save
  // until the current policy is reloaded.
  const pending = changeCount(policy, mode, rights) + (mode === "restricted" || policy.mode === "trusted" ? reissue.filter(r => rights.some(right => rightKey(right) === rightKey(r))).length : 0)
  const invalidPublish = mode === "restricted" && rights.some(right => right.kind === "routine" && right.operation === "publish" && !rights.some(edit => edit.kind === "routine" && edit.id === right.id && edit.operation === "edit"))
  usePageSave({
    label: `Reach for ${label}`, count: pending, canSave: !conflict && !presetBusy && !invalidPublish, saving: save.isPending || presetBusy,
    save: () => save.mutateAsync(),
    discard: () => { setMode(policy.mode); setRights(policy.rights); setReissue([]) },
  })
  function addRight() {
    if (!directory.data?.some(resource => resource.id === resourceId) || !availableOperations.includes(operation) || (mode === "trusted" && (!canAddTrusted || kind !== "crew"))) return
    const right = { kind, id: resourceId, operation }
    if (!rights.some(existing => existing.kind === kind && existing.id === resourceId && existing.operation === operation)) setRights([...rights, right])
  }
  const selectClass = nativeSelect
  const protectedRole = role === "OWNER" || role === "ADMIN"
  const busy = save.isPending || presetBusy || conflict
  const canShare = mode === "restricted" || canAddTrusted
  const guestWhy = protectedRole ? "Owners and admins always reach the whole workspace."
    : policy.mode === "trusted" && policy.rights.length > 0 ? "Remove the crew shares below and save, then switch to Guest." : undefined
  const nameOf = (right: Right) => right.kind === "crew" ? directories.crew.data?.find(resource => resource.id === right.id)?.name ?? right.id : directories[right.kind].data?.find(resource => resource.id === right.id)?.name ?? right.id
  const switchTo = (next: Policy["mode"]) => { setMode(next); setKind("crew"); setResourceId(""); setOperation(next === "trusted" ? "manage" : "view") }
  const reissueChosen = (right: Right) => reissue.some(r => rightKey(r) === rightKey(right))
  const toggleReissue = (right: Right) => setReissue(current => reissueChosen(right) ? current.filter(r => rightKey(r) !== rightKey(right)) : [...current, right])
  return <div className="space-y-3 text-label">
    <div role="radiogroup" aria-label="Reach" className="space-y-1.5">
      <RadioOption checked={mode === "trusted"} title="Whole workspace" description="Every crew, agent and project the role allows" disabled={busy} onSelect={() => switchTo("trusted")} />
      <RadioOption checked={mode === "restricted"} title="Guest" description="Only what is shared with them below. Applies to their account in every workspace."
        disabled={busy || Boolean(guestWhy)} why={guestWhy} onSelect={() => switchTo("restricted")} />
    </div>
    {guestWhy && mode === "trusted" && <p className="text-muted-foreground" data-slot="reach-why">{guestWhy}</p>}
    <p className="text-muted-foreground">{mode === "trusted"
      ? (policy.mode === "trusted" ? "Whole workspace keeps their workspace role and any crew management shares. Remove and save crew shares before switching to Guest." : "Restoring whole-workspace reach clears the Guest shares and restores the workspace role and abilities.")
      : "Guest restrictions apply to the entire account, across all workspaces. Only listed operations are allowed; conversation privacy checks still apply. Running a routine also requires run access to every agent it uses, and uses its configured credentials."}</p>
    {(mode === "restricted" || policy.mode === "trusted") && <>
      <div className="flex items-center justify-between text-micro font-semibold text-muted-foreground"><span>Shared with them</span><span className="font-normal tabular-nums">{rights.length} of 256</span></div>
      <ul className="-mx-4 border-y border-border/60" aria-label="Resource grants">{rights.map((right, index) => <li key={`${right.kind}:${right.id}:${right.operation}`} className="flex flex-wrap items-center gap-x-3 gap-y-1 border-b border-border/60 px-4 py-2 last:border-b-0">
        <span className="min-w-0 flex-1 basis-40">
          <span className="block truncate text-control">{kindName(right.kind)} · {nameOf(right)}</span>
          <span className="block text-micro text-muted-foreground">{operationName(right.kind, right.operation)}
            {policy.grant_details?.filter(detail => rightKey(detail) === rightKey(right)).map(detail => <span key={rightKey(detail)}> · Shared by {detail.issuer_name || detail.issuer_id}, {detail.live ? "active" : "inactive: the current share authority is unavailable"}</span>)}
          </span>
        </span>
        <Button size="sm" variant="ghost" className="coarse:h-[44px]" disabled={busy} onClick={() => setRights(rights.filter((_, row) => row !== index))}
          aria-label={`Stop sharing ${nameOf(right)}: ${operationName(right.kind, right.operation)}`}>Stop sharing</Button>
      </li>)}</ul>
      {rights.length === 0 && <p className="text-muted-foreground">Nothing is shared with them.</p>}
    </>}
    {invalidPublish && <p role="alert">Publish requires Edit on the same routine. Add Edit or remove Publish before saving.</p>}
    {canShare && <div className="space-y-2">
      <Button size="sm" variant="outline" className="coarse:h-[44px]" aria-expanded={sharing} disabled={busy} onClick={() => setSharing(!sharing)}>
        {sharing ? "Close sharing" : "Share something"}
      </Button>
      {sharing && <div className="space-y-2 rounded-lg border border-border p-3" data-slot="member-share">
        <div className="flex flex-wrap gap-2">
          <select aria-label="What" className={selectClass} value={kind} disabled={busy} onChange={event => {
            const next = event.target.value as keyof typeof operations; setKind(next); setResourceId(""); setOperation(operations[next][0])
          }}><option value="crew">Crew</option>{mode === "restricted" && <><option value="agent">Agent</option><option value="project">Project</option><option value="routine">Routine</option></>}</select>
          <select aria-label="Which one" className={selectClass} value={resourceId} disabled={busy || !directory.data} onChange={event => setResourceId(event.target.value)}>
            <option value="">Choose one</option>{directory.data?.map(resource => <option key={resource.id} value={resource.id}>{resource.name}</option>)}
          </select>
          <select aria-label="They may" className={selectClass} value={operation} disabled={busy} onChange={event => setOperation(event.target.value)}>
            {availableOperations.map(value => <option key={value} value={value}>{operationName(kind, value)}</option>)}
          </select>
          <Button size="sm" className="coarse:h-[44px]" variant="outline" disabled={busy || !resourceId || rights.length >= 256} onClick={addRight}>Share</Button>
        </div>
        {kind === "crew" && mode === "trusted" && <p className="text-muted-foreground">Manage permits this crew’s profile/configuration and ordinary membership. It does not assign crew roles, delete the crew, change budgets/policy, administer the workspace or expose credentials.</p>}
        {kind === "crew" && mode === "restricted" && <p className="text-muted-foreground">See it shares the crew identity and current agent identities and status. Use it also shares Chat and Run for current and future agents, subject to each agent’s authority and execution preflight. Newly added agents become discoverable. This does not grant Delegate, credential visibility, existing conversations, activity or crew administration.</p>}
        {policy.mode === "restricted" && (kind === "project" || kind === "routine" || kind === "crew") && resourceId && <ResourcePresetEditor key={`${kind}:${resourceId}:${policy.revision}`} workspaceId={workspaceId} policy={policy} resource={{kind,id:resourceId}}
          disabled={pending > 0 || save.isPending || conflict} onSaveStarted={onPolicySaveStarted} onBusy={setPresetBusy} onSaved={updated => queryClient.setQueryData(queryKey,updated)} />}
        {directory.isError && <p role="alert">Resources unavailable. <button type="button" className="font-medium text-primary-hover hover:underline" onClick={() => void directory.refetch()}>Reload resources</button></p>}
      </div>}
    </div>}
    <EffectiveAccess url={url.replace("/access?", "/access/effective?")} revision={policy.revision} memberId={policy.membership_id}
      inactive={(policy.grant_details ?? []).filter(detail => !detail.live && rights.some(right => rightKey(right) === rightKey(detail)))}
      canReissue={policy.mode === "restricted" || trustedEligible} chosen={reissueChosen} onToggle={toggleReissue} busy={busy} nameOf={nameOf} />
    {conflict && <p role="alert">Policy changed. Reload to discard this draft and review the current grants.</p>}
    <div className="flex flex-wrap gap-2">
      <Button size="sm" variant="ghost" className="coarse:h-[44px]" disabled={save.isPending || presetBusy} onClick={() => void onReload()}>Reload current policy</Button>
    </div>
  </div>
}

const rightKey = (right: Right) => `${right.kind}:${right.id}:${right.operation}`

/** Edits pending against the saved policy: the mode, then each grant added or removed. */
function changeCount(policy: Policy, mode: Policy["mode"], rights: Right[]): number {
  const before = new Set(policy.rights.map(rightKey))
  const after = new Set(mode === "trusted" && policy.mode !== "trusted" ? [] : rights.map(rightKey))
  let n = mode === policy.mode ? 0 : 1
  for (const key of after) if (!before.has(key)) n++
  for (const key of before) if (!after.has(key)) n++
  return n
}

function useResourceDirectory(workspaceId: string, kind: keyof typeof operations, enabled: boolean) {
  return useQuery({
    queryKey: ["resource-access-directory", workspaceId, kind], enabled, retry: false,
    queryFn: async ({ signal }) => {
      const path = kind === "routine" ? `workspaces/${requireApiPathSegment(workspaceId)}/pipelines` : kind === "agent" ? "agents" : kind === "crew" ? "crews" : "projects"
      const response = await apiFetch(`/api/v1/${path}?workspace_id=${encodeURIComponent(workspaceId)}`, { signal })
      if (!response.ok) throw new Error("Resources unavailable")
      return directorySchema.parse(await response.json())
    },
  })
}

const effectiveSchema = z.object({membership_id:z.string(), revision:z.number(), explanation:z.string(), account_restricted:z.boolean(), decisions:z.array(rightSchema.extend({name:z.string().optional(),state:z.string(),reason:z.string()}))})
type Detail = NonNullable<Policy["grant_details"]>[number]
const decisionPill: Record<string, { tone: "success" | "warn" | "muted"; label: string }> = {
  allowed: { tone: "success", label: "Allowed" }, conditional: { tone: "warn", label: "With conditions" },
  denied: { tone: "muted", label: "Not allowed" }, unavailable: { tone: "muted", label: "Unavailable" },
}
/**
 * What they can reach right now: every decision of the SAVED policy with its
 * reason. A grant whose issuer lost the authority to share is inactive; "Share
 * it as me" puts it back in the draft under the current administrator.
 */
function EffectiveAccess({url,revision,memberId,inactive,canReissue,chosen,onToggle,busy,nameOf}:{url:string;revision:number;memberId:string;inactive:Detail[];canReissue:boolean;chosen:(right:Right)=>boolean;onToggle:(right:Right)=>void;busy:boolean;nameOf:(right:Right)=>string}) {
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
  const plain=(right:Right):Right=>({kind:right.kind,id:right.id,operation:right.operation})
  const isInactive=(right:Right)=>inactive.some(detail=>rightKey(detail)===rightKey(right))
  return <details className="rounded-lg border border-border" data-slot="member-effective" open={inactive.length>0||undefined}>
    <summary className="cursor-pointer px-3 py-2 text-control font-medium">What they can reach right now</summary>
    <div className="space-y-2 border-t border-border/60 px-3 py-2">
      {query.isPending && <p>Checking what they can reach…</p>}
      {query.isError && <p role="status">Could not check what they can reach. <button type="button" className="font-medium text-primary-hover hover:underline" onClick={()=>void query.refetch()}>Check again</button></p>}
      {query.data && <>
        <p className="text-muted-foreground">{query.data.explanation}</p>
        {query.data.decisions.length===0 && <p className="text-muted-foreground">Nothing is shared with them yet.</p>}
        <ul className="-mx-3 border-t border-border/60">{query.data.decisions.map(row=>{
          const off=isInactive(row)
          const pill=off?{tone:"warn" as const,label:"Inactive"}:decisionPill[row.state]??{tone:"muted" as const,label:row.state}
          return <li key={rightKey(row)} className="flex flex-wrap items-center gap-x-3 gap-y-1 border-b border-border/60 px-3 py-2 last:border-b-0">
            <span className="min-w-0 flex-1 basis-48">
              <span className="block truncate text-control">{kindName(row.kind)} · {row.name || nameOf(row)} · {operationName(row.kind,row.operation)}</span>
              <span className="block text-micro text-muted-foreground">{row.reason}</span>
            </span>
            <StatusPill tone={pill.tone} label={pill.label} />
            {off&&canReissue&&<Button size="sm" variant="outline" className="coarse:h-[44px]" disabled={busy} onClick={()=>onToggle(plain(row))}>{chosen(row)?"Cancel share as me":"Share it as me"}</Button>}
          </li>})}</ul>
      </>}
      {query.isError && inactive.length>0 && <ul>{inactive.map(detail=><li key={rightKey(detail)} className="flex items-center gap-2"><span className="min-w-0 flex-1 truncate">{kindName(detail.kind)} · {nameOf(detail)} · {operationName(detail.kind,detail.operation)} · inactive</span>
        {canReissue&&<Button size="sm" variant="outline" className="coarse:h-[44px]" disabled={busy} onClick={()=>onToggle(plain(detail))}>{chosen(detail)?"Cancel share as me":"Share it as me"}</Button>}</li>)}</ul>}
    </div>
  </details>
}
