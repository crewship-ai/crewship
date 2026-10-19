"use client"

import * as React from "react"
import type { PageView } from "@/hooks/use-pages"
import { Button } from "@/components/ui/button"
import { artifactCopyMove } from "@/lib/feature-flags"
import { catalogFacets, EMPTY_CATALOG_FILTERS, filterCatalog, toggle, type CatalogFilters } from "@/lib/pages/catalog-share"
import { CatalogFilterBar } from "./catalog-filter-bar"

/** Page metadata only: this catalog never guesses a Document revision or author. */
export function DocumentCatalog({ pages, onSelect, immutableTargets = false, filterable = false, onShare, onDuplicate }: {
  pages: PageView[]
  onSelect: (reference: string) => void
  immutableTargets?: boolean
  /** Type, scope, owner and state filters over the rows this catalog was given. */
  filterable?: boolean
  /** Given, items can be selected and moved into a working folder. */
  onShare?: (pages: PageView[]) => void
  /** Given, and the copy flag is on, each card offers Duplicate (the only way to a second copy). */
  onDuplicate?: (page: PageView) => void
}) {
  const [filters, setFilters] = React.useState<CatalogFilters>(EMPTY_CATALOG_FILTERS)
  const [picked, setPicked] = React.useState<string[]>([])
  const all = pages.filter(page => page.hasDocument === true)
  const facets = React.useMemo(() => catalogFacets(all), [all])
  const documents = filterable ? filterCatalog(all, filters) : all
  if (!all.length) return null
  const selected = documents.filter(page => picked.includes(page.id) && page.canMove !== false)
  return <section aria-label="Documents" className="space-y-3 border-b p-6">
    <h2 className="text-lg font-semibold">Documents</h2>
    {filterable && <CatalogFilterBar facets={facets} value={filters} onChange={setFilters} />}
    {onShare && <div className="flex items-center gap-2">
      <Button type="button" size="sm" variant="outline" disabled={!selected.length} onClick={() => onShare(selected)}>Move to a working folder{selected.length ? ` (${selected.length})` : ""}</Button>
    </div>}
    {filterable && !documents.length && <p role="status" className="text-sm text-muted-foreground">Nothing matches these filters.</p>}
    {immutableTargets && documents.some(page => !page.immutablePageId) && <p role="status" className="text-sm text-muted-foreground">A Document is unavailable. Refresh the list and try again.</p>}
    <div className="grid gap-3 md:grid-cols-2 xl:grid-cols-3">{documents.map(page => {
      const target = immutableTargets ? (page.immutablePageId ? `@${page.immutablePageId}` : undefined) : page.slug
      return <div key={page.id} className="flex items-start gap-2">
        {onShare && <input type="checkbox" className="mt-4 h-4 w-4" aria-label={`Select ${page.name}`} disabled={page.canMove === false} checked={picked.includes(page.id)} onChange={() => setPicked(toggle(picked, page.id))} />}
        <Button variant="outline" className="h-auto min-h-24 flex-1 items-start justify-start whitespace-normal p-4 text-left" disabled={!target} onClick={() => { if (target) onSelect(target) }}><span><span className="block font-medium">{page.name}</span>{page.description && <span className="mt-1 block text-sm font-normal text-muted-foreground">{page.description}</span>}<span className="mt-2 block text-xs font-normal text-muted-foreground">Doc</span></span></Button>
        {onDuplicate && artifactCopyMove() && <Button type="button" size="sm" variant="ghost" aria-label={`Duplicate ${page.name}`} onClick={() => onDuplicate(page)}>Duplicate</Button>}
      </div>
    })}</div>
  </section>
}
