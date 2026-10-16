import { apiFetch } from "@/lib/api-fetch"
import { SLIDES_MAX_IMAGE_ASSETS, SLIDES_MAX_IMAGE_BYTES, SLIDES_MAX_IMAGE_PIXELS, SLIDES_IMAGE_MAX_DIMENSION, SLIDES_IMAGE_MAX_ASSET_PIXELS, SLIDES_IMAGE_MAX_UPLOAD_BYTES, SLIDES_IMAGE_MAX_STORED_BYTES, type SlideImage, type SlideDeck } from "./model"

export const PAGE_IMAGE_UPLOAD_MAX_BYTES = SLIDES_IMAGE_MAX_UPLOAD_BYTES
export const PAGE_IMAGE_STORED_MAX_BYTES = SLIDES_IMAGE_MAX_STORED_BYTES

export interface PageImageMetadata { id: string; page_id: string; sha256: string; content_type: "image/png" | "image/jpeg"; width: number; height: number; byte_size: number; state: "staged" | "retained"; created_at: string; expires_at: string }
export class PageImageError extends Error { constructor(message: string, public status?: number) { super(message) } }
const integer = (value: unknown): value is number => Number.isSafeInteger(value) && Number(value) > 0
export function validPageImageMetadata(value: unknown, pageId: string): value is PageImageMetadata {
  if (!value || typeof value !== "object") return false
  const v = value as PageImageMetadata
  return typeof v.id === "string" && /^[A-Za-z0-9_-]{1,128}$/.test(v.id) && v.page_id === pageId && typeof v.sha256 === "string" && /^[a-f0-9]{64}$/.test(v.sha256) && ["image/png", "image/jpeg"].includes(v.content_type) && integer(v.width) && v.width <= SLIDES_IMAGE_MAX_DIMENSION && integer(v.height) && v.height <= SLIDES_IMAGE_MAX_DIMENSION && v.width * v.height <= SLIDES_IMAGE_MAX_ASSET_PIXELS && integer(v.byte_size) && v.byte_size <= PAGE_IMAGE_STORED_MAX_BYTES && ["staged", "retained"].includes(v.state) && typeof v.created_at === "string" && v.created_at.length <= 128 && typeof v.expires_at === "string" && v.expires_at.length <= 128
}
export function pageImagePath(workspace: string, slug: string, path = "", mutationId?: string) {
  const query = new URLSearchParams({ workspace_id: workspace }); if (mutationId) query.set("mutation_id", mutationId)
  return `/api/v1/pages/${encodeURIComponent(slug)}/assets${path}?${query}`
}
async function checked(response: Response) {
  if (!response.ok) { const body = await response.json().catch(() => null); throw new PageImageError(typeof body?.error === "string" ? body.error : `Image request failed (${response.status}).`, response.status) }
  return response
}
export async function readPageImageMetadata(workspace: string, slug: string, pageId: string, image: SlideImage, signal?: AbortSignal) {
  const response = await checked(await apiFetch(pageImagePath(workspace, slug, `/${encodeURIComponent(image.asset_id)}`), { signal }))
  const metadata: unknown = await response.json()
  if (!validPageImageMetadata(metadata, pageId) || metadata.id !== image.asset_id || metadata.sha256 !== image.expected_sha256) throw new PageImageError("Image Page identity or expected digest changed.", 409)
  return metadata
}
export async function loadPageImage(workspace: string, slug: string, pageId: string, image: SlideImage, signal: AbortSignal) {
  const metadata = await readPageImageMetadata(workspace, slug, pageId, image, signal)
  signal.throwIfAborted()
  const response = await checked(await apiFetch(pageImagePath(workspace, slug, `/${encodeURIComponent(image.asset_id)}/bytes`), { signal }))
  if (response.headers.get("Content-Type")?.split(";")[0].trim() !== metadata.content_type || !response.body) throw new PageImageError("Unsupported image bytes.", 409)
  const reader = response.body.getReader(), chunks: Uint8Array[] = []; let size = 0
  try {
    for (;;) { const part = await reader.read(); if (part.done) break; size += part.value.byteLength; if (size > metadata.byte_size) throw new PageImageError("Image byte size changed.", 409); chunks.push(part.value) }
  } finally { await reader.cancel().catch(() => {}); reader.releaseLock() }
  if (size !== metadata.byte_size || signal.aborted) throw new PageImageError("Image read interrupted or incomplete.")
  const bytes = new Uint8Array(size); let offset = 0; for (const chunk of chunks) { bytes.set(chunk, offset); offset += chunk.length }
  const digest = Array.from(new Uint8Array(await crypto.subtle.digest("SHA-256", bytes)), byte => byte.toString(16).padStart(2, "0")).join("")
  if (digest !== image.expected_sha256) throw new PageImageError("Image digest verification failed.", 409)
  return { blob: new Blob([bytes], { type: metadata.content_type }), bytes: size, pixels: metadata.width * metadata.height }
}
interface Loaded { blob: Blob; bytes: number; pixels: number }
interface Entry { refs: number; controller: AbortController; promise: Promise<string>; url?: string; bytes: number; pixels: number }
/** One instance per immutable Page/revision. Repeated placements share a fetch
 * and URL; only active frames acquire entries, with four concurrent reads. */
export class PageImageCache {
  private entries = new Map<string, Entry>()
  private listeners = new Set<() => void>()
  private waiting: Array<() => void> = []
  private running = 0
  private totalBytes = 0
  private totalPixels = 0
  version = 0
  closed = false
  reason?: string
  constructor(private load: (image: SlideImage, signal: AbortSignal) => Promise<Loaded>, private createURL = (blob: Blob) => URL.createObjectURL(blob), private revokeURL = (url: string) => URL.revokeObjectURL(url), private onDenial?: (reason: string) => void) {}
  subscribe = (listener: () => void) => { this.listeners.add(listener); return () => { this.listeners.delete(listener) } }
  snapshot = () => this.version
  private publish() { this.version++; for (const listener of this.listeners) listener() }
  private limited<T>(signal: AbortSignal, task: () => Promise<T>): Promise<T> {
    return new Promise((resolve, reject) => {
      let started = false
      const begin = () => {
        started = true; signal.removeEventListener("abort", cancel)
        if (signal.aborted) { reject(new DOMException("Image read cancelled", "AbortError")); this.waiting.shift()?.(); return }
        this.running++
        void task().then(resolve, reject).finally(() => { this.running--; this.waiting.shift()?.() })
      }
      const cancel = () => { if (!started) { const index = this.waiting.indexOf(begin); if (index >= 0) this.waiting.splice(index, 1); reject(new DOMException("Image read cancelled", "AbortError")) } }
      signal.addEventListener("abort", cancel, { once: true })
      if (this.running < 4) begin(); else this.waiting.push(begin)
    })
  }
  acquire(image: SlideImage) {
    if (this.closed) throw new PageImageError(this.reason ?? "Image source is closed.")
    const key = `${image.asset_id}:${image.expected_sha256}`
    let entry = this.entries.get(key)
    if (!entry) {
      if (this.entries.size >= SLIDES_MAX_IMAGE_ASSETS) throw new PageImageError("Too many active image assets.")
      const controller = new AbortController()
      const current: Entry = { refs: 0, controller, promise: Promise.resolve(""), bytes: 0, pixels: 0 }
      current.promise = this.limited(controller.signal, () => this.load(image, controller.signal)).then(value => {
        if (this.closed || controller.signal.aborted) throw new DOMException("Image source changed", "AbortError")
        if (this.totalBytes + value.bytes > SLIDES_MAX_IMAGE_BYTES || this.totalPixels + value.pixels > SLIDES_MAX_IMAGE_PIXELS) throw new PageImageError("Active images exceed the shared encoded byte or decoded pixel budget.", 409)
        current.bytes = value.bytes; current.pixels = value.pixels; this.totalBytes += value.bytes; this.totalPixels += value.pixels
        current.url = this.createURL(value.blob); return current.url
      }).catch(cause => { if (cause instanceof PageImageError && [401, 403, 404, 409, 410].includes(cause.status ?? 0)) this.deny(cause.message); throw cause })
      entry = current; this.entries.set(key, current)
    }
    entry.refs++
    let released = false
    return { promise: entry.promise, release: () => {
      if (released) return; released = true
      if (--entry.refs === 0 && this.entries.get(key) === entry) { entry.controller.abort(); if (entry.url) this.revokeURL(entry.url); this.totalBytes -= entry.bytes; this.totalPixels -= entry.pixels; this.entries.delete(key) }
    } }
  }
  deny(reason: string) { this.dispose(reason); this.onDenial?.(reason) }
  dispose(reason?: string) {
    if (this.closed) return
    this.closed = true; this.reason = reason
    for (const entry of this.entries.values()) { entry.controller.abort(); if (entry.url) this.revokeURL(entry.url) }
    this.entries.clear(); this.totalBytes = 0; this.totalPixels = 0; this.publish()
  }
}

/** Advisory whole-candidate preflight. The durable writer rechecks all bytes,
 * pixels, Page ACL and immutable revision pins atomically when saved. */
export async function preflightPageImages(workspace: string, slug: string, pageId: string, deck: SlideDeck, signal?: AbortSignal) {
  const images = new Map<string, SlideImage>()
  for (const slide of deck.slides) for (const element of slide.elements) if (element.type === "image") {
    const previous = images.get(element.image.asset_id)
    if (previous && previous.expected_sha256 !== element.image.expected_sha256) throw new PageImageError("Conflicting image asset identity.")
    images.set(element.image.asset_id, element.image)
  }
  if (images.size > SLIDES_MAX_IMAGE_ASSETS) throw new PageImageError("A saved revision supports at most32 unique image assets.", 400)
  let bytes = 0, pixels = 0
  const pending = [...images.values()]
  for (let offset = 0; offset < pending.length; offset += 4) {
    const items = await Promise.all(pending.slice(offset, offset + 4).map(image => readPageImageMetadata(workspace, slug, pageId, image, signal)))
    for (const metadata of items) { bytes += metadata.byte_size; pixels += metadata.width * metadata.height }
    if (bytes > SLIDES_MAX_IMAGE_BYTES || pixels > SLIDES_MAX_IMAGE_PIXELS) throw new PageImageError("Images exceed the revision's shared encoded byte or decoded pixel budget. Remove unused image assets before inserting.", 400)
  }
}
export async function uploadPageImage(workspace: string, slug: string, pageId: string, file: File, mutationId: string, signal: AbortSignal) {
  const response = await checked(await apiFetch(pageImagePath(workspace, slug, "", mutationId), { method: "POST", headers: { "Content-Type": file.type || "application/octet-stream" }, body: file, signal }))
  const metadata: unknown = await response.json()
  if (!validPageImageMetadata(metadata, pageId)) throw new PageImageError("Uploaded image Page identity or metadata changed.", 409)
  return metadata
}

/** Live authorization is Page-scoped, while image bytes remain revision-scoped.
 * A denial in any preview invalidates every active revision until explicit reload. */
export class PageImageAuthorization {
  private caches = new Set<PageImageCache>()
  reason?: string
  register(cache: PageImageCache) {
    this.caches.add(cache)
    if (this.reason) cache.dispose(this.reason)
    return () => { this.caches.delete(cache) }
  }
  deny = (reason: string) => {
    this.reason = reason
    for (const cache of this.caches) cache.dispose(reason)
  }
}
