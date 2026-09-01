import { useEffect, type Dispatch, type SetStateAction } from "react"

import type { InboxV2Filters } from "./inbox-v2-derive"

/**
 * Keep the view in step with the URL for the whole life of the route.
 *
 * Reading `?item=` and `?agent=` only in the `useState` initializers meant they
 * were honoured on the first mount and never again: Next keeps the component
 * mounted across a same-route navigation, so a `router.push()` from the inbox
 * bell changed the URL and left the reading pane on whatever was already open.
 *
 * The first effect existed for that reason but only ever *set* a selection.
 * Leaving `/inbox-v2?item=x` for plain `/inbox-v2` therefore kept row x in the
 * pane — a link that names no row is a request to show no row, and answering it
 * with the previous one is how a person ends up deciding the wrong request. The
 * search filter had no effect at all, so `?agent=riley` after `?agent=casey`
 * kept filtering on casey.
 *
 * Neither effect fights the user: both re-run only when the URL value itself
 * changes, so typing in the search box or clicking a row — neither of which
 * touches the query string — is never clobbered.
 */
export function useInboxV2DeepLink(
  requestedID: string | null,
  requestedSearch: string,
  setSelectedKey: Dispatch<SetStateAction<string | null>>,
  setFilters: Dispatch<SetStateAction<InboxV2Filters>>,
) {
  // `request:<id>` rather than `inbox:<id>`: the caller of ?item= knows an id,
  // not which source owns it. selectEntry resolves it against both.
  useEffect(() => {
    setSelectedKey(requestedID ? `request:${requestedID}` : null)
  }, [requestedID, setSelectedKey])

  useEffect(() => {
    setFilters((current) => (current.search === requestedSearch ? current : { ...current, search: requestedSearch }))
  }, [requestedSearch, setFilters])
}
