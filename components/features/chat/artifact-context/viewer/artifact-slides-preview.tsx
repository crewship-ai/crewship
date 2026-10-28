"use client"
import { useCallback, useEffect, useMemo, useRef, useState } from "react"
import { SlideCanvas } from "@/components/features/slides/slide-canvas"
import { SlidePresent } from "@/components/features/slides/slide-present"
import { artifactSlideThumbnail, type ArtifactSlidesProjection } from "@/lib/pages/artifact-slides"
import { useArtifactSlides } from "../projects/use-artifact-slides"
import type { PreviewSelection } from "./artifact-preview-frame"
import "./artifact-slides-preview.css"
export interface ArtifactSlidesPreviewProps {
  workspaceId: string; pageId: string; codeVersion: number; artifactKind?: string; path: string; title: string
  selectMode: boolean; onSelect: (selection: PreviewSelection) => void; onNavigate: (path: string) => void; className?: string
}
export function ArtifactSlidesPreview(props: ArtifactSlidesPreviewProps) {
  const data = useArtifactSlides(props)
  const [font, setFont] = useState("loading")
  useEffect(() => {
    if (!data.enabled) return
    let active = true
    void document.fonts?.load('16px "Go Regular"').then(faces => { if (active) setFont(faces.length ? "ready" : "error") }).catch(() => { if (active) setFont("error") })
    if (!document.fonts) setFont("error")
    return () => { active = false }
  }, [data.enabled])
  if (!data.enabled) return <p role="status">Slides preview is unavailable.</p>
  if (data.error) return <p role="alert">{data.error instanceof Error && data.error.message === "Slides response exceeds the data limit" ? "Slides exceeds the preview data limit." : data.error instanceof Error && data.error.message === "This Slides format is unsupported." ? data.error.message : "Slides preview is unavailable. Reload to check access."}</p>
  if (!data.projection) return <p role="status">Loading Slides…</p>
  if (font === "error") return <p role="alert">Slides font is unavailable.</p>
  if (font !== "ready") return <p role="status">Loading Slides font…</p>
  return <Slides key={data.scope} {...props} projection={data.projection} download={data.downloadPDF} />
}

const control = "min-h-9 coarse:min-h-[44px] rounded border px-3 text-sm"
function Slides({ projection, path, title, selectMode, onSelect, onNavigate, className, codeVersion, download }: ArtifactSlidesPreviewProps & { projection: ArtifactSlidesProjection; download: () => Promise<Uint8Array<ArrayBuffer> | null> }) {
  const { deck, anchors } = projection
  const requested = deck.slide_order.find(id => path === `slides/${id}.html`)
  const slideId = requested ?? deck.slide_order[0]
  // The server's slide order chooses the initial view; the parent must own the
  // corresponding source path before an authored selection can become a comment.
  useEffect(() => { if (!requested) onNavigate(`slides/${slideId}.html`) }, [requested, slideId, onNavigate])
  const thumbnails = useMemo(() => deck.slide_order.map(id => ({ id, deck: artifactSlideThumbnail(deck, id) })), [deck])
  const [present, setPresent] = useState(false)
  const [pdfPending, setPdfPending] = useState(false)
  const [pdfError, setPdfError] = useState("")
  const [selectionError, setSelectionError] = useState("")
  const [overflow, setOverflow] = useState<string[]>([])
  const onOverflow = useCallback((ids: string[]) => setOverflow(ids), [])
  const mounted = useRef(true)
  useEffect(() => { mounted.current = true; return () => { mounted.current = false } }, [])
  const select = (id: string) => {
    if (!selectMode || !Object.hasOwn(anchors, id) || anchors[id].slide_id !== slideId || anchors[id].path !== path) { setSelectionError("Choose authored text or a table to comment on."); return }
    setSelectionError("")
    onSelect({ selector: anchors[id].selector, quote: anchors[id].quote })
  }
  const pdf = async () => {
    if (pdfPending) return
    setPdfPending(true); setPdfError("")
    try {
      const bytes = await download()
      if (!bytes || !mounted.current) return
      const url = URL.createObjectURL(new Blob([bytes], { type: "application/pdf" }))
      try {
        const link = document.createElement("a")
        link.href = url; link.download = `slides-v${codeVersion}.pdf`; link.click()
      } finally { URL.revokeObjectURL(url) }
    } catch (error) { if (mounted.current && (error as { name?: string })?.name !== "AbortError") setPdfError(error instanceof Error && error.message === "Slides response exceeds the data limit" ? "PDF exceeds the download data limit." : "PDF download is unavailable. Reload to check access or slide overflow.") }
    finally { if (mounted.current) setPdfPending(false) }
  }
  return <section aria-label={title} className={`artifact-slides flex h-full min-h-0 flex-1 flex-col overflow-auto bg-muted p-3 ${className ?? ""}`}>
    <div className="mb-3 flex flex-wrap items-center gap-2">
      <span className="mr-auto text-sm">Slide {deck.slide_order.indexOf(slideId) + 1} / {deck.slide_order.length}</span>
      <button type="button" className={control} onClick={() => setPresent(true)}>Present</button>
      <button type="button" className={control} disabled={pdfPending} onClick={() => void pdf()}>{pdfPending ? "Preparing PDF…" : "Download PDF"}</button>
    </div>
    {pdfError && <p role="alert" className="mb-2 text-sm">{pdfError}</p>}
    {selectionError && <p role="status" className="mb-2 text-sm">{selectionError}</p>}
    <div className="artifact-slides-main min-h-0 w-full flex-1 rounded border bg-white"><SlideCanvas deck={deck} slideId={slideId} onSelectElement={selectMode && requested ? select : undefined} onOverflowChange={onOverflow} /></div>
    {overflow.length > 0 && <p role="status" className="mt-2 text-sm">Some slide content overflows its frame. PDF export may be unavailable.</p>}
    <nav aria-label="Slide thumbnails" className="mt-3 flex shrink-0 gap-2 overflow-x-auto pb-1">
      {thumbnails.map(({ id, deck: thumbnail }, index) => <button type="button" key={id} aria-label={`Slide ${index + 1}`} aria-current={id === slideId ? "true" : undefined} className="w-36 shrink-0 overflow-hidden rounded border-2 border-transparent aria-[current=true]:border-primary coarse:min-h-[44px]" onClick={() => { setSelectionError(""); onNavigate(`slides/${id}.html`) }}>
        <SlideCanvas deck={thumbnail} slideId={id} />
      </button>)}
    </nav>
    {present && <SlidePresent deck={deck} initialSlideId={slideId} onClose={() => setPresent(false)} />}
  </section>
}
