type PageProvenance = { pageId: string; slug: string; name: string; snapshotAt: string }

/** Only a complete server-built Page context gets a badge after history reload. */
export function pageProvenanceForTurn(turn: { metadata?: unknown; parts?: { metadata?: unknown }[] }): PageProvenance | null {
  for (const source of [turn.metadata, ...(turn.parts ?? []).map((p) => p.metadata)]) {
    if (!source || typeof source !== "object") continue
    const raw = (source as Record<string, unknown>).page_context
    if (!raw || typeof raw !== "object") continue
    const page = raw as Record<string, unknown>
    if (typeof page.page_id !== "string" || !page.page_id ||
        typeof page.slug !== "string" || !page.slug || page.slug.length > 128 ||
        typeof page.name !== "string" || !page.name || page.name.length > 256 ||
        typeof page.snapshot_at !== "string" || !Number.isFinite(Date.parse(page.snapshot_at))) continue
    return { pageId: page.page_id, slug: page.slug, name: page.name, snapshotAt: page.snapshot_at }
  }
  return null
}
