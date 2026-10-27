"use client"

import { Suspense, useEffect } from "react"
import { useRouter, useSearchParams } from "next/navigation"
import { Skeleton } from "@/components/ui/skeleton"
import { usePageWorkspace } from "@/hooks/use-page-workspace"
import { artifactsLibrary } from "@/lib/feature-flags"
import { ArtifactsLibrary } from "@/components/features/artifacts-library/artifacts-library"

// /artifacts — the Artifacts library behind its flag. The open artifact lives
// in ?item= next to the workspace, so a detail survives reload and can be shared.
export default function ArtifactsPage() {
  return <Suspense fallback={<Skeleton className="m-6 h-12" />}><ArtifactsEntry /></Suspense>
}

function ArtifactsEntry() {
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

  const select = (slug: string | null) => {
    const next = new URLSearchParams(workspaceQuery)
    if (slug) next.set("item", slug)
    router.push(`/artifacts${next.size ? `?${next}` : ""}`)
  }
  return <ArtifactsLibrary workspaceId={workspaceId} item={params?.get("item") || null} onSelect={select} />
}
