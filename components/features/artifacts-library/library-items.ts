import type { PageFolderRef, PageView } from "@/hooks/use-pages"

/**
 * The kinds the library shows: each has a backend that stores and opens it.
 * A legacy Page whose body is panels is its own kind, Page, and is never
 * called a dashboard: only the server's explicit kind names a Dashboard.
 */
export type LibraryKind = "project" | "dashboard" | "page" | "word" | "sheet" | "slides"

/** Web things run in a sandbox; documents are Crafts or decks. */
export type LibraryFamily = "web" | "document"

export const LIBRARY_KINDS: { kind: LibraryKind; label: string; plural: string; family: LibraryFamily }[] = [
  { kind: "project", label: "Project", plural: "Projects", family: "web" },
  { kind: "dashboard", label: "Dashboard", plural: "Dashboards", family: "web" },
  { kind: "page", label: "Page", plural: "Pages", family: "web" },
  { kind: "word", label: "Word", plural: "Word", family: "document" },
  { kind: "sheet", label: "Sheet", plural: "Sheets", family: "document" },
  { kind: "slides", label: "Slides", plural: "Slides", family: "document" },
]

export const kindLabel = (kind: LibraryKind) => LIBRARY_KINDS.find(entry => entry.kind === kind)!.label
export const kindFamily = (kind: LibraryKind) => LIBRARY_KINDS.find(entry => entry.kind === kind)!.family

export interface LibraryItem {
  slug: string
  /** The immutable Page id, when the server sent a valid one. */
  pageId?: string
  name: string
  kind: LibraryKind
  family: LibraryFamily
  /** The version readers see; null when the kind has no version head or nothing is shared. */
  sharedVersion: number | null
  /** A Word or Sheet with a version head is a native Craft and carries an edit lease. */
  native: boolean
  /** An explicit server kind backed by the artifact VersionStore, never legacy panels. */
  projectBacked: boolean
  /** Nothing in it yet: no kind, no document, no panels. Hidden unless asked for. */
  empty: boolean
  private: boolean
  ownerLabel: string | null
  /** Unfiled is null; a server without folders sends nothing and that is null too. */
  folder: PageFolderRef | null
  updatedAt: Date | null
}

/** The version head names projects, dashboards, slides, Word and Sheet; older storage names the rest. */
function kindOf(page: PageView): LibraryKind {
  if (page.artifactKind) return page.artifactKind
  if (page.hasWorkbook) return "sheet"
  if (page.hasSlides) return "slides"
  if (page.hasDocument) return "word"
  return "page"
}

/** Legacy storage with nothing to open: no typed kind, no Craft, no deck, no panel. */
function isEmpty(page: PageView): boolean {
  return !page.artifactKind && !page.hasWorkbook && !page.hasSlides && !page.hasDocument && page.tally.total === 0
}

/** A crew by name and the caller as You; another person's account id is not a name, so it is left out. */
function ownerOf(page: PageView): string | null {
  if (page.ownerRef?.startsWith("crew/")) return page.ownerLabel
  return page.reach?.includes("owner") ? "You" : null
}

/** Every Page the caller can read, typed or not; `visibleItems` decides what the default views show. */
export function libraryItems(pages: PageView[]): LibraryItem[] {
  const items: LibraryItem[] = pages.map(page => {
    const kind = kindOf(page)
    const typedKind = page.artifactKind
    return {
      slug: page.slug, name: page.name, kind, family: kindFamily(kind),
      ...(page.immutablePageId ? { pageId: page.immutablePageId } : {}),
      sharedVersion: page.artifactSharedVersion ?? null,
      native: typedKind === "word" || typedKind === "sheet",
      projectBacked: typedKind === "project" || typedKind === "dashboard" || typedKind === "slides",
      empty: isEmpty(page),
      // Only the server's explicit classification; ownership or a missing folder is not privacy.
      private: page.privateAudience === true,
      ownerLabel: ownerOf(page),
      folder: page.folder ?? null,
      updatedAt: page.updatedAt,
    }
  })
  // The server already orders by last edit; keep a stable newest-first order if it did not.
  return items.sort((a, b) => (b.updatedAt?.getTime() ?? 0) - (a.updatedAt?.getTime() ?? 0))
}

/** The default views leave empty items out; the Empty view shows only them. Nothing is deleted. */
export function visibleItems(items: LibraryItem[], view: "default" | "empty" = "default"): LibraryItem[] {
  return items.filter(item => (view === "empty" ? item.empty : !item.empty))
}

export function kindCounts(items: LibraryItem[]): [LibraryKind, number][] {
  return LIBRARY_KINDS.map(({ kind }) => [kind, items.filter(item => item.kind === kind).length] as [LibraryKind, number]).filter(([, n]) => n > 0)
}

export interface ItemSeries {
  /** The shared name with the run marker taken off, e.g. "Smoke test". */
  stem: string
  kind: LibraryKind
  items: LibraryItem[]
}

/** A trailing run marker: "#12", "12", "run 4", "v3", "(2)", a date or a time stamp. */
const RUN_MARKER = /(?:[\s_-]*(?:#?\d+|run\s*\d+|v\d+|\(\d+\)|\d{4}-\d{2}-\d{2}(?:[t\s_]\d{2}[:.]?\d{2}(?:[:.]?\d{2})?z?)?))+$/i

export function seriesStem(name: string): string {
  return name.trim().replace(RUN_MARKER, "").trim()
}

/**
 * Repeated runs of the same thing — "Smoke test 1 … Smoke test 9" — so the
 * owner can tidy them up. Only a suggestion for a view: it never archives or
 * deletes, and a series needs `min` items of one kind with one stem.
 */
export function repeatedSeries(items: LibraryItem[], min = 3): ItemSeries[] {
  const groups = new Map<string, ItemSeries>()
  for (const item of items) {
    const stem = seriesStem(item.name)
    if (!stem || stem === item.name.trim()) continue
    const key = `${item.kind}\u0000${stem.toLowerCase()}`
    const group = groups.get(key) ?? { stem, kind: item.kind, items: [] }
    group.items.push(item)
    groups.set(key, group)
  }
  return [...groups.values()].filter(group => group.items.length >= min).sort((a, b) => b.items.length - a.items.length)
}
