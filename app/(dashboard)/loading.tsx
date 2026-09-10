import { Skeleton } from "@/components/ui/skeleton"

/**
 * The route-transition gap. Individual pages already render their own
 * skeletons once they are mounted and fetching, but between tapping a
 * destination and that component existing there was nothing at all — on a
 * phone's slower CPU and network that blank window is long enough to read as
 * a dropped tap, which invites a second one.
 */
export default function DashboardLoading() {
  return (
    <div className="space-y-4 p-4 sm:p-6" aria-busy="true" aria-label="Loading">
      <Skeleton className="h-6 w-40" />
      <Skeleton className="h-4 w-64" />
      <div className="grid gap-3 grid-cols-2 lg:grid-cols-4">
        {Array.from({ length: 4 }, (_, i) => (
          <Skeleton key={i} className="h-20" />
        ))}
      </div>
      <Skeleton className="h-48" />
    </div>
  )
}
