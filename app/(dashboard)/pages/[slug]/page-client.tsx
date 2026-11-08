"use client"

import { useSearchParams } from "next/navigation"
import { ArtifactProject } from "@/components/features/pages/artifact-project"
import { useUrlSegment } from "@/lib/use-url-segment"
import { usePageWorkspace } from "@/hooks/use-page-workspace"
import { PagesLayout } from "@/components/features/pages/pages-layout"
import { useAccessContext } from "@/components/features/restricted/access-notice"
import { RestrictedPages } from "@/components/features/pages/restricted-pages"
import { useAccessMode } from "@/hooks/use-access-mode"
import { artifactsLibrary } from "@/lib/feature-flags"
import { legacyPagesTarget } from "@/components/features/artifacts-library/legacy-pages-url"
import { LegacyPagesRedirect, LegacyPagesSkeleton as DetailSkeleton } from "@/components/features/artifacts-library/legacy-pages-redirect"

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
  const project = search.get("artifact") === "1"
  const view = project
    ? <ArtifactProject key={`${workspaceId}:${slug}:${initialVersion ?? "latest"}`} workspaceId={workspaceId} slug={slug} initialVersion={initialVersion} />
    : <PagesLayout workspaceId={workspaceId} slug={slug} {...(initialVersion === undefined ? {} : { initialArtifactVersion: initialVersion })} />
  // With the library on, a page opens at its one address, /artifacts/<id>
  // (#5129): a plain, @<id>, project (#4995) or pinned link resolves to it,
  // and the Pages editor (`mode=edit`) and project source editor
  // (`artifact=1&edit=1`) open inside it at `?edit=1`. Anything that does not
  // resolve stays here. The decision is taken once for the entry address.
  const target = legacyPagesTarget(`/pages/${encodeURIComponent(slug)}`, search.toString(), { workspaceId, library: artifactsLibrary(), trusted: accessMode === "trusted" })
  if (target.to !== "resolve") return view
  return <LegacyPagesRedirect key={project ? `${workspaceId}:${slug}` : undefined} target={target}>{view}</LegacyPagesRedirect>
}
