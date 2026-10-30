"use client"

import { useEffect } from "react"
import { useRouter, useSearchParams } from "next/navigation"
import dynamic from "next/dynamic"
import { Skeleton } from "@/components/ui/skeleton"
import { usePageWorkspace } from "@/hooks/use-page-workspace"
import { artifactsDesign, artifactsLibrary } from "@/lib/feature-flags"
import { ArtifactsLibrary } from "./artifacts-library"
import { filtersFromParams, filtersToParams } from "./library-filter"

const DesignEditor = dynamic(() => import("@/components/features/artifacts-design/design-editor").then(module => module.DesignEditor), { ssr: false })

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
export function ArtifactsRoute({ item, bySlug }: { item: string | null; bySlug?: boolean }) {
  const params = useSearchParams()
  const router = useRouter()
  const design = artifactsDesign()
  const enabled = artifactsLibrary() || design
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
  const select = (id: string | null, slug?: string) => {
    const next = filtersToParams(filters, workspaceQuery)
    if (id) return router.push(artifactHref(id, next))
    // An artifact without an immutable ID keeps the slug address it always had.
    if (slug) next.set("item", slug)
    router.push(`/artifacts${next.size ? `?${next}` : ""}`)
  }
  // A link from before IDs, /artifacts?item=<slug>, moves to the ID address once the slug resolves.
  const canonical = (id: string) => router.replace(artifactHref(id, filtersToParams(filters, workspaceQuery)))
  // A filter change replaces the entry: typing a search is not a trail of Back steps.
  const filter = (nextFilters: typeof filters) => {
    const next = filtersToParams(nextFilters, workspaceQuery)
    router.replace(`/artifacts${next.size ? `?${next}` : ""}`)
  }
  const designSlug = design && params?.get("design") === "1" ? params.get("item") : null
  const version = Number(params?.get("version"))
  if (designSlug) return <div className="h-full p-4"><DesignEditor workspaceId={workspaceId} slug={designSlug} initialVersion={Number.isSafeInteger(version) && version > 0 ? version : undefined} /></div>
  return <ArtifactsLibrary workspaceId={workspaceId} item={item} bySlug={bySlug} onSelect={select} onCanonical={canonical} filters={filters} onFiltersChange={filter} />
}
