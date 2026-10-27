import type { PageView } from "@/hooks/use-pages"

/** The kinds the library shows: each has a backend that stores and opens it. */
export type LibraryKind = "project" | "word" | "sheet" | "slides" | "dashboard"

export const LIBRARY_KINDS: { kind: LibraryKind; label: string; plural: string }[] = [
  { kind: "project", label: "Project", plural: "Projects" },
  { kind: "word", label: "Word", plural: "Word" },
  { kind: "sheet", label: "Sheet", plural: "Sheet" },
  { kind: "slides", label: "Slides", plural: "Slides" },
  { kind: "dashboard", label: "Dashboard", plural: "Dashboards" },
]

export const kindLabel = (kind: LibraryKind) => LIBRARY_KINDS.find(entry => entry.kind === kind)!.label

export interface LibraryItem {
  slug: string
  /** The immutable Page id, when the server sent a valid one. */
  pageId?: string
  name: string
  kind: LibraryKind
  /** The version readers see; null when the kind has no version head or nothing is shared. */
  sharedVersion: number | null
  /** A Word or Sheet with a version head is a native Craft and carries an edit lease. */
  native: boolean
  private: boolean
  ownerLabel: string | null
  updatedAt: Date | null
}

/** The version head names projects, Word and Sheet; older storage names the rest. Anything else is not listed. */
function kindOf(page: PageView): LibraryKind | null {
  if (page.artifactKind) return page.artifactKind
  if (page.hasWorkbook) return "sheet"
  if (page.hasSlides) return "slides"
  if (page.hasDocument) return "word"
  if (page.tally.total > 0) return "dashboard"
  return null
}

/** A crew by name and the caller as You; another person's account id is not a name, so it is left out. */
function ownerOf(page: PageView): string | null {
  if (page.ownerRef?.startsWith("crew/")) return page.ownerLabel
  return page.reach?.includes("owner") ? "You" : null
}

export function libraryItems(pages: PageView[]): LibraryItem[] {
  const items: LibraryItem[] = []
  for (const page of pages) {
    const kind = kindOf(page)
    if (!kind) continue
    items.push({
      slug: page.slug, name: page.name, kind,
      ...(page.immutablePageId ? { pageId: page.immutablePageId } : {}),
      sharedVersion: page.artifactSharedVersion ?? null,
      native: Boolean(page.artifactKind) && kind !== "project",
      // Only the server's explicit classification; ownership or a missing folder is not privacy.
      private: page.privateAudience === true,
      ownerLabel: ownerOf(page),
      updatedAt: page.updatedAt,
    })
  }
  // The server already orders by last edit; keep a stable newest-first order if it did not.
  return items.sort((a, b) => (b.updatedAt?.getTime() ?? 0) - (a.updatedAt?.getTime() ?? 0))
}

export function kindCounts(items: LibraryItem[]): [LibraryKind, number][] {
  return LIBRARY_KINDS.map(({ kind }) => [kind, items.filter(item => item.kind === kind).length] as [LibraryKind, number]).filter(([, n]) => n > 0)
}
