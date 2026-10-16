"use client"

import { useEffect, useRef, useState } from "react"
import type { SlideDeck } from "@/lib/slides/model"
import { SLIDES_MAX_IMAGE_ALT_BYTES, SLIDES_MAX_IMAGE_ASSETS } from "@/lib/slides/model"
import { PAGE_IMAGE_UPLOAD_MAX_BYTES, PageImageError, preflightPageImages, uploadPageImage } from "@/lib/slides/page-images"
import { freshSlideId } from "./native-model"
import { useSlideImageCache } from "./slide-images"

const control = "min-h-9 coarse:min-h-12 rounded border px-3 disabled:opacity-50"
interface Intent { file: File; alt: string; slideId: string; pageId: string; deck: SlideDeck; mutationId: string; elementId: string }
const validAlt = (value: string) => new TextEncoder().encode(value).length <= SLIDES_MAX_IMAGE_ALT_BYTES && !/[\u0000-\u0008\u000b-\u001f\u007f\p{Cs}]/u.test(value)
export function SlideImageInsert({ workspaceId, slug, pageId, deck, disabled, onInsert, onPendingChange, onDraftChange }: { workspaceId: string; slug: string; pageId: string; deck: SlideDeck; disabled: boolean; onInsert: (candidate: SlideDeck) => void; onPendingChange: (pending: boolean) => void; onDraftChange: (dirty: boolean) => void }) {
  const [file, setFile] = useState<File>(), [alt, setAlt] = useState(""), [slideId, setSlideId] = useState(deck.slide_order[0])
  const [pending, setPending] = useState(false), [busy, setBusy] = useState(false), [blocked, setBlocked] = useState(false), [error, setError] = useState<string>(), [status, setStatus] = useState<string>()
  const intent = useRef<Intent | undefined>(undefined), controller = useRef<AbortController | undefined>(undefined), generation = useRef(0)
  const imageCache = useSlideImageCache()
  const latestSlug = useRef(slug); latestSlug.current = slug
  const input = useRef<HTMLInputElement>(null)
  const uniqueAssets = new Set(deck.slides.flatMap(slide => slide.elements.flatMap(element => element.type === "image" ? [element.image.asset_id] : []))).size
  const cancelCurrent = () => { generation.current++; controller.current?.abort() }
  useEffect(() => { onDraftChange(!!file || pending); return () => onDraftChange(false) }, [file, pending, onDraftChange])
  useEffect(() => {
    const active = controller, version = generation
    intent.current = undefined; setPending(false); setBusy(false); setBlocked(false); setFile(undefined); setAlt(""); setError(undefined); setStatus(undefined)
    return () => { version.current++; active.current?.abort(); onPendingChange(false) }
  }, [workspaceId, pageId, onPendingChange])
  async function insert() {
    if (busy || blocked || (!intent.current && disabled)) return
    if (!intent.current) {
      if (deck.deck_id !== pageId || !file || !["", "image/png", "image/jpeg"].includes(file.type) || file.size < 1 || file.size > PAGE_IMAGE_UPLOAD_MAX_BYTES || !validAlt(alt) || uniqueAssets >= SLIDES_MAX_IMAGE_ASSETS || !deck.slides.some(slide => slide.id === slideId)) { setError("Choose a PNG/JPEG up to5MiB, a current slide and alternative text up to2048 UTF-8 bytes. Remove unused assets if the revision already has32 unique images."); return }
      intent.current = { file, alt, slideId, pageId, deck: structuredClone(deck), mutationId: crypto.randomUUID(), elementId: freshSlideId(deck) }
      setPending(true); onPendingChange(true)
    }
    const original = intent.current, requestGeneration = ++generation.current
    controller.current = new AbortController(); setBusy(true); setError(undefined)
    const current = () => generation.current === requestGeneration
    try {
      setStatus("Uploading private image; Slides have not been saved.")
      const metadata = await uploadPageImage(workspaceId, latestSlug.current, original.pageId, original.file, original.mutationId, controller.current.signal)
      if (!current()) return
      const candidate = structuredClone(original.deck), slide = candidate.slides.find(value => value.id === original.slideId)!
      const inset = Math.min(24, Math.floor(Math.min(candidate.width, candidate.height) / 8))
      const scale = Math.min(Math.min(480, candidate.width - 2 * inset) / metadata.width, Math.min(360, candidate.height - 2 * inset) / metadata.height)
      candidate.schema_version = 2
      slide.elements.push({ id: original.elementId, type: "image", x: inset, y: inset, width: Math.max(1, Math.floor(metadata.width * scale)), height: Math.max(1, Math.floor(metadata.height * scale)), image: { asset_id: metadata.id, expected_sha256: metadata.sha256, alt: original.alt } })
      setStatus("Image uploaded; checking the complete candidate image budget. Slides have not been saved.")
      await preflightPageImages(workspaceId, latestSlug.current, original.pageId, candidate, controller.current.signal)
      if (!current()) return
      onInsert(candidate); intent.current = undefined; setPending(false); onPendingChange(false); setFile(undefined); setAlt(""); if (input.current) input.current.value = ""
      setStatus("Image added to the unsaved Slides draft. Save changes to commit it.")
    } catch (cause) {
      if (!current()) return
      if (cause instanceof PageImageError && cause.status && cause.status >= 400 && cause.status < 500 && ![408, 429].includes(cause.status)) {
        setBlocked(true); setStatus("Image insertion rejected. The original file and target are retained; discard this attempt before changing them.")
        if ([401, 403, 404, 409, 410].includes(cause.status)) imageCache?.deny("Image access or identity changed.")
      } else setStatus("Image upload or insertion outcome uncertain. Retry keeps the original file, target and upload mutation ID.")
      setError(cause instanceof Error ? cause.message : "Could not insert image.")
    } finally { if (current()) setBusy(false) }
  }
  return <section aria-label="Insert private slide image" className="space-y-2 rounded border p-3">
    <h3>Insert private image</h3>
    <p className="text-sm">PNG/JPEG up to5MiB. Uploading stages a Page asset; only saving Slides confirms a revision. Shared per-revision asset, byte and pixel limits apply.</p>
    <fieldset disabled={disabled || pending} className="space-y-2">
      <label className="block">Image slide<select className={control} value={deck.slides.some(slide => slide.id === slideId) ? slideId : ""} onChange={event => setSlideId(event.target.value)}><option value="">Choose slide</option>{deck.slide_order.map((id, index) => <option key={id} value={id}>Slide {index + 1}</option>)}</select></label>
      <label className="block">PNG or JPEG file<input ref={input} type="file" accept="image/png,image/jpeg" className="block min-h-9 coarse:min-h-12" onChange={event => setFile(event.target.files?.[0])} /></label>
      <label className="block">Image alternative text<textarea className="min-h-20 w-full rounded border p-2" value={alt} onChange={event => setAlt(event.target.value)} /></label>
    </fieldset>
    {error && <p role="alert">{error}</p>}{status && <p role="status">{status}</p>}
    <button className={control} disabled={busy || blocked || (!pending && (disabled || !file || !validAlt(alt) || uniqueAssets >= SLIDES_MAX_IMAGE_ASSETS))} onClick={() => void insert()}>{busy ? "Inserting image…" : pending ? "Retry original image insertion" : "Upload and insert image"}</button>
    {(file || pending) && <button className={control} onClick={() => { if (pending && !window.confirm("Discard this image insertion attempt? An uncertain upload may already exist as a staged Page asset, but no Slides image is confirmed by upload alone.")) return; cancelCurrent(); intent.current = undefined; setPending(false); onPendingChange(false); setBusy(false); setBlocked(false); setFile(undefined); setAlt(""); setError(undefined); setStatus(undefined); if (input.current) input.current.value = "" }}>Discard image insertion</button>}
  </section>
}
