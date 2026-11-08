"use client"

import { useEffect, useMemo, useState } from "react"
import { Check, Code2, FolderInput, Pencil, Star } from "lucide-react"
import { Button } from "@/components/ui/button"
import { StatusPill } from "@/components/ui/status-pill"
import { useSessionSafe } from "@/hooks/use-auth"
import { useDocumentViewerRead } from "@/hooks/use-document-viewer-read"
import { DocumentReadonly } from "@/components/features/pages/document-readonly/document-readonly"
import type { NativeCraftHostStatus } from "@/components/features/pages/native-craft-editor-host"
import { formatRelativeShort } from "@/lib/time"
import { artifactDashboards, artifactSlides, artifactsDesign, artifactsLibrary } from "@/lib/feature-flags"
import { ArtifactViewerProvider } from "@/components/features/chat/artifact-context/projects/artifact-viewer-context"
import { useArtifactProjectsAvailable } from "@/components/features/chat/artifact-context/projects/use-artifact-project-data"
import { ArtifactViewerPanel } from "@/components/features/chat/artifact-context/viewer/artifact-viewer-panel"
import { CraftLockBar, craftEditorHref } from "@/components/features/chat/artifact-context/craft/craft-lock-bar"
import { useCraftLeaseState } from "@/components/features/chat/artifact-context/craft/use-craft-lease"
import { readLocalDraft, type LocalDraft } from "@/components/features/chat/artifact-context/craft/local-draft"
import { LocalDraftBanner } from "@/components/features/chat/artifact-context/craft/local-draft-banner"
import { ArtifactFrame, craftFrameStatus, type FrameAction, type FrameCapabilities, type FrameStatus } from "./frame"
import { ArtifactPageContent } from "./page-content"
import { ArtifactProject } from "@/components/features/pages/artifact-project"
import { FramePageEditor } from "./edit/frame-page-editor"
import { LegacyDeckBody, LegacyWorkbookBody } from "./kinds/legacy/legacy-views"
import type { NativeAvailability } from "./native-editor"
import { CRAFT_LOCK_RULE, CraftBody, useCraftLeaseTarget } from "./kinds/craft/craft-body"
import { ProjectKindView } from "./kinds/project-kind-view"
import type { KindReport } from "./kinds/project-parts"
import { artifactsProjectHref, opensAsProject, type LibraryItem } from "./library-items"
import { versioned } from "./library-filter"
import { KINDS, kindOf } from "./kinds"
import { KindBadge } from "./kind-tile"
import { DocsBody, type DocsFrameReport } from "./kinds/docs/docs-view"
import { ShareDialog } from "./sharing/share-dialog"
import { AccessTab } from "./sharing/access-tab"
import { MoveToFolderDialog } from "./sharing/bulk-actions"
import { DesignBody, type KindFrameReport } from "./kinds/design/design-detail"
import { DesignLibraryGate } from "./kinds/design/design-library"
import { SlidesBody } from "./kinds/slides/slides-detail"


/**
 * The saved Word document, read-only: the same current-version read the chat
 * viewer uses. It never opens an editor, a CRDT or a lease, and writes nothing;
 * editing stays in the Craft editor behind the lock bar.
 */
function WordPreview({ workspaceId, item, onStatus }: { workspaceId: string; item: LibraryItem; onStatus?: (status: string) => void }) {
  const userId = useSessionSafe()?.data?.user?.id ?? null
  const target = useMemo(() => item.pageId ? { workspaceId, pageId: item.pageId, type: "document" as const, mode: "latest" as const } : null, [workspaceId, item.pageId])
  // chatId and activationKey only scope the read; the library is its own scope, one per opened item.
  const { read } = useDocumentViewerRead({ workspaceId, chatId: "artifacts-library", currentUserId: userId, activationKey: `library:${item.slug}`, target, enabled: target !== null })
  useEffect(() => { onStatus?.(read.status) }, [onStatus, read.status])
  if (read.status === "loading") return <div aria-busy="true" className="h-40 animate-pulse rounded-card border bg-muted/40" />
  if (read.status !== "ready") return <p role="status" className="rounded-card border border-dashed p-4 text-sm text-muted-foreground">
    {read.status === "denied" ? "You can't read this document."
      // A Word without a version head is from the old editor, which this environment no longer runs.
      : !item.native ? "This document is from an older version of the editor and does not open here. Nothing was changed."
        : read.status === "unavailable" ? "The document service is not answering right now, so there is no preview. Nothing was changed."
          : "The document preview could not be loaded. Nothing was changed."}
  </p>
  return <div className="rounded-card border bg-card">
    <DocumentReadonly schema={read.content.schema} content={read.content.content} accessibleLabel={`${item.name}, saved version`} />
  </div>
}

/**
 * Lost Craft lock: edits this browser kept as a local draft come back for
 * compare or copy, never saved over whoever holds the lock now. Reads the
 * lease only when such a draft exists.
 */
function LockLostDraft({ workspaceId, slug }: { workspaceId: string; slug: string }) {
  const userId = useSessionSafe()?.data?.user?.id ?? ""
  // Keyed by who, where and what: another person signing in remounts the whole
  // recovery in the same render, so no draft or open comparison of theirs survives it.
  return userId ? <OwnLockLostDraft key={JSON.stringify([userId, workspaceId, slug])} userId={userId} workspaceId={workspaceId} slug={slug} /> : null
}

function OwnLockLostDraft({ userId, workspaceId, slug }: { userId: string; workspaceId: string; slug: string }) {
  const [present, setPresent] = useState(false)
  // Read after mount: storage does not exist during prerender.
  useEffect(() => { setPresent(Boolean(readLocalDraft(userId, workspaceId, slug))) }, [userId, workspaceId, slug])
  return present ? <RecoveredDraft userId={userId} workspaceId={workspaceId} slug={slug} /> : null
}

function RecoveredDraft({ userId, workspaceId, slug }: { userId: string; workspaceId: string; slug: string }) {
  const lease = useCraftLeaseState(workspaceId, slug)
  const holder = lease.status?.holder
  const [compare, setCompare] = useState<LocalDraft | null>(null)
  return <>
    <LocalDraftBanner userId={userId} workspaceId={workspaceId} slug={slug} holderName={holder && !lease.heldByMe ? holder.display_name : null} onCompare={setCompare} />
    {compare && <section aria-label="Your local draft" className="grid gap-2 rounded-card border bg-card p-3 text-sm">
      <div className="flex items-center gap-2">
        <b className="font-semibold">Your local draft</b>
        <span className="text-xs text-muted-foreground">based on v{compare.baseVersion}; the saved version is below</span>
        <Button type="button" size="sm" variant="ghost" className="ml-auto coarse:min-h-[44px]" onClick={() => setCompare(null)}>Hide</Button>
      </div>
      <p className="whitespace-pre-wrap">{compare.content}</p>
    </section>}
  </>
}

/** The Craft lock and an Edit that opens the full Craft editor in a new tab. */
function WordLockBar({ workspaceId, pageId }: { workspaceId: string; pageId: string }) {
  return <div className="overflow-hidden rounded-card border">
    <CraftLockBar workspaceId={workspaceId} slug={"@" + pageId}
      onEdit={() => window.open(craftEditorHref(workspaceId, "@" + pageId), "_blank", "noopener,noreferrer")} />
  </div>
}

/** The existing chat, with this artifact as its context and the prompt as an unsent draft (chat-client `?page=`). */
export function chatAskHref(workspaceId: string, slug: string, prompt: string): string {
  const query = new URLSearchParams({ workspace_id: workspaceId, page: slug })
  if (prompt) { query.set("prompt", prompt); query.set("draft", "1") }
  return `/chat?${query}`
}

/** Stale or failed panels from the catalog: the only status a non-Craft item can report today. */
function catalogStatus(item: LibraryItem): FrameStatus | null {
  if ((item.failedPanels ?? 0) > 0) return { key: "stale", label: "Refresh failed", detail: `${item.failedPanels} ${item.failedPanels === 1 ? "panel" : "panels"} failed to refresh and keep their last confirmed data.` }
  if ((item.stalePanels ?? 0) > 0) return { key: "stale", label: `Stale · ${item.stalePanels} ${item.stalePanels === 1 ? "panel" : "panels"}`, detail: "Some panels are past their refresh window. Values keep their last confirmed state." }
  return null
}

function ArtifactInfo({ workspaceId, item, onShare }: { workspaceId: string; item: LibraryItem; onShare: () => void }) {
  const resolved = kindOf(item)
  const facts: [string, React.ReactNode][] = [
    ["Kind", <KindBadge key="kind" resolved={resolved} />],
    ["Version", versioned(item)
      ? item.sharedVersion ? <StatusPill tone="success" label={`Shared v${item.sharedVersion}`} /> : <StatusPill tone="muted" label="Not shared" />
      : "No versions"],
    ["Owner", item.ownerLabel ?? "Someone else"],
    ["Folder", item.folder?.name ?? "Unfiled"],
    ["Edited", item.updatedAt ? formatRelativeShort(item.updatedAt.toISOString()) : "—"],
    ...(item.pageId ? [["Link", <span key="link" className="break-all font-mono text-xs text-muted-foreground">/artifacts/{item.pageId}</span>] as [string, React.ReactNode]] : []),
  ]
  return <div className="grid content-start">
    <dl className="m-0 grid grid-cols-[6.5rem_1fr] gap-x-3 gap-y-2.5 p-3.5 text-sm">
      {facts.map(([term, value]) => <div key={term} className="contents">
        <dt className="text-muted-foreground">{term}</dt>
        <dd className="m-0 flex min-w-0 flex-wrap items-center gap-1.5">{value}</dd>
      </div>)}
    </dl>
    {item.pageId && <p className="px-3.5 pb-2 text-xs text-muted-foreground">The address never changes when the artifact is renamed or moved.</p>}
    <div className="border-t"><AccessTab workspaceId={workspaceId} item={item} onShare={onShare} /></div>
  </div>
}

/**
 * One artifact inside the ArtifactFrame. The CURRENT body of each kind sits in
 * the stage unchanged (design editor, native Craft editor, project viewer,
 * Page content, Word preview); per-kind views move their controls into slots.
 * Nothing links out to /pages (owner, 8 Nov 2026): a kind whose own view is
 * behind a build switch opens read-only in the view Pages used to show, and
 * `edit` (`/artifacts/<id>?edit=1`) hosts the editor Pages used to open.
 */
export function ArtifactDetail({ workspaceId, item, version, folders, onBack, favorite, onFavorite, edit = false, onEditChange }: {
  workspaceId: string; item: LibraryItem; version?: number; folders: { slug: string; name: string }[]; onBack: () => void; favorite: boolean; onFavorite: () => void
  /** `?edit=1`: the App source editor or the panel editor, inside this frame. */
  edit?: boolean
  /** The frame entered or left the editor; the route keeps `?edit=1` in step. */
  onEditChange?: (edit: boolean) => void
}) {
  const resolved = kindOf(item)
  const [dialog, setDialog] = useState<"share" | "move" | null>(null)
  const [craft, setCraft] = useState<NativeCraftHostStatus | null>(null)
  const [availability, setAvailability] = useState<NativeAvailability>()
  // Status, edit right, presence and actions a kind body reports (Design, Slides).
  const [kindFrame, setKindFrame] = useState<KindFrameReport | null>(null)
  const craftLease = useCraftLeaseTarget()
  const projects = useArtifactProjectsAvailable(workspaceId)
  const [editing, setEditingState] = useState(edit)
  useEffect(() => { setEditingState(edit) }, [edit])
  const setEditing = (next: boolean) => { setEditingState(next); onEditChange?.(next) }
  const design = item.kind === "design" && artifactsDesign()
  const project = opensAsProject(item) && Boolean(item.pageId)
  // The App source editor: the project's own view with its source, restore and share controls.
  const sourceEditor = project && editing
  // The kind's own project viewer, where the server serves it; a Dashboard and Slides only behind their switches.
  const projectPreview = project && !sourceEditor && projects
  // An App or Dashboard project lays itself out in the frame and reports its chip and edit right.
  const dataApp = projectPreview && (resolved.kind === "app" || resolved.kind === "dashboard" && artifactDashboards())
  // Without the project viewer, the project's own view, read-only: never a link to its old Pages view.
  const projectReadOnly = project && !sourceEditor && !projectPreview
  const [projectWrite, setProjectWrite] = useState(false)
  const [kindReport, setKindReport] = useState<KindReport | null>(null)
  // A legacy Page shows its own content here, with the views Pages draws; `edit` opens its panel editor.
  const pageContent = item.kind === "page" && Boolean(item.pageId)
  const pageEditor = pageContent && editing
  // Behind the single-home switch a native Word or Sheet is edited here, in the
  // one Craft editor and its lease; /pages/<slug> sends it back here.
  const nativeEditor = artifactsLibrary() && item.native && (item.kind === "word" || item.kind === "sheet") && Boolean(item.pageId)
  // Behind the same switch a Docs page (a native Tiptap/Yjs document) opens in its Docs view here; Canvas has no backend yet.
  const docsPage = artifactsLibrary() && resolved.kind === "docs" && resolved.mode !== "canvas" && Boolean(item.pageId)
  const [docs, setDocs] = useState<DocsFrameReport | null>(null)
  // Older storage Pages drew itself: a deck and a workbook without a version head.
  const legacyDeck = item.kind === "slides" && !item.projectBacked && Boolean(item.pageId)
  const legacyWorkbook = item.kind === "sheet" && !item.native && Boolean(item.pageId)
  // A pinned address (a shared-version link) opens that version, not Latest.
  const target = useMemo(() => ({ slug: item.slug, tab: "preview" as const, ...(version === undefined ? {} : { version }) }), [item.slug, version])
  const projectRef = item.pageId ? "@" + item.pageId : item.slug
  const versionHref = (pinned: number) => artifactsProjectHref(item.pageId ?? "", workspaceId, pinned)

  const nativeStatus = nativeEditor && availability === "enabled" && craft ? craftFrameStatus(craft)
    : nativeEditor && (availability === "readonly" || availability === "preview") ? { key: "readonly" as const } : null
  const editAction: FrameAction[] = sourceEditor || pageEditor
    ? [{ id: "done-editing", label: "Done editing", icon: Check, onSelect: () => setEditing(false), separated: true }]
    : projectReadOnly && projectWrite ? [{ id: "edit-source", label: "Edit source", icon: Code2, onSelect: () => setEditing(true), separated: true }]
      : pageContent ? [{ id: "edit", label: "Edit", icon: Pencil, onSelect: () => setEditing(true), separated: true }]
        : []
  const actions: FrameAction[] = [
    { id: "move", label: "Move to folder…", icon: FolderInput, onSelect: () => setDialog("move"),
      ...(item.canMove === false ? { disabledReason: "You can't move this artifact" } : {}) },
    { id: "favorite", label: favorite ? "Remove from favorites" : "Add to favorites", icon: Star, onSelect: onFavorite },
    ...editAction,
  ]
  const caps: FrameCapabilities = {
    access: "ok",
    canEdit: kindFrame ? kindFrame.canEdit : nativeEditor && availability === "enabled" ? craft?.canEdit === true : docsPage ? docs?.canEdit === true
      : sourceEditor || projectReadOnly ? projectWrite : kindReport?.canEdit === true,
    status: kindFrame ? kindFrame.status : docsPage ? docs?.status ?? null : nativeStatus ?? kindReport?.status
      ?? (projectReadOnly ? { key: "readonly" as const } : sourceEditor ? null : catalogStatus(item)),
    agent: {
      ask: { chatHref: prompt => chatAskHref(workspaceId, item.slug, prompt) },
      permission: "The agent reads this artifact with your access. Ask changes nothing.",
    },
    actions: [...actions, ...(kindFrame?.actions ?? [])],
  }

  const documentColumn = (children: React.ReactNode) => <div className="mx-auto grid w-full max-w-5xl content-start gap-3 p-4 md:p-6">{children}</div>
  const hostedProject = (readOnly: boolean) => <ArtifactProject key={`${workspaceId}:${projectRef}:${readOnly ? "read" : "edit"}`} workspaceId={workspaceId} slug={projectRef} initialVersion={version}
    hosted={{ readOnly, versionHref, onCanWrite: setProjectWrite }} />
  let body: React.ReactNode
  if (design) body = <DesignBody workspaceId={workspaceId} slug={item.pageId ? "@" + item.pageId : item.slug} onFrame={setKindFrame} />
  else if (sourceEditor) body = hostedProject(false)
  else if (projectPreview && item.kind === "slides" && artifactSlides()) body = <SlidesBody workspaceId={workspaceId} slug={item.slug} version={version} onFrame={setKindFrame} />
  else if (dataApp) body = <ProjectKindView workspaceId={workspaceId} target={target} kind={resolved.kind as "app" | "dashboard"} onBack={onBack} onReport={setKindReport} />
  // A project whose manifest is a design system opens as a Design library; any other project keeps its viewer.
  else if (projectPreview) body = <DesignLibraryGate workspaceId={workspaceId} slug={item.slug} version={version} onFrame={setKindFrame}>
    <ArtifactViewerProvider workspaceId={workspaceId} target={target} onTargetChange={next => { if (!next) onBack() }}>
      <div className="flex min-h-[520px] flex-1 overflow-hidden"><ArtifactViewerPanel variant="library" /></div>
    </ArtifactViewerProvider>
  </DesignLibraryGate>
  else if (projectReadOnly) body = hostedProject(true)
  else if (pageEditor) body = <FramePageEditor workspaceId={workspaceId} pageId={item.pageId!} onLeave={reason => reason === "deleted" ? onBack() : setEditing(false)} />
  else if (pageContent) body = <ArtifactPageContent workspaceId={workspaceId} pageId={item.pageId!} onBack={onBack} onReport={setKindReport} />
  else if (docsPage) body = <DocsBody workspaceId={workspaceId} item={item} onFrame={setDocs}
    fallback={documentColumn(<WordPreview workspaceId={workspaceId} item={item} />)} />
  else if (nativeEditor) body = <div className="flex min-h-0 flex-1 flex-col">
    {item.kind === "word" && <LockLostDraft workspaceId={workspaceId} slug={"@" + item.pageId} />}
    {/* Without the editor here there is none elsewhere either: the fallback offers no Edit, only the saved version. */}
    <CraftBody workspaceId={workspaceId} item={item} craft={craft} onStatus={setCraft} onAvailability={setAvailability} placeLease={craftLease.place}
      fallback={item.kind === "word" && !item.empty ? documentColumn(<WordPreview workspaceId={workspaceId} item={item} />) : null} />
  </div>
  else if (legacyDeck) body = <LegacyDeckBody workspaceId={workspaceId} pageId={item.pageId!} name={item.name} />
  else if (legacyWorkbook) body = <LegacyWorkbookBody workspaceId={workspaceId} pageId={item.pageId!} name={item.name} />
  else body = documentColumn(<>
    {item.native && item.kind === "word" && item.pageId && <WordLockBar workspaceId={workspaceId} pageId={item.pageId} />}
    {item.native && item.kind === "word" && item.pageId && <LockLostDraft workspaceId={workspaceId} slug={"@" + item.pageId} />}
    {item.kind === "word" && !item.empty && item.pageId && <WordPreview workspaceId={workspaceId} item={item} />}
    {!item.pageId && <p role="status" className="rounded-card border border-dashed p-4 text-sm text-muted-foreground">This artifact has no stable address yet, so it can&apos;t be shown here. Nothing was changed.</p>}
  </>)

  return <ArtifactFrame name={item.name} kind={kindFrame?.kind ?? resolved} folder={item.folder?.name ?? null} caps={caps} onHome={onBack}
    href={item.pageId ? `/artifacts/${encodeURIComponent(item.pageId)}` : undefined} onShare={() => setDialog("share")} presence={kindFrame?.presence}
    statusActions={docsPage ? docs?.statusActions : nativeEditor ? craftLease.statusActions : undefined}
    statusFoot={docsPage && docs?.statusFoot ? docs.statusFoot : item.kind === "word" || item.kind === "sheet" ? CRAFT_LOCK_RULE : `${KINDS[resolved.kind].label}${item.sharedVersion ? ` · shared v${item.sharedVersion}` : ""}${item.ownerLabel ? ` · owned by ${item.ownerLabel}` : ""}`}
    info={<ArtifactInfo workspaceId={workspaceId} item={item} onShare={() => setDialog("share")} />}>
    {body}
    {dialog === "share" && <ShareDialog workspaceId={workspaceId} item={item} open onOpenChange={open => { if (!open) setDialog(null) }} />}
    {dialog === "move" && <MoveToFolderDialog workspaceId={workspaceId} item={item} folders={folders} open
      onOpenChange={open => { if (!open) setDialog(null) }} onDone={() => setDialog(null)} />}
  </ArtifactFrame>
}
