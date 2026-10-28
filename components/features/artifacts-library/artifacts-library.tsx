"use client"
import { useMemo, useState } from "react"
import { AppWindow, ArrowLeft, ExternalLink, FileText, LayoutDashboard, Lock, Presentation, Sheet, type LucideIcon } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Skeleton } from "@/components/ui/skeleton"
import { usePages } from "@/hooks/use-pages"
import { formatRelativeShort } from "@/lib/time"
import { cn } from "@/lib/utils"
import { ArtifactViewerProvider } from "@/components/features/chat/artifact-context/projects/artifact-viewer-context"
import { useArtifactProjectsAvailable } from "@/components/features/chat/artifact-context/projects/use-artifact-project-data"
import { ArtifactViewerPanel } from "@/components/features/chat/artifact-context/viewer/artifact-viewer-panel"
import { CraftLockBar, craftEditorHref } from "@/components/features/chat/artifact-context/craft/craft-lock-bar"
import { kindCounts, kindLabel, LIBRARY_KINDS, libraryItems, type LibraryItem, type LibraryKind } from "./library-items"

const KIND_ICON: Record<LibraryKind, LucideIcon> = { project: AppWindow, word: FileText, sheet: Sheet, slides: Presentation, dashboard: LayoutDashboard }
const CHIP = "inline-flex items-center gap-1 rounded-full px-1.5 text-micro font-semibold leading-[1.6]"

const pagesHref = (workspaceId: string, slug: string) => `/pages/${encodeURIComponent(slug)}?${new URLSearchParams({ workspace_id: workspaceId })}`
/** Project, and Word or Sheet stored natively, carry a version head; only they can say what is shared. */
const versioned = (item: LibraryItem) => item.kind === "project" || item.native

function KindTile({ kind, large }: { kind: LibraryKind; large?: boolean }) {
  const Icon = KIND_ICON[kind]
  return <span aria-hidden="true" className={cn("flex shrink-0 items-center justify-center rounded-[28%] bg-primary/10 text-primary", large ? "size-10" : "size-8")}>
    <Icon className={large ? "size-5" : "size-4"} />
  </span>
}

function KindBadge({ kind }: { kind: LibraryKind }) {
  return <span className="shrink-0 rounded-sm border bg-muted px-1.5 text-micro font-medium leading-[1.6] text-muted-foreground">{kindLabel(kind)}</span>
}

function StateChips({ item }: { item: LibraryItem }) {
  return <>
    {versioned(item) && (item.sharedVersion
      ? <span className={cn(CHIP, "bg-success/10 text-success")}>Shared v{item.sharedVersion}</span>
      : <span className={cn(CHIP, "bg-muted text-muted-foreground ring-1 ring-inset ring-border")}>Not shared</span>)}
    {item.private && <span className={cn(CHIP, "bg-muted text-muted-foreground")}><Lock aria-hidden className="size-2.5" />Private</span>}
  </>
}

const meta = (item: LibraryItem) => [item.ownerLabel, item.updatedAt ? formatRelativeShort(item.updatedAt.toISOString()) : null].filter(Boolean).join(" · ")

function GalleryCard({ item, onOpen }: { item: LibraryItem; onOpen: () => void }) {
  return <li className="min-w-0">
    <button type="button" aria-label={`Open ${item.name}`} onClick={onOpen}
      className="grid h-full w-full min-w-0 content-start gap-2.5 rounded-card border bg-card p-3 text-left transition-colors hover:border-foreground/20 focus-visible:outline-2 focus-visible:outline-primary">
      <span className="flex min-w-0 items-center gap-2.5">
        <KindTile kind={item.kind} large />
        <span className="grid min-w-0 gap-0.5">
          <h3 className="truncate text-sm font-semibold">{item.name}</h3>
          <small className="truncate text-xs text-muted-foreground">{meta(item)}</small>
        </span>
      </span>
      <span className="flex min-w-0 flex-wrap items-center gap-1.5"><KindBadge kind={item.kind} /><StateChips item={item} /></span>
    </button>
  </li>
}

function LibraryTable({ items, onOpen }: { items: LibraryItem[]; onOpen: (slug: string) => void }) {
  return <div className="overflow-x-auto rounded-card border bg-card">
    <table className="w-full text-sm">
      <thead className="bg-muted/50 text-left text-xs text-muted-foreground">
        <tr><th className="px-3 py-2 font-medium">Name</th><th className="px-3 py-2 font-medium">Kind</th><th className="px-3 py-2 font-medium">Shared</th><th className="px-3 py-2 font-medium">Updated</th></tr>
      </thead>
      <tbody>
        {items.map(item => <tr key={item.slug} className="border-t">
          <td className="max-w-[320px] px-3 py-2">
            <button type="button" className="flex min-w-0 items-center gap-2 font-medium hover:underline" onClick={() => onOpen(item.slug)}>
              <KindTile kind={item.kind} /><span className="truncate">{item.name}</span>
            </button>
          </td>
          <td className="px-3 py-2"><KindBadge kind={item.kind} /></td>
          <td className="px-3 py-2 text-xs">{versioned(item) ? item.sharedVersion ? <span className="font-mono">v{item.sharedVersion}</span> : <span className="text-muted-foreground">Not shared</span> : "—"}</td>
          <td className="whitespace-nowrap px-3 py-2 text-xs text-muted-foreground">{item.updatedAt ? formatRelativeShort(item.updatedAt.toISOString()) : "—"}</td>
        </tr>)}
      </tbody>
    </table>
  </div>
}

function OpenInPages({ workspaceId, slug }: { workspaceId: string; slug: string }) {
  return <Button asChild size="sm" variant="outline" className="coarse:min-h-[44px]">
    <a href={pagesHref(workspaceId, slug)}>Open in Pages<ExternalLink aria-hidden /></a>
  </Button>
}

/** One artifact: a project shows its history in the project viewer, a native Word or Sheet its edit lock. */
function ArtifactDetail({ workspaceId, item, onBack }: { workspaceId: string; item: LibraryItem; onBack: () => void }) {
  const projects = useArtifactProjectsAvailable(workspaceId)
  const target = useMemo(() => ({ slug: item.slug, tab: "preview" as const }), [item.slug])
  return <div className="flex min-h-0 flex-1 flex-col gap-3">
    <div><Button type="button" variant="ghost" size="sm" className="-ml-2 coarse:min-h-[44px]" onClick={onBack}><ArrowLeft aria-hidden />All artifacts</Button></div>
    <header className="flex min-w-0 flex-wrap items-center gap-3">
      <KindTile kind={item.kind} large />
      <div className="grid min-w-0 flex-[1_1_220px] gap-1">
        <h1 className="truncate text-xl font-semibold">{item.name}</h1>
        <p className="flex min-w-0 flex-wrap items-center gap-1.5 text-xs text-muted-foreground"><KindBadge kind={item.kind} /><StateChips item={item} /><span>{meta(item)}</span></p>
      </div>
      {!(item.kind === "project" && projects) && <OpenInPages workspaceId={workspaceId} slug={item.slug} />}
    </header>
    {item.kind === "project" && projects
      ? <ArtifactViewerProvider workspaceId={workspaceId} target={target} onTargetChange={next => { if (!next) onBack() }}>
        <div className="flex min-h-[520px] flex-1 overflow-hidden rounded-card border"><ArtifactViewerPanel variant="library" /></div>
      </ArtifactViewerProvider>
      : item.native && item.kind === "word" && item.pageId
        ? <div className="overflow-hidden rounded-card border">
          <CraftLockBar workspaceId={workspaceId} slug={"@" + item.pageId}
            onEdit={() => window.open(craftEditorHref(workspaceId, "@" + item.pageId), "_blank", "noopener,noreferrer")} />
        </div>
        : null}
    {item.kind !== "project" && <p className="text-sm text-muted-foreground">
      {item.kind === "word" || item.kind === "sheet" ? "Read and edit it in its editor; Open in Pages shows the saved document." : "Open in Pages shows it with everything it contains."}
    </p>}
  </div>
}

interface ArtifactsLibraryProps {
  workspaceId: string
  /** The open artifact's slug, or null for the gallery. Kept in the URL by the route. */
  item: string | null
  onSelect: (slug: string | null) => void
}

/**
 * The Artifacts library: everything people and agents published that has a
 * backend to open it, newest first, as a gallery or a table, filtered by kind.
 * Detail shows a project's version history; Word and Sheet edit in their own editors.
 */
export function ArtifactsLibrary({ workspaceId, item, onSelect }: ArtifactsLibraryProps) {
  const { pages, loading, error, refresh } = usePages(workspaceId)
  const items = useMemo(() => libraryItems(pages), [pages])
  const [kind, setKind] = useState<LibraryKind | "all">("all")
  const [view, setView] = useState<"gallery" | "table">("gallery")

  if (loading) {
    return <div aria-busy="true" aria-label="Loading artifacts" className="grid gap-3 p-6">
      <Skeleton className="h-7 w-40" />
      <div className="grid grid-cols-[repeat(auto-fill,minmax(220px,1fr))] gap-3">{[0, 1, 2, 3].map(i => <Skeleton key={i} className="h-24" />)}</div>
    </div>
  }
  if (error && !items.length) {
    return <div className="grid gap-2 p-6 text-sm">
      <p className="text-muted-foreground">Artifacts could not be loaded.</p>
      <div><Button type="button" size="sm" variant="outline" onClick={refresh}>Try again</Button></div>
    </div>
  }

  if (item !== null) {
    const selected = items.find(entry => entry.slug === item)
    return <div className="flex min-h-full flex-col p-4 sm:p-6">
      {selected ? <ArtifactDetail workspaceId={workspaceId} item={selected} onBack={() => onSelect(null)} />
        : <div className="grid gap-2 text-sm">
          <p>This artifact is not available.</p>
          <div><Button type="button" size="sm" variant="outline" onClick={() => onSelect(null)}><ArrowLeft aria-hidden />All artifacts</Button></div>
        </div>}
    </div>
  }

  const counts = kindCounts(items)
  const shown = kind === "all" ? items : items.filter(entry => entry.kind === kind)
  return <div className="grid content-start gap-4 p-4 sm:p-6">
    <header className="grid gap-1">
      <h1 className="text-xl font-semibold">Artifacts</h1>
      <p className="text-sm text-muted-foreground">Projects, documents and dashboards people and agents published, newest first.</p>
    </header>
    {!items.length
      ? <p className="rounded-card border border-dashed p-6 text-sm text-muted-foreground">No artifacts yet. Ask an agent in Chat to make a project, or create a document in Pages.</p>
      : <>
        <div className="grid min-w-0 gap-2 sm:flex sm:flex-wrap sm:items-center">
          {/* One scrolling row on a phone: wrapped 44px chips would push the gallery off the first screen. */}
          <div className="flex min-w-0 flex-[1_1_240px] gap-1.5 overflow-x-auto [scrollbar-width:none] sm:flex-wrap sm:overflow-visible" role="group" aria-label="Kind">
            {[["all", "All", items.length] as const, ...counts.map(([k, n]) => [k, LIBRARY_KINDS.find(entry => entry.kind === k)!.plural, n] as const)].map(([key, label, n]) =>
              <button key={key} type="button" aria-pressed={kind === key} onClick={() => setKind(key)}
                className={cn("inline-flex h-7 shrink-0 items-center gap-1.5 rounded-full border px-2.5 text-xs coarse:min-h-[44px]",
                  kind === key ? "border-primary/40 bg-primary/10 text-primary" : "bg-card text-muted-foreground hover:text-foreground")}>
                {label} <span className="font-mono text-micro">{n}</span>
              </button>)}
          </div>
          <div role="radiogroup" aria-label="View" className="inline-flex w-fit rounded-md border bg-muted p-0.5">
            {(["gallery", "table"] as const).map(option => <button key={option} type="button" role="radio" aria-checked={view === option} onClick={() => setView(option)}
              className={cn("rounded-sm px-2.5 py-0.5 text-xs coarse:min-h-[44px]", view === option ? "bg-card font-medium shadow-sm" : "text-muted-foreground")}>
              {option === "gallery" ? "Gallery" : "Table"}
            </button>)}
          </div>
        </div>
        {view === "gallery"
          ? <ul aria-label="Artifacts" className="grid grid-cols-[repeat(auto-fill,minmax(min(100%,220px),1fr))] gap-3">
            {shown.map(entry => <GalleryCard key={entry.slug} item={entry} onOpen={() => onSelect(entry.slug)} />)}
          </ul>
          : <LibraryTable items={shown} onOpen={onSelect} />}
      </>}
  </div>
}
