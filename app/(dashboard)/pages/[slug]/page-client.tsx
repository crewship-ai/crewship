"use client"


import { Skeleton } from "@/components/ui/skeleton"
import { useUrlSegment } from "@/lib/use-url-segment"
import { useWorkspace } from "@/hooks/use-workspace"
import { PagesLayout } from "@/components/features/pages/pages-layout"

// /pages/<slug> — one page's panel grid (docs/prd/pages.md §9).
//
// Same shell as /pages: the slug is what turns the main pane from the
// overview into the page. A page is slug-addressable because the slug goes
// in a URL — internal/pages/spec.go says so in the validator.
const PAGE_PATH_RE = /^\/pages\/([^/]+)\/?$/

export function PageDetailClient() {
  const { workspaceId, loading: wsLoading } = useWorkspace()
  // The slug comes from the URL, never from useParams(). Under
  // `output: "export"` this route is exported once, as /pages/_/index.html,
  // and the Go binary serves that file for every real slug — so useParams()
  // hands back the literal "_" placeholder and the page renders
  // `No page is addressed "_"`. useUrlSegment reads window.location after
  // mount and is the repo's existing answer to exactly this bug class; the
  // issue, skill, mission and chat detail routes all had it first.
  const slug = useUrlSegment(PAGE_PATH_RE)

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
