/** Size helpers for a native Sheet document on its way to the server.
 *
 * The engine exports indented JSON (a real 235 000 cell workbook is 80 MB that
 * way and 30 MB compact). The host compacts it before it hashes, validates,
 * stores a draft or sends it, so one form of the document is hashed on both
 * sides. The scan yields to the event loop between slices so the editor stays
 * responsive; gzip runs in the browser's own stream, off the main thread. */

const SLICE_BYTES = 4 << 20

/** Removes the whitespace outside JSON strings. Input must be UTF-8 JSON. */
export async function compactNativeJson(input: Uint8Array, signal?: AbortSignal, sliceBytes = SLICE_BYTES): Promise<Uint8Array> {
  const out = new Uint8Array(input.length)
  let o = 0, inString = false, escaped = false, next = sliceBytes
  for (let i = 0; i < input.length; i++) {
    const c = input[i]
    if (inString) {
      out[o++] = c
      if (escaped) escaped = false
      else if (c === 0x5c) escaped = true
      else if (c === 0x22) inString = false
    } else if (c === 0x22) {
      inString = true
      out[o++] = c
    } else if (c !== 0x20 && c !== 0x0a && c !== 0x0d && c !== 0x09) {
      out[o++] = c
    }
    if (i + 1 === next) {
      next += sliceBytes
      signal?.throwIfAborted()
      await new Promise<void>(resolve => setTimeout(resolve, 0))
    }
  }
  return out.slice(0, o)
}

/** Bodies under this size are sent as they are. */
export const GZIP_THRESHOLD = 1 << 20

/** gzip of data, or undefined where the browser has no CompressionStream. */
export async function gzipBytes(data: Uint8Array): Promise<Uint8Array | undefined> {
  if (typeof CompressionStream === "undefined") return undefined
  const stream = new Blob([data as BlobPart]).stream().pipeThrough(new CompressionStream("gzip"))
  return new Uint8Array(await new Response(stream).arrayBuffer())
}

/** gzip of a Blob (no copy of its bytes in script memory), or undefined without CompressionStream. */
export async function gzipBlob(blob: Blob): Promise<Uint8Array | undefined> {
  if (typeof CompressionStream === "undefined") return undefined
  return new Uint8Array(await new Response(blob.stream().pipeThrough(new CompressionStream("gzip"))).arrayBuffer())
}

/** Base64 of bytes as a list of string parts of about 4 MiB, so no single
 * 40 MB string (nor a JSON.stringify copy of it) is ever built. */
export function base64Parts(bytes: Uint8Array): string[] {
  const parts: string[] = []
  const slice = 3 * (1 << 20) // a multiple of 3: parts concatenate to the plain base64
  for (let at = 0; at < bytes.length; at += slice) {
    const part = bytes.subarray(at, Math.min(at + slice, bytes.length))
    let binary = ""
    for (let i = 0; i < part.length; i += 30000) binary += String.fromCharCode(...part.subarray(i, i + 30000))
    parts.push(btoa(binary))
  }
  return parts
}
