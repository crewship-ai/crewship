// Facet state for the Tools (MCP) tab.
//
// Its own file so `facets.ts` can import the type without pulling in a React
// component, and so the tab's filter shape sits next to the notification one
// rather than inside a view.

export interface McpFilters {
  /** Composio toolkit slug — gmail, github, … */
  toolkit: string | null
  /** Composio user id an account belongs to. */
  user: string | null
}

export const EMPTY_MCP_FILTERS: McpFilters = { toolkit: null, user: null }

export function mcpFiltersActive(f: McpFilters): number {
  return (f.toolkit ? 1 : 0) + (f.user ? 1 : 0)
}
