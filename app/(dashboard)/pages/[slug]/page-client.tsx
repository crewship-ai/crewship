"use client"


import { useEffect } from "react"
import { useRouter, useSearchParams } from "next/navigation"
import { pagesOpenArtifacts } from "@/lib/feature-flags"
import { ArtifactProject } from "@/components/features/pages/artifact-project"
import { Skeleton } from "@/components/ui/skeleton"
import { useUrlSegment } from "@/lib/use-url-segment"
import { usePageWorkspace } from "@/hooks/use-page-workspace"
import { PagesLayout } from "@/components/features/pages/pages-layout"
import { useAccessContext } from "@/components/features/restricted/access-notice"
import { RestrictedPages } from "@/components/features/pages/restricted-pages"
import { useAccessMode } from "@/hooks/use-access-mode"

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
  const router = useRouter()
  // Behind its switch a single page opens inside Artifacts at its own address,
  // so bookmarks and shared /pages/<slug> links keep working. A reference link
  // and the panel editor stay here; a restricted member's surface is not this.
  const toArtifacts = pagesOpenArtifacts() && accessMode === "trusted" && Boolean(slug) && !search.has("ref") && search.get("mode") !== "edit"
  const linkedWorkspace = search.get("workspace_id")
  useEffect(() => {
    if (!toArtifacts || !slug) return
    const query = new URLSearchParams()
    if (linkedWorkspace) query.set("workspace_id", linkedWorkspace)
    // The static export has no server redirect; replace, so Back does not return here.
    // An immutable reference is the artifact's ID address. A slug is never one: it
    // opens by slug in the library, which then moves to the ID address.
    if (slug.startsWith("@")) return router.replace(`/artifacts/${encodeURIComponent(slug.slice(1))}${query.size ? `?${query}` : ""}`)
    query.set("item", slug)
    router.replace(`/artifacts?${query}`)
  }, [toArtifacts, slug, linkedWorkspace]) // eslint-disable-line react-hooks/exhaustive-deps

  if (linkUnavailable) return <p className="p-6" role="alert">Document unavailable.</p>

  if (wsLoading || !workspaceId || !slug || accessMode === "loading" || toArtifacts) {
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

  if (accessMode === "restricted") return <RestrictedPages key={workspaceId} workspaceId={workspaceId} access={access} />
  const value = Number(search.get("version"))
  const initialVersion = Number.isSafeInteger(value) && value > 0 ? value : undefined
  if (search.get("artifact") === "1") {
    return <ArtifactProject key={`${workspaceId}:${slug}:${initialVersion ?? "latest"}`} workspaceId={workspaceId} slug={slug} initialVersion={initialVersion} />
  }
  return <PagesLayout workspaceId={workspaceId} slug={slug} {...(initialVersion === undefined ? {} : { initialArtifactVersion: initialVersion })} />
}
