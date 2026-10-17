import { useEffect, type Dispatch, type SetStateAction } from "react"

import type { InboxV2Filters } from "./inbox-v2-derive"

/**
 * Keep the view in step with the URL for the whole life of the route.
 *
 * Reading `?item=` and `?filter=` only in the `useState` initializers meant they
 * were honoured on the first mount and never again: Next keeps the component
 * mounted across a same-route navigation, so a `router.push()` from the inbox
 * bell changed the URL and left the reading pane on whatever was already open.
 *
 * The first effect existed for that reason but only ever *set* a selection.
 * Leaving `/inbox?item=x` for plain `/inbox` therefore kept row x in the
 * pane — a link that names no row is a request to show no row, and answering it
 * with the previous one is how a person ends up deciding the wrong request. The
 * search filter had no effect at all, so `?filter=riley` after `?filter=casey`
 * kept filtering on casey.
 *
 * The agent (`?agent_id=` from old /approvals links, `?agent=<slug>` from the
 * agent pages) is the Agent facet, not search text: it follows the URL the
 * same way, and a link that names no agent clears it. `undefined` leaves the
 * facet alone, for callers that do not read it from the URL.
 *
 * No effect fights the user: each re-runs only when the URL value itself
 * changes, so typing in the search box, picking a facet or clicking a row —
 * none of which touches the query string — is never clobbered.
 */
export function useInboxV2DeepLink(
  requestedID: string | null,
  requestedSearch: string,
  setSelectedKey: Dispatch<SetStateAction<string | null>>,
  setFilters: Dispatch<SetStateAction<InboxV2Filters>>,
  requestedAgent?: string | null,
) {
  // `request:<id>` rather than `inbox:<id>`: the caller of ?item= knows an id,
  // not which source owns it. selectEntry resolves it against both.
  useEffect(() => {
    setSelectedKey(requestedID ? `request:${requestedID}` : null)
  }, [requestedID, setSelectedKey])

  useEffect(() => {
    setFilters((current) => (current.search === requestedSearch ? current : { ...current, search: requestedSearch }))
  }, [requestedSearch, setFilters])

  useEffect(() => {
    if (requestedAgent === undefined) return
    setFilters((current) => (current.agent === requestedAgent ? current : { ...current, agent: requestedAgent }))
  }, [requestedAgent, setFilters])
}

/** The agent a URL names: `agent_id` (old /approvals links) wins over `agent`. */
export function requestedAgentOf(params: Pick<URLSearchParams, "get"> | null): string | null {
  const id = params?.get("agent_id")?.trim()
  if (id) return id
  const slug = params?.get("agent")?.trim()
  return slug || null
}
