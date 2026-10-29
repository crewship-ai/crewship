"use client"

import { Suspense } from "react"
import { useSearchParams } from "next/navigation"
import { Skeleton } from "@/components/ui/skeleton"
import { usePageWorkspace } from "@/hooks/use-page-workspace"
import { useAccessMode } from "@/hooks/use-access-mode"
import { ArtifactProject } from "@/components/features/pages/artifact-project"
import CraftEditorPage from "../../pages/craft/editor/page"

// /artifacts/edit — where an artifact is edited, opened from its Artifacts
// detail. `?page_id=<id>&workspace_id=<ws>` is a native Word's editor behind
// its lock, the same entry as /pages/craft/editor; `&artifact=1` is a
// project's source editor, the same ArtifactProject Pages renders. Only the
// address stays inside Artifacts; locking and saving are unchanged.
export default function ArtifactEditPage() {
  return <Suspense fallback={<Skeleton className="m-6 h-12" />}><ArtifactEditEntry /></Suspense>
}

function ArtifactEditEntry() {
  const params = useSearchParams()
  return params?.get("artifact") === "1" ? <ProjectSource /> : <CraftEditorPage />
}

function ProjectSource() {
  const params = useSearchParams()
  const { workspaceId, loading, linkUnavailable } = usePageWorkspace()
  const accessMode = useAccessMode()
  const pageIds = params?.getAll("page_id") ?? []
  const pageId = pageIds.length === 1 ? pageIds[0] : ""
  if (linkUnavailable || !pageId || accessMode === "restricted") return <p className="p-6" role="alert">This artifact is not available.</p>
  if (loading || !workspaceId || accessMode === "loading") return <Skeleton className="m-6 h-12" />
  const back = `/artifacts/${encodeURIComponent(pageId)}?${new URLSearchParams({ workspace_id: workspaceId })}`
  return <ArtifactProject key={`${workspaceId}:${pageId}`} workspaceId={workspaceId} slug={"@" + pageId} back={{ href: back, label: "Back to artifact" }} />
}
