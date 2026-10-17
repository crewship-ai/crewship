"use client"

import { useEffect, useRef, useState } from "react"
import { useQuery } from "@tanstack/react-query"
import { apiFetch } from "@/lib/api-fetch"
import { useSessionSafe } from "@/hooks/use-auth"
import { useRealtimeEventSafe } from "@/hooks/use-realtime"
import { useChatAgent } from "../chat-agent-context"
import { relativeToAgent } from "../files/file-scope"
import { FilePreview } from "../files/file-preview"
import { previewMime } from "../files/file-preview-data"
import { isClientArtifactPath } from "./artifact-scope"
import { artifactDownloadUrl, readArtifactSnapshot, readArtifactVersion } from "./artifact-file-io"
import type { FileEntry } from "../chat-tree-row"

/** Run identity and authorized listing are the only source of image paths. */
export function runImageFiles(rows: FileEntry[], runId: string, crewId?: string | null, slug?: string | null): FileEntry[] {
  if (!/^[A-Za-z0-9_-]{1,80}$/.test(runId)) return []
  return [...new Map(rows.filter(file => {
    if (!file || typeof file.path !== "string" || typeof file.name !== "string"
      || typeof file.mod_time !== "string" || typeof file.size !== "number"
      || !Number.isFinite(file.size) || file.size < 0 || file.is_dir !== false) return false
    const relative = relativeToAgent(file.path, crewId, slug)
    return relative.startsWith(`runs/${runId}/`) && isClientArtifactPath(relative)
      && /\.(png|jpe?g|webp)$/i.test(relative) && !relative.includes("\\")
      && !relative.split("/").includes("..")
  }).map(file => [file.path, file])).values()].slice(0, 8)
}

export async function loadRunImages(opts: { agentId: string; workspaceId: string; chatId: string; runId: string; crewId?: string | null; slug?: string | null; signal: AbortSignal }) {
  if (!opts.chatId || !/^[A-Za-z0-9_-]{1,80}$/.test(opts.runId)) return []
  const profileResponse = await apiFetch(`/api/v1/chats/${encodeURIComponent(opts.chatId)}/execution-profile?workspace_id=${encodeURIComponent(opts.workspaceId)}`, { signal: opts.signal, cache: "no-store" })
  if (!profileResponse.ok) throw new Error("Images unavailable")
  const profile: unknown = await profileResponse.json()
  if (!profile || typeof profile !== "object" || !("mode" in profile) || profile.mode !== "trusted") return []
  const params = new URLSearchParams({ workspace_id: opts.workspaceId, recursive: "true", subdir: `runs/${opts.runId}` })
  const response = await apiFetch(`/api/v1/agents/${encodeURIComponent(opts.agentId)}/files?${params}`, { signal: opts.signal, cache: "no-store" })
  if (!response.ok) throw new Error("Images unavailable")
  const rows: unknown = await response.json()
  if (!Array.isArray(rows)) throw new Error("Images unavailable")
  return Promise.all(runImageFiles(rows, opts.runId, opts.crewId, opts.slug).map(async file => {
    const snapshot = await readArtifactSnapshot({ ...opts, path: file.path })
    // The listing and byte read are separate operations. Reject an overwrite
    // during the read rather than claiming the bytes are the listed revision.
    const version = await readArtifactVersion({ ...opts, relativePath: relativeToAgent(file.path, opts.crewId, opts.slug) })
    if (!version || version !== JSON.stringify([file.mod_time, file.size])) throw new Error("Image changed; refresh to load its current version")
    const mime = previewMime(snapshot.bytes)
    if (!mime?.startsWith("image/")) return null
    const digest = await crypto.subtle.digest("SHA-256", snapshot.bytes)
    const fingerprint = Array.from(new Uint8Array(digest), byte => byte.toString(16).padStart(2, "0")).join("")
    return { ...snapshot, name: file.name, fingerprint }
  })).then(images => images.filter(image => image !== null))
}

/** Current authorized output, not a historical immutable file archive. */
function VisibleRunImages({ agentId, workspaceId, chatId, runId }: { agentId: string; workspaceId: string; chatId: string; runId: string }) {
  const agent = useChatAgent()
  const { data: session } = useSessionSafe()
  const viewer = session?.user?.id
  const query = useQuery({
    queryKey: ["chat-run-images", workspaceId, { agentId, chatId, runId, viewer }],
    enabled: !!viewer && !!chatId && /^[A-Za-z0-9_-]{1,80}$/.test(runId),
    queryFn: ({ signal }) => loadRunImages({ agentId, workspaceId, chatId, runId, crewId: agent?.crewId, slug: agent?.slug, signal }),
    retry: false, gcTime: 0, staleTime: 0,
    refetchInterval: 60_000, refetchIntervalInBackground: false,
  })
  const refresh = () => { if (viewer && chatId && /^[A-Za-z0-9_-]{1,80}$/.test(runId)) void query.refetch() }
  useRealtimeEventSafe("file.event", refresh)
  useRealtimeEventSafe("run.completed", refresh)
  useRealtimeEventSafe("realtime.reconnected", refresh)
  if (!viewer) return null
  // Never retain cached bytes/titles after a failed permission recheck.
  if (query.isError) return <div role="alert" className="text-xs text-muted-foreground">Images unavailable. <button type="button" className="underline coarse:min-h-[44px]" onClick={refresh}>Refresh images</button></div>
  if (query.isPending) return <p role="status" className="text-xs text-muted-foreground">Checking run images…</p>
  return <div className="min-w-0 space-y-3">{query.data?.map(image => <figure key={`${image.path}:${image.fingerprint}`} className="w-full max-w-lg overflow-hidden rounded-lg border">
    <div className="h-72 max-h-[50vh]"><FilePreview url={`${artifactDownloadUrl(agentId, workspaceId, image.path)}&revision=${image.fingerprint}`} name={image.name} bytes={image.bytes} showHeader={false} onClose={() => {}} /></div>
    <figcaption className="break-words border-t p-2 text-xs text-muted-foreground">{image.name} · Current run output · version {image.fingerprint.slice(0, 12)}</figcaption>
  </figure>)}</div>
}


type InlineRunImagesProps = { agentId: string; workspaceId: string; chatId: string; runId: string }

/** Offscreen history must not retain file bytes or keep authority polls alive. */
export function InlineRunImages(props: InlineRunImagesProps) {
  const { data: session } = useSessionSafe()
  if (!session?.user?.id) return null
  return <RunImagesBoundary key={JSON.stringify([props, session.user.id])} {...props} />
}

function RunImagesBoundary(props: InlineRunImagesProps) {
  const boundary = useRef<HTMLDivElement>(null)
  const [visible, setVisible] = useState(false)
  const [manual, setManual] = useState(false)
  const [height, setHeight] = useState(1)
  useEffect(() => {
    if (typeof IntersectionObserver === "undefined") { setManual(true); return }
    const observer = new IntersectionObserver(entries => {
      const entry = entries[0]
      if (!entry) return
      if (!entry.isIntersecting && boundary.current) {
        setHeight(Math.max(1, boundary.current.getBoundingClientRect().height))
      }
      setVisible(entry.isIntersecting)
    }, { rootMargin: "200px 0px" })
    if (boundary.current) observer.observe(boundary.current)
    return () => observer.disconnect()
  }, [])
  return <div ref={boundary} style={{ minHeight: height }}>
    {manual && <button type="button" className="text-xs underline coarse:min-h-[44px]" onClick={() => {
      if (visible && boundary.current) setHeight(Math.max(1, boundary.current.getBoundingClientRect().height))
      setVisible(value => !value)
    }}>{visible ? "Hide run images" : "Load run images"}</button>}
    {visible && <VisibleRunImages {...props} />}
  </div>
}
