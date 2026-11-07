"use client"
import { useEffect, useMemo, useRef, useState } from "react"
import { useSessionSafe } from "@/hooks/use-auth"
import { apiFetch, AUTH_EVENT } from "@/lib/api-fetch"
import { cn } from "@/lib/utils"
import { pageBrowserRequirement, supportsPageApplications } from "@/lib/pages/runtime-support"
import { createInteractiveDataAccess, interactiveArtifactSource, InteractiveFrameSession, validateInteractiveRuntime } from "@/lib/pages/interactive-artifact"
import type { ArtifactFile, ArtifactInteractiveRuntime } from "../projects/types"

interface Props {
  workspaceId: string; pageId: string; codeVersion: number; files: ArtifactFile[]; path: string; title: string
  runtime: ArtifactInteractiveRuntime
  /** The viewer's Edit right on the shown version; the server still decides every write. */
  canWrite: boolean
  className?: string
}

/**
 * The artifact's own scripts run in the Page runtime on a separate site with an
 * opaque sandbox. They hold no Crewship credential; the broker below is their
 * only way to data, and it uses this viewer's session for each request.
 */
export function ArtifactInteractivePreview(props: Props) {
  const user = useSessionSafe()?.data?.user
  const [retired, setRetired] = useState(false)
  useEffect(() => {
    const retire = () => setRetired(true)
    window.addEventListener(AUTH_EVENT, retire)
    return () => window.removeEventListener(AUTH_EVENT, retire)
  }, [])
  let invalid: string | null = null
  try {
    if (!props.runtime.runtime_url) throw new Error("Interactive artifacts need a separate application domain. Ask an operator to set CREWSHIP_PAGE_RUNTIME_ORIGIN.")
    validateInteractiveRuntime(props.runtime.runtime_url, window.location.href, props.runtime.development_same_origin)
  } catch (cause) { invalid = cause instanceof Error ? cause.message : "Invalid runtime configuration." }
  if (!supportsPageApplications(navigator.userAgent)) return <p role="alert" className="p-4 text-sm">{pageBrowserRequirement}</p>
  if (invalid) return <p role="alert" className="p-4 text-sm">{invalid}</p>
  if (!user?.id || retired) return <p role="status" className="p-4 text-sm">Sign in again to open this interactive artifact.</p>
  return <InteractiveFrame key={JSON.stringify([user.id, props.workspaceId, props.pageId, props.codeVersion, props.path, props.canWrite, props.runtime.runtime_url])} {...props} viewerName={user.name ?? ""} />
}

function InteractiveFrame({ workspaceId, pageId, codeVersion, files, path, title, runtime, canWrite, className, viewerName }: Props & { viewerName: string }) {
  const frame = useRef<HTMLIFrameElement>(null)
  const [ready, setReady] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const name = useRef(viewerName)
  name.current = viewerName
  const source = useMemo(() => {
    try { return interactiveArtifactSource(files, path) }
    catch (cause) { return cause instanceof Error ? cause.message : "This screen cannot run." }
  }, [files, path])
  // Created on the frame's first load only, so a StrictMode re-run cannot end a live session.
  const session = useRef<InteractiveFrameSession | null>(null)
  useEffect(() => () => { session.current?.close(); session.current = null }, [])
  const load = () => {
    if (typeof source === "string") return
    if (!session.current) {
      const access = createInteractiveDataAccess({ fetcher: apiFetch, workspaceId, pageId, codeVersion, viewerName: () => name.current })
      session.current = new InteractiveFrameSession(source, access, { collections: runtime.collections, canWrite, onReady: () => setReady(true) })
    }
    if (!session.current.load(frame.current?.contentWindow)) setError("The artifact navigated away from its verified document and was stopped. Reload to retry.")
  }
  useEffect(() => {
    if (ready) return
    const timeout = setTimeout(() => setError("The artifact did not finish loading. Reload to retry."), 20_000)
    return () => clearTimeout(timeout)
  }, [ready])
  if (typeof source === "string") return <p role="alert" className="p-4 text-sm">{source}</p>
  if (error) return <p role="alert" className="p-4 text-sm">{error}</p>
  return <div className="relative size-full" aria-busy={!ready}>
    {runtime.development_same_origin && <p role="note" className="absolute right-2 top-2 z-10 rounded border bg-background px-2 py-1 text-xs">Development runtime: browser process isolation is not guaranteed.</p>}
    {!ready && <p role="status" className="absolute inset-0 flex items-center justify-center text-sm">Loading interactive artifact…</p>}
    <iframe ref={frame} title={title} sandbox="allow-scripts" referrerPolicy="no-referrer" src={runtime.runtime_url}
      className={cn("block size-full border-0 bg-white", className)} style={{ opacity: ready ? 1 : 0 }}
      onLoad={load} />
  </div>
}
