"use client"

import { useCallback, useEffect, useRef, useState } from "react"
import { Paperclip } from "lucide-react"
import { DetailCard } from "@/components/ui/detail"
import { Button } from "@/components/ui/button"
import { apiFetch } from "@/lib/api-fetch"
import type { Mission } from "@/lib/types/mission"

type IssueFile = { id: string; filename: string; size_bytes: number; uploaded_by_name?: string }

export function IssueFilesCard({ issue, editable }: { issue: Mission; editable: boolean }) {
 const latestLoad = useRef(0)
 const [files, setFiles] = useState<IssueFile[]>([])
 const [error, setError] = useState<string | null>(null)
 const [busy, setBusy] = useState(false)
 const base = `/api/v1/crews/${encodeURIComponent(issue.crew_id)}/issues/${encodeURIComponent(issue.identifier ?? issue.id)}/attachments`
 const qs = `workspace_id=${encodeURIComponent(issue.workspace_id)}`
 const load = useCallback(async (signal?: AbortSignal) => {
  const request = ++latestLoad.current
  try {
   const res = await apiFetch(`${base}?${qs}`, { signal })
   if (!res.ok) throw new Error("Could not load files")
   const rows = await res.json()
   if (!signal?.aborted && request === latestLoad.current) {setFiles(rows); setError(null)}
  } catch { if (!signal?.aborted && request === latestLoad.current) setError("Could not load files") }
 }, [base, qs])
 useEffect(() => {const controller = new AbortController(); void load(controller.signal); return () => controller.abort()}, [load, issue.updated_at])
 async function upload(file: File) {
  setBusy(true); setError(null)
  try {
   const data = new FormData(); data.append("file", file)
   const res = await apiFetch(`${base}?${qs}`, { method: "POST", body: data })
   if (!res.ok) {const body = await res.json().catch(() => null); throw new Error(body?.detail || body?.error || "File could not be uploaded")}
   await load()
  } catch(err) {setError(err instanceof Error ? err.message : "File could not be uploaded")} finally {setBusy(false)}
 }
 async function download(file: IssueFile) {
  try {
   const res = await apiFetch(`${base}/${encodeURIComponent(file.id)}?${qs}`)
   if (!res.ok) throw new Error()
   const blob = await res.blob(); const url = URL.createObjectURL(blob)
   const link = document.createElement("a"); link.href = url; link.download = file.filename; link.click()
   setTimeout(() => URL.revokeObjectURL(url), 1000)
  } catch {setError("File could not be downloaded. Try again.")}
 }
 return <DetailCard title="Files and deliverables" icon={Paperclip} action={<Button size="sm" variant="ghost" onClick={() => void load()}>Refresh</Button>}>
  {files.length === 0 && !error && <p className="text-xs text-muted-foreground">Attach the brief, reference files, or the finished result here.</p>}
  <ul className="space-y-2">{files.map((file) => <li key={file.id} className="flex flex-wrap items-center justify-between gap-2 text-xs"><button className="min-w-0 break-all text-left text-primary hover:underline" onClick={() => void download(file)}>{file.filename}</button><span className="text-muted-foreground">{Math.max(1, Math.round(file.size_bytes / 1024))} KB{file.uploaded_by_name ? ` · ${file.uploaded_by_name}` : ""}</span></li>)}</ul>
  {editable && <label className="mt-3 block text-xs text-muted-foreground">{busy ? "Uploading…" : "Attach a file"}<input type="file" disabled={busy} className="mt-1 block max-w-full text-xs" onChange={(event) => {const file = event.target.files?.[0]; if(file) void upload(file); event.target.value = ""}} /></label>}
  {error && <p role="alert" className="mt-2 text-xs text-destructive">{error}</p>}
 </DetailCard>
}
