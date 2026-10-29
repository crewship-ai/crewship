"use client"
import { useEffect, useMemo, useRef } from "react"
import { cn } from "@/lib/utils"
import { artifactProjectDocument, artifactProjectQuote } from "@/lib/pages/artifact-project-preview"
import { DASHBOARD_DATA_MESSAGE, type DashboardCollection, type DashboardManifest } from "@/lib/pages/artifact-dashboard"
import type { ArtifactFile } from "../projects/types"
import { DASHBOARD_PREVIEW_LIMIT_ERROR, useArtifactDashboardData } from "../projects/use-artifact-dashboard-data"
import { isValidAnchorSelector } from "./element-select"
import type { PreviewSelection } from "./artifact-preview-frame"

interface Props {
  workspaceId: string; pageId: string; codeVersion: number; files: ArtifactFile[]; path: string; title: string
  selectMode: boolean; onSelect: (selection: PreviewSelection) => void; onNavigate: (path: string) => void; className?: string
}
/** Data stays under the human's Page authority; this iframe has neither credentials nor author JavaScript. */
export function ArtifactDashboardPreview(props: Props) {
  const data = useArtifactDashboardData(props)
  if (data.limitExceeded) return <p role="alert" className="p-4 text-sm">{DASHBOARD_PREVIEW_LIMIT_ERROR}</p>
  if (!data.manifest || !data.enabled) return <p role="status" className="p-4 text-sm">Dashboard preview is unavailable.</p>
  if (!data.collections || data.error) return <p role={data.error ? "alert" : "status"} className="p-4 text-sm">{data.error ? "Dashboard data is unavailable. Reload to check access." : "Loading Dashboard data…"}</p>
  return <DashboardFrame key={`${data.scope}:${props.path}:${props.selectMode}`} {...props} manifest={data.manifest} collections={data.collections} />
}

function DashboardFrame({ files, path, codeVersion, selectMode, title, onSelect, onNavigate, className, manifest, collections }: Props & { manifest: DashboardManifest; collections: DashboardCollection[] }) {
  const frame = useRef<HTMLIFrameElement>(null)
  const token = useMemo(() => Array.from(crypto.getRandomValues(new Uint8Array(32)), value => value.toString(16).padStart(2, "0")).join(""), [])
  const source = useMemo(() => artifactProjectDocument(files, path, token, selectMode, { hostOrigin: window.location.origin, codeVersion, collections: manifest.collections }), [files, path, token, selectMode, codeVersion, manifest.collections])
  const latest = useRef({ collections, onSelect, onNavigate, selectMode, files })
  latest.current = { collections, onSelect, onNavigate, selectMode, files }
  const sequence = useRef(0)
  const post = () => frame.current?.contentWindow?.postMessage({ type: DASHBOARD_DATA_MESSAGE, token, code_version: codeVersion, seq: ++sequence.current, collections: latest.current.collections }, "*")
  useEffect(post, [collections, token, codeVersion])
  useEffect(() => {
    const receive = (event: MessageEvent) => {
      const data = event.data, current = latest.current
      if (event.origin !== "null" || event.source !== frame.current?.contentWindow || !data || data.token !== token) return
      if (typeof data.screen === "string" && current.files.some(file => file.path === data.screen && file.media_type === "text/html")) current.onNavigate(data.screen)
      else if (current.selectMode && typeof data.selector === "string" && isValidAnchorSelector(data.selector) && typeof data.quote === "string" && data.quote.length <= 5000) current.onSelect({ selector: data.selector, quote: artifactProjectQuote(data.quote) })
    }
    window.addEventListener("message", receive)
    return () => window.removeEventListener("message", receive)
  }, [token])
  return <iframe ref={node => { frame.current = node; node?.setAttribute("credentialless", "") }} title={title} sandbox="allow-scripts" referrerPolicy="no-referrer" srcDoc={source} onLoad={post} className={cn("block size-full border-0 bg-white", className)} />
}
