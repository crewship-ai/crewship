"use client"
import { useEffect, useMemo, useRef, useState } from "react"
import { useQuery } from "@tanstack/react-query"
import { ArrowLeft, LayoutGrid, Library, List, Lock, Maximize2, Plus, Settings2, Share2, Star, Table2, X } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Segmented } from "@/components/ui/segmented"
import { StatusPill } from "@/components/ui/status-pill"
import { SubBar } from "@/components/layout/sub-bar"
import { SecondaryPanel } from "@/components/layout/secondary-panel"
import { PageEmpty, PageError, PageLoading, PageNoResults } from "@/components/layout/page-state"
import { usePageCatalogWindow, usePages, type PageCatalogFilter } from "@/hooks/use-pages"
import { usePageFolders } from "@/hooks/use-page-folders"
import { useSessionSafe } from "@/hooks/use-auth"
import { useUserPreference } from "@/hooks/use-user-preference"
import { useIsMobile } from "@/hooks/use-mobile"
import { formatRelativeShort } from "@/lib/time"
import { artifactsDesign, artifactsServerCatalog, artifactsSharingMock } from "@/lib/feature-flags"
import { cn } from "@/lib/utils"
import { readLocalDraft } from "@/components/features/chat/artifact-context/craft/local-draft"
import { ARTIFACT_KINDS, type ArtifactKindKey } from "@/components/features/chat/artifact-context/new-artifact/artifact-kinds"
import { useArtifactCapabilities } from "@/components/features/chat/artifact-context/new-artifact/use-artifact-capabilities"
import { NewArtifactDialog } from "./new-artifact-dialog"
import { libraryItems, withLibraryState, type LibraryItem } from "./library-items"
import { sharingKeys, useSharingApi } from "./sharing/sharing-api"
import { ShareDialog } from "./sharing/share-dialog"
import { FolderSettingsDialog } from "./sharing/folder-settings-dialog"
import { BulkBar } from "./sharing/bulk-actions"
import {
  applyFilters, EMPTY_FILTERS, groupItems, hiddenAsEmpty, inView, serverCatalogParams, SERVER_PAGE_SIZE, sortItems, versioned,
  type GroupKey, type LibraryFilters, type Personal, type SortKey,
} from "./library-filter"
import { attentionItems } from "./attention"
import { ArtifactDetail } from "./artifact-detail"
import { ArtifactFrame, NO_FRAME_CAPABILITIES } from "./frame"
import { kindOf, kindText } from "./kinds"
import { KindBadge, KindTile } from "./kind-tile"
import { LibraryHome, NewArtifactMenu } from "./library-home"
import { LibraryPanel } from "./library-panel"
import { KindThumb } from "./kind-thumb"
import { useArtifactAccess } from "./use-artifact-access"

function KindLabel({ item }: { item: LibraryItem }) {
  return <KindBadge resolved={kindOf(item)} />
}

function StateChips({ item }: { item: LibraryItem }) {
  return <>
    {versioned(item) && (item.sharedVersion
      ? <StatusPill tone="success" label={`Shared v${item.sharedVersion}`} />
      : <StatusPill tone="muted" label="Not shared" />)}
    {item.private && <StatusPill tone="muted" label="Private" icon={<Lock aria-hidden className="size-2.5" />} />}
    {item.empty && <StatusPill tone="muted" label="Empty" />}
    {item.test && <StatusPill tone="muted" label="Test" />}
    {item.archived && <StatusPill tone="muted" label="Archived" />}
  </>
}

/** The server leaves archived and test-made Pages out unless asked (#4444); the Archive and Test items views need them. */
const CATALOG: PageCatalogFilter = { archived: "include", testItems: "include" }

const meta = (item: LibraryItem) => [item.ownerLabel, item.folder?.name, item.updatedAt ? formatRelativeShort(item.updatedAt.toISOString()) : null].filter(Boolean).join(" · ")

function GalleryCard({ item, onOpen, peeked }: { item: LibraryItem; onOpen: () => void; peeked?: boolean }) {
  return <li className="min-w-0">
    <button type="button" aria-label={`Open ${item.name}`} onClick={onOpen} data-library-item={item.slug} aria-current={peeked || undefined}
      className={cn("grid h-full w-full min-w-0 content-start overflow-hidden rounded-card border bg-card text-left transition-colors hover:border-foreground/20 focus-visible:outline-2 focus-visible:outline-primary",
        peeked && "border-primary/60 ring-1 ring-primary/40")}>
      <KindThumb kind={kindOf(item).kind} mode={kindOf(item).mode} empty={item.empty} className="rounded-none border-b" />
      <span className="grid min-w-0 gap-2 p-3">
        <span className="flex min-w-0 items-center gap-2.5">
          <KindTile kind={kindOf(item).kind} />
          <span className="grid min-w-0 gap-0.5">
            <h3 className="truncate text-sm font-semibold">{item.name}</h3>
            <small className="truncate text-xs text-muted-foreground">{meta(item)}</small>
          </span>
        </span>
        <span className="flex min-w-0 flex-wrap items-center gap-1.5"><KindLabel item={item} /><StateChips item={item} /></span>
      </span>
    </button>
  </li>
}

type Selection = { has: (slug: string) => boolean; toggle: (slug: string) => void }

function ListRow({ item, onOpen, selection, peeked }: { item: LibraryItem; onOpen: () => void; selection?: Selection; peeked?: boolean }) {
  return <li className={cn("flex min-w-0 items-center border-t first:border-t-0", peeked && "bg-accent")}>
    {selection && <label className="flex shrink-0 cursor-pointer items-center self-stretch pl-3 coarse:min-w-[44px] coarse:justify-center coarse:pl-0">
      <input type="checkbox" className="size-4 accent-primary" aria-label={`Select ${item.name}`}
        checked={selection.has(item.slug)} onChange={() => selection.toggle(item.slug)} />
    </label>}
    <button type="button" aria-label={`Open ${item.name}`} onClick={onOpen} data-library-item={item.slug} aria-current={peeked || undefined}
      className="row-interactive row-hover flex w-full min-w-0 items-center gap-3 px-3 py-2 text-left coarse:min-h-[44px]">
      <KindTile kind={kindOf(item).kind} />
      <span className="grid min-w-0 flex-1">
        <span className="truncate text-sm font-medium">{item.name}</span>
        <small className="truncate text-xs text-muted-foreground">{kindText(kindOf(item))}{meta(item) ? ` · ${meta(item)}` : ""}</small>
      </span>
      <span className="hidden shrink-0 items-center gap-1.5 sm:flex"><StateChips item={item} /></span>
    </button>
  </li>
}

function LibraryTable({ items, onOpen, peeked }: { items: LibraryItem[]; onOpen: (slug: string) => void; peeked?: string | null }) {
  return <div className="overflow-x-auto rounded-card border bg-card">
    <table className="w-full text-sm">
      <thead className="bg-muted/50 text-left text-xs text-muted-foreground">
        <tr><th className="px-3 py-2 font-medium">Name</th><th className="px-3 py-2 font-medium">Kind</th><th className="px-3 py-2 font-medium">Shared</th><th className="px-3 py-2 font-medium">Folder</th><th className="px-3 py-2 font-medium">Updated</th></tr>
      </thead>
      <tbody>
        {items.map(item => <tr key={item.slug} className={cn("border-t", peeked === item.slug && "bg-accent")}>
          <td className="max-w-[320px] px-3 py-2">
            <button type="button" data-library-item={item.slug} aria-current={peeked === item.slug || undefined}
              className="flex min-w-0 items-center gap-2 font-medium hover:underline" onClick={() => onOpen(item.slug)}>
              <KindTile kind={kindOf(item).kind} /><span className="truncate">{item.name}</span>
            </button>
          </td>
          <td className="px-3 py-2"><KindLabel item={item} /></td>
          <td className="px-3 py-2 text-xs">{versioned(item) ? item.sharedVersion ? <span className="font-mono">v{item.sharedVersion}</span> : <span className="text-muted-foreground">Not shared</span> : "—"}</td>
          <td className="max-w-[180px] truncate px-3 py-2 text-xs text-muted-foreground">{item.folder?.name ?? "—"}</td>
          <td className="whitespace-nowrap px-3 py-2 text-xs text-muted-foreground">{item.updatedAt ? formatRelativeShort(item.updatedAt.toISOString()) : "—"}</td>
        </tr>)}
      </tbody>
    </table>
  </div>
}

type Layout = "list" | "gallery" | "table"

function ItemsView({ items, layout, onOpen, selection, peeked }: {
  items: LibraryItem[]; layout: Layout; onOpen: (slug: string) => void; selection?: Selection; peeked?: string | null
}) {
  if (layout === "table") return <LibraryTable items={items} onOpen={onOpen} peeked={peeked} />
  if (layout === "gallery") return <ul aria-label="Artifacts" className="grid grid-cols-[repeat(auto-fill,minmax(min(100%,220px),1fr))] gap-3">
    {items.map(entry => <GalleryCard key={entry.slug} item={entry} onOpen={() => onOpen(entry.slug)} peeked={peeked === entry.slug} />)}
  </ul>
  return <ul aria-label="Artifacts" className="overflow-hidden rounded-card border bg-card">
    {items.map(entry => <ListRow key={entry.slug} item={entry} onOpen={() => onOpen(entry.slug)} selection={selection} peeked={peeked === entry.slug} />)}
  </ul>
}

/** Sort, group and layout over the list, never in the panel (Harbor 2 Display row). */
function DisplayRow({ count, sort, group, layout, onSort, onGroup, onLayout, selecting, onSelecting }: {
  count: number; sort: SortKey; group: GroupKey; layout: Layout
  onSort: (sort: SortKey) => void; onGroup: (group: GroupKey) => void; onLayout: (layout: Layout) => void
  // Phones show row checkboxes only in select mode; desktop leaves this unset.
  selecting?: boolean; onSelecting?: (selecting: boolean) => void
}) {
  const select = "h-[var(--control-sm)] rounded-md border border-border bg-card px-2 text-xs text-foreground coarse:min-h-[44px]"
  return <div role="toolbar" aria-label="Display" className="flex min-w-0 flex-wrap items-center gap-2">
    <span className="mr-auto text-xs tabular-nums text-muted-foreground">{count} {count === 1 ? "artifact" : "artifacts"}</span>
    {onSelecting && <Button type="button" size="sm" variant="outline" className="coarse:min-h-[44px]"
      onClick={() => onSelecting(!selecting)}>{selecting ? "Done" : "Select"}</Button>}
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

/**
 * The peek on the right: what an item is, without leaving the list. The list
 * keeps its filter, selection and scroll; Open (or Enter) opens the full
 * detail. It reads nothing beyond the list's own data: no document, lease or
 * project fetch per J/K step.
 */
function ArtifactPeek({ workspaceId, item, favorite, onFavorite, onOpen, onClose }: {
  workspaceId: string; item: LibraryItem; favorite: boolean; onFavorite: () => void; onOpen: () => void; onClose: () => void
}) {
  const [sharing, setSharing] = useState(false)
  const facts: [string, React.ReactNode][] = [
    ["Kind", kindText(kindOf(item))],
    ["Version", versioned(item)
      ? item.sharedVersion ? <StatusPill tone="success" label={`Shared v${item.sharedVersion}`} /> : <StatusPill tone="muted" label="Not shared" />
      : "No versions"],
    ["Folder", item.folder?.name ?? "Unfiled"],
    ["Who can open", <>
      {item.private ? "Private" : "Everyone with access"}
      <Button type="button" variant="link" size="sm" className="h-auto p-0 text-xs" onClick={() => setSharing(true)}>Manage</Button>
    </>],
    ["Owner", item.ownerLabel ?? "Someone else"],
    ["Edited", item.updatedAt ? formatRelativeShort(item.updatedAt.toISOString()) : "—"],
  ]
  return <aside aria-label="Preview" className="flex min-h-0 flex-col">
    <div className="flex h-12 shrink-0 items-center gap-1.5 border-b pl-3.5 pr-2">
      <KindLabel item={item} />
      <Button type="button" variant="ghost" size="icon-sm" aria-pressed={favorite} className="ml-auto coarse:min-h-[44px] coarse:min-w-[44px]"
        aria-label={favorite ? "Remove from favorites" : "Add to favorites"} title={favorite ? "Remove from favorites" : "Add to favorites"} onClick={onFavorite}>
        <Star aria-hidden className={cn("size-4", favorite && "fill-current text-warn")} />
      </Button>
      <Button type="button" variant="ghost" size="icon-sm" aria-label="Close preview" title="Close preview (Esc)" className="coarse:min-h-[44px] coarse:min-w-[44px]" onClick={onClose}>
        <X aria-hidden className="size-4" />
      </Button>
    </div>
    <div className="min-h-0 flex-1 overflow-y-auto">
      <div className="grid gap-2.5 border-b p-3.5">
        <KindThumb kind={kindOf(item).kind} mode={kindOf(item).mode} empty={item.empty} className="h-[140px] rounded-card border" />
        <div className="flex min-w-0 items-center gap-2.5">
          <KindTile kind={kindOf(item).kind} />
          <div className="grid min-w-0 gap-0.5">
            <h2 className="truncate text-sm font-semibold">{item.name}</h2>
            {meta(item) && <small className="truncate text-xs text-muted-foreground">{meta(item)}</small>}
          </div>
        </div>
        <div className="flex flex-wrap items-center gap-1.5">
          <Button type="button" size="sm" className="coarse:min-h-[44px]" onClick={onOpen} title="Open (Enter)"><Maximize2 aria-hidden />Open</Button>
          <Button type="button" size="sm" variant="outline" className="coarse:min-h-[44px]" onClick={() => setSharing(true)}><Share2 aria-hidden />Share</Button>
        </div>
      </div>
      <dl className="m-0 grid grid-cols-[6.5rem_1fr] gap-x-3 gap-y-2 border-b px-3.5 py-3 text-xs">
        {facts.map(([term, value]) => <div key={term} className="contents">
          <dt className="text-muted-foreground">{term}</dt>
          <dd className="m-0 flex min-w-0 flex-wrap items-center gap-1.5">{value}</dd>
        </div>)}
      </dl>
    </div>
    {sharing && <ShareDialog workspaceId={workspaceId} item={item} open onOpenChange={open => { if (!open) setSharing(false) }} />}
  </aside>
}

/** What a tidy-up view is, said where it is chosen; acting on it is a previewed bulk action. */
const VIEW_NOTE: Partial<Record<LibraryFilters["view"], string>> = {
  series: "Suggested clean-up: runs of the same thing with a number or date in the name. Select what you no longer need and archive it after a preview. Nothing is deleted.",
  test: "Made by tests and probes. Hidden from every other view; archive them after a preview. Nothing is deleted.",
  empty: "Nothing in these yet. Archive them after a preview; nothing is deleted.",
  archive: "Archived artifacts are hidden everywhere else. Select them to restore.",
}

interface LibraryKeys {
  detailOpen: boolean
  onBack: () => void
  /** The peeked item's slug; J and K move it, Enter opens it, Escape closes it. */
  peek: string | null
  /** Null where there is no peek (a phone): J and K only move focus. */
  onPeek: ((slug: string | null) => void) | null
  onOpen: (slug: string) => void
}

/**
 * J and K move between the items on screen and peek at each; Enter opens the
 * peeked one; Escape closes the peek, or leaves a detail. Keys meant for a
 * field, a chord or an open dialog are left alone.
 */
function useLibraryKeys(root: React.RefObject<HTMLElement | null>, keys: LibraryKeys) {
  const latest = useRef(keys)
  useEffect(() => { latest.current = keys })
  useEffect(() => {
    const onKey = (event: KeyboardEvent) => {
      if (event.defaultPrevented || event.metaKey || event.ctrlKey || event.altKey) return
      const target = event.target as HTMLElement | null
      if (target?.closest?.("input, textarea, select, [contenteditable=''], [contenteditable=true], [role=dialog], [role=alertdialog]")) return
      if (document.querySelector("[role=dialog][data-state=open], [role=alertdialog][data-state=open]")) return
      const { detailOpen, onBack, peek, onPeek, onOpen } = latest.current
      if (event.key === "Escape" && detailOpen) { event.preventDefault(); onBack(); return }
      if (detailOpen) return
      const row = (slug: string) => root.current?.querySelector<HTMLElement>(`[data-library-item="${CSS.escape(slug)}"]`)
      if (event.key === "Escape" && peek && onPeek) {
        event.preventDefault()
        onPeek(null)
        row(peek)?.focus()
        return
      }
      if (event.key === "Enter" && peek) {
        // The peeked row, or nothing in particular: open it. Another control keeps its own Enter.
        const onRow = target?.closest?.<HTMLElement>("[data-library-item]")
        if (onRow ? onRow.dataset.libraryItem !== peek : target?.closest?.("button, a, [role=button], [tabindex]")) return
        event.preventDefault()
        onOpen(peek)
        return
      }
      if (event.key !== "j" && event.key !== "k") return
      const items = [...(root.current?.querySelectorAll<HTMLElement>("[data-library-item]") ?? [])]
      if (!items.length) return
      event.preventDefault()
      let at = items.indexOf(document.activeElement as HTMLElement)
      if (at < 0 && peek) at = items.findIndex(entry => entry.dataset.libraryItem === peek)
      const next = at < 0 ? 0 : Math.min(items.length - 1, Math.max(0, at + (event.key === "j" ? 1 : -1)))
      items[next].focus()
      const slug = items[next].dataset.libraryItem
      if (slug && onPeek) onPeek(slug)
    }
    window.addEventListener("keydown", onKey)
    return () => window.removeEventListener("keydown", onKey)
  }, [root])
}

interface ArtifactsLibraryProps {
  workspaceId: string
  /** The open artifact's immutable page ID, or null for the library. Kept in the URL by the route. */
  item: string | null
  /** A code version the address pins (`?version=N`, e.g. a shared-version link); the open project shows it instead of Latest. */
  version?: number
  /** `item` is a slug from a link made before IDs (or an item that has no ID). It never matches an ID. */
  bySlug?: boolean
  /** Opens an artifact by its ID; one without an ID opens by `slug`. Null returns to the list. */
  onSelect: (id: string | null, slug?: string) => void
  /** A slug link found its artifact: the route moves to the ID address. */
  onCanonical?: (id: string) => void
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
export function ArtifactsLibrary({ workspaceId, item, version, bySlug, onSelect, onCanonical, filters: controlled, onFiltersChange }: ArtifactsLibraryProps) {
  const { pages, loading, error, refresh } = usePages(workspaceId, undefined, CATALOG)
  const { folders } = usePageFolders(workspaceId)
  const caps = useArtifactCapabilities(workspaceId)
  const sharing = useSharingApi(workspaceId)
  // Archive, test origin and mock moves; re-read whenever a sharing write settles.
  const stateTick = useQuery({ queryKey: sharingKeys.state(workspaceId), queryFn: () => Date.now(), staleTime: Infinity }).data
  const items = useMemo(() => {
    const base = libraryItems(pages).filter(entry => entry.kind !== "design" || artifactsDesign())
    return withLibraryState(base, sharing.libraryState(base))
  }, [pages, sharing, stateTick]) // eslint-disable-line react-hooks/exhaustive-deps -- stateTick is the re-read signal
  const [picked, setPicked] = useState<ReadonlySet<string>>(new Set())
  const [selecting, setSelecting] = useState(false)
  const [folderSettings, setFolderSettings] = useState<{ slug: string; name: string } | null>(null)
  const folderChoices = useMemo(() => folders.map(folder => ({ slug: folder.slug, name: folder.name })), [folders])
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
  // What this person made from New artifact: a blank Page they just created is still theirs to find.
  const [madeHere, setMadeHere] = useUserPreference<string[]>(`artifacts.${workspaceId}.created`, [])
  // A recovered Word draft lives only in this browser: read after mount, storage does not exist during prerender.
  const userId = useSessionSafe()?.data?.user?.id ?? ""
  const draftRefs = useMemo(() => items.filter(entry => entry.kind === "word" && entry.native && entry.pageId).map(entry => [entry.slug, "@" + entry.pageId] as const), [items])
  const [drafts, setDrafts] = useState<ReadonlySet<string>>(new Set())
  useEffect(() => {
    setDrafts(new Set(userId ? draftRefs.filter(([, ref]) => readLocalDraft(userId, workspaceId, ref)).map(([slug]) => slug) : []))
  }, [userId, workspaceId, draftRefs])
  const personal = useMemo<Personal>(() => ({
    drafts,
    opened: new Map(Array.isArray(recent) ? recent.filter(entry => typeof entry?.slug === "string" && Number.isFinite(entry?.at)).map(entry => [entry.slug, entry.at]) : []),
    favorites: new Set(Array.isArray(favorites) ? favorites.filter(slug => typeof slug === "string") : []),
    created: new Set(Array.isArray(madeHere) ? madeHere.filter(slug => typeof slug === "string") : []),
  }), [recent, favorites, madeHere, drafts])
  // Behind its own default-off flag, the All view is filtered, sorted and paged
  // by the server; the mock sharing store changes archive state the server cannot see.
  // A blank Page this person made here stays visible in All (hiddenAsEmpty);
  // the server's empty=exclude cannot see that list, so the browser keeps All
  // while such a Page exists rather than dropping it from the list and count.
  const keptBlank = items.some(entry => entry.empty && !hiddenAsEmpty(entry, personal))
  const serverParams = artifactsServerCatalog() && !artifactsSharingMock() && item === null && !keptBlank
    ? serverCatalogParams(filters, sort, { design: artifactsDesign() }) : null
  const serverKey = serverParams?.toString() ?? null
  const [serverWindow, setServerWindow] = useState<{ key: string | null; page: number }>({ key: null, page: 0 })
  const serverPage = serverWindow.key === serverKey ? serverWindow.page : 0
  const catalogWindow = usePageCatalogWindow(workspaceId, serverParams, serverPage * SERVER_PAGE_SIZE, SERVER_PAGE_SIZE)
  const windowItems = useMemo(() => {
    const base = libraryItems(catalogWindow.pages, { keepOrder: true })
    return withLibraryState(base, sharing.libraryState(base))
  }, [catalogWindow.pages, sharing])
  // A write can shrink the total under the current offset (archive the only
  // item on the last page): move to the last page that still has rows.
  const lastServerPage = Math.max(0, Math.ceil(catalogWindow.total / SERVER_PAGE_SIZE) - 1)
  const pastEnd = serverParams !== null && !catalogWindow.loading && !catalogWindow.error && serverPage > lastServerPage
  useEffect(() => {
    if (pastEnd) setServerWindow({ key: serverKey, page: lastServerPage })
  }, [pastEnd, serverKey, lastServerPage])
  // An ID address fails closed: it never falls back to a slug, which another artifact may hold now.
  const selected = item === null ? null : items.find(entry => bySlug ? entry.slug === item : entry.pageId === item) ?? null
  const canonical = bySlug ? selected?.pageId : undefined
  useEffect(() => { if (canonical) onCanonical?.(canonical) }, [canonical]) // eslint-disable-line react-hooks/exhaustive-deps
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
  // Desktop: a click peeks on the right and the list stays; the peeked row's
  // second click, Enter or Open opens the detail. A phone opens it directly.
  const mobile = useIsMobile()
  const [peek, setPeek] = useState<string | null>(null)
  const peeked = !mobile && item === null ? items.find(entry => entry.slug === peek) ?? null : null
  const open = (slug: string) => {
    setPeek(null)
    const id = items.find(entry => entry.slug === slug)?.pageId
    if (id) onSelect(id); else onSelect(null, slug)
  }
  const pick = (slug: string) => { if (mobile || peek === slug) open(slug); else setPeek(slug) }
  useLibraryKeys(contentRef, { detailOpen: item !== null, onBack: () => onSelect(null), peek: peeked?.slug ?? null, onPeek: mobile ? null : setPeek, onOpen: open })
  // A selection and a peek belong to the list they were made in.
  useEffect(() => { setPicked(new Set()); setPeek(null) }, [filters.view, filters.folder, item])

  // The open artifact's frame owns the bar; the library's bar is only for lists.
  const subBar = item === null ? <SubBar icon={Library} title="Artifacts" ariaLabel="Artifacts" heading={false}
    actions={<NewArtifactMenu creatable={creatable} onNew={kind => setNewKind(kind)} />} /> : null
  // Not in the catalog: ask the server why, so the frame can say signed out, no access or not there.
  const access = useArtifactAccess(workspaceId, item === null ? null : bySlug ? item : "@" + item, item !== null && !selected && !loading)
  // The folder panel steps aside while an artifact is open, unless pinned; its own toggle still works per open.
  const [pinned, setPinned] = useUserPreference<boolean>("artifacts.panelPinned", false)
  const [panelOpen, setPanelOpen] = useState(false)
  useEffect(() => { setPanelOpen(false) }, [item])
  const panelCollapsed = item === null ? undefined : !(pinned || panelOpen)
  const folderInView = filters.folder ? folders.find(entry => entry.slug === filters.folder) : undefined

  let content: React.ReactNode
  if (loading) content = <div aria-label="Loading artifacts"><PageLoading variant="cards" /></div>
  else if (error && !items.length) content = <PageError title="Artifacts could not be loaded" onRetry={refresh} retryLabel="Try again" />
  else if (item !== null) {
    content = selected ? <ArtifactDetail key={selected.slug} workspaceId={workspaceId} item={selected} version={version} folders={folderChoices} onBack={() => onSelect(null)}
      favorite={personal.favorites?.has(selected.slug) ?? false} onFavorite={() => toggleFavorite(selected.slug)} />
      : access === "loading" ? <div aria-label="Loading artifact"><PageLoading variant="cards" /></div>
        : access === "unknown" || access === "ok" ? <div className="grid gap-2 p-4 text-sm md:p-6">
          <p>This artifact is not available.</p>
          <div><Button type="button" size="sm" variant="outline" onClick={() => onSelect(null)}><ArrowLeft aria-hidden />All artifacts</Button></div>
        </div>
          : <ArtifactFrame name="" kind={{ kind: "app" }} caps={{ ...NO_FRAME_CAPABILITIES, access }} onHome={() => onSelect(null)}>{null}</ArtifactFrame>
  } else if (!inView(items, "all", personal).length && !VIEW_NOTE[filters.view]) {
    // Blank items are only hidden, never gone: say where they are.
    const blank = inView(items, "empty", personal).length
    content = <PageEmpty icon={Library} title="No artifacts yet"
      description={blank
        ? `${blank === 1 ? "1 blank item is" : `${blank} blank items are`} in Empty. Ask an agent in Chat to make a project or a dashboard, or start a document here.`
        : "Ask an agent in Chat to make a project or a dashboard, or start a document here."}
      primary={creatable.length ? { label: "New artifact", icon: Plus, onClick: () => setNewKind(null) } : undefined}
      secondary={blank ? { label: `Show empty (${blank})`, onClick: () => setFilters({ ...EMPTY_FILTERS, view: "empty" }) } : undefined} />
  } else if (filters.view === "home") {
    const inHome = inView(items, "all", personal).filter(entry => filters.folder === null || (entry.folder?.slug ?? "") === filters.folder)
    content = <LibraryHome items={inHome} attention={attentionItems(inHome, { drafts })} title={folderInView?.name ?? (filters.folder === "" ? "Unfiled" : "All artifacts")}
      onOpen={pick} peeked={peeked?.slug} />
  } else if (serverParams && catalogWindow.loading) {
    content = <div aria-label="Loading artifacts"><PageLoading variant="cards" /></div>
  } else if (serverParams && catalogWindow.error) {
    content = <PageError title="Artifacts could not be loaded" onRetry={refresh} retryLabel="Try again" />
  } else {
    const filtered = serverParams ? windowItems : applyFilters(items, filters, personal)
    // Recently opened keeps its own order until another sort is chosen; a server window keeps the server's.
    const shown = serverParams || filters.view === "opened" && sort === "updated" ? filtered : sortItems(filtered, sort)
    const total = serverParams ? catalogWindow.total : shown.length
    const first = serverPage * SERVER_PAGE_SIZE
    const chosen = shown.filter(entry => picked.has(entry.slug))
    const selection: Selection = {
      has: slug => picked.has(slug),
      toggle: slug => setPicked(prev => { const next = new Set(prev); if (next.has(slug)) next.delete(slug); else next.add(slug); return next }),
    }
    const folder = filters.folder ? folders.find(entry => entry.slug === filters.folder) : undefined
    const note = VIEW_NOTE[filters.view]
    content = <div className="grid content-start gap-3">
      {folder && <div className="flex min-w-0 items-center gap-2">
        <h1 className="truncate text-lg font-semibold">{folder.name}</h1>
        <Button type="button" variant="outline" size="sm" className="ml-auto coarse:min-h-[44px]" onClick={() => setFolderSettings({ slug: folder.slug, name: folder.name })}>
          <Settings2 aria-hidden />Folder settings
        </Button>
      </div>}
      <DisplayRow count={total} sort={sort} group={group} layout={layout} onSort={setSort} onGroup={setGroup} onLayout={setLayout}
        selecting={mobile ? selecting || picked.size > 0 : undefined} onSelecting={mobile && layout === "list" ? next => { setSelecting(next); if (!next) setPicked(new Set()) } : undefined} />
      {note && <div className="flex min-w-0 flex-wrap items-center gap-2 rounded-card border border-dashed p-3 text-sm text-muted-foreground">
        <span className="min-w-0 flex-[1_1_260px]">{note}</span>
        {shown.length > 0 && <Button type="button" size="sm" variant="outline" onClick={() => setPicked(new Set(shown.map(entry => entry.slug)))}>Select all {shown.length}</Button>}
      </div>}
      {chosen.length > 0 && <BulkBar workspaceId={workspaceId} selected={chosen} folders={folderChoices} archivedView={filters.view === "archive"}
        onClear={() => setPicked(new Set())} onDone={() => setPicked(new Set())} />}
      {shown.length === 0
        ? <PageNoResults onClear={() => setFilters({ ...EMPTY_FILTERS, view: filters.view })} />
        : groupItems(shown, group).map(entry => <section key={entry.key} aria-label={entry.label || undefined} className="grid gap-2">
          {entry.label && <h2 className="text-xs font-semibold text-muted-foreground">{entry.label} <span className="tabular-nums">{entry.items.length}</span></h2>}
          <ItemsView items={entry.items} layout={layout} onOpen={pick} selection={layout === "list" && (!mobile || selecting || picked.size > 0) ? selection : undefined} peeked={peeked?.slug} />
        </section>)}
      {serverParams && (total > SERVER_PAGE_SIZE || serverPage > 0) && <nav aria-label="Artifact pages" className="flex items-center justify-end gap-2 text-sm text-muted-foreground">
        <span className="tabular-nums">{shown.length ? `${first + 1}–${first + shown.length}` : "None"} of {total}</span>
        <Button type="button" size="sm" variant="outline" disabled={serverPage === 0} onClick={() => setServerWindow({ key: serverKey, page: serverPage - 1 })}>Previous</Button>
        <Button type="button" size="sm" variant="outline" disabled={first + SERVER_PAGE_SIZE >= total} onClick={() => setServerWindow({ key: serverKey, page: serverPage + 1 })}>Next</Button>
      </nav>}
    </div>
  }

  return <div className="flex h-[calc(100dvh-var(--app-header-h)-var(--mobile-tab-bar-h))] min-h-0 flex-col bg-background">
    {subBar}
    <div className="@container relative flex min-h-0 flex-1 overflow-hidden">
      {!loading && !(error && !items.length) && <SecondaryPanel id="artifacts" label="Artifacts" collapsed={panelCollapsed} onCollapsedChange={shut => setPanelOpen(!shut)}>
        <LibraryPanel items={items} folders={folders} filters={filters} personal={personal}
          pin={item !== null ? { pinned, onToggle: () => setPinned(!pinned) } : undefined}
          onFiltersChange={next => {
            setFilters(next)
            // The route drops ?item= with its own replace; a second navigation would race it.
            if (item !== null && !onFiltersChange) onSelect(null)
          }} />
      </SecondaryPanel>}
      <div ref={contentRef} className={cn("min-w-0 flex-1", item !== null ? "flex min-h-0 flex-col overflow-hidden" : "overflow-y-auto")}>
        {item !== null
          ? <div className="flex min-h-0 flex-1 flex-col">{content}</div>
          : <div className="mx-auto flex w-full max-w-6xl flex-col p-4 md:p-6">{content}</div>}
      </div>
      {/* Beside the list on a wide app; over it, behind a scrim, where the app is narrower. */}
      {peeked && <>
        <div aria-hidden data-testid="peek-scrim" className="absolute inset-0 z-20 hidden bg-foreground/6 @max-[1180px]:block" onClick={() => setPeek(null)} />
        <div className="flex w-[330px] shrink-0 flex-col border-l bg-background @max-[1180px]:absolute @max-[1180px]:inset-y-0 @max-[1180px]:right-0 @max-[1180px]:z-30 @max-[1180px]:shadow-lg @max-[560px]:w-full">
          <ArtifactPeek key={peeked.slug} workspaceId={workspaceId} item={peeked} onOpen={() => open(peeked.slug)} onClose={() => setPeek(null)}
            favorite={personal.favorites?.has(peeked.slug) ?? false} onFavorite={() => toggleFavorite(peeked.slug)} />
        </div>
      </>}
    </div>
    {folderSettings && <FolderSettingsDialog workspaceId={workspaceId} folder={folderSettings} open onOpenChange={open => { if (!open) setFolderSettings(null) }} />}
    {/* A just-created artifact is not in the list yet. With its ID it opens at /artifacts/<id> at once: opening it by
        slug first mounted the editor (whose admission refreshes the session) right before the exported build reloaded
        the page as /artifacts/<id>, losing that refresh's cookies and signing the person out (#4902). */}
    {newKind !== undefined && <NewArtifactDialog workspaceId={workspaceId} open onOpenChange={open => { if (!open) setNewKind(undefined) }}
      defaultKind={newKind ?? undefined} defaultFolderSlug={filters.folder || undefined}
      onCreated={(slug, id) => {
        setNewKind(undefined)
        if (slug) setMadeHere([slug, ...(Array.isArray(madeHere) ? madeHere : []).filter(entry => entry !== slug)].slice(0, 50))
        refresh()
        if (id) onSelect(id)
        else if (slug) onSelect(null, slug)
      }} />}
  </div>
}
