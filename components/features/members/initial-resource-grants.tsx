"use client"

import { useState } from "react"
import { useQuery } from "@tanstack/react-query"
import { z } from "zod"
import { apiFetch } from "@/lib/api-fetch"
import { requireApiPathSegment } from "@/lib/api-path-segment"
import { Button } from "@/components/ui/button"
import { nativeSelect } from "@/components/features/settings/shared"

export type InitialRight = {kind: "agent" | "routine" | "project"; id: string; operation: string}
const operations = {agent: ["discover", "chat", "run", "delegate"], routine: ["run"], project: ["list", "read", "write", "delete"]}
const directorySchema = z.array(z.object({id:z.string(), name:z.string()}))

/** The complete initial policy travels in the provisioning transaction. */
export function InitialResourceGrants({workspaceId, rights, onChange}: {
  workspaceId: string; rights: InitialRight[]; onChange: (rights: InitialRight[]) => void
}) {
  const [kind,setKind]=useState<InitialRight["kind"]>("routine")
  const [id,setId]=useState("")
  const [operation,setOperation]=useState("run")
  const directory=useQuery({
    queryKey:["initial-resource-grants",workspaceId,kind], retry:false,
    queryFn:async({signal})=>{
      const path=kind==="routine"?`workspaces/${requireApiPathSegment(workspaceId)}/pipelines`:kind==="agent"?"agents":"projects"
      const response=await apiFetch(`/api/v1/${path}?workspace_id=${encodeURIComponent(workspaceId)}`,{signal})
      if(!response.ok)throw new Error("Resources unavailable")
      return directorySchema.parse(await response.json())
    },
  })
  return <fieldset className="space-y-2 text-sm">
    <legend>Share now</legend>
    <p className="text-xs text-muted-foreground">Guests see only shared resources. A routine also needs run access to every agent it uses. Add discover and chat separately to offer agent conversations.</p>
    <div className="flex flex-wrap gap-2">
      <select aria-label="Initial resource kind" className={nativeSelect} value={kind} onChange={event=>{
        const next=event.target.value as InitialRight["kind"];setKind(next);setId("");setOperation(operations[next][0])
      }}><option value="routine">Routine</option><option value="agent">Agent</option><option value="project">Project</option></select>
      <select aria-label="Initial resource" className={nativeSelect} value={id} onChange={event=>setId(event.target.value)}>
        <option value="">Choose a resource</option>{directory.data?.map(row=><option key={row.id} value={row.id}>{row.name}</option>)}
      </select>
      <select aria-label="Initial operation" className={nativeSelect} value={operation} onChange={event=>setOperation(event.target.value)}>{operations[kind].map(op=><option key={op}>{op}</option>)}</select>
      <Button type="button" variant="outline" disabled={!id || rights.length>=256} onClick={()=>{
        if(!directory.data?.some(row=>row.id===id))return
        if(!rights.some(row=>row.kind===kind&&row.id===id&&row.operation===operation))onChange([...rights,{kind,id,operation}])
      }}>Share resource</Button>
    </div>
    {directory.isError && <p role="alert">Resources unavailable. <button type="button" onClick={()=>void directory.refetch()}>Retry</button></p>}
    {rights.length===0?<p className="text-xs text-muted-foreground">Nothing shared yet. They can open account settings.</p>:<ul>{rights.map((right,index)=><li key={`${right.kind}:${right.id}:${right.operation}`} className="flex items-center gap-2">
      <span>{right.kind} · {right.id} · {right.operation}</span>
      <Button type="button" size="sm" variant="ghost" onClick={()=>onChange(rights.filter((_,i)=>i!==index))} aria-label={`Remove initial ${right.kind} ${right.id} ${right.operation}`}>Remove</Button>
    </li>)}</ul>}
  </fieldset>
}
