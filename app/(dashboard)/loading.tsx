import { PageLoading, QUIET_LOAD_MS } from "@/components/layout/page-state"

/**
 * The route-transition gap. Individual pages already render their own
 * skeletons once they are mounted and fetching, but between tapping a
 * destination and that component existing there was nothing at all — on a
 * phone's slower CPU and network that blank window is long enough to read as
 * a dropped tap, which invites a second one.
 *
 * Harbor 2 G08: the kit's loading state, list-shaped — nothing for the first
 * second, then the skeleton, then "Still loading…" after ten. An area whose
 * page is cards can ship its own loading.tsx with `variant="cards"`.
 */
export default function DashboardLoading() {
  return <PageLoading delay={QUIET_LOAD_MS} />
}
