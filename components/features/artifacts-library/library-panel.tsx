"use client"
import { AlertCircle, Copy, Folder, FolderOpen, Home, Inbox, Library } from "lucide-react"
import { PanelSpaces } from "@/components/layout/panel-spaces"
import {
  SidebarActiveChip, SidebarActiveChips, SidebarFilterButton, SidebarRow, SidebarSearch, SidebarSection, SidebarToolbar,
} from "@/components/layout/sidebar-kit"
import { SidebarFacetBlock, SidebarInlineFacet, useSidebarFacets } from "@/components/layout/sidebar-facets"
import type { PageFolderView } from "@/hooks/use-page-folders"
import { KIND_ICON } from "@/components/features/chat/artifact-context/new-artifact/artifact-kinds"
import { kindLabel, LIBRARY_KINDS, type LibraryItem } from "./library-items"
import {
  activeFacetCount, EMPTY_FILTERS, facetCounts, inView, OWNER_NONE,
  type Access, type LibraryFilters, type LibraryView, type VersionState,
} from "./library-filter"

const ACCESS_LABEL: Record<Access, string> = { open: "Everyone with access", private: "Private" }
const VERSION_LABEL: Record<VersionState, string> = { shared: "Shared version", unshared: "Not shared yet", unversioned: "No versions" }
const ownerText = (owner: string) => (owner === OWNER_NONE ? "Someone else" : owner)

type Spaces = "home" | "all" | "attention"
const SPACES = [
  { key: "home", label: "Home", icon: Home },
  { key: "all", label: "All artifacts", icon: Library },
  { key: "attention", label: "Needs attention", icon: AlertCircle },
] as const

const meta = (n: number) => <span className="shrink-0 text-micro tabular-nums text-muted-foreground">{n}</span>

function toggle<T extends string>(list: T[], value: T): T[] {
  return list.includes(value) ? list.filter(entry => entry !== value) : [...list, value]
}

/**
 * The Artifacts side panel (Harbor 2 anatomy): search and Filter on top, the
 * spaces, then Views and Folders. Facets open inside the panel under the
 * toolbar; chips say what is set while the block is closed.
 */
export function LibraryPanel({ items, folders, filters, onFiltersChange }: {
  items: LibraryItem[]
  folders: PageFolderView[]
  filters: LibraryFilters
  onFiltersChange: (next: LibraryFilters) => void
}) {
  const facets = useSidebarFacets()
  const set = (patch: Partial<LibraryFilters>) => onFiltersChange({ ...filters, ...patch })
  // Home has no list to narrow; choosing a facet there shows everything it matches.
  const narrow = (patch: Partial<LibraryFilters>) => set({ ...patch, ...(filters.view === "home" ? { view: "all" as const } : {}) })
  const counts = facetCounts(items, filters.view === "home" ? { ...filters, view: "all" } : filters)
  const viewCount = (view: LibraryView) => inView(items, view).length
  const folderCount = (slug: string) => items.filter(item => !item.empty && (item.folder?.slug ?? "") === slug).length
  const unfiled = folderCount("")
  const active = activeFacetCount(filters)

  const chips: { key: string; text: string; remove: () => void }[] = [
    ...filters.kinds.map(kind => ({ key: `kind:${kind}`, text: `Kind: ${kindLabel(kind)}`, remove: () => set({ kinds: toggle(filters.kinds, kind) }) })),
    ...filters.access.map(access => ({ key: `access:${access}`, text: `Who can open: ${ACCESS_LABEL[access]}`, remove: () => set({ access: toggle(filters.access, access) }) })),
    ...filters.version.map(version => ({ key: `version:${version}`, text: `Version: ${VERSION_LABEL[version]}`, remove: () => set({ version: toggle(filters.version, version) }) })),
    ...filters.owners.map(owner => ({ key: `owner:${owner}`, text: `Owner: ${ownerText(owner)}`, remove: () => set({ owners: toggle(filters.owners, owner) }) })),
    ...(filters.folder !== null ? [{ key: "folder", text: `Folder: ${folders.find(folder => folder.slug === filters.folder)?.name ?? "Unfiled"}`, remove: () => set({ folder: null }) }] : []),
  ]

  return <div className="flex h-full min-h-0 flex-col">
    <SidebarToolbar>
      <div className="min-w-0 flex-1">
        <SidebarSearch value={filters.q} onValueChange={q => narrow({ q })}
          placeholder="Search, or kind: is: owner:" aria-label="Search artifacts" />
      </div>
      <SidebarFilterButton activeCount={active} {...facets.triggerProps}>Filter</SidebarFilterButton>
    </SidebarToolbar>

    <SidebarFacetBlock {...facets.blockProps} label="Filter artifacts" activeCount={active}
      onClear={() => onFiltersChange({ ...EMPTY_FILTERS, view: filters.view, q: filters.q })}>
      <SidebarInlineFacet label="Kind" limit={5} allLabel={`+${Math.max(0, counts.kinds.length - 5)} more`}
        values={counts.kinds.map(([kind, n]) => {
          const Icon = KIND_ICON[kind]
          return { value: kind, label: LIBRARY_KINDS.find(entry => entry.kind === kind)!.plural, count: n, icon: <Icon className="size-3.5 shrink-0 text-muted-foreground" aria-hidden /> }
        })}
        selected={filters.kinds} onToggle={kind => narrow({ kinds: toggle(filters.kinds, kind as LibraryItem["kind"]) })} onClear={() => set({ kinds: [] })} />
      <SidebarInlineFacet label="Who can open" values={counts.access.map(([access, n]) => ({ value: access, label: ACCESS_LABEL[access], count: n }))}
        selected={filters.access} onToggle={access => narrow({ access: toggle(filters.access, access as Access) })} onClear={() => set({ access: [] })} />
      <SidebarInlineFacet label="Version" values={counts.version.map(([version, n]) => ({ value: version, label: VERSION_LABEL[version], count: n }))}
        selected={filters.version} onToggle={version => narrow({ version: toggle(filters.version, version as VersionState) })} onClear={() => set({ version: [] })} />
      {counts.owners.length > 0 && <SidebarInlineFacet label="Owner" values={counts.owners.map(([owner, n]) => ({ value: owner, label: ownerText(owner), count: n }))}
        selected={filters.owners} onToggle={owner => narrow({ owners: toggle(filters.owners, owner) })} onClear={() => set({ owners: [] })} />}
    </SidebarFacetBlock>

    <div className="min-h-0 flex-1 overflow-y-auto pb-2">
      <PanelSpaces<Spaces> label="Artifacts" value={filters.view === "home" || filters.view === "attention" ? filters.view : "all"}
        onChange={view => set({ view })}
        items={SPACES.map(space => ({ ...space, count: space.key === "home" ? undefined : viewCount(space.key) }))} />

      {!facets.open && chips.length > 0 && <SidebarActiveChips className="pt-2">
        {chips.map(chip => <SidebarActiveChip key={chip.key} label={chip.text} onRemove={chip.remove}>{chip.text}</SidebarActiveChip>)}
      </SidebarActiveChips>}

      <SidebarSection label="Views">
        <SidebarRow selected={filters.view === "series"} aria-current={filters.view === "series" ? "page" : undefined} onSelect={() => set({ view: filters.view === "series" ? "all" : "series" })}>
          <Copy className="size-3.5 shrink-0 text-muted-foreground" aria-hidden />
          <span className="min-w-0 flex-1 truncate">Repeated runs</span>{meta(viewCount("series"))}
        </SidebarRow>
        <SidebarRow selected={filters.view === "empty"} aria-current={filters.view === "empty" ? "page" : undefined} onSelect={() => set({ view: filters.view === "empty" ? "all" : "empty" })}>
          <Inbox className="size-3.5 shrink-0 text-muted-foreground" aria-hidden />
          <span className="min-w-0 flex-1 truncate">Empty</span>{meta(viewCount("empty"))}
        </SidebarRow>
      </SidebarSection>

      {(folders.length > 0 || unfiled > 0) && <SidebarSection label="Folders" count={folders.length || undefined}>
        {folders.map(folder => {
          const on = filters.folder === folder.slug
          const Icon = on ? FolderOpen : Folder
          return <SidebarRow key={folder.slug} selected={on} aria-current={on ? "page" : undefined}
            onSelect={() => narrow({ folder: on ? null : folder.slug })}>
            <Icon className="size-3.5 shrink-0 text-muted-foreground" aria-hidden />
            <span className="min-w-0 flex-1 truncate">{folder.name}</span>{meta(folderCount(folder.slug))}
          </SidebarRow>
        })}
        {folders.length > 0 && unfiled > 0 && <SidebarRow selected={filters.folder === ""} aria-current={filters.folder === "" ? "page" : undefined}
          onSelect={() => narrow({ folder: filters.folder === "" ? null : "" })}>
          <Folder className="size-3.5 shrink-0 text-muted-foreground-soft" aria-hidden />
          <span className="min-w-0 flex-1 truncate text-muted-foreground">Unfiled</span>{meta(unfiled)}
        </SidebarRow>}
      </SidebarSection>}
    </div>
  </div>
}
