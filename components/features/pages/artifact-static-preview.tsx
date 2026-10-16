"use client"

import { useEffect, useRef, useState } from "react"
import type { ArtifactAnchor } from "@/hooks/use-artifact-threads"

type PointAnchor = Extract<ArtifactAnchor, { type: "point" }>

/** Static HTML has no bridge. A point is explicitly in the visible viewport,
 * captured after reloading to the top, never a fabricated DOM/document anchor. */
export function ArtifactStaticPreview({ document, revision, picking, shown, onPick }: {
  document: string
  revision: number
  picking: boolean
  shown?: { revision: number; anchor: PointAnchor } | null
  onPick: (anchor: PointAnchor) => void
}) {
  const container = useRef<HTMLDivElement>(null)
  const [size, setSize] = useState({ width: 0, height: 0 })
  const [ready, setReady] = useState(false)
  const mode = picking ? "capture" : shown?.revision === revision ? "anchor" : "browse"
  useEffect(() => { setReady(false) }, [document, revision, mode])
  useEffect(() => {
    const target = container.current
    if (!target) return
    const update = () => { const rect = target.getBoundingClientRect(); setSize({ width: rect.width, height: rect.height }) }
    update()
    const observer = new ResizeObserver(update)
    observer.observe(target)
    return () => observer.disconnect()
  }, [])
  const anchor = shown?.revision === revision ? shown.anchor : undefined
  const matching = anchor && Math.abs(size.width - anchor.viewport_width) < 1 && Math.abs(size.height - anchor.viewport_height) < 1
  return <div>
    {(picking || anchor) && <p className="border-b bg-muted/30 px-4 py-2 text-xs text-muted-foreground">Preview restarted at the top. A point describes this visible viewport only, not a DOM element or a scrolled document location.{anchor && !matching && " This viewport differs from the captured size; the point is not overlaid."}</p>}
    <div ref={container} className="relative h-[min(65vh,720px)] min-h-80">
      <iframe key={`${revision}:${mode}:${document ? "ready" : "pending"}`} title="Artifact static preview" sandbox="" referrerPolicy="no-referrer" srcDoc={document} onLoad={() => setReady(Boolean(document))} className="h-full w-full border-0 bg-white" />
      {(picking || anchor) && <div className="absolute inset-0" style={{ pointerEvents: "auto" }}>
        {picking && <button type="button" aria-label="Choose comment point in preview" disabled={!ready} className="absolute inset-0 h-full w-full cursor-crosshair focus-visible:outline-2 focus-visible:outline-primary" onClick={event => {
          const rect = event.currentTarget.getBoundingClientRect()
          if (rect.width < 1 || rect.height < 1 || rect.width > 16384 || rect.height > 16384) return
          const keyboard = event.detail === 0
          onPick({ schema_version: 1, type: "point", coordinate_space: "viewport", x: keyboard ? rect.width / 2 : Math.min(rect.width, Math.max(0, event.clientX - rect.left)), y: keyboard ? rect.height / 2 : Math.min(rect.height, Math.max(0, event.clientY - rect.top)), viewport_width: rect.width, viewport_height: rect.height })
        }} />}
        {!picking && matching && ready && <span aria-label="Comment point" className="pointer-events-none absolute flex h-6 w-6 -translate-x-1/2 -translate-y-1/2 items-center justify-center rounded-full border-2 border-background bg-primary text-xs text-primary-foreground shadow" style={{ left: anchor.x, top: anchor.y }}>•</span>}
      </div>}
    </div>
  </div>
}
