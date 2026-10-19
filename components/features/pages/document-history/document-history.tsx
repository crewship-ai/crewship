"use client"

import { Button } from "@/components/ui/button"
import { formatDateTime } from "@/lib/time"
import { DocumentReadonly } from "../document-readonly/document-readonly"
import type { DocumentHistoryProps, DocumentCheckpointSnapshot } from "./types"
export type { DocumentHistoryProps } from "./types"

function supported(schema: number): schema is 2 | 3 { return schema === 2 || schema === 3 }
function when(value: string) {
  return Number.isFinite(Date.parse(value)) ? formatDateTime(value) : "Date unavailable"
}

/** Separate immutable view: never binds to or dispatches into the live editor. */
function ConfirmedContent({ snapshot }: { snapshot: DocumentCheckpointSnapshot }) {
  if (!supported(snapshot.schema)) return <p role="status">Snapshot unavailable. This content cannot be displayed by this reader.</p>
  return <DocumentReadonly schema={snapshot.schema} content={snapshot.content} accessibleLabel={`Confirmed revision ${snapshot.rev}`} />
}

export function DocumentHistory({ pageId, scopeKey, list, selectedId, snapshot, onSelect, onClose }: DocumentHistoryProps) {
  // The caller must invalidate scope on current-human/workspace/Page/auth changes.
  // Never retain an old response merely because the new request is pending.
  const denied = list.status === "denied" || snapshot.status === "denied"
  const admitted = Boolean(pageId && scopeKey) && !denied && list.status === "ready" && list.scopeKey === scopeKey
  const checkpoints = admitted ? list.data.checkpoints : []
  const selected = checkpoints.find(checkpoint => checkpoint.id === selectedId)
  const read = snapshot.status === "ready" && snapshot.scopeKey === scopeKey ? snapshot.data : undefined
  const exact = selected && read && read.page_id === pageId && read.id === selected.id && read.rev === selected.rev && read.schema === selected.schema
  let listMessage: string | undefined
  if (!admitted) {
    listMessage = denied ? "History access is unavailable."
      : list.status === "error" ? "History could not be loaded."
      : list.status === "unavailable" ? "History is unavailable."
      : "Loading history…"
  }
  return <section aria-label="Document history" className="min-w-0 space-y-4 rounded-lg border p-4">
    <div className="flex items-center justify-between gap-3">
      <h2 className="font-semibold">Document history</h2>
      <Button type="button" variant="ghost" className="coarse:min-h-[44px]" onClick={onClose}>Close history</Button>
    </div>
    <p className="text-sm text-muted-foreground">Read-only confirmed checkpoints. Browsing does not change your current draft.</p>
    {listMessage ? <p role={denied || list.status === "error" ? "alert" : "status"}>{listMessage}</p> : <>
      {checkpoints.length === 0 ? <p>No checkpoints yet.</p> : <ul aria-label="Confirmed checkpoints" className="max-h-72 space-y-2 overflow-y-auto">
        {checkpoints.map(checkpoint => <li key={checkpoint.id}>
          <Button type="button" variant="outline" aria-pressed={checkpoint.id === selectedId}
            className="h-auto coarse:min-h-[44px] w-full flex-wrap justify-start whitespace-normal break-all p-3 text-left"
            onClick={() => onSelect(checkpoint.id)}>
            <span>Revision {checkpoint.rev}</span>
            <span className="font-normal">{checkpoint.actor}</span>
            <time dateTime={checkpoint.created_at} className="font-normal">{when(checkpoint.created_at)}</time>
            {!supported(checkpoint.schema) && <span>Unsupported schema</span>}
          </Button>
        </li>)}
      </ul>}
      {selectedId && <div className="space-y-2">
        {exact ? <ConfirmedContent key={`${scopeKey}/${selectedId}/${read.rev}`} snapshot={read} />
          : <p role={snapshot.status === "denied" || snapshot.status === "error" ? "alert" : "status"}>
            {snapshot.status === "loading" ? "Loading checkpoint…"
              : snapshot.status === "denied" ? "Snapshot access is unavailable."
              : snapshot.status === "error" ? "Snapshot could not be loaded."
              : "Snapshot unavailable. Select a confirmed checkpoint to read it."}
          </p>}
      </div>}
    </>}
  </section>
}
