"use client"

import { useState } from "react"
import { toast } from "sonner"
import { useQuery, useQueryClient } from "@tanstack/react-query"
import { apiFetch } from "@/lib/api-fetch"
import { requireApiPathSegment } from "@/lib/api-path-segment"
import { resourcePolicySchema, type ResourcePolicy } from "@/lib/resource-sharing"
import { presetCatalogSchema, presetPreviewSchema, type PresetPreview } from "@/lib/resource-presets"
import { Button } from "@/components/ui/button"
import { nativeSelect } from "./shared"

type Resource = {kind:"project"|"routine";id:string}
export function ResourcePresetEditor({workspaceId,policy,resource,disabled,onBusy,onSaved,onSaveStarted}:{
  workspaceId:string;policy:ResourcePolicy;resource:Resource;disabled:boolean;onBusy:(busy:boolean)=>void;onSaved:(policy:ResourcePolicy)=>void;onSaveStarted:()=>((policy:ResourcePolicy)=>boolean)
}) {
  const client=useQueryClient()
  const [selected,setSelected]=useState("")
  const [reissue,setReissue]=useState<string[]>([])
  const [preview,setPreview]=useState<PresetPreview|null>(null)
  const [confirmed,setConfirmed]=useState(false)
  const [busy,setBusy]=useState(false)
  const [stale,setStale]=useState(false)
  const [error,setError]=useState("")
  const base=`/api/v1/workspaces/${requireApiPathSegment(workspaceId)}`
  const scope=`?workspace_id=${encodeURIComponent(workspaceId)}`
  const catalog=useQuery({queryKey:["resource-preset-catalog",workspaceId],retry:false,refetchOnWindowFocus:false,
    queryFn:async({signal})=>{
      const response=await apiFetch(`${base}/access/presets${scope}`,{signal})
      if(!response.ok)throw new Error("Preset catalog unavailable")
      return presetCatalogSchema.parse(await response.json())
    },
  })
  const choices=catalog.data?.presets.filter(entry=>entry.definition.kind===resource.kind).flatMap(entry=>entry.definition.levels.map(level=>({entry,level,key:JSON.stringify([entry.definition.preset_id,entry.definition.version,level.level])})))??[]
  const choice=choices.find(item=>item.key===selected)
  const saved=policy.preset_selections?.find(item=>item.kind===resource.kind&&item.resource_id===resource.id)
  const retained=policy.grant_details?.filter(right=>right.kind===resource.kind&&right.id===resource.id&&choice?.level.operations.includes(right.operation))??[]
  const blocked=disabled||busy||stale||policy.mode!=="restricted"
  function clearPreview(){setPreview(null);setConfirmed(false);setError("")}
  async function requestPreview(){
    if(blocked||!choice)return
    setBusy(true);onBusy(true);clearPreview()
    const selection={membership_id:policy.membership_id,revision:policy.revision,resource,preset_id:choice.entry.definition.preset_id,preset_version:choice.entry.definition.version,level:choice.level.level,
      reissue:retained.filter(right=>reissue.includes(right.operation)).map(({kind,id,operation})=>({kind,id,operation})),
    }
    try{
      const response=await apiFetch(`${base}/members/${requireApiPathSegment(policy.membership_id)}/access/presets/preview${scope}`,{method:"POST",headers:{"Content-Type":"application/json"},body:JSON.stringify(selection)})
      if(response.status===409){setStale(true);setError("Access changed. Reload saved policy and preview again.");return}
      if(!response.ok)throw new Error("Preview unavailable")
      const result=presetPreviewSchema.parse(await response.json())
      const actual=result.selection
      const keys=(rights:typeof actual.reissue)=>rights.map(right=>JSON.stringify([right.kind,right.id,right.operation])).sort().join("|")
      if(actual.membership_id!==policy.membership_id||actual.revision!==policy.revision||actual.resource.kind!==resource.kind||actual.resource.id!==resource.id||actual.preset_id!==selection.preset_id||actual.preset_version!==selection.preset_version||actual.level!==selection.level||result.definition_hash!==choice.entry.definition_hash||keys(actual.reissue)!==keys(selection.reissue))throw new Error("Mismatched preview")
      setPreview(result)
    }catch{setError("Preview unavailable. Reload saved policy before trying again.")}
    finally{setBusy(false);onBusy(false)}
  }
  async function apply(){
    if(blocked||!preview||!confirmed)return
    const confirm=onSaveStarted()
    setBusy(true);onBusy(true);setError("")
    try{
      const response=await apiFetch(`${base}/members/${requireApiPathSegment(policy.membership_id)}/access/presets/apply${scope}`,{method:"POST",headers:{"Content-Type":"application/json"},body:JSON.stringify({selection:preview.selection,confirmation_digest:preview.confirmation_digest})})
      if(response.status===409){setStale(true);setPreview(null);setConfirmed(false);setError("Access changed. Reload saved policy and preview again.");return}
      if(!response.ok)throw new Error("Preset was not applied")
      const updated=resourcePolicySchema.parse(await response.json())
      if(updated.membership_id!==policy.membership_id||updated.mode!=="restricted"||updated.revision<=policy.revision)throw new Error("Membership changed")
      const saved=updated.preset_selections?.find(item=>item.kind===resource.kind&&item.resource_id===resource.id)
      if(!saved||saved.preset_id!==preview.selection.preset_id||saved.version!==preview.selection.preset_version||saved.level!==preview.selection.level||saved.definition_hash!==preview.definition_hash||saved.state!=="selected")throw new Error("Selection not confirmed")
      if(!confirm(updated))return
      client.setQueryData(["member-resource-access",workspaceId,policy.membership_id],updated)
      void client.invalidateQueries({queryKey:["own-access",workspaceId]})
      void client.invalidateQueries({queryKey:["object-resource-sharing",workspaceId],refetchType:"none"})
      toast.success("Preset applied")
      onSaved(updated)
    }catch{setStale(true);setPreview(null);setConfirmed(false);setError("Apply could not be confirmed. Reload saved policy before creating another preview.")}
    finally{setBusy(false);onBusy(false)}
  }
  return <section className="space-y-2 rounded-md border p-3" aria-label="Ordered resource preset">
    <p>{saved?`Saved ${saved.level} v${saved.version} · ${saved.state} — intent, not effective authority`:"Custom — no preset was explicitly selected"}</p>
    <label>Ordered preset<select aria-label="Ordered preset" className={nativeSelect} value={selected} disabled={blocked||!catalog.data} onChange={event=>{setSelected(event.target.value);setReissue([]);clearPreview()}}>
      <option value="">Choose an explicit preset</option>{choices.map(item=><option key={item.key} value={item.key}>{item.level.level} v{item.entry.definition.version} · {item.level.operations.join(" + ")}</option>)}
    </select></label>
    {catalog.isPending&&<p role="status">Loading preset definitions…</p>}
    {catalog.isError&&<div role="alert"><p>Preset catalog unavailable.</p><Button size="sm" variant="outline" className="coarse:h-[44px]" disabled={busy||disabled} onClick={()=>{clearPreview();setSelected("");setReissue([]);void catalog.refetch()}}>Retry catalog</Button></div>}
    {disabled&&<p>Save or discard manual changes before previewing a saved policy.</p>}
    {retained.map(right=><label key={right.operation} className="flex min-h-9 items-center gap-2 coarse:min-h-[44px]"><input type="checkbox" disabled={blocked} checked={reissue.includes(right.operation)} onChange={event=>{setReissue(event.target.checked?[...reissue,right.operation]:reissue.filter(op=>op!==right.operation));clearPreview()}}/>Reissue {right.operation} as me · {right.live?"active":"inactive"} · original issuer {right.issuer_name||right.issuer_id}</label>)}
    <Button variant="outline" size="sm" className="coarse:h-[44px]" disabled={blocked||!choice} onClick={()=>void requestPreview()}>Preview preset</Button>
    {preview&&<div className="space-y-2" aria-label="Preset consequences">
      <p>Review exact changes. Retained inactive grants remain inactive unless explicitly reissued. Other authority and privacy gates still apply.</p>
      <ul>{preview.changes.map(change=><li key={`${change.right.kind}:${change.right.id}:${change.right.operation}`}>{change.action} {change.right.operation} · issuer {change.issuer_id} · {change.created_at||"created only on successful apply"} · {change.live?"active":"inactive"}</li>)}</ul>
      <p>Other rights preserved unchanged:</p><ul>{preview.orthogonal_rights.map(right=><li key={`${right.kind}:${right.id}:${right.operation}`}>{right.kind} {right.id} · {right.operation} · issuer {right.issuer_id} · {right.created_at} · {right.live?"active":"inactive"}</li>)}</ul>
      {preview.warnings.map(warning=><p key={warning.code}>{warning.message}{warning.before_eligible!==undefined&&warning.after_eligible!==undefined?` Eligibility ${warning.before_eligible} → ${warning.after_eligible}.`:""}</p>)}
      <label className="flex min-h-9 items-center gap-2 coarse:min-h-[44px]"><input type="checkbox" disabled={blocked} checked={confirmed} onChange={event=>setConfirmed(event.target.checked)}/>I reviewed these exact changes and consequences</label>
      <Button size="sm" className="coarse:h-[44px]" disabled={blocked||!confirmed} onClick={()=>void apply()}>Apply confirmed preset</Button>
    </div>}
    {error&&<p role="alert">{error}</p>}
  </section>
}
