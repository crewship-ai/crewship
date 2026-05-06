"use client"

// ConnectorCatalog — the catalog-first replacement for AddMCPWizard's
// 4-step flow. Renders branded tiles in a grid, search-driven, with a
// single "Add custom MCP server" escape-hatch link at the bottom for
// the long tail. Click a tile → fires onSelect; the parent opens
// ConnectorConnectSheet for that manifest.
//
// TDD STUB — body throws until implemented. Tests in
// __tests__/connector-catalog.test.tsx drive the contract.

import type { ReactElement } from "react"
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

export function ConnectorCatalog(_: ConnectorCatalogProps): ReactElement {
  throw new Error("TDD STUB — implement ConnectorCatalog")
}
