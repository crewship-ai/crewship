"use client"

import { usePage } from "@/hooks/use-pages"
import { PageApplicationView } from "@/components/features/pages/page-application"
import { PageView } from "@/components/features/pages/page-view"

/**
 * A Page's content inside its Artifacts detail: the same application and
 * panel views Pages draws, read by the immutable page ID so a renamed or
 * reassigned slug never shows another page.
 */
export function ArtifactPageContent({ workspaceId, pageId, onBack }: { workspaceId: string; pageId: string; onBack: () => void }) {
  const slug = "@" + pageId
  const detail = usePage(workspaceId, slug)
  const view = <PageView page={detail.page} slug={slug} loading={detail.loading} error={detail.error} notFound={detail.notFound}
    onBack={onBack} backLabel="All artifacts" workspaceId={workspaceId} live={detail.live} />
  return <div className="relative flex min-h-[520px] flex-1 flex-col overflow-hidden rounded-card border">
    {detail.page && !detail.loading && !detail.error && !detail.accessDenied
      ? <PageApplicationView workspaceId={workspaceId} slug={slug} page={detail.raw} fallback={view} />
      : view}
  </div>
}
