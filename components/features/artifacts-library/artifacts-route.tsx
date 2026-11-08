"use client"

import { useEffect, useState } from "react"
import { useRouter, useSearchParams } from "next/navigation"
import dynamic from "next/dynamic"
import { Skeleton } from "@/components/ui/skeleton"
import { usePageWorkspace } from "@/hooks/use-page-workspace"
import { useAccessMode } from "@/hooks/use-access-mode"
import { usePage } from "@/hooks/use-pages"
import { artifactsDesign, artifactsLibrary } from "@/lib/feature-flags"
import { useAccessContext } from "@/components/features/restricted/access-notice"
import { ArtifactsLibrary } from "./artifacts-library"
import { RestrictedArtifacts } from "./restricted-artifacts"
import { filtersFromParams, filtersToParams } from "./library-filter"
import { libraryItems, versionPin } from "./library-items"

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
  // Session-level: one restricted membership restricts every request (#2861).
  const accessMode = useAccessMode()
  const workspaceQuery = new URLSearchParams()
  const linked = params?.get("workspace_id")
  if (linked) workspaceQuery.set("workspace_id", linked)

  const pinned = versionPin(params?.get("version"))
  // A restricted member's Artifacts is their restricted catalog (#5132), with
  // or without the library flag: the library reads (GET /api/v1/pages,
  // GET /api/v1/pages/{slug}) are outside the restricted allowlist, so the
  // library, the detail, Design and every redirect below stay unmounted.
  const restricted = accessMode === "restricted"
  // Without the library an ID address opens on Pages; its kind decides how.
  const resolveOnPages = !enabled && !restricted && accessMode !== "loading" && Boolean(item) && !bySlug
  useEffect(() => {
    if (enabled || loading || linkUnavailable || restricted || accessMode === "loading" || resolveOnPages) return
    // The static export has no server redirect; a saved link still lands on Pages.
    router.replace(`/pages${workspaceQuery.size ? `?${workspaceQuery}` : ""}`)
  }, [enabled, loading, linkUnavailable, restricted, accessMode, resolveOnPages]) // eslint-disable-line react-hooks/exhaustive-deps

  if (linkUnavailable) return <p className="p-6" role="alert">Artifacts unavailable.</p>
  if (loading || !workspaceId || accessMode === "loading") return enabled || resolveOnPages ? <Skeleton className="m-6 h-12" /> : null
  if (restricted) return <RestrictedEntry key={workspaceId} workspaceId={workspaceId} item={item} bySlug={bySlug} workspaceQuery={workspaceQuery} />
  if (resolveOnPages) return <OpenOnPages workspaceId={workspaceId} id={item!} version={pinned} workspaceQuery={workspaceQuery} />
  if (!enabled) return null

  const filters = filtersFromParams(params)
  const select = (id: string | null, slug?: string) => {
    const next = filtersToParams(filters, workspaceQuery)
    if (id) return router.push(artifactHref(id, next))
    // An artifact without an immutable ID keeps the slug address it always had.
    if (slug) next.set("item", slug)
    router.push(`/artifacts${next.size ? `?${next}` : ""}`)
  }
  // A link from before IDs, /artifacts?item=<slug>, moves to the ID address once the slug resolves.
  const canonical = (id: string) => {
    const next = filtersToParams(filters, workspaceQuery)
    if (params?.get("edit") === "1") next.set("edit", "1")
    router.replace(artifactHref(id, next))
  }
  // A filter change replaces the entry: typing a search is not a trail of Back steps.
  const filter = (nextFilters: typeof filters) => {
    const next = filtersToParams(nextFilters, workspaceQuery)
    router.replace(`/artifacts${next.size ? `?${next}` : ""}`)
  }
  // `?edit=1` opens the editor inside the frame; leaving it replaces the entry, so Back leaves the artifact.
  const edit = item !== null && params?.get("edit") === "1"
  const editChange = (next: boolean) => {
    const query = new URLSearchParams(params?.toString() ?? "")
    if (next) query.set("edit", "1"); else query.delete("edit")
    router.replace(`${window.location.pathname}${query.size ? `?${query}` : ""}`)
  }
  const designSlug = design && params?.get("design") === "1" ? params.get("item") : null
  if (designSlug) return <div className="h-full p-4"><DesignEditor workspaceId={workspaceId} slug={designSlug} initialVersion={pinned} /></div>
  return <ArtifactsLibrary workspaceId={workspaceId} item={item} version={item ? pinned : undefined} bySlug={bySlug} onSelect={select} onCanonical={canonical} filters={filters} onFiltersChange={filter}
    edit={edit} onEditChange={editChange} />
}

/**
 * /artifacts for a restricted member (#5132). `?item=<slug>` and
 * `/artifacts/<id>` (#5135) open one granted artifact's actions. Both are
 * matched against the restricted catalog, the one read the home makes too,
 * which names each granted artifact by slug and ID; the address itself is
 * never sent, so any ID outside the catalog gets the same response and the
 * same gate (the server stays authoritative for every action either way).
 */
function RestrictedEntry({ workspaceId, item, bySlug, workspaceQuery }: { workspaceId: string; item: string | null; bySlug?: boolean; workspaceQuery: URLSearchParams }) {
  const access = useAccessContext()
  const homeHref = `/artifacts${workspaceQuery.size ? `?${workspaceQuery}` : ""}`
  const hrefFor = (slug: string) => {
    const next = new URLSearchParams(workspaceQuery)
    next.set("item", slug)
    return `/artifacts?${next}`
  }
  return <RestrictedArtifacts workspaceId={workspaceId} access={access} focus={item} byId={item !== null && !bySlug} hrefFor={hrefFor} homeHref={homeHref} />
}

/**
 * /artifacts/<id> in a build without the library: the canonical address
 * every generated link uses opens on Pages. A versioned project opens in the
 * Pages project viewer with its pin (`artifact=1`); every other kind in its
 * Pages detail. The kind comes from the same page read Pages makes; a refused
 * or failed read goes to the plain Pages detail, which answers it itself.
 */
function OpenOnPages({ workspaceId, id, version, workspaceQuery }: { workspaceId: string; id: string; version?: number; workspaceQuery: URLSearchParams }) {
  const router = useRouter()
  const [entry] = useState(id)
  const detail = usePage(workspaceId, "@" + entry)
  const settled = Boolean(detail.error || detail.accessDenied) || !detail.loading
  const project = settled && !detail.error && !detail.accessDenied && detail.page ? libraryItems([detail.page])[0].projectBacked : false
  useEffect(() => {
    if (!settled) return
    const query = new URLSearchParams(workspaceQuery)
    if (project) query.set("artifact", "1")
    if (version !== undefined) query.set("version", String(version))
    router.replace(`/pages/${encodeURIComponent("@" + entry)}${query.size ? `?${query}` : ""}`)
  }, [settled]) // eslint-disable-line react-hooks/exhaustive-deps
  return <Skeleton className="m-6 h-12" />
}
