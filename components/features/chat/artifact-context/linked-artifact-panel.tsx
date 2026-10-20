"use client"
import { useCallback, useEffect, useMemo, useState, type RefObject } from "react"
import { ArrowLeft, Maximize2, Minimize2, X } from "lucide-react"
import { Button } from "@/components/ui/button"
import { SavedDocumentPanel } from "../artifacts/saved-document-panel"
import { DocumentReadonly } from "@/components/features/pages/document-readonly/document-readonly"
import { checkpointFetcher } from "./client"
import { readExactRevision, type ExactRevisionRead } from "./exact-revision"
import { targetKey, type ConversationLink, type LinkedViewerTarget } from "./linked-viewer-targets"
import { PinnedRevisionBody } from "./pinned-revision-body"
import { ViewerLinkSwitcher } from "./viewer-link-switcher"

export interface LinkedActive { target: LinkedViewerTarget; title: string | null; activation: number }

interface PanelProps {
  workspaceId: string
  chatId: string
  currentUserId: string
  active: LinkedActive
  links: ConversationLink[]
  layout: "side" | "mobile"
  expanded: boolean
  onExpandedChange: (expanded: boolean) => void
  onSwitch: (target: LinkedViewerTarget, title: string | null) => void
  onUseInMessage: (target: LinkedViewerTarget, title: string | null) => void
  onClose: () => void
  openerRef?: RefObject<HTMLElement | null>
}

/** Pinned revisions are read through the checkpoint reader, never the document endpoint's latest. */
function usePinnedRead(target: Extract<LinkedViewerTarget, { mode: "revision" }>, attempt: number): ExactRevisionRead {
  const [state, setState] = useState<{ key: string; read: ExactRevisionRead }>()
  const key = `${targetKey(target)}:${attempt}`
  useEffect(() => {
    const controller = new AbortController()
    void readExactRevision(checkpointFetcher, target, controller.signal).then(read => { if (!controller.signal.aborted) setState({ key, read }) })
    return () => controller.abort()
  }, [key, target])
  // Mask during render: a different pin never shows the previous pin's content.
  return state?.key === key ? state.read : { status: "loading" }
}

function PinnedPanel({ active, links, layout, expanded, onExpandedChange, onClose, extra, openerRef }: PanelProps & { extra: (ready: boolean) => React.ReactNode }) {
  const target = active.target as Extract<LinkedViewerTarget, { mode: "revision" }>
  const [attempt, setAttempt] = useState(0)
  const read = usePinnedRead(target, attempt)
  const mobile = layout === "mobile"
  const CloseIcon = mobile ? ArrowLeft : X
  // Title and revision are shown only for a verified read; after denial or a missing revision the header names nothing.
  const ready = read.status === "ready"
  const title = !ready ? "Document" : active.title ?? links.find(link => targetKey(link.target) === targetKey(target))?.title ?? "Document"
  const close = () => { onClose(); requestAnimationFrame(() => openerRef?.current?.isConnected && openerRef.current.focus()) }
  return <section aria-label="Document preview" data-layout={layout} data-expanded={expanded} data-pinned="true" className="flex h-full min-w-0 flex-col overflow-hidden border-l bg-background"
    onKeyDown={event => { if (event.key === "Escape" && !event.defaultPrevented) { event.preventDefault(); event.stopPropagation(); close() } }}>
    <header className="flex shrink-0 items-center gap-2 border-b p-3">
      <Button type="button" variant="ghost" size={mobile ? "default" : "icon"} className="coarse:min-h-12 coarse:min-w-12" aria-label={mobile ? "Back to chat" : "Close document preview"} onClick={close}>
        <CloseIcon className="size-4" aria-hidden />{mobile && <span>Back to chat</span>}
      </Button>
      <div className="min-w-0 flex-1">
        <h2 className="truncate font-semibold">{title}</h2>
        {ready && <p className="text-xs text-muted-foreground">Saved revision {target.revision}</p>}
        {extra(ready)}
      </div>
      {!mobile && <Button type="button" variant="ghost" size="icon" className="coarse:min-h-12 coarse:min-w-12" aria-label={expanded ? "Show chat alongside" : "Expand document preview"} aria-pressed={expanded} onClick={() => onExpandedChange(!expanded)}>
        {expanded ? <Minimize2 className="size-4" aria-hidden /> : <Maximize2 className="size-4" aria-hidden />}
      </Button>}
    </header>
    <div className="min-h-0 min-w-0 flex-1 overflow-auto p-4">
      <PinnedRevisionBody read={read}>
        {ready => <DocumentReadonly schema={ready.schema as 2 | 3} content={ready.content} accessibleLabel="Pinned revision body" />}
      </PinnedRevisionBody>
      {read.status === "error" && <Button type="button" size="sm" variant="outline" className="mt-2 coarse:min-h-12" onClick={() => setAttempt(value => value + 1)}>Try again</Button>}
    </div>
  </section>
}

/** The viewer for a conversation-linked item: exact revision through the checkpoint reader, or the explicit latest through the saved-document read. */
export function LinkedArtifactPanel(props: PanelProps) {
  const { active, links, onSwitch, onUseInMessage } = props
  const titleFor = useCallback((target: LinkedViewerTarget) => links.find(link => targetKey(link.target) === targetKey(target))?.title ?? null, [links])
  // Without a verified read the header offers nothing that names the item (the switcher lists titles).
  const extra = useCallback((ready: boolean) => !ready ? null : <div className="mt-1 flex min-w-0 flex-wrap items-center gap-2">
    <ViewerLinkSwitcher links={links} current={active.target}
      onSwitch={target => onSwitch(target, titleFor(target))}
      onViewLatest={() => onSwitch({ workspaceId: active.target.workspaceId, pageId: active.target.pageId, mode: "latest" }, active.title)} />
    <Button type="button" size="sm" variant="outline" className="coarse:min-h-12" onClick={() => onUseInMessage(active.target, active.title)}>Use in message</Button>
  </div>, [links, active, onSwitch, onUseInMessage, titleFor])
  if (active.target.mode === "revision") return <PinnedPanel {...props} extra={extra} />
  return <LatestPanel {...props} extra={extra} />
}

/** The explicit latest of a linked item: the same saved-document panel as a saved Document, with the linked-item controls in its header. */
function LatestPanel({ workspaceId, chatId, currentUserId, active, layout, expanded, onExpandedChange, onClose, openerRef, extra }: PanelProps & { extra: (ready: boolean) => React.ReactNode }) {
  const viewerTarget = useMemo(() => ({ workspaceId: active.target.workspaceId, pageId: active.target.pageId, type: "document" as const, mode: "latest" as const }), [active.target])
  return <SavedDocumentPanel workspaceId={workspaceId} chatId={chatId} currentUserId={currentUserId} activationKey={String(active.activation)} target={viewerTarget} enabled
    layout={layout} expanded={expanded} onExpandedChange={onExpandedChange} onClose={onClose} openerRef={openerRef} headerExtra={extra} />
}
