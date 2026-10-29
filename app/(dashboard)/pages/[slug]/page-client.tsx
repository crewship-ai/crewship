"use client"

import { useEffect, useState, type ReactNode } from "react"
import { useRouter, useSearchParams } from "next/navigation"
import { ArtifactProject } from "@/components/features/pages/artifact-project"
import { Skeleton } from "@/components/ui/skeleton"
import { useUrlSegment } from "@/lib/use-url-segment"
import { usePageWorkspace } from "@/hooks/use-page-workspace"
import { PagesLayout } from "@/components/features/pages/pages-layout"
import { useAccessContext } from "@/components/features/restricted/access-notice"
import { RestrictedPages } from "@/components/features/pages/restricted-pages"
import { useAccessMode } from "@/hooks/use-access-mode"
import { useSessionSafe } from "@/hooks/use-auth"
import { usePage, type PageView } from "@/hooks/use-pages"
import { pagesOpenArtifacts } from "@/lib/feature-flags"
import { libraryItems } from "@/components/features/artifacts-library/library-items"

// /pages/<slug> — one page's panel grid (docs/specs/pages.md §9).
//
// Same shell as /pages: the slug is what turns the main pane from the
// overview into the page. A page is slug-addressable because the slug goes
// in a URL — internal/pages/spec.go says so in the validator.
const PAGE_PATH_RE = /^\/pages\/([^/]+)\/?$/

export function PageDetailClient() {
  const { workspaceId, loading: wsLoading, linkUnavailable } = usePageWorkspace()
  const access = useAccessContext()
  const search = useSearchParams()
  // The slug comes from the URL, never from useParams(). Under
  // `output: "export"` this route is exported once, as /pages/_/index.html,
  // and the Go binary serves that file for every real slug — so useParams()
  // hands back the literal "_" placeholder and the page renders
  // `No page is addressed "_"`. useUrlSegment reads window.location after
  // mount and is the repo's existing answer to exactly this bug class; the
  // issue, skill, mission and chat detail routes all had it first.
  const slug = useUrlSegment(PAGE_PATH_RE)
  // A restricted session cannot open the Studio page view (its endpoints are
  // not on the allowlist); it gets the same declared-actions surface as /pages.
  const accessMode = useAccessMode()

  if (linkUnavailable) return <p className="p-6" role="alert">Document unavailable.</p>

  if (wsLoading || !workspaceId || !slug || accessMode === "loading") return <DetailSkeleton />

  if (accessMode === "restricted") return <RestrictedPages key={workspaceId} workspaceId={workspaceId} access={access} />
  const value = Number(search.get("version"))
  const initialVersion = Number.isSafeInteger(value) && value > 0 ? value : undefined
  if (search.get("artifact") === "1") {
    return <ArtifactProject key={`${workspaceId}:${slug}:${initialVersion ?? "latest"}`} workspaceId={workspaceId} slug={slug} initialVersion={initialVersion} />
  }
  const layout = <PagesLayout workspaceId={workspaceId} slug={slug} {...(initialVersion === undefined ? {} : { initialArtifactVersion: initialVersion })} />
  // Behind its switch one page opens in the Artifacts library too. A reference
  // link, a pinned version, an edit address and a restricted member stay here,
  // as the /pages overview keeps them.
  const toLibrary = pagesOpenArtifacts() && accessMode === "trusted" && !KEEP_ON_PAGES.some(key => search.has(key))
  return toLibrary ? <OpenInArtifacts workspaceId={workspaceId} slug={slug}>{layout}</OpenInArtifacts> : layout
}

/** Query parameters only Pages understands: an address carrying one stays on Pages. */
const KEEP_ON_PAGES = ["ref", "artifact", "version", "mode"]

/**
 * Where a Page opens in the Artifacts library: its immutable ID, for a kind
 * whose Artifacts detail opens it there (a Page, a native Word or Sheet, a
 * versioned project). Anything else, an older Craft among them, stays on
 * Pages, so its "Open in Pages" link never leads back to the library.
 */
export function artifactsIdFor(page: PageView | null): string | null {
  if (!page?.immutablePageId) return null
  const [item] = libraryItems([page])
  return item.projectBacked || item.native || item.kind === "page" ? page.immutablePageId : null
}

/**
 * Decides once, for the address this page was entered with: resolve the slug
 * to its ID and replace the entry with /artifacts/<id>, or stay on Pages.
 * Fails closed: an error, a refusal or no usable ID keeps the Pages view.
 * Later in-page navigation never redirects, so an open editor is not torn down.
 */
function OpenInArtifacts({ workspaceId, slug, children }: { workspaceId: string; slug: string; children: ReactNode }) {
  const [stay, setStay] = useState(false)
  if (stay) return <>{children}</>
  return <ResolveArtifact workspaceId={workspaceId} slug={slug} onStay={() => setStay(true)} />
}

function ResolveArtifact({ workspaceId, slug, onStay }: { workspaceId: string; slug: string; onStay: () => void }) {
  const router = useRouter()
  const session = useSessionSafe()
  const [entry] = useState(slug)
  const detail = usePage(workspaceId, entry)
  const signedOut = session.status !== "loading" && !session.data?.user?.id
  const settled = signedOut || Boolean(detail.error || detail.accessDenied) || !detail.loading
  const id = settled && !signedOut && !detail.error && !detail.accessDenied ? artifactsIdFor(detail.page) : null
  useEffect(() => {
    if (!settled) return
    // The static export has no server redirect; replace, so Back does not return here.
    if (id) router.replace(`/artifacts/${encodeURIComponent(id)}?${new URLSearchParams({ workspace_id: workspaceId })}`)
    else onStay()
  }, [settled, id]) // eslint-disable-line react-hooks/exhaustive-deps
  return <DetailSkeleton />
}

function DetailSkeleton() {
  return (
    <div className="flex h-[calc(100dvh-var(--app-header-h)-var(--mobile-tab-bar-h))] flex-col gap-3 p-4">
      <Skeleton className="h-9 w-full" />
      <div className="flex flex-1 gap-3">
        <Skeleton className="h-full w-60" />
        <Skeleton className="h-full flex-1" />
      </div>
    </div>
  )
}
