"use client"
import { useEffect, useMemo, useRef, useState } from "react"
import { ArrowLeft, ExternalLink, LayoutGrid, Library, List, Lock, Plus, Star, Table2 } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Segmented } from "@/components/ui/segmented"
import { StatusPill } from "@/components/ui/status-pill"
import { SubBar, SubBarPrimary } from "@/components/layout/sub-bar"
import { SecondaryPanel } from "@/components/layout/secondary-panel"
import { PageEmpty, PageError, PageLoading, PageNoResults } from "@/components/layout/page-state"
import { usePages } from "@/hooks/use-pages"
import { usePageFolders } from "@/hooks/use-page-folders"
import { useSessionSafe } from "@/hooks/use-auth"
import { useDocumentViewerRead } from "@/hooks/use-document-viewer-read"
import { useUserPreference } from "@/hooks/use-user-preference"
import { DocumentReadonly } from "@/components/features/pages/document-readonly/document-readonly"
import { formatRelativeShort } from "@/lib/time"
import { artifactDashboards, artifactSlides } from "@/lib/feature-flags"
import { cn } from "@/lib/utils"
import { ArtifactViewerProvider } from "@/components/features/chat/artifact-context/projects/artifact-viewer-context"
import { useArtifactProjectsAvailable } from "@/components/features/chat/artifact-context/projects/use-artifact-project-data"
import { ArtifactViewerPanel } from "@/components/features/chat/artifact-context/viewer/artifact-viewer-panel"
import { CraftLockBar, craftEditorHref } from "@/components/features/chat/artifact-context/craft/craft-lock-bar"
import { KIND_ACCENT, KIND_ICON, ARTIFACT_KINDS, type ArtifactKindKey } from "@/components/features/chat/artifact-context/new-artifact/artifact-kinds"
import { useArtifactCapabilities } from "@/components/features/chat/artifact-context/new-artifact/use-artifact-capabilities"
import { NewArtifactDialog } from "./new-artifact-dialog"
import { kindLabel, libraryItems, type LibraryItem, type LibraryKind } from "./library-items"
import {
  applyFilters, EMPTY_FILTERS, groupItems, inView, needsAttention, sortItems, versioned,
  type GroupKey, type LibraryFilters, type Personal, type SortKey,
} from "./library-filter"
import { LibraryPanel } from "./library-panel"
import { KindThumb } from "./kind-thumb"

/** Each kind wears its registry colour, so Word reads blue and Sheet green before the label is read. */
function KindTile({ kind, large }: { kind: LibraryKind; large?: boolean }) {
  const Icon = KIND_ICON[kind]
  const accent = KIND_ACCENT[kind]
  return <span aria-hidden="true" className={cn("flex shrink-0 items-center justify-center rounded-[28%] border", accent.chip, accent.fg, large ? "size-10" : "size-7")}>
    <Icon className={large ? "size-5" : "size-3.5"} />
  </span>
}

function KindBadge({ kind }: { kind: LibraryKind }) {
  return <span className="shrink-0 rounded-sm border bg-muted px-1.5 text-micro font-medium leading-[1.6] text-muted-foreground">{kindLabel(kind)}</span>
}

function StateChips({ item }: { item: LibraryItem }) {
  return <>
    {versioned(item) && (item.sharedVersion
      ? <StatusPill tone="success" label={`Shared v${item.sharedVersion}`} />
      : <StatusPill tone="muted" label="Not shared" />)}
    {item.private && <StatusPill tone="muted" label="Private" icon={<Lock aria-hidden className="size-2.5" />} />}
    {item.empty && <StatusPill tone="muted" label="Empty" />}
  </>
}

const meta = (item: LibraryItem) => [item.ownerLabel, item.folder?.name, item.updatedAt ? formatRelativeShort(item.updatedAt.toISOString()) : null].filter(Boolean).join(" · ")

function GalleryCard({ item, onOpen }: { item: LibraryItem; onOpen: () => void }) {
  return <li className="min-w-0">
    <button type="button" aria-label={`Open ${item.name}`} onClick={onOpen} data-library-item=""
      className="grid h-full w-full min-w-0 content-start overflow-hidden rounded-card border bg-card text-left transition-colors hover:border-foreground/20 focus-visible:outline-2 focus-visible:outline-primary">
      <KindThumb kind={item.kind} empty={item.empty} />
      <span className="grid min-w-0 gap-2 p-3">
        <span className="flex min-w-0 items-center gap-2.5">
          <KindTile kind={item.kind} />
          <span className="grid min-w-0 gap-0.5">
            <h3 className="truncate text-sm font-semibold">{item.name}</h3>
            <small className="truncate text-xs text-muted-foreground">{meta(item)}</small>
          </span>
        </span>
        <span className="flex min-w-0 flex-wrap items-center gap-1.5"><KindBadge kind={item.kind} /><StateChips item={item} /></span>
      </span>
    </button>
  </li>
}

function ListRow({ item, onOpen }: { item: LibraryItem; onOpen: () => void }) {
  return <li className="min-w-0 border-t first:border-t-0">
    <button type="button" aria-label={`Open ${item.name}`} onClick={onOpen} data-library-item=""
      className="row-interactive row-hover flex w-full min-w-0 items-center gap-3 px-3 py-2 text-left coarse:min-h-[44px]">
      <KindTile kind={item.kind} />
      <span className="grid min-w-0 flex-1">
        <span className="truncate text-sm font-medium">{item.name}</span>
        <small className="truncate text-xs text-muted-foreground">{kindLabel(item.kind)}{meta(item) ? ` · ${meta(item)}` : ""}</small>
      </span>
      <span className="hidden shrink-0 items-center gap-1.5 sm:flex"><StateChips item={item} /></span>
    </button>
  </li>
}

function LibraryTable({ items, onOpen }: { items: LibraryItem[]; onOpen: (slug: string) => void }) {
  return <div className="overflow-x-auto rounded-card border bg-card">
    <table className="w-full text-sm">
      <thead className="bg-muted/50 text-left text-xs text-muted-foreground">
        <tr><th className="px-3 py-2 font-medium">Name</th><th className="px-3 py-2 font-medium">Kind</th><th className="px-3 py-2 font-medium">Shared</th><th className="px-3 py-2 font-medium">Folder</th><th className="px-3 py-2 font-medium">Updated</th></tr>
      </thead>
      <tbody>
        {items.map(item => <tr key={item.slug} className="border-t">
          <td className="max-w-[320px] px-3 py-2">
            <button type="button" data-library-item="" className="flex min-w-0 items-center gap-2 font-medium hover:underline" onClick={() => onOpen(item.slug)}>
              <KindTile kind={item.kind} /><span className="truncate">{item.name}</span>
            </button>
          </td>
          <td className="px-3 py-2"><KindBadge kind={item.kind} /></td>
          <td className="px-3 py-2 text-xs">{versioned(item) ? item.sharedVersion ? <span className="font-mono">v{item.sharedVersion}</span> : <span className="text-muted-foreground">Not shared</span> : "—"}</td>
          <td className="max-w-[180px] truncate px-3 py-2 text-xs text-muted-foreground">{item.folder?.name ?? "—"}</td>
          <td className="whitespace-nowrap px-3 py-2 text-xs text-muted-foreground">{item.updatedAt ? formatRelativeShort(item.updatedAt.toISOString()) : "—"}</td>
        </tr>)}
      </tbody>
    </table>
  </div>
}

type Layout = "list" | "gallery" | "table"

function ItemsView({ items, layout, onOpen }: { items: LibraryItem[]; layout: Layout; onOpen: (slug: string) => void }) {
  if (layout === "table") return <LibraryTable items={items} onOpen={onOpen} />
  if (layout === "gallery") return <ul aria-label="Artifacts" className="grid grid-cols-[repeat(auto-fill,minmax(min(100%,220px),1fr))] gap-3">
    {items.map(entry => <GalleryCard key={entry.slug} item={entry} onOpen={() => onOpen(entry.slug)} />)}
  </ul>
  return <ul aria-label="Artifacts" className="overflow-hidden rounded-card border bg-card">
    {items.map(entry => <ListRow key={entry.slug} item={entry} onOpen={() => onOpen(entry.slug)} />)}
  </ul>
}

/** Sort, group and layout over the list, never in the panel (Harbor 2 Display row). */
function DisplayRow({ count, sort, group, layout, onSort, onGroup, onLayout }: {
  count: number; sort: SortKey; group: GroupKey; layout: Layout
  onSort: (sort: SortKey) => void; onGroup: (group: GroupKey) => void; onLayout: (layout: Layout) => void
}) {
  const select = "h-[var(--control-sm)] rounded-md border border-border bg-card px-2 text-xs text-foreground coarse:min-h-[44px]"
  return <div role="toolbar" aria-label="Display" className="flex min-w-0 flex-wrap items-center gap-2">
    <span className="mr-auto text-xs tabular-nums text-muted-foreground">{count} {count === 1 ? "artifact" : "artifacts"}</span>
    <label className="flex items-center gap-1.5 text-xs text-muted-foreground">Sort
      <select aria-label="Sort" className={select} value={sort} onChange={event => onSort(event.target.value as SortKey)}>
        <option value="updated">Last edited</option><option value="name">Name</option><option value="kind">Kind</option>
      </select>
    </label>
    <label className="flex items-center gap-1.5 text-xs text-muted-foreground">Group
      <select aria-label="Group" className={select} value={group} onChange={event => onGroup(event.target.value as GroupKey)}>
        <option value="none">None</option><option value="kind">Kind</option><option value="folder">Folder</option>
      </select>
    </label>
    <Segmented<Layout> ariaLabel="View" value={layout} onChange={onLayout} options={[
      { value: "list", label: "List", icon: List, ariaLabel: "List" },
      { value: "gallery", label: "Gallery", icon: LayoutGrid, ariaLabel: "Gallery" },
      { value: "table", label: "Table", icon: Table2, ariaLabel: "Table" },
    ]} />
  </div>
}

/** Home: what can be made, what waits on its owner, what this person opened last (or what changed last). */
function LibraryHome({ items, creatable, personal, onNew, onOpen, onShow }: {
  items: LibraryItem[]; creatable: ArtifactKindKey[]; personal: Personal
  onNew: (kind: ArtifactKindKey) => void; onOpen: (slug: string) => void; onShow: (view: "all" | "opened") => void
}) {
  const visible = items.filter(item => !item.empty)
  const attention = visible.filter(needsAttention).slice(0, 5)
  const opened = inView(items, "opened", personal)
  // Each item once on Home: what waits on its owner is not repeated below.
  const below = (opened.length ? opened : visible).filter(item => !attention.includes(item))
  return <div className="grid content-start gap-6">
    <header className="grid gap-1">
      <h1 className="text-xl font-semibold">Artifacts</h1>
      <p className="text-sm text-muted-foreground">Projects, dashboards, pages and documents people and agents made, in one place.</p>
    </header>
    {creatable.length > 0 && <section aria-labelledby="make-heading" className="grid gap-2">
      <h2 id="make-heading" className="text-sm font-semibold">Make something</h2>
      <ul className="grid grid-cols-[repeat(auto-fill,minmax(min(100%,180px),1fr))] gap-2">
        {ARTIFACT_KINDS.filter(kind => creatable.includes(kind.key)).map(kind => <li key={kind.key}>
          <button type="button" onClick={() => onNew(kind.key)} aria-label={`New ${kind.newLabel ?? kind.label}`}
            className="flex h-full w-full items-start gap-2.5 rounded-card border bg-card p-3 text-left transition-colors hover:border-foreground/20 coarse:min-h-[44px]">
            <KindTile kind={kind.key} />
            <span className="grid min-w-0 gap-0.5">
              <span className="text-sm font-medium">{kind.newLabel ?? kind.label}</span>
              <small className="text-xs text-muted-foreground">{kind.description}</small>
            </span>
          </button>
        </li>)}
      </ul>
    </section>}
    {attention.length > 0 && <section aria-labelledby="attention-heading" className="grid gap-2">
      <h2 id="attention-heading" className="text-sm font-semibold">Not shared yet</h2>
      <ItemsView items={attention} layout="list" onOpen={onOpen} />
    </section>}
    <section aria-labelledby="recent-heading" className="grid gap-2">
      <div className="flex items-center gap-2">
        <h2 id="recent-heading" className="text-sm font-semibold">{opened.length ? "Recently opened" : "Recent"}
          {opened.length > 0 && <span className="ml-1.5 font-normal text-muted-foreground">by you</span>}</h2>
        {below.length > 8 && <Button type="button" variant="link" size="sm" className="ml-auto h-auto p-0" onClick={() => onShow(opened.length ? "opened" : "all")}>All {below.length}</Button>}
      </div>
      <ItemsView items={below.slice(0, 8)} layout="gallery" onOpen={onOpen} />
    </section>
  </div>
}

function OpenInPages({ workspaceId, item }: { workspaceId: string; item: LibraryItem }) {
  const query = new URLSearchParams({ workspace_id: workspaceId })
  if (item.projectBacked) query.set("artifact", "1")
  const reference = item.projectBacked && item.pageId ? "@" + item.pageId : item.slug
  const href = `/pages/${encodeURIComponent(reference)}?${query}`
  return <Button asChild size="sm" variant="outline" className="coarse:min-h-[44px]">
    <a href={href}>{item.kind === "page" ? "Open page" : "Open in Pages"}<ExternalLink aria-hidden /></a>
  </Button>
}

/**
 * The saved Word document, read-only: the same current-version read the chat
 * viewer uses. It never opens an editor, a CRDT or a lease, and writes nothing;
 * editing stays in the Craft editor behind the lock bar.
 */
function WordPreview({ workspaceId, item }: { workspaceId: string; item: LibraryItem }) {
  const userId = useSessionSafe()?.data?.user?.id ?? null
  const target = useMemo(() => item.pageId ? { workspaceId, pageId: item.pageId, type: "document" as const, mode: "latest" as const } : null, [workspaceId, item.pageId])
  // chatId and activationKey only scope the read; the library is its own scope, one per opened item.
  const { read } = useDocumentViewerRead({ workspaceId, chatId: "artifacts-library", currentUserId: userId, activationKey: `library:${item.slug}`, target, enabled: target !== null })
  if (read.status === "loading") return <div aria-busy="true" className="h-40 animate-pulse rounded-card border bg-muted/40" />
  if (read.status !== "ready") return <p role="status" className="rounded-card border border-dashed p-4 text-sm text-muted-foreground">
    {read.status === "denied" ? "You can't read this document." : "The document preview is unavailable. Open in Pages shows it."}
  </p>
  return <div className="bg-card">
    <DocumentReadonly schema={read.content.schema} content={read.content.content} accessibleLabel={`${item.name}, saved version`} />
  </div>
}

const DETAIL_HINT: Partial<Record<LibraryKind, string>> = {
  word: "This is the saved version. Edit it in its editor from Open in Pages.",
  sheet: "Read and edit it in its editor; Open in Pages shows the saved document.",
  page: "Its panels open on the page, where crews keep them filled.",
}

/** One artifact: a project shows its history in the project viewer, a native Word or Sheet its edit lock. */
function ArtifactDetail({ workspaceId, item, onBack, favorite, onFavorite }: {
  workspaceId: string; item: LibraryItem; onBack: () => void; favorite: boolean; onFavorite: () => void
}) {
  const projects = useArtifactProjectsAvailable(workspaceId)
  const projectPreview = item.projectBacked && (item.kind === "project" || item.kind === "dashboard" && artifactDashboards() || item.kind === "slides" && artifactSlides()) && projects
  const target = useMemo(() => ({ slug: item.slug, tab: "preview" as const }), [item.slug])
  return <div className="flex min-h-0 flex-1 flex-col gap-3">
    <div className="flex min-w-0 flex-wrap items-center gap-x-3 gap-y-1">
      <Button type="button" variant="ghost" size="sm" className="-ml-2 coarse:min-h-[44px]" onClick={onBack}><ArrowLeft aria-hidden />All artifacts</Button>
      {/* The viewer names the artifact, its kind and version itself; here only what it does not say. */}
      {projectPreview && meta(item) && <span className="ml-auto truncate text-xs text-muted-foreground">{meta(item)}</span>}
      <Button type="button" variant="ghost" size="icon-sm" aria-pressed={favorite} className={cn("coarse:min-h-[44px] coarse:min-w-[44px]", !(projectPreview && meta(item)) && "ml-auto")}
        aria-label={favorite ? "Remove from favorites" : "Add to favorites"} title={favorite ? "Remove from favorites" : "Add to favorites"} onClick={onFavorite}>
        <Star aria-hidden className={cn("size-4", favorite && "fill-current text-warn")} />
      </Button>
    </div>
    {!projectPreview && <header className="flex min-w-0 flex-wrap items-center gap-3">
      <KindTile kind={item.kind} large />
      <div className="grid min-w-0 flex-[1_1_220px] gap-1">
        <h1 className="truncate text-xl font-semibold">{item.name}</h1>
        <p className="flex min-w-0 flex-wrap items-center gap-1.5 text-xs text-muted-foreground"><KindBadge kind={item.kind} /><StateChips item={item} /><span>{meta(item)}</span></p>
      </div>
      <OpenInPages workspaceId={workspaceId} item={item} />
    </header>}
    {projectPreview
      ? <ArtifactViewerProvider workspaceId={workspaceId} target={target} onTargetChange={next => { if (!next) onBack() }}>
        <div className="flex min-h-[520px] flex-1 overflow-hidden rounded-card border"><ArtifactViewerPanel variant="library" /></div>
      </ArtifactViewerProvider>
      : item.native && item.kind === "word" && item.pageId
        ? <div className="overflow-hidden rounded-card border">
          <CraftLockBar workspaceId={workspaceId} slug={"@" + item.pageId}
            onEdit={() => window.open(craftEditorHref(workspaceId, "@" + item.pageId), "_blank", "noopener,noreferrer")} />
        </div>
        : null}
    {item.kind === "word" && !item.empty && item.pageId && <WordPreview workspaceId={workspaceId} item={item} />}
    {!projectPreview && item.kind !== "project" && <p className="text-sm text-muted-foreground">
      {DETAIL_HINT[item.kind] ?? "Open in Pages shows it with everything it contains."}
    </p>}
  </div>
}

/**
 * J and K move between the items on screen and Enter opens the focused one;
 * Escape leaves a detail. Keys meant for a field, a chord or an open dialog
 * are left alone.
 */
function useLibraryKeys(root: React.RefObject<HTMLElement | null>, detailOpen: boolean, onBack: () => void) {
  const latest = useRef({ detailOpen, onBack })
  useEffect(() => { latest.current = { detailOpen, onBack } })
  useEffect(() => {
    const onKey = (event: KeyboardEvent) => {
      if (event.defaultPrevented || event.metaKey || event.ctrlKey || event.altKey) return
      const target = event.target as HTMLElement | null
      if (target?.closest?.("input, textarea, select, [contenteditable=''], [contenteditable=true], [role=dialog], [role=alertdialog]")) return
      if (document.querySelector("[role=dialog][data-state=open], [role=alertdialog][data-state=open]")) return
      if (event.key === "Escape" && latest.current.detailOpen) { event.preventDefault(); latest.current.onBack(); return }
      if (latest.current.detailOpen || (event.key !== "j" && event.key !== "k")) return
      const items = [...(root.current?.querySelectorAll<HTMLElement>("[data-library-item]") ?? [])]
      if (!items.length) return
      event.preventDefault()
      const at = items.indexOf(document.activeElement as HTMLElement)
      const next = at < 0 ? 0 : Math.min(items.length - 1, Math.max(0, at + (event.key === "j" ? 1 : -1)))
      items[next].focus()
    }
    window.addEventListener("keydown", onKey)
    return () => window.removeEventListener("keydown", onKey)
  }, [root])
}

interface ArtifactsLibraryProps {
  workspaceId: string
  /** The open artifact's slug, or null for the library. Kept in the URL by the route. */
  item: string | null
  onSelect: (slug: string | null) => void
  /** Controlled filters, when the route keeps them in the URL; otherwise the library holds them. */
  filters?: LibraryFilters
  onFiltersChange?: (next: LibraryFilters) => void
}

/**
 * The Artifacts library: one place for everything people and agents made —
 * projects, dashboards, panel Pages, Word, Sheet and Slides. The side panel
 * searches, filters and switches views; Home offers what can be made. Empty
 * items and repeated runs have their own views and are never deleted here.
 */
export function ArtifactsLibrary({ workspaceId, item, onSelect, filters: controlled, onFiltersChange }: ArtifactsLibraryProps) {
  const { pages, loading, error, refresh } = usePages(workspaceId)
  const { folders } = usePageFolders(workspaceId)
  const caps = useArtifactCapabilities(workspaceId)
  const items = useMemo(() => libraryItems(pages), [pages])
  const [own, setOwn] = useState<LibraryFilters>(EMPTY_FILTERS)
  const filters = controlled ?? own
  const setFilters = (next: LibraryFilters) => { setOwn(next); onFiltersChange?.(next) }
  const [sort, setSort] = useState<SortKey>("updated")
  const [group, setGroup] = useState<GroupKey>("none")
  const [layout, setLayout] = useState<Layout>("list")
  const [newKind, setNewKind] = useState<ArtifactKindKey | null | undefined>(undefined)
  const creatable = ARTIFACT_KINDS.filter(kind => kind.live(caps)).map(kind => kind.key)
  // Per person, synced across devices: what they opened and what they starred. Never shared with others.
  const [recent, setRecent] = useUserPreference<{ slug: string; at: number }[]>(`artifacts.${workspaceId}.recentlyOpened`, [])
  const [favorites, setFavorites] = useUserPreference<string[]>(`artifacts.${workspaceId}.favorites`, [])
  const personal = useMemo<Personal>(() => ({
    opened: new Map(Array.isArray(recent) ? recent.filter(entry => typeof entry?.slug === "string" && Number.isFinite(entry?.at)).map(entry => [entry.slug, entry.at]) : []),
    favorites: new Set(Array.isArray(favorites) ? favorites.filter(slug => typeof slug === "string") : []),
  }), [recent, favorites])
  const selected = item !== null ? items.find(entry => entry.slug === item) ?? null : null
  const contentRef = useRef<HTMLDivElement>(null)
  const openedSlug = selected?.slug
  useEffect(() => {
    if (!openedSlug) return
    const rest = (Array.isArray(recent) ? recent : []).filter(entry => entry?.slug !== openedSlug)
    setRecent([{ slug: openedSlug, at: Date.now() }, ...rest].slice(0, 50))
  }, [openedSlug]) // eslint-disable-line react-hooks/exhaustive-deps -- one record per open, not per list change
  const toggleFavorite = (slug: string) => {
    const list = [...(personal.favorites ?? [])]
    setFavorites(list.includes(slug) ? list.filter(entry => entry !== slug) : [slug, ...list])
  }
  useLibraryKeys(contentRef, item !== null, () => onSelect(null))

  const subBar = <SubBar icon={Library} title="Artifacts" ariaLabel="Artifacts" heading={false}
    section={selected ? selected.name : undefined}
    actions={creatable.length > 0 ? <SubBarPrimary icon={Plus} aria-label="New artifact" onClick={() => setNewKind(null)}>
      <span className="hidden sm:inline">New artifact</span>
    </SubBarPrimary> : undefined} />

  let content: React.ReactNode
  if (loading) content = <div aria-label="Loading artifacts"><PageLoading variant="cards" /></div>
  else if (error && !items.length) content = <PageError title="Artifacts could not be loaded" onRetry={refresh} retryLabel="Try again" />
  else if (item !== null) {
    content = selected ? <ArtifactDetail workspaceId={workspaceId} item={selected} onBack={() => onSelect(null)}
      favorite={personal.favorites?.has(selected.slug) ?? false} onFavorite={() => toggleFavorite(selected.slug)} />
      : <div className="grid gap-2 text-sm">
        <p>This artifact is not available.</p>
        <div><Button type="button" size="sm" variant="outline" onClick={() => onSelect(null)}><ArrowLeft aria-hidden />All artifacts</Button></div>
      </div>
  } else if (!items.some(entry => !entry.empty) && filters.view !== "empty") {
    content = <PageEmpty icon={Library} title="No artifacts yet"
      description="Ask an agent in Chat to make a project or a dashboard, or start a document here."
      primary={creatable.length ? { label: "New artifact", icon: Plus, onClick: () => setNewKind(null) } : undefined} />
  } else if (filters.view === "home") {
    content = <LibraryHome items={items} creatable={creatable} personal={personal} onNew={kind => setNewKind(kind)} onOpen={onSelect}
      onShow={view => setFilters({ ...filters, view })} />
  } else {
    const filtered = applyFilters(items, filters, personal)
    // Recently opened keeps its own order until another sort is chosen.
    const shown = filters.view === "opened" && sort === "updated" ? filtered : sortItems(filtered, sort)
    content = <div className="grid content-start gap-3">
      <DisplayRow count={shown.length} sort={sort} group={group} layout={layout} onSort={setSort} onGroup={setGroup} onLayout={setLayout} />
      {filters.view === "series" && <p className="text-sm text-muted-foreground">Runs of the same thing with a number or date in the name. Nothing here is archived or deleted.</p>}
      {shown.length === 0
        ? <PageNoResults onClear={() => setFilters({ ...EMPTY_FILTERS, view: filters.view })} />
        : groupItems(shown, group).map(entry => <section key={entry.key} aria-label={entry.label || undefined} className="grid gap-2">
          {entry.label && <h2 className="text-xs font-semibold text-muted-foreground">{entry.label} <span className="tabular-nums">{entry.items.length}</span></h2>}
          <ItemsView items={entry.items} layout={layout} onOpen={onSelect} />
        </section>)}
    </div>
  }

  return <div className="flex h-[calc(100dvh-var(--app-header-h)-var(--mobile-tab-bar-h))] min-h-0 flex-col bg-background">
    {subBar}
    <div className="relative flex min-h-0 flex-1 overflow-hidden">
      {!loading && !(error && !items.length) && <SecondaryPanel id="artifacts" label="Artifacts">
        <LibraryPanel items={items} folders={folders} filters={filters} personal={personal}
          onFiltersChange={next => {
            setFilters(next)
            // The route drops ?item= with its own replace; a second navigation would race it.
            if (item !== null && !onFiltersChange) onSelect(null)
          }} />
      </SecondaryPanel>}
      <div ref={contentRef} className={cn("min-w-0 flex-1", selected ? "flex min-h-0 flex-col overflow-y-auto" : "overflow-y-auto")}>
        <div className={cn("mx-auto flex w-full max-w-6xl flex-col p-4 md:p-6", selected && "min-h-full")}>{content}</div>
      </div>
    </div>
    {newKind !== undefined && <NewArtifactDialog workspaceId={workspaceId} open onOpenChange={open => { if (!open) setNewKind(undefined) }}
      defaultKind={newKind ?? undefined} defaultFolderSlug={filters.folder || undefined}
      onCreated={slug => { setNewKind(undefined); refresh(); if (slug) onSelect(slug) }} />}
  </div>
}
