"use client"

import { Suspense, useEffect } from "react"
import { useRouter, useSearchParams } from "next/navigation"
import { Skeleton } from "@/components/ui/skeleton"
import { usePageWorkspace } from "@/hooks/use-page-workspace"
import dynamic from "next/dynamic"
import { artifactsDesign, artifactsLibrary } from "@/lib/feature-flags"
import { ArtifactsLibrary } from "@/components/features/artifacts-library/artifacts-library"
import { filtersFromParams, filtersToParams } from "@/components/features/artifacts-library/library-filter"

const DesignEditor = dynamic(() => import("@/components/features/artifacts-design/design-editor").then(module => module.DesignEditor), { ssr: false })

// /artifacts — the Artifacts library behind its flag. The open artifact lives
// in ?item= and the view and filters in their own parameters next to the
// workspace, so a detail or a filtered view survives reload and can be shared.
export default function ArtifactsPage() {
  return <Suspense fallback={<Skeleton className="m-6 h-12" />}><ArtifactsEntry /></Suspense>
}

function ArtifactsEntry() {
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
  const select = (slug: string | null) => {
    const next = filtersToParams(filters, workspaceQuery)
    if (slug) next.set("item", slug)
    router.push(`/artifacts${next.size ? `?${next}` : ""}`)
  }
  // A filter change replaces the entry: typing a search is not a trail of Back steps.
  const filter = (nextFilters: typeof filters) => {
    const next = filtersToParams(nextFilters, workspaceQuery)
    router.replace(`/artifacts${next.size ? `?${next}` : ""}`)
  }
  if (design && params?.get("design") === "1" && params.get("item")) return <div className="h-full p-4"><DesignEditor workspaceId={workspaceId} slug={params.get("item")!} initialVersion={Number.isSafeInteger(Number(params.get("version"))) && Number(params.get("version")) > 0 ? Number(params.get("version")) : undefined} /></div>
  return <ArtifactsLibrary workspaceId={workspaceId} item={params?.get("item") || null} onSelect={select} filters={filters} onFiltersChange={filter} />
}
