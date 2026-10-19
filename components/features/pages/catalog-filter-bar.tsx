"use client"

import { Button } from "@/components/ui/button"
import {
  EMPTY_CATALOG_FILTERS,
  catalogFilterCount,
  toggle,
  type CatalogFacets,
  type CatalogFilters,
} from "@/lib/pages/catalog-share"

const TYPE_LABEL = { document: "Document", page: "Page" } as const
const SCOPE_LABEL = { private: "Private", shared: "In a folder", unknown: "Audience unknown" } as const
const STATE_LABEL = { fresh: "Fresh", stale: "Stale", failed: "Failed", never_produced: "No data yet" } as const

/** Facets come from `facets`, which the caller builds from authorized rows only. */
export function CatalogFilterBar({ facets, value, onChange }: {
  facets: CatalogFacets
  value: CatalogFilters
  onChange: (next: CatalogFilters) => void
}) {
  const chip = <T extends string>(group: string, key: keyof CatalogFilters, id: T, label: string, count: number) => {
    const list = value[key] as T[]
    const on = list.includes(id)
    return <Button key={`${group}:${id}`} type="button" size="sm" variant={on ? "secondary" : "outline"} aria-pressed={on}
      disabled={!on && count === 0} onClick={() => onChange({ ...value, [key]: toggle(list, id) })}>{label} <span className="tabular-nums text-muted-foreground">{count}</span></Button>
  }
  return <div role="group" aria-label="Catalog filters" className="flex flex-wrap items-center gap-2">
    <div role="group" aria-label="Type" className="flex flex-wrap gap-1">
      {(Object.keys(TYPE_LABEL) as (keyof typeof TYPE_LABEL)[]).map(t => chip("type", "types", t, TYPE_LABEL[t], facets.types[t]))}
    </div>
    <div role="group" aria-label="Scope" className="flex flex-wrap gap-1">
      {(Object.keys(SCOPE_LABEL) as (keyof typeof SCOPE_LABEL)[]).map(s => chip("scope", "scopes", s, SCOPE_LABEL[s], facets.scopes[s]))}
    </div>
    {facets.owners.length > 1 && <div role="group" aria-label="Owner" className="flex flex-wrap gap-1">
      {facets.owners.map(o => chip("owner", "owners", o.ref, o.label, o.count))}
    </div>}
    <div role="group" aria-label="State" className="flex flex-wrap gap-1">
      {(Object.keys(STATE_LABEL) as (keyof typeof STATE_LABEL)[]).map(s => chip("state", "states", s, STATE_LABEL[s], facets.states[s]))}
    </div>
    <p className="w-full text-xs text-muted-foreground">Slides, Board, App and Design are not in this catalog yet.</p>
    {catalogFilterCount(value) > 0 && <Button type="button" size="sm" variant="ghost" onClick={() => onChange(EMPTY_CATALOG_FILTERS)}>Clear filters</Button>}
  </div>
}
