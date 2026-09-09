"use client"

import { supportsPageApplications, pageBrowserRequirement } from "@/lib/pages/runtime-support"
import { publicationReceiptMessage } from "@/lib/pages/publication-receipt"

import { useRealtimeEventSafe } from "@/hooks/use-realtime"
import { useWorkspacePagesTheme, refreshWorkspaceSettings } from "@/hooks/use-workspace"
import { normalizePageTheme } from "@/lib/pages/theme"
import { usePageApplication } from "@/hooks/use-page-application"
import { useEffect, useRef, useState } from "react"
import { Button } from "@/components/ui/button"
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { usePagePreview, type PreviewArtifact } from "@/hooks/use-page-preview"
import type { WirePage } from "@/hooks/use-pages"
import { validatePreview, previewSnapshot, PreviewSnapshotChannel, type PageRequestHandler } from "@/lib/pages/preview-runtime"

export function PagePreviewFrame({ workspaceId, artifact, page, runtimeURL, developmentSameOrigin = false, onRequest, title = "Application preview" }: { workspaceId?: string; artifact: PreviewArtifact; page: WirePage; runtimeURL: string; developmentSameOrigin?: boolean; onRequest?: PageRequestHandler; title?: string }) {
  const theme = useWorkspacePagesTheme(workspaceId)
  useRealtimeEventSafe("workspace.updated", refreshWorkspaceSettings)
  useRealtimeEventSafe("realtime.reconnected", refreshWorkspaceSettings)
  const latestTheme = useRef(normalizePageTheme(theme))
  latestTheme.current = normalizePageTheme(theme)
  useEffect(() => {
    const onFocus = () => { if (workspaceId) void refreshWorkspaceSettings() }
    window.addEventListener("focus", onFocus)
    return () => window.removeEventListener("focus", onFocus)
  }, [workspaceId])
  const frame = useRef<HTMLIFrameElement>(null)
  const channel = useRef<MessageChannel | null>(null)
  const latest = useRef(page)
  const delivery = useRef<PreviewSnapshotChannel | null>(null)
  const [ready, setReady] = useState(false)
  useEffect(() => {
    if (ready) return
    let timeout: ReturnType<typeof setTimeout> | undefined
    const watch = () => {
      clearTimeout(timeout)
      if (document.visibilityState !== "hidden") timeout = setTimeout(() => setError("The application did not finish loading. Close and reopen it to retry."), 20_000)
    }
    watch()
    document.addEventListener("visibilitychange", watch)
    return () => { clearTimeout(timeout); document.removeEventListener("visibilitychange", watch) }
  }, [ready])
  const [error, setError] = useState<string | null>(null)
  function send(snapshotPage: WirePage) {
    try { delivery.current?.push(previewSnapshot(snapshotPage, latestTheme.current)) }
    catch (cause) { delivery.current?.close(); setError(cause instanceof Error ? cause.message : "Preview data could not be delivered.") }
  }
  useEffect(() => {
    latest.current = page
    send(page)
  }, [page, theme])
  useEffect(() => () => { delivery.current?.close(); channel.current?.port1.close(); channel.current?.port2.close() }, [])
  let invalid: string | null = null
  try { validatePreview(artifact, runtimeURL, window.location.href, developmentSameOrigin) }
  catch (cause) { invalid = cause instanceof Error ? cause.message : "Invalid preview configuration." }
  if (!supportsPageApplications(navigator.userAgent)) return <p role="alert" className="text-sm">{pageBrowserRequirement}</p>
  if (error || invalid) return <p role="alert" className="text-sm text-destructive">{error ?? invalid}</p>
  return <div className="relative h-full min-h-0 w-full overflow-hidden rounded-md border" style={{ backgroundColor: latestTheme.current.background }} aria-busy={!ready}>
    {developmentSameOrigin && <p role="note" className="absolute right-2 top-2 z-10 rounded border bg-background px-2 py-1 text-xs">Development preview: browser process isolation is not guaranteed.</p>}
    {!ready && <div role="status" className="absolute inset-0 flex items-center justify-center text-sm" style={{ color: latestTheme.current.muted }}>Loading application…</div>}
    <iframe ref={frame} title={title} sandbox="allow-scripts" referrerPolicy="no-referrer" className="block h-full w-full border-0 transition-opacity duration-300 ease-out motion-reduce:transition-none" style={{ opacity: ready ? 1 : 0, backgroundColor: latestTheme.current.background, pointerEvents: ready ? "auto" : "none" }} tabIndex={ready ? 0 : -1} aria-hidden={!ready} src={runtimeURL} onLoad={() => {
    delivery.current?.close()
    channel.current?.port1.close()
    channel.current?.port2.close()
    // Only the first document receives the port. A navigation invalidates
    // the connection rather than giving a new document access to Page data.
    if (channel.current) { setError("The application navigated away from its verified document and was stopped. Reopen it to retry."); return }
    const target = frame.current?.contentWindow
    if (!target) return
    const connection = new MessageChannel()
    channel.current = connection
    // '*' is required for an opaque origin; the WindowProxy is the exact
    // frame we created. There is no global message listener; RPC uses this dedicated port.
    target.postMessage({ type: "crewship.pages.boot/v1", artifact }, "*", [connection.port2])
    delivery.current = new PreviewSnapshotChannel(connection.port1, onRequest, () => setReady(true))
    send(latest.current)
  }} />
  </div>
}

export function PagePreviewDialog({ workspaceId, slug, page, onClose }: { workspaceId: string; slug: string; page: WirePage | null; onClose: () => void }) {
  const { query, build } = usePagePreview(workspaceId, slug)
  const application = usePageApplication(workspaceId, slug)
  const [reviewed, setReviewed] = useState(false)
  const data = query.data
  const job = data?.build
  useEffect(() => setReviewed(false), [job?.id, application.query.data?.publication_version, application.query.data?.publication?.version])
  const running = build.isPending || job?.state === "running"
  const [stopped, setStopped] = useState(false)
  return <Dialog open onOpenChange={open => { if (!open) onClose() }}>
    <DialogContent className="flex h-[90vh] max-w-[96vw] flex-col sm:max-w-[1200px]">
      <DialogHeader>
        <DialogTitle>Application preview</DialogTitle>
        <DialogDescription>Try the draft using the panels you can read on this Page. Opening a preview does not publish it.</DialogDescription>
      </DialogHeader>
      <div className="flex flex-wrap items-center gap-2">
        <Button disabled={!data || running || query.isError} onClick={() => { setStopped(false); build.mutate(data!.revision) }}>{running ? "Building…" : "Build preview"}</Button>
        <Button variant="outline" onClick={() => void query.refetch()}>Check status</Button>
        {data?.artifact && !stopped && <Button variant="outline" onClick={() => setStopped(true)}>Stop preview</Button>}
        {data && <span className="text-sm text-muted-foreground">Draft {data.revision}{job ? ` · Build ${job.source_revision}: ${job.state}` : " · Not built yet"}</span>}
      </div>
      {(query.error || build.error) && <p role="alert" className="text-sm text-destructive">{query.error?.message ?? build.error?.message}</p>}
      {(application.check.error || application.publish.error) && <p role="alert" className="text-sm text-destructive">{application.check.error?.message ?? application.publish.error?.message}</p>}
      {application.check.isSuccess && <p role="status" className="text-sm">Source, artifact and binding checks passed. Review the application behavior before publishing.</p>}
      {application.publish.isSuccess && <p role="status" className="text-sm">{publicationReceiptMessage(application.publish.data)}</p>}
      {job?.state === "ready" && data && <div className="flex flex-wrap items-center gap-3">
        <Button variant="outline" disabled={application.check.isPending} onClick={() => application.check.mutate({ build: job.id, revision: job.source_revision })}>Check application</Button>
        {application.query.data?.can_publish && <>
          <label className="flex items-center gap-2 text-sm"><input type="checkbox" checked={reviewed} onChange={event => setReviewed(event.target.checked)} />I reviewed this code and its behavior.</label>
          <Button disabled={!reviewed || application.publish.isPending || application.query.isError || job.source_revision !== data.revision} onClick={() => application.publish.mutate({ build_id: job.id, expected_revision: data.revision, expected_publication: application.query.data?.publication_version ?? application.query.data?.publication?.version ?? 0, reviewed_code: reviewed })}>Publish application</Button>
        </>}
      </div>}
      {job?.error && <pre role="alert" className="max-h-48 overflow-auto whitespace-pre-wrap rounded-md bg-muted p-3 text-xs">{job.error}</pre>}
      {job && data && job.source_revision !== data.revision && <p className="text-sm text-muted-foreground">This preview uses an older draft. Build again to see your latest changes.</p>}
      {data && !data.runtime_url && <p role="alert" className="text-sm text-muted-foreground">An administrator needs to configure the application preview domain.</p>}
      <div className="min-h-0 flex-1">
        {data?.artifact && data.runtime_url && job?.state === "ready" && page && !query.isError && !stopped
          ? <PagePreviewFrame workspaceId={workspaceId} key={`${workspaceId}:${slug}:${job.id}`} artifact={data.artifact} page={page} runtimeURL={data.runtime_url} developmentSameOrigin={data.development_same_origin === true} />
          : <div className="flex h-full items-center justify-center rounded-md border border-dashed text-sm text-muted-foreground">{running ? "Preparing your application…" : stopped ? "Preview stopped." : "Your application preview will appear here."}</div>}
      </div>
    </DialogContent>
  </Dialog>
}
