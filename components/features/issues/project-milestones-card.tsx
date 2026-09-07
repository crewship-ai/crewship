"use client"

import { useCallback, useEffect, useState } from "react"
import { Milestone as MilestoneIcon } from "lucide-react"
import { DetailCard } from "@/components/ui/detail"
import { Button } from "@/components/ui/button"
import { apiFetch } from "@/lib/api-fetch"
import type { Milestone } from "@/lib/types/mission"

export function ProjectMilestonesCard({ projectId, workspaceId, editable }: { projectId: string; workspaceId: string; editable: boolean }) {
 const [items, setItems] = useState<Milestone[]>([])
 const [name, setName] = useState("")
 const [date, setDate] = useState("")
 const [error, setError] = useState<string | null>(null)
 const [busy, setBusy] = useState(false)
 const url = `/api/v1/projects/${encodeURIComponent(projectId)}/milestones?workspace_id=${encodeURIComponent(workspaceId)}`
 const load = useCallback(async (signal?: AbortSignal) => {
  try {const res = await apiFetch(url, { signal }); if(!res.ok) throw new Error(); const body = await res.json(); if(!signal?.aborted) {setItems(body); setError(null)}} catch {if(!signal?.aborted) setError("Could not load milestones")}
 },[url])
 useEffect(() => {const controller = new AbortController(); void load(controller.signal); return () => controller.abort()},[load])
 return <DetailCard title="Milestones" icon={MilestoneIcon}>
  {items.length === 0 && !error && <p className="text-xs text-muted-foreground">Break the project into clear checkpoints.</p>}
  <ul className="space-y-2 text-sm">{items.map((item) => <li key={item.id} className="flex flex-wrap justify-between gap-2"><span>{item.name}</span><span className="text-xs text-muted-foreground">{item.done_count}/{item.issue_count} done{item.target_date ? ` · ${item.target_date.slice(0,10)}` : ""}</span></li>)}</ul>
  {editable && <form className="mt-3 flex flex-wrap gap-2" onSubmit={async (event) => {
   event.preventDefault(); if(busy || !name.trim()) return; setBusy(true); setError(null)
   try {const res = await apiFetch(url, {method: "POST", headers: {"Content-Type":"application/json"},body:JSON.stringify({name: name.trim(),target_date:date || undefined})});if(!res.ok) {const body = await res.json().catch(()=>null);throw new Error(body?.detail || body?.error || "Could not create milestone")};setName("");setDate("");await load()} catch(err) {setError(err instanceof Error ? err.message : "Could not create milestone")} finally {setBusy(false)}
  }}><input aria-label="Milestone name" placeholder="Milestone name" required value={name} disabled={busy} onChange={(e)=>setName(e.target.value)} className="h-9 min-w-0 flex-1 rounded-md border border-input bg-background px-2 text-sm" /><input aria-label="Milestone target date" type="date" value={date} disabled={busy} onChange={(e)=>setDate(e.target.value)} className="h-9 rounded-md border border-input bg-background px-2 text-sm" /><Button size="sm" disabled={busy || !name.trim()}>Add milestone</Button></form>}
  {error && <p role="alert" className="mt-2 text-xs text-destructive">{error} <button className="underline" onClick={()=>void load()}>Retry loading</button></p>}
 </DetailCard>
}
