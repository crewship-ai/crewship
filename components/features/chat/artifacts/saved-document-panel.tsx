"use client"

import { useEffect, useRef, useState, type ReactNode } from "react"
import { ExternalLink, Pencil } from "lucide-react"
import { Button } from "@/components/ui/button"
import { chatCraftLease } from "@/lib/feature-flags"
import { useDocumentViewerRead, type DocumentViewerReadInput } from "@/hooks/use-document-viewer-read"
import { DocumentReadonly } from "@/components/features/pages/document-readonly/document-readonly"
import { PageDocument } from "@/components/features/pages/page-document"
import { DocumentViewer } from "../artifact-viewer/document-viewer"
import { CraftLockBarView, craftEditorHref } from "../artifact-context/craft/craft-lock-bar"
import { useCraftLeaseState } from "../artifact-context/craft/use-craft-lease"
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
  // Edit mode belongs to one human, chat, activation and Page: any change ends it
  // for good, so returning to an earlier identity does not reopen the editor.
  const identity = JSON.stringify([props.currentUserId, props.workspaceId, props.chatId, props.activationKey, props.target?.pageId, props.enabled])
  const [editing, setEditing] = useState<string | null>(null)
  const isEditing = editing === identity
  useEffect(() => { if (editing !== null && editing !== identity) setEditing(null) }, [editing, identity])
  const [dirty, setDirty] = useState(false)
  // pending || recovery from the editor: some may never drain (a refused or
  // blocked write), so this never disables Done, it only asks first.
  const unsent = isEditing && dirty
  const [asking, setAsking] = useState<{ identity: string; action: "close" | "done" } | null>(null)
  const ask = asking?.identity === identity && isEditing ? asking.action : null
  const actionButton = useRef<HTMLButtonElement>(null)
  const { scopeKey, read, refresh } = useDocumentViewerRead({ ...props, hold: isEditing })
  const done = () => { setAsking(null); setEditing(null); refresh() }
  const close = () => { if (unsent) setAsking({ identity, action: "close" }); else onClose() }

  const editHere = <Button ref={actionButton} type="button" size="sm" className="coarse:min-h-[44px]" onClick={() => setEditing(identity)}>
    <Pencil className="size-4" aria-hidden />Edit here
  </Button>
  return <DocumentViewer {...props} onClose={close} scopeKey={scopeKey} read={read}
    headerExtra={typeof headerExtra === "function" ? headerExtra(read.status === "ready") : headerExtra}
    renderBody={document => <div className="space-y-3">
      {ask && <LeaveWithUnsent action={ask} onKeep={() => { setAsking(null); actionButton.current?.focus() }} onLeave={ask === "close" ? onClose : done} />}
      <div className="flex flex-wrap items-center gap-2">
        {isEditing
          ? <>
            <Button ref={actionButton} type="button" size="sm" className="coarse:min-h-[44px]" onClick={() => unsent ? setAsking({ identity, action: "done" }) : done()}>Done editing</Button>
            {unsent && <span role="status" className="text-xs text-muted-foreground">Edits not saved yet</span>}
          </>
          : chatCraftLease()
            ? <CraftEditControl workspaceId={document.target.workspaceId} pageId={document.target.pageId}>{editHere}</CraftEditControl>
            : editHere}
        <Button asChild variant="outline" size="sm" className="coarse:min-h-[44px]">
          <a href={`/pages?${new URLSearchParams({ ref: document.target.pageId, workspace_id: document.target.workspaceId })}`}
            target="_blank" rel="noopener noreferrer">
            Open full editor<ExternalLink className="size-4" aria-hidden />
          </a>
        </Button>
      </div>
      {isEditing
        ? <PageDocument workspaceId={document.target.workspaceId} slug={"@" + document.target.pageId} pageId={document.target.pageId}
          name={document.title} onDirtyChange={setDirty} />
        : <DocumentReadonly schema={document.content.schema} content={document.content.content} accessibleLabel="Saved document body"
          emptyText="This document is empty. Choose Edit here to start writing, or ask the agent in this chat to draft it." />}
    </div>} />
}

/** On a native Craft Page the lease decides who edits: the lock bar replaces Edit here, and Edit
 *  opens the native editor, which takes the lease on its own admission. One admission per panel. */
function CraftEditControl({ workspaceId, pageId, children }: { workspaceId: string; pageId: string; children: ReactNode }) {
  const slug = "@" + pageId
  const lease = useCraftLeaseState(workspaceId, slug)
  if (!lease.status) return children
  return <div className="basis-full overflow-hidden rounded-md border">
    <CraftLockBarView workspaceId={workspaceId} slug={slug} lease={lease}
      onEdit={() => window.open(craftEditorHref(workspaceId, slug), "_blank", "noopener,noreferrer")} />
  </div>
}

function LeaveWithUnsent({ action, onKeep, onLeave }: { action: "close" | "done"; onKeep: () => void; onLeave: () => void }) {
  const keep = useRef<HTMLButtonElement>(null)
  useEffect(() => { keep.current?.focus() }, [])
  const title = action === "close" ? "Close with unsent edits?" : "Stop editing with unsent edits?"
  // Only what is certain: whether this browser kept a copy depends on its
  // storage, and the editor says so itself when it could not.
  return <div role="alertdialog" aria-label={title} aria-describedby="saved-document-unsent"
    className="space-y-2 rounded-md border border-warn/40 bg-warn/10 p-3 text-sm"
    onKeyDown={event => { if (event.key !== "Escape") return; event.preventDefault(); event.stopPropagation(); onKeep() }}>
    <p className="font-medium">{title}</p>
    <p id="saved-document-unsent" className="text-muted-foreground">Some of your edits have not reached the server. If you leave now they may be lost; the editor below says whether this browser kept a copy.</p>
    <div className="flex flex-wrap gap-2">
      <Button ref={keep} type="button" size="sm" className="coarse:min-h-[44px]" onClick={onKeep}>Keep editing</Button>
      <Button type="button" size="sm" variant="outline" className="coarse:min-h-[44px]" onClick={onLeave}>{action === "close" ? "Close anyway" : "Stop editing"}</Button>
    </div>
  </div>
}
