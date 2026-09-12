"use client"

import { useEffect, useState, type ReactNode } from "react"
import type { WirePage } from "@/hooks/use-pages"
import { usePageApplication, type PageApplication } from "@/hooks/use-page-application"
import { PagePreviewFrame } from "./page-preview"
import { useApplicationActions } from "./use-application-actions"
import { useWorkspacePagesTheme } from "@/hooks/use-workspace"
import { normalizePageTheme } from "@/lib/pages/theme"
import { Button } from "@/components/ui/button"
import { supportsPageApplications, pageBrowserRequirement } from "@/lib/pages/runtime-support"

export interface PageApplicationViewProps {
  workspaceId: string
  slug: string
  page: WirePage | null
  /** The panel grid, shown whenever the application is not — or the reader asked for it. */
  fallback: ReactNode
  /**
   * Show the panels instead of the running application. Owned by the page
   * header's Application | Panels switch, not by this view: the switch has
   * to sit beside Edit, where the page is named, and it used to live in a
   * second bar under it reading "Stop application / show panels" — three
   * verbs for one toggle, and a "stop" that stopped nothing on the server.
   * Switching to panels still unmounts the application frame, so the host
   * keeps its direct way of ending the sandboxed code in this tab.
   */
  panels?: boolean
  /**
   * Whether a running application is actually on screen right now. The
   * header shows its switch only while this is true — a Page whose
   * application failed to load, was withdrawn, or is not supported in this
   * browser is already showing panels and has nothing to switch to.
   */
  onAvailableChange?: (available: boolean) => void
}

export function PageApplicationView({ workspaceId, slug, page, fallback, panels = false, onAvailableChange }: PageApplicationViewProps) {
  const theme = normalizePageTheme(useWorkspacePagesTheme(workspaceId))
  const browserSupported = typeof navigator !== "undefined" && supportsPageApplications(navigator.userAgent)
  const { query } = usePageApplication(workspaceId, slug, browserSupported && page !== null && page.has_application !== false)
  const [opened, setOpened] = useState<PageApplication | null>(null)
  const actions = useApplicationActions(workspaceId, slug, opened?.publication?.version ?? 0)
  const storageBusy = query.isError && (query.error as Error & { status?: number } | null)?.status === 503
  const hasApplication = page !== null && page.has_application !== false
  useEffect(() => {
    if (!browserSupported || !hasApplication || (query.isError && !storageBusy) || (!query.isPending && query.data?.publication === null)) {
      setOpened(null)
    } else if (!opened && query.data?.publication && (page?.publication_version === undefined || page.publication_version === query.data.publication.version)) {
      setOpened(query.data)
    }
  }, [browserSupported, hasApplication, opened, page?.publication_version, query.data, query.isError, query.isPending, storageBusy])
  const available = Boolean(hasApplication && query.data?.publication && opened?.artifact && opened.runtime_url && page && (!query.isError || storageBusy))
  const versionMismatch = hasApplication && !opened && !query.isError && query.data?.publication && page?.publication_version !== undefined && page.publication_version !== query.data.publication.version
  // Reported from an effect so the header's switch can never disagree with
  // what this view is rendering, and cleared on unmount so leaving the Page
  // does not leave a switch behind for a Page that is no longer on screen.
  const reportable = browserSupported && !versionMismatch && available
  useEffect(() => {
    onAvailableChange?.(reportable)
  }, [onAvailableChange, reportable])
  useEffect(() => () => onAvailableChange?.(false), [onAvailableChange])
  if (!browserSupported) return <>{hasApplication && <p role="status" className="px-4 text-sm">{pageBrowserRequirement}</p>}{fallback}</>
  if (versionMismatch) return <><p role="status" className="px-4 text-sm">Application versions are not synchronized. Showing panels until they agree.</p>{fallback}</>
  if (hasApplication && !query.isError && (query.isPending || (query.data?.publication && !opened))) return <div role="status" className="flex min-h-0 flex-1 items-center justify-center text-sm" style={{ backgroundColor: theme.background, color: theme.muted }}>Loading application…</div>
  if (!available || !opened) return <>{query.error && <p role="alert" className="px-4 text-sm text-destructive">{query.error.message}</p>}{fallback}</>
  const changed = opened.publication?.version !== query.data?.publication?.version
  return <div className="flex min-h-0 flex-1 flex-col">
    {storageBusy && <p role="status" className="px-4 text-sm">Application storage is temporarily busy. Showing the last verified version.</p>}
    {/* Only when there is something to say: a newer publication landed while
        this one was open. The reader loads it on purpose, never by refresh. */}
    {changed && (
      <div className="flex flex-wrap items-center gap-2 border-b px-4 py-2">
        <span className="text-sm text-muted-foreground">Publication {query.data?.publication?.version} is live; this tab is still on {opened.publication?.version}.</span>
        <Button size="sm" onClick={() => setOpened(query.data!)}>Load new version {query.data?.publication?.version}</Button>
      </div>
    )}
    {actions.confirmation}
    {panels ? fallback : <div className="min-h-0 flex-1"><PagePreviewFrame workspaceId={workspaceId} title="Page application" onRequest={actions.handleRequest} key={`${workspaceId}:${slug}:${opened.publication?.version}`} artifact={opened.artifact!} runtimeURL={opened.runtime_url!} developmentSameOrigin={opened.development_same_origin === true} page={page!} /></div>}
  </div>
}
