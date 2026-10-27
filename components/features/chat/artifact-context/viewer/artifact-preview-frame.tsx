"use client"
import { useEffect, useMemo, useRef } from "react"
import { cn } from "@/lib/utils"
import type { ArtifactFile } from "../projects/types"
import { HIGHLIGHT_MESSAGE, NAVIGATE_MESSAGE, SELECT_MESSAGE, buildPreviewDocument, inlineProjectAssets, isValidAnchorSelector, normalizeQuote } from "./element-select"

export interface PreviewSelection { selector: string; quote: string }

interface Props {
  html: string
  /** The project's files, so the screen's own stylesheets and scripts can be written into it. */
  files?: ArtifactFile[]
  screens: string[]
  /** The screen being shown. */
  path: string
  selectMode: boolean
  title: string
  /** A `#id` to outline once the frame has loaded (comment → element); a new `seq` outlines the same element again. */
  highlight?: { selector: string; seq: number } | null
  onSelect: (selection: PreviewSelection) => void
  onNavigate: (path: string) => void
  className?: string
}

/**
 * The artifact in an opaque-origin sandbox: scripts may run, but without
 * allow-same-origin they cannot read the app's cookies or storage, and
 * without forms/popups/top-navigation they cannot leave the frame. The only
 * channel back is postMessage, accepted solely from this frame's window.
 */
export function ArtifactPreviewFrame({ html, files, screens, path, selectMode, title, highlight, onSelect, onNavigate, className }: Props) {
  const ref = useRef<HTMLIFrameElement>(null)
  const srcDoc = useMemo(() => buildPreviewDocument(files ? inlineProjectAssets(html, path, files) : html, { selectMode, screens, path }), [html, files, selectMode, screens, path])
  const latest = useRef({ onSelect, onNavigate, screens, selectMode })
  useEffect(() => { latest.current = { onSelect, onNavigate, screens, selectMode } })

  useEffect(() => {
    const onMessage = (event: MessageEvent) => {
      const frame = ref.current?.contentWindow
      if (!frame || event.source !== frame) return
      const data: unknown = event.data
      if (!data || typeof data !== "object") return
      const message = data as { type?: unknown; selector?: unknown; quote?: unknown; path?: unknown }
      const current = latest.current
      if (message.type === SELECT_MESSAGE) {
        if (!current.selectMode || typeof message.selector !== "string" || !isValidAnchorSelector(message.selector)) return
        current.onSelect({ selector: message.selector, quote: normalizeQuote(typeof message.quote === "string" ? message.quote : "") })
      } else if (message.type === NAVIGATE_MESSAGE) {
        if (typeof message.path === "string" && current.screens.includes(message.path)) current.onNavigate(message.path)
      }
    }
    window.addEventListener("message", onMessage)
    return () => window.removeEventListener("message", onMessage)
  }, [])

  const postHighlight = () => {
    if (highlight && isValidAnchorSelector(highlight.selector)) ref.current?.contentWindow?.postMessage({ type: HIGHLIGHT_MESSAGE, selector: highlight.selector }, "*")
  }
  // A changed highlight on an already loaded document; a new document posts it from onLoad.
  useEffect(postHighlight, [highlight])

  return <iframe ref={ref} title={title} sandbox="allow-scripts" referrerPolicy="no-referrer" srcDoc={srcDoc} onLoad={postHighlight}
    className={cn("block size-full border-0 bg-white", className)} />
}
