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

export function PageApplicationView({ workspaceId, slug, page, fallback }: { workspaceId: string; slug: string; page: WirePage | null; fallback: ReactNode }) {
  const theme = normalizePageTheme(useWorkspacePagesTheme(workspaceId))
  const browserSupported = typeof navigator !== "undefined" && supportsPageApplications(navigator.userAgent)
  const { query } = usePageApplication(workspaceId, slug, browserSupported && page !== null && page.has_application !== false)
  const [opened, setOpened] = useState<PageApplication | null>(null)
  const actions = useApplicationActions(workspaceId, slug, opened?.publication?.version ?? 0)
  const [panels, setPanels] = useState(false)
  const storageBusy = query.isError && (query.error as Error & { status?: number } | null)?.status === 503
  const hasApplication = page !== null && page.has_application !== false
  useEffect(() => {
    if (!browserSupported || !hasApplication || (query.isError && !storageBusy) || (!query.isPending && query.data?.publication === null)) {
      setOpened(null)
    } else if (!opened && query.data?.publication && (page?.publication_version === undefined || page.publication_version === query.data.publication.version)) {
      setOpened(query.data)
    }
  }, [browserSupported, hasApplication, opened, page?.publication_version, query.data, query.isError, query.isPending, storageBusy])
  const available = hasApplication && query.data?.publication && opened?.artifact && opened.runtime_url && page && (!query.isError || storageBusy)
  const versionMismatch = hasApplication && !opened && !query.isError && query.data?.publication && page?.publication_version !== undefined && page.publication_version !== query.data.publication.version
  if (!browserSupported) return <>{hasApplication && <p role="status" className="px-4 text-sm">{pageBrowserRequirement}</p>}{fallback}</>
  if (versionMismatch) return <><p role="status" className="px-4 text-sm">Application versions are not synchronized. Showing panels until they agree.</p>{fallback}</>
  if (hasApplication && !query.isError && (query.isPending || (query.data?.publication && !opened))) return <div role="status" className="flex min-h-0 flex-1 items-center justify-center text-sm" style={{ backgroundColor: theme.background, color: theme.muted }}>Loading application…</div>
  if (!available) return <>{query.error && <p role="alert" className="px-4 text-sm text-destructive">{query.error.message}</p>}{fallback}</>
  const changed = opened.publication?.version !== query.data?.publication?.version
  return <div className="flex min-h-0 flex-1 flex-col">
    {storageBusy && <p role="status" className="px-4 text-sm">Application storage is temporarily busy. Showing the last verified version.</p>}
    <div className="flex flex-wrap items-center gap-2 border-b px-4 py-2">
      <span className="text-sm font-medium">{page.name ?? slug}</span>
      <Button size="sm" variant="outline" onClick={() => setPanels(value => !value)}>{panels ? "Open application" : "Stop application / show panels"}</Button>
      {changed && <Button size="sm" onClick={() => setOpened(query.data!)}>Load new version {query.data?.publication?.version}</Button>}
    </div>
    {actions.confirmation}
    {panels ? fallback : <div className="min-h-0 flex-1"><PagePreviewFrame workspaceId={workspaceId} title="Page application" onRequest={actions.handleRequest} key={`${workspaceId}:${slug}:${opened.publication?.version}`} artifact={opened.artifact!} runtimeURL={opened.runtime_url!} developmentSameOrigin={opened.development_same_origin === true} page={page} /></div>}
  </div>
}
