"use client"

import * as React from "react"
import { createPortal } from "react-dom"
import { History } from "lucide-react"
import { useSessionSafe } from "@/hooks/use-auth"
import { useDocumentViewerRead } from "@/hooks/use-document-viewer-read"
import { DocumentReadonly } from "@/components/features/pages/document-readonly/document-readonly"
import type { NativeCraftHostStatus } from "@/components/features/pages/native-craft-editor-host"
import { ArtifactSlot, useArtifactFrame } from "../../frame"
import { KINDS } from "../../kinds"
import { ArtifactNativeEditor, type NativeAvailability } from "../../native-editor"
import type { LibraryItem } from "../../library-items"

/**
 * Word and Sheet (the Craft family) inside the ArtifactFrame, Harbor 2 v87:
 * the lock lives only in the status chip and its popover; history is a drawer
 * tab; the agent is the drawer's Edit mode; the view slot names the file and
 * its version. The native editor's own ribbon and grid run inside its isolated
 * frame and stay where they are: they are the "tools" area.
 *
 * Presentation only. The lease, save and admission logic stay in the editor
 * host; this file decides only where its controls render.
 */

/**
 * The status popover's lock controls: a target the popover mounts while it is
 * open, and the host's `placeLease` that renders into it. Closed, the controls
 * are not in the page at all; the chip still says who holds the lock.
 */
export function useCraftLeaseTarget() {
  const [target, setTarget] = React.useState<HTMLElement | null>(null)
  const place = React.useCallback((controls: React.ReactNode) => target ? createPortal(controls, target) : null, [target])
  const statusActions = <div ref={setTarget} data-craft-lock className="w-full" />
  return { statusActions, place }
}

/** The popover's foot: the one rule of a Craft file. */
export const CRAFT_LOCK_RULE = "One editor at a time. The lock is a server lease and ends when the tab closes."

/** "v3" for the version the editor shows; nothing until the server named one. */
export const versionLabel = (version?: string) => version && /^\d+$/.test(version) ? `v${version}` : null

/**
 * What am I looking at: the artifact's name and the version shown. Never a
 * storage file name. Opens the History drawer when the body mounted one.
 */
export function CraftViewSlot({ name, kind, version, history }: { name: string; kind: "word" | "sheet"; version?: string; history: boolean }) {
  const frame = useArtifactFrame()
  const Icon = KINDS[kind].icon
  const shown = versionLabel(version)
  const content = <>
    <Icon aria-hidden className="size-4 shrink-0" />
    <span className="max-w-[14rem] truncate text-foreground">{name}</span>
    {shown && <span className="font-mono text-xs">{shown}</span>}
    {shown && <span className="text-xs">Latest</span>}
  </>
  const className = "inline-flex h-[30px] items-center gap-1.5 rounded-[var(--radius-row)] px-2 text-sm text-muted-foreground coarse:min-h-[44px]"
  return <ArtifactSlot name="view" label={`${KINDS[kind].label} view`}>
    {history && frame
      ? <button type="button" data-craft-version aria-label={`${name}${shown ? `, ${shown}, latest` : ""}. Version history`} title="Version history"
        onClick={() => frame.openDrawer("history")} className={`${className} transition-colors hover:bg-accent hover:text-foreground`}>
        {content}<History aria-hidden className="size-3.5 shrink-0" />
      </button>
      : <span data-craft-version className={className}>{content}</span>}
  </ArtifactSlot>
}

/** Mounted inside the History section: tells the body that the drawer has a History tab to open. */
function HistoryPresence({ onChange }: { onChange: (present: boolean) => void }) {
  React.useEffect(() => { onChange(true); return () => onChange(false) }, [onChange])
  return null
}

/**
 * A Word on a phone, while this person is not editing it: the saved version as
 * reflowed text (the same current-version read the library preview uses). It
 * opens no editor surface and writes nothing; editing starts from the popover.
 */
function WordReader({ workspaceId, item }: { workspaceId: string; item: LibraryItem }) {
  const userId = useSessionSafe()?.data?.user?.id ?? null
  const target = React.useMemo(() => item.pageId ? { workspaceId, pageId: item.pageId, type: "document" as const, mode: "latest" as const } : null, [workspaceId, item.pageId])
  const { read } = useDocumentViewerRead({ workspaceId, chatId: "artifacts-library", currentUserId: userId, activationKey: `library:${item.slug}:phone`, target, enabled: target !== null })
  if (read.status === "loading") return <div aria-busy="true" className="m-3 h-40 animate-pulse rounded-card border bg-muted/40" />
  if (read.status !== "ready") return <p role="status" className="m-3 rounded-card border border-dashed p-4 text-sm text-muted-foreground">
    {read.status === "denied" ? "You can't read this document." : "The document could not be loaded. Nothing was changed."}
  </p>
  return <div data-craft-reader className="px-4 pb-6 pt-1 text-base leading-relaxed">
    <DocumentReadonly schema={read.content.schema} content={read.content.content} accessibleLabel={`${item.name}, saved version`} />
  </div>
}

export interface CraftBodyProps {
  workspaceId: string
  item: LibraryItem
  /** The host's last report; drives the phone reader and the version shown. */
  craft: NativeCraftHostStatus | null
  onStatus: (status: NativeCraftHostStatus) => void
  onAvailability: (state: NativeAvailability) => void
  placeLease: (controls: React.ReactNode) => React.ReactNode
  /** Shown when the editor is not confirmed (the saved Word preview). */
  fallback: React.ReactNode
}

/** The stage of a native Word or Sheet. */
export function CraftBody({ workspaceId, item, craft, onStatus, onAvailability, placeLease, fallback }: CraftBodyProps) {
  const frame = useArtifactFrame()
  const kind = item.kind === "sheet" ? "sheet" : "word"
  const [history, setHistory] = React.useState(false)
  // A phone reads a Word reflowed; the editor surface appears once this person holds the lock.
  const reading = kind === "word" && frame?.layout === "phone" && craft?.ready === true && !craft.editing
  return <div data-craft-body={kind} data-craft-reading={reading ? "" : undefined} className="flex min-h-0 flex-1 flex-col pt-12">
    {reading && <WordReader workspaceId={workspaceId} item={item} />}
    <ArtifactNativeEditor workspaceId={workspaceId} pageId={item.pageId!} onStatus={onStatus} onAvailability={onAvailability}
      placeLease={placeLease} editorHidden={reading} fallback={fallback} historyPresence={<HistoryPresence onChange={setHistory} />} />
    <CraftViewSlot name={item.name} kind={kind} version={craft?.ready ? craft.version : undefined} history={history} />
  </div>
}
