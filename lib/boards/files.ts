import { apiFetch } from '@/lib/api-fetch'
import type { BinaryFileData } from '@excalidraw/excalidraw/types'
import type { FileId } from '@excalidraw/excalidraw/element/types'
import type { DataURL } from '@excalidraw/excalidraw/types'

export { boardImageSkeleton } from './image-element'

export const MAX_BOARD_IMAGE_INPUT = 5 * 1024 * 1024
export const MAX_BOARD_IMAGE_BYTES = 8 * 1024 * 1024
export const MAX_BOARD_IMAGE_FILES = 100
export const MAX_BOARD_IMAGE_TOTAL_BYTES = 50 * 1024 * 1024
export const MAX_BOARD_IMAGE_PIXELS = 64 * 1024 * 1024
export interface BoardImageUpload { file_id: string; content_type: 'image/png' | 'image/jpeg'; width: number; height: number; bytes: number; seq: number; result_seq: number }
export class BoardImageError extends Error { constructor(message: string, public status = 0) { super(message) } }
const validID = (id: string) => /^[A-Za-z0-9_-]{1,128}$/.test(id)
const endpoint = (workspace: string, slug: string, suffix = '') => `/api/v1/pages/${encodeURIComponent(slug)}/board/files${suffix}?workspace_id=${encodeURIComponent(workspace)}`
const assertCurrent = (signal: AbortSignal) => { if (signal.aborted) throw new DOMException('Image operation cancelled', 'AbortError') }
async function failure(response: Response): Promise<never> {
  let message = 'Board image request failed'
  try { const body = await response.json(); if (typeof body.error === 'string') message = body.error } catch { /* preserve bounded generic error */ }
  throw new BoardImageError(message, response.status)
}
export async function uploadBoardImage(workspace: string, slug: string, file: File, baseSeq: number, mutation: string, signal: AbortSignal): Promise<BoardImageUpload> {
  if (!file.size || file.size > MAX_BOARD_IMAGE_INPUT) throw new BoardImageError('Choose a PNG or JPEG image up to 5 MiB.')
  if (!Number.isSafeInteger(baseSeq) || baseSeq < 0 || !validID(mutation)) throw new BoardImageError('Invalid image upload intent')
  assertCurrent(signal)
  const response = await apiFetch(`${endpoint(workspace, slug)}&base_seq=${baseSeq}&mutation_id=${encodeURIComponent(mutation)}`, { method: 'POST', body: file, headers: { 'Content-Type': file.type || 'application/octet-stream' }, signal })
  if (!response.ok) return failure(response)
  const result = await response.json() as BoardImageUpload
  assertCurrent(signal)
  if (!validID(result.file_id) || !['image/png', 'image/jpeg'].includes(result.content_type) || ![result.width, result.height].every(value => Number.isInteger(value) && value > 0 && value <= 2048) || !Number.isInteger(result.bytes) || result.bytes < 1 || result.bytes > MAX_BOARD_IMAGE_BYTES || !Number.isSafeInteger(result.seq) || !Number.isSafeInteger(result.result_seq) || result.seq < result.result_seq || result.result_seq < 0) throw new BoardImageError('Invalid image upload response')
  return result
}
async function boundedBlob(response: Response, signal: AbortSignal): Promise<Blob> {
  const mime = response.headers.get('Content-Type')?.split(';')[0].trim()
  if (mime !== 'image/png' && mime !== 'image/jpeg') throw new BoardImageError('Unsupported board image response')
  const declared = Number(response.headers.get('Content-Length'))
  if (declared > MAX_BOARD_IMAGE_BYTES) throw new BoardImageError('Board image exceeds 8 MiB')
  const reader = response.body?.getReader()
  if (!reader) throw new BoardImageError('Board image stream unavailable')
  const chunks: Uint8Array<ArrayBuffer>[] = []; let size = 0
  try { while (true) { assertCurrent(signal); const part = await reader.read(); if (part.done) break; size += part.value.byteLength; if (size > MAX_BOARD_IMAGE_BYTES) throw new BoardImageError('Board image exceeds 8 MiB'); chunks.push(new Uint8Array(part.value)) } }
  finally { await reader.cancel().catch(() => undefined); reader.releaseLock() }
  assertCurrent(signal)
  return new Blob(chunks, { type: mime })
}
function dataURL(blob: Blob, signal: AbortSignal): Promise<DataURL> {
  return new Promise((resolve, reject) => {
    const reader = new FileReader()
    const abort = () => { signal.removeEventListener("abort",abort);reader.abort(); reject(new DOMException('Image operation cancelled', 'AbortError')) }
    signal.addEventListener('abort', abort, { once: true })
    reader.onload = () => { signal.removeEventListener('abort', abort); if (signal.aborted) return abort(); resolve(reader.result as DataURL) }
    reader.onerror = () => { signal.removeEventListener('abort', abort); reject(new BoardImageError('Could not decode board image')) }
    if (signal.aborted) return abort()
    reader.readAsDataURL(blob)
  })
}
// Public Excalidraw BinaryFiles are transient editor memory only. They never
// become scene JSON, local storage, or an unauthenticated image URL.
export async function loadBoardImages(workspace: string, slug: string, ids: readonly string[], parentSignal: AbortSignal): Promise<BinaryFileData[]> {
  const unique = [...new Set(ids)]
  if (unique.length > MAX_BOARD_IMAGE_FILES || unique.some(id => !validID(id))) throw new BoardImageError('Board image limit exceeded')
  const controller = new AbortController(); const signal = controller.signal
  const abort = () => controller.abort()
  parentSignal.addEventListener("abort", abort, { once: true })
  if (parentSignal.aborted) controller.abort()
  let totalBytes = 0; let totalPixels = 0; let next = 0
  const output: BinaryFileData[] = []
  const worker = async () => {
    while (next < unique.length) {
      const id = unique[next++]
      assertCurrent(signal)
      const response = await apiFetch(endpoint(workspace, slug, `/${encodeURIComponent(id)}`), { signal })
      if (!response.ok) return failure(response)
      const blob = await boundedBlob(response, signal)
      totalBytes += blob.size
      if (totalBytes > MAX_BOARD_IMAGE_TOTAL_BYTES) throw new BoardImageError('Board images exceed 50 MiB')
      const bitmap = await createImageBitmap(blob)
      const pixels = bitmap.width * bitmap.height
      const valid = bitmap.width > 0 && bitmap.height > 0 && bitmap.width <= 2048 && bitmap.height <= 2048
      bitmap.close(); assertCurrent(signal); totalPixels += pixels
      if (!valid || totalPixels > MAX_BOARD_IMAGE_PIXELS) throw new BoardImageError('Board image pixel limit exceeded')
      const encoded = await dataURL(blob, signal)
      assertCurrent(signal)
      output.push({ id: id as FileId, dataURL: encoded, mimeType: blob.type as 'image/png' | 'image/jpeg', created: Date.now(), lastRetrieved: Date.now() })
    }
  }
  try {await Promise.all(Array.from({ length: Math.min(4, unique.length) }, worker));assertCurrent(signal);return output}
 catch(error) {controller.abort();throw error}
 finally {parentSignal.removeEventListener("abort",abort)}
}
