import { apiFetch } from '@/lib/api-fetch'

export const MAX_PREVIEW_BYTES = 20 * 1024 * 1024
export type PreviewMime = 'application/pdf' | 'image/png' | 'image/jpeg' | 'image/webp'

/** Never infer active content from an extension or an untrusted server MIME. */
export function previewMime(bytes: Uint8Array): PreviewMime | null {
  const starts = (signature: number[]) => signature.every((byte, i) => bytes[i] === byte)
  if (starts([0x25, 0x50, 0x44, 0x46, 0x2d])) return 'application/pdf'
  if (starts([137, 80, 78, 71, 13, 10, 26, 10])) return 'image/png'
  if (starts([0xff, 0xd8, 0xff])) return 'image/jpeg'
  if (starts([82, 73, 70, 70]) && [87, 69, 66, 80].every((b, i) => bytes[i + 8] === b)) return 'image/webp'
  return null
}

export async function readPreviewBytes(url: string, signal: AbortSignal): Promise<Uint8Array<ArrayBuffer>> {
  const response = await apiFetch(url, { signal })
  if (!response.ok) throw new Error(`Could not load file (${response.status}).`)
  const tooLarge = () => new Error('This file exceeds the 20 MiB preview limit. Download it to open locally.')
  if (Number(response.headers.get('content-length')) > MAX_PREVIEW_BYTES) {
    await response.body?.cancel()
    throw tooLarge()
  }
  if (!response.body) throw new Error('File content is unavailable.')
  const reader = response.body.getReader()
  const chunks: Uint8Array[] = []
  let length = 0
  try {
    while (true) {
      signal.throwIfAborted()
      const { done, value } = await reader.read()
      if (done) break
      length += value.byteLength
      if (length > MAX_PREVIEW_BYTES) {
        await reader.cancel()
        throw tooLarge()
      }
      chunks.push(value)
    }
  } finally { reader.releaseLock() }
  signal.throwIfAborted()
  const result = new Uint8Array(length)
  let offset = 0
  for (const chunk of chunks) { result.set(chunk, offset); offset += chunk.length }
  return result
}
