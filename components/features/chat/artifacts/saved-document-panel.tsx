"use client"

import { useEffect, useRef, useState, type ReactNode } from "react"
import { ExternalLink, Pencil } from "lucide-react"
import { Button } from "@/components/ui/button"
import { useDocumentViewerRead, type DocumentViewerReadInput } from "@/hooks/use-document-viewer-read"
import { DocumentReadonly } from "@/components/features/pages/document-readonly/document-readonly"
import { PageDocument } from "@/components/features/pages/page-document"
import { DocumentViewer } from "../artifact-viewer/document-viewer"
import type { DocumentViewerProps } from "../artifact-viewer/types"

export type SavedDocumentPanelProps = Omit<DocumentViewerReadInput, "hold"> & Pick<DocumentViewerProps,
  "layout" | "expanded" | "onClose" | "onExpandedChange" | "openerRef"> & {
  /** Optional host controls under the title; a function receives whether the read is verified (linked-item switcher). Absent leaves the header unchanged. */
  headerExtra?: ReactNode | ((ready: boolean) => ReactNode)
}

/** Current saved content, resolved with the current human's authority on each activation.
 *  "Edit here" swaps the read-only body for the Page's own editor; the server
 *  still authorizes every write, and Done re-reads the saved document. */
export function SavedDocumentPanel({ headerExtra, onClose, ...props }: SavedDocumentPanelProps) {
  // Edit mode belongs to one human, chat, activation and Page: any change ends it.
  const identity = JSON.stringify([props.currentUserId, props.workspaceId, props.chatId, props.activationKey, props.target?.pageId, props.enabled])
  const [editing, setEditing] = useState<string | null>(null)
  const isEditing = editing === identity
  const [dirty, setDirty] = useState(false)
  const unsent = isEditing && dirty
  const [askingFor, setAskingFor] = useState<string | null>(null)
  const asking = isEditing && askingFor === identity
  const setAsking = (on: boolean) => setAskingFor(on ? identity : null)
  const { scopeKey, read, refresh } = useDocumentViewerRead({ ...props, hold: isEditing })
  const close = () => { if (isEditing && unsent) setAsking(true); else onClose() }
  return <DocumentViewer {...props} onClose={close} scopeKey={scopeKey} read={read}
    headerExtra={typeof headerExtra === "function" ? headerExtra(read.status === "ready") : headerExtra}
    renderBody={document => <div className="space-y-3">
      {asking && <CloseWithUnsent onKeep={() => setAsking(false)} onClose={onClose} />}
      <div className="flex flex-wrap items-center gap-2">
        {isEditing
          ? <>
            <Button type="button" size="sm" className="coarse:min-h-[44px]" disabled={unsent} onClick={() => { setEditing(null); refresh() }}>Done editing</Button>
            {unsent && <span role="status" className="text-xs text-muted-foreground">Sending your edits…</span>}
          </>
          : <Button type="button" size="sm" className="coarse:min-h-[44px]" onClick={() => setEditing(identity)}>
            <Pencil className="size-4" aria-hidden />Edit here
          </Button>}
        <Button asChild variant="outline" size="sm" className="coarse:min-h-[44px]">
          <a href={`/pages?${new URLSearchParams({ ref: "@" + document.target.pageId, workspace_id: document.target.workspaceId })}`}
            target="_blank" rel="noopener noreferrer">
            Open full editor<ExternalLink className="size-4" aria-hidden />
          </a>
        </Button>
      </div>
      {isEditing
        ? <PageDocument workspaceId={document.target.workspaceId} slug={"@" + document.target.pageId} pageId={document.target.pageId}
          name={document.title} onDirtyChange={setDirty} />
        : <DocumentReadonly schema={document.content.schema} content={document.content.content} accessibleLabel="Saved document body" />}
    </div>} />
}

function CloseWithUnsent({ onKeep, onClose }: { onKeep: () => void; onClose: () => void }) {
  const keep = useRef<HTMLButtonElement>(null)
  useEffect(() => { keep.current?.focus() }, [])
  return <div role="alertdialog" aria-label="Close with unsent edits?" aria-describedby="saved-document-unsent" className="space-y-2 rounded-md border border-warn/40 bg-warn/10 p-3 text-sm">
    <p className="font-medium">Close with unsent edits?</p>
    <p id="saved-document-unsent" className="text-muted-foreground">Your latest edits have not reached the server yet. They stay in this browser, and reopening this document offers to recover them.</p>
    <div className="flex flex-wrap gap-2">
      <Button ref={keep} type="button" size="sm" className="coarse:min-h-[44px]" onClick={onKeep}>Keep editing</Button>
      <Button type="button" size="sm" variant="outline" className="coarse:min-h-[44px]" onClick={onClose}>Close anyway</Button>
    </div>
  </div>
}
