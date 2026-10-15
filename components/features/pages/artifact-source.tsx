"use client"

import { Button } from "@/components/ui/button"
import { Textarea } from "@/components/ui/textarea"

interface ArtifactSourceProps {
  html: string
  draft: string | null
  base: number
  head: number
  canWrite: boolean
  pending: boolean
  onChange: (html: string) => void
  onUseLatest: () => void
  onSave: () => void
  onCancel: () => void
  onEdit: () => void
  onDownload: () => void
}
export function ArtifactSource(props: ArtifactSourceProps) {
  const { draft, base, head, pending } = props
  if (draft !== null && props.canWrite) return (
    <div className="space-y-3 p-4">
      <label htmlFor="artifact-source" className="text-sm font-medium">HTML for the next revision</label>
      <Textarea id="artifact-source" value={draft} onChange={event => props.onChange(event.target.value)} className="min-h-80 font-mono text-xs" />
      {base !== head && <p className="text-sm text-warn">This draft began at revision {base}. Review the latest source or history before choosing a new base.</p>}
      <div className="flex flex-wrap gap-2">
        {base !== head && <Button variant="outline" disabled={pending} onClick={props.onUseLatest}>Use latest revision {head} as base</Button>}
        <Button disabled={pending || !draft.trim() || base !== head} onClick={props.onSave}>{pending ? "Saving…" : "Save revision"}</Button>
        <Button variant="ghost" disabled={pending} onClick={props.onCancel}>Cancel</Button>
      </div>
    </div>
  )
  return (
    <div className="space-y-3 p-4">
      <pre className="max-h-[60vh] overflow-auto whitespace-pre-wrap rounded-md bg-muted/40 p-4 text-xs">{props.html || "No source saved yet."}</pre>
      <div className="flex gap-2">
        {props.canWrite && <Button variant="outline" onClick={props.onEdit}>Edit as new revision</Button>}
        <Button variant="ghost" disabled={!head} onClick={props.onDownload}>Download source as text</Button>
      </div>
    </div>
  )
}
