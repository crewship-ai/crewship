export type LibraryKind = "files" | "artifacts" | "tools" | "work"
export type LibraryFilter = "all" | LibraryKind

/** One explicit conversation link. Unlinked objects are never part of this list. */
export type LibraryItem =
  | { status: "readable"; linkId: string; kind: LibraryKind; name: string; revisionKey: string }
  | { status: "restricted"; linkId: string }

export interface LibraryEntry { linkId: string; label: string; kind: LibraryKind | null; restricted: boolean }

/** Same revision is shown once; restricted links stay anonymous and are counted only under All. */
export function buildLibrary(items: LibraryItem[], filter: LibraryFilter) {
  const seen = new Set<string>()
  const entries: LibraryEntry[] = []
  for (const item of items) {
    if (item.status === "restricted") { entries.push({ linkId: item.linkId, label: "Restricted item", kind: null, restricted: true }); continue }
    const key = `${item.kind}:${item.revisionKey}`
    if (seen.has(key)) continue
    seen.add(key)
    entries.push({ linkId: item.linkId, label: item.name, kind: item.kind, restricted: false })
  }
  const count = (kind: LibraryKind) => entries.filter(entry => entry.kind === kind).length
  return {
    counts: { all: entries.length, files: count("files"), artifacts: count("artifacts"), tools: count("tools"), work: count("work") } satisfies Record<LibraryFilter, number>,
    entries: filter === "all" ? entries : entries.filter(entry => entry.kind === filter),
  }
}
