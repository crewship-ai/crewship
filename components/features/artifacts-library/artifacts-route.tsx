"use client"

import { useEffect } from "react"
import { useRouter, useSearchParams } from "next/navigation"
import { Skeleton } from "@/components/ui/skeleton"
import { usePageWorkspace } from "@/hooks/use-page-workspace"
import { artifactsLibrary } from "@/lib/feature-flags"
import { ArtifactsLibrary } from "./artifacts-library"
import { filtersFromParams, filtersToParams } from "./library-filter"

/** Where one artifact opens: its own address inside Artifacts, with the view it was opened from. */
export function artifactHref(id: string, params: URLSearchParams) {
  return `/artifacts/${encodeURIComponent(id)}${params.size ? `?${params}` : ""}`
}

/**
 * The Artifacts library behind its flag. /artifacts is the library and
 * /artifacts/<id> one open artifact; the view and filters stay in their own
 * parameters next to the workspace, so a detail or a filtered view survives
 * reload, can be shared, and Back returns to the list it was opened from.
 */
export function ArtifactsRoute({ item }: { item: string | null }) {
  const params = useSearchParams()
  const router = useRouter()
  const enabled = artifactsLibrary()
  const { workspaceId, loading, linkUnavailable } = usePageWorkspace()
  const workspaceQuery = new URLSearchParams()
  const linked = params?.get("workspace_id")
  if (linked) workspaceQuery.set("workspace_id", linked)

  useEffect(() => {
    // The static export has no server redirect; a saved link still lands on Pages.
    if (!enabled) router.replace(`/pages${workspaceQuery.size ? `?${workspaceQuery}` : ""}`)
  }, [enabled]) // eslint-disable-line react-hooks/exhaustive-deps

  if (!enabled) return null
  if (linkUnavailable) return <p className="p-6" role="alert">Artifacts unavailable.</p>
  if (loading || !workspaceId) return <Skeleton className="m-6 h-12" />

  const filters = filtersFromParams(params)
  const select = (id: string | null) => {
    const next = filtersToParams(filters, workspaceQuery)
    router.push(id ? artifactHref(id, next) : `/artifacts${next.size ? `?${next}` : ""}`)
  }
  // A filter change replaces the entry: typing a search is not a trail of Back steps.
  const filter = (nextFilters: typeof filters) => {
    const next = filtersToParams(nextFilters, workspaceQuery)
    router.replace(`/artifacts${next.size ? `?${next}` : ""}`)
  }
  return <ArtifactsLibrary workspaceId={workspaceId} item={item} onSelect={select} filters={filters} onFiltersChange={filter} />
}
