"use client"

// ConnectorCatalog — catalog-first replacement for AddMCPWizard's
// 4-step flow. Renders branded tiles in a grid, search-driven, with
// a single "Add custom MCP server" escape-hatch button at the bottom
// for the long tail. Click a tile → fires onSelect; the parent opens
// ConnectorConnectSheet for that manifest.
//
// Tests in __tests__/connector-catalog.test.tsx drive the contract:
//   - one tile per item, name + description rendered
//   - search input filters case-insensitively over name AND description
//   - empty-state copy when nothing matches; suppressed while loading
//   - escape-hatch button always rendered, even when filter is empty

import { useMemo, useState, type ReactElement } from "react"

import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"

import type { ConnectorListItem } from "./types"

export interface ConnectorCatalogProps {
  /** Initial catalog items. Parent fetches; component renders. */
  items: ConnectorListItem[]
  /** Called when user clicks a tile. */
  onSelect: (item: ConnectorListItem) => void
  /** Called when user clicks the "Add custom MCP server" escape hatch. */
  onCustom: () => void
  /** Optional initial search value (e.g. URL state). */
  initialSearch?: string
  /** Loading state — when true, render a skeleton. */
  loading?: boolean
}

export function ConnectorCatalog(props: ConnectorCatalogProps): ReactElement {
  const { items, onSelect, onCustom, initialSearch = "", loading = false } = props
  const [search, setSearch] = useState(initialSearch)

  // Case-insensitive substring match over name + description. Memoized
  // so a parent re-render with the same items doesn't re-walk the list.
  const filtered = useMemo(() => {
    const needle = search.trim().toLowerCase()
    if (!needle) return items
    return items.filter((it) => {
      const hay = (it.name + " " + it.description).toLowerCase()
      return hay.includes(needle)
    })
  }, [items, search])

  // Loading and empty render differently: loading suppresses the
  // "no connectors" copy because we don't yet know if the catalog is
  // empty or just slow.
  const showEmpty = !loading && filtered.length === 0

  return (
    <div className="flex flex-col gap-4">
      <Input
        type="search"
        placeholder="Search connectors…"
        value={search}
        onChange={(e) => setSearch(e.currentTarget.value)}
        aria-label="Search connectors"
      />

      {loading && items.length === 0 ? (
        // Skeleton tiles — purely visual filler while the parent fetches.
        <div
          className="grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-3"
          aria-busy="true"
          aria-live="polite"
        >
          {[0, 1, 2, 3, 4, 5].map((i) => (
            <div
              key={i}
              data-testid="connector-tile-skeleton"
              className="h-24 animate-pulse rounded-md border bg-muted/40"
            />
          ))}
        </div>
      ) : (
        <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-3">
          {filtered.map((it) => (
            <button
              key={it.id}
              type="button"
              onClick={() => onSelect(it)}
              className="flex flex-col items-start gap-1 rounded-md border bg-card p-4 text-left shadow-sm transition-colors hover:bg-accent hover:text-accent-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
            >
              <span className="text-sm font-medium">{it.name}</span>
              <span className="text-xs text-muted-foreground">{it.description}</span>
            </button>
          ))}
        </div>
      )}

      {showEmpty && (
        <div
          role="status"
          aria-live="polite"
          className="rounded-md border border-dashed p-6 text-center text-sm text-muted-foreground"
        >
          No connectors match “{search}”. Try a different term or add a custom MCP server below.
        </div>
      )}

      <div className="flex justify-center">
        <Button variant="link" type="button" onClick={onCustom}>
          Add custom MCP server
        </Button>
      </div>
    </div>
  )
}
