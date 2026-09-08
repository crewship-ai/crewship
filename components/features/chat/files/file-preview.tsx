'use client'

import { useEffect, useRef, useState } from 'react'
import type { PDFDocumentProxy, RenderTask } from 'pdfjs-dist'
import { ArrowLeft, Download, Minus, Plus } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { previewMime, readPreviewBytes } from './file-preview-data'

type FilePreviewProps = { url: string; name: string; onClose: () => void }
type Loaded = { bytes: Uint8Array<ArrayBuffer>; mime: ReturnType<typeof previewMime>; blobUrl: string }

export function FilePreview({ url, name, onClose }: FilePreviewProps) {
  // Keying this boundary also protects callers that reuse the panel across files.
  return <PreviewContent key={url} url={url} name={name} onClose={onClose} />
}

function PreviewContent({ url, name, onClose }: FilePreviewProps) {
  const [loaded, setLoaded] = useState<Loaded | null>(null)
  const [error, setError] = useState('')
  const [attempt, setAttempt] = useState(0)
  useEffect(() => {
    const controller = new AbortController()
    let objectUrl: string | undefined
    void readPreviewBytes(url, controller.signal).then(bytes => {
      if (controller.signal.aborted) return
      const mime = previewMime(bytes)
      objectUrl = URL.createObjectURL(new Blob([bytes], { type: mime ?? 'application/octet-stream' }))
      setLoaded({ bytes, mime, blobUrl: objectUrl })
    }).catch((cause: unknown) => {
      if (!controller.signal.aborted) setError(cause instanceof Error ? cause.message : 'Could not load this preview. Try again or download the file.')
    })
    return () => { controller.abort(); if (objectUrl) URL.revokeObjectURL(objectUrl) }
  }, [url, attempt])

  function download() {
    if (!loaded) return
    // Force a download MIME even for unrecognized HTML/SVG content.
    const downloadUrl = URL.createObjectURL(new Blob([loaded.bytes], { type: 'application/octet-stream' }))
    const link = document.createElement('a')
    link.href = downloadUrl; link.download = name; link.click()
    // Browsers consume the object URL asynchronously after the click.
    setTimeout(() => URL.revokeObjectURL(downloadUrl), 1000)
  }
  const safeDownload = url.startsWith('/api/') && !url.includes('\\')
  return <section aria-label={`Preview ${name}`} className="flex h-full min-h-0 min-w-0 flex-col">
    <header className="flex shrink-0 items-center gap-2 border-b p-2">
      <Button variant="ghost" size="icon" aria-label="Back to files" onClick={onClose}><ArrowLeft className="size-4" /></Button>
      <span className="min-w-0 flex-1 truncate text-sm" title={name}>{name}</span>
      <Button variant="ghost" size="icon" aria-label="Download file" disabled={!loaded} onClick={download}><Download className="size-4" /></Button>
    </header>
    {!loaded && !error && <p role="status" className="p-4 text-sm text-muted-foreground">Loading preview…</p>}
    {error && <div className="space-y-3 p-4 text-sm"><p role="alert">{error}</p><Button variant="outline" onClick={() => { setError(''); setLoaded(null); setAttempt(n => n + 1) }}>Retry preview</Button>
      {safeDownload && <p><a className="underline" href={url} download={name}>Download file</a></p>}
    </div>}
    {loaded?.mime?.startsWith('image/') && <div className="flex min-h-0 flex-1 items-center justify-center overflow-auto p-3">
      {/* Blob URL contains only signature-checked raster bytes, never SVG/HTML. */}
      <img src={loaded.blobUrl} alt={name} className="max-h-full max-w-full object-contain" onError={() => setError('The image could not be decoded. Download the original file to inspect it.')} />
    </div>}
    {loaded?.mime === 'application/pdf' && <PdfCanvas key={attempt} bytes={loaded.bytes} />}
    {loaded && !loaded.mime && <p className="p-4 text-sm text-muted-foreground">Preview is available for PDF, PNG, JPEG and WebP files. Download this file to open it.</p>}
  </section>
}

function PdfCanvas({ bytes }: { bytes: Uint8Array<ArrayBuffer> }) {
  const [attempt, setAttempt] = useState(0)
  return <PdfDocument key={attempt} bytes={bytes} onRetry={() => setAttempt(n => n + 1)} />
}

function PdfDocument({ bytes, onRetry }: { bytes: Uint8Array<ArrayBuffer>; onRetry: () => void }) {
  const [pdf, setPdf] = useState<PDFDocumentProxy | null>(null)
  const [page, setPage] = useState(1)
  const [zoom, setZoom] = useState(1)
  const [error, setError] = useState('')
  const [rendering, setRendering] = useState(true)
  const [availableWidth, setAvailableWidth] = useState(400)
  const canvas = useRef<HTMLCanvasElement>(null)
  const container = useRef<HTMLDivElement>(null)
  useEffect(() => {
    if (!container.current || typeof ResizeObserver === 'undefined') return
    const observer = new ResizeObserver(entries => {
      const width = entries[0]?.contentRect.width
      if (width && width > 0) setAvailableWidth(width)
    })
    observer.observe(container.current)
    return () => observer.disconnect()
  }, [])
  useEffect(() => {
    let disposed = false
    let task: ReturnType<typeof import('pdfjs-dist').getDocument> | undefined
    void import('pdfjs-dist').then(lib => {
      if (disposed) return
      lib.GlobalWorkerOptions.workerSrc = '/pdfjs/pdf.worker.min.mjs'
      task = lib.getDocument({ data: bytes.slice(), cMapUrl: '/pdfjs/cmaps/', cMapPacked: true,
        standardFontDataUrl: '/pdfjs/standard_fonts/', wasmUrl: '/pdfjs/wasm/',
        enableXfa: false, maxImageSize: 16_777_216, canvasMaxAreaInBytes: 64 * 1024 * 1024 })
      task.onPassword = () => {
        if (!disposed) setError('This PDF is password protected. Download the original file to open it locally.')
        void task?.destroy()
      }
      return task.promise.then(document => { if (!disposed) setPdf(document) })
    }).catch(() => { if (!disposed) setError(previous => previous || 'This PDF could not be opened. It may be damaged or password protected. Download the original file to open it locally.') })
    return () => { disposed = true; void task?.destroy() }
  }, [bytes])
  useEffect(() => {
    if (!pdf) return
    let disposed = false
    let task: RenderTask | undefined
    void pdf.getPage(page).then(documentPage => {
      if (disposed || !canvas.current) return
      const base = documentPage.getViewport({ scale: 1 })
      const fit = Math.min(1.5, Math.max(200, availableWidth - 24) / base.width)
      const viewport = documentPage.getViewport({ scale: fit * zoom })
      // Bound raster allocation even for malicious PDF page dimensions.
      const ratio = Math.min(window.devicePixelRatio || 1, 2, Math.sqrt(16_777_216 / (viewport.width * viewport.height)))
      const target = canvas.current
      target.width = Math.max(1, Math.floor(viewport.width * ratio))
      target.height = Math.max(1, Math.floor(viewport.height * ratio))
      target.style.width = `${viewport.width}px`; target.style.height = `${viewport.height}px`
      task = documentPage.render({ canvas: target, viewport, transform: [ratio, 0, 0, ratio, 0, 0] })
      return task.promise.then(() => { if (!disposed) setRendering(false) })
    }).catch(() => { if (!disposed) { setError('This PDF page could not be rendered. Download the original file to open it locally.'); setRendering(false) } })
    return () => { disposed = true; task?.cancel() }
  }, [pdf, page, zoom, availableWidth])
  const changePage = (next: number) => { setRendering(true); setPage(next) }
  const changeZoom = (next: number) => { setRendering(true); setZoom(next) }
  return <div className="flex min-h-0 flex-1 flex-col">
    {pdf && <div className="flex flex-wrap items-center justify-center gap-1 border-b p-2">
      <Button variant="ghost" size="sm" disabled={page <= 1} onClick={() => changePage(page - 1)}>Previous page</Button>
      <span className="text-xs" aria-live="polite">Page {page} of {pdf.numPages}</span>
      <Button variant="ghost" size="sm" disabled={page >= pdf.numPages} onClick={() => changePage(page + 1)}>Next page</Button>
      <Button variant="ghost" size="icon" aria-label="Zoom out" disabled={zoom <= .5} onClick={() => changeZoom(Math.max(.5, zoom - .25))}><Minus className="size-4" /></Button>
      <span className="text-xs">{Math.round(zoom * 100)}%</span>
      <Button variant="ghost" size="icon" aria-label="Zoom in" disabled={zoom >= 3} onClick={() => changeZoom(Math.min(3, zoom + .25))}><Plus className="size-4" /></Button>
    </div>}
    {error && <div className="p-4 text-sm"><p role="alert">{error}</p><Button variant="outline" onClick={onRetry}>Retry PDF preview</Button></div>}
    {!error && rendering && <p role="status" className="p-2 text-xs text-muted-foreground">Rendering PDF…</p>}
    <div ref={container} className="min-h-0 flex-1 overflow-auto p-3"><canvas ref={canvas} aria-label={`PDF page ${page}`} className="mx-auto bg-white" /></div>
  </div>
}
