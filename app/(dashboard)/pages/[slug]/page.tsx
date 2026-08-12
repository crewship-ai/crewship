"use client"

import { useParams } from "next/navigation"

import { Skeleton } from "@/components/ui/skeleton"
import { useWorkspace } from "@/hooks/use-workspace"
import { PagesLayout } from "@/components/features/pages/pages-layout"

// /pages/<slug> — one page's panel grid (docs/prd/pages.md §9).
//
// Same shell as /pages: the slug is what turns the main pane from the
// overview into the page. A page is slug-addressable because the slug goes
// in a URL — internal/pages/spec.go says so in the validator.
export default function PageDetailPage() {
  const { workspaceId, loading: wsLoading } = useWorkspace()
  const params = useParams<{ slug: string | string[] }>()
  const raw = params?.slug
  const slug = Array.isArray(raw) ? raw[0] : raw

  if (wsLoading || !workspaceId || !slug) {
    return (
      <div className="flex h-[calc(100vh-48px)] flex-col gap-3 p-4">
        <Skeleton className="h-9 w-full" />
        <div className="flex flex-1 gap-3">
          <Skeleton className="h-full w-60" />
          <Skeleton className="h-full flex-1" />
        </div>
      </div>
    )
  }

  return <PagesLayout workspaceId={workspaceId} slug={slug} />
}
