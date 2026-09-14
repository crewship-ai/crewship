"use client"

/**
 * The sandboxed frame a Page application runs in, and nothing else.
 *
 * This file used to also export a `PagePreviewDialog` — the "App preview"
 * toolbar button's destination, which additionally carried an unfenced
 * publish. Both the button and that publish path are gone: the candidate's
 * preview is a workspace inside the editor (`editor/review-preview.tsx`) and
 * publishing happens on the review screen behind a consent bound to the
 * digests the reviewer was shown. The frame's own security properties —
 * opaque sandbox, no same-origin runtime without the server's development
 * flag, no execution in an unsupported browser, and unmounting rather than
 * leaving code running when authorization fails — are asserted against the
 * real frame in `editor/__tests__/review-preview.test.tsx`.
 */

import { supportsPageApplications, pageBrowserRequirement } from "@/lib/pages/runtime-support"

import { useRealtimeEventSafe } from "@/hooks/use-realtime"
import { useWorkspacePagesTheme, refreshWorkspaceSettings } from "@/hooks/use-workspace"
import { normalizePageTheme } from "@/lib/pages/theme"
import { useEffect, useRef, useState } from "react"
import type { PreviewArtifact } from "@/hooks/use-page-preview"
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
