/** The static preview accepts local, canonical base64 PNG/JPEG containers only.
 * It never resolves a URL. Bounded native decoding follows before any preview frame receives the source. */
export const STATIC_RASTER_LIMITS = {
  sourceBytes: 10 * 1024 * 1024,
  imageBytes: 1024 * 1024,
  totalBytes: 4 * 1024 * 1024,
  dimension: 2048,
  totalPixels: 8 * 1024 * 1024,
  images: 32,
} as const

export interface StaticRaster { src: string; bytes: number; width: number; height: number }

function dimensions(width: number, height: number): boolean {
  return width > 0 && height > 0 && width <= STATIC_RASTER_LIMITS.dimension && height <= STATIC_RASTER_LIMITS.dimension
}

const crcTable = Array.from({ length: 256 }, (_, n) => {
  let crc = n
  for (let bit = 0; bit < 8; bit++) crc = (crc >>> 1) ^ (crc & 1 ? 0xedb88320 : 0)
  return crc >>> 0
})
function crc32(data: Uint8Array, from: number, to: number): number {
  let crc = 0xffffffff
  for (let i = from; i < to; i++) crc = (crc >>> 8) ^ crcTable[(crc ^ data[i]) & 255]
  return (crc ^ 0xffffffff) >>> 0
}

function png(data: Uint8Array): { width: number; height: number } | null {
  if (data.length < 57 || ![137, 80, 78, 71, 13, 10, 26, 10].every((byte, i) => data[i] === byte)) return null
  const view = new DataView(data.buffer, data.byteOffset, data.byteLength)
  let pos = 8, width = 0, height = 0, idat = false, endedData = false, palette = false, indexed = false
  while (pos + 12 <= data.length) {
    const length = view.getUint32(pos)
    if (length > data.length - pos - 12) return null
    const type = String.fromCharCode(...data.subarray(pos + 4, pos + 8))
    if (!/^[A-Za-z]{4}$/.test(type) || crc32(data, pos + 4, pos + 8 + length) !== view.getUint32(pos + 8 + length)) return null
    if (pos === 8) {
      if (type !== 'IHDR' || length !== 13) return null
      width = view.getUint32(pos + 8); height = view.getUint32(pos + 12)
      const depth = data[pos + 16], color = data[pos + 17]
      // Bound decoded memory too: no 16-bit channels in this first profile.
      const allowedDepths: Record<number, number[]> = { 0: [1, 2, 4, 8], 2: [8], 3: [1, 2, 4, 8], 4: [8], 6: [8] }
      if (!dimensions(width, height) || !allowedDepths[color]?.includes(depth) || data[pos + 18] !== 0 || data[pos + 19] !== 0 || data[pos + 20] > 1) return null
      indexed = color === 3
    } else if (type === 'IHDR' || ['acTL', 'fcTL', 'fdAT'].includes(type)) return null
    else if (type === 'PLTE') {
      if (idat || palette || !length || length > 768 || length % 3 !== 0) return null
      palette = true
    } else if (type === 'IDAT') {
      if (endedData || (indexed && !palette)) return null
      idat = true
    } else if (type === 'IEND') {
      return length === 0 && idat && pos + 12 === data.length ? { width, height } : null
    } else {
      if (idat) endedData = true
      // Unknown critical chunks must not change decoder interpretation.
      if (type[0] === type[0].toUpperCase()) return null
    }
    pos += length + 12
  }
  return null
}

function jpeg(data: Uint8Array): { width: number; height: number } | null {
  if (data.length < 15 || data[0] !== 255 || data[1] !== 216) return null
  let pos = 2, width = 0, height = 0, scan = false
  while (pos < data.length) {
    if (data[pos++] !== 255) return null
    while (data[pos] === 255) pos++
    const marker = data[pos++]
    if (marker === 217) return scan && width > 0 && pos === data.length ? { width, height } : null
    if (marker === undefined || marker === 0 || marker === 216 || marker === 1 || (marker >= 208 && marker <= 215) || pos + 2 > data.length) return null
    const length = (data[pos] << 8) | data[pos + 1]
    if (length < 2 || pos + length > data.length) return null
    if (marker >= 192 && marker <= 207 && ![196, 200, 204].includes(marker)) {
      // Baseline/progressive 8-bit JPEG only; reject secondary frame headers.
      if (![192, 194].includes(marker) || width || length < 8 || data[pos + 2] !== 8) return null
      height = (data[pos + 3] << 8) | data[pos + 4]
      width = (data[pos + 5] << 8) | data[pos + 6]
      const components = data[pos + 7]
      if (!dimensions(width, height) || ![1, 3, 4].includes(components) || length !== 8 + 3 * components) return null
    }
    if (marker === 220) return null // DNL must not redefine the bounded height.
    pos += length
    if (marker === 218) {
      if (!width) return null
      scan = true
      // Skip entropy-coded bytes, preserving the next real marker for parsing.
      while (pos < data.length) {
        if (data[pos] !== 255) { pos++; continue }
        if (data[pos + 1] === 0 || (data[pos + 1] >= 208 && data[pos + 1] <= 215)) { pos += 2; continue }
        break
      }
    }
  }
  return null
}

export function staticRaster(src: string): StaticRaster | null {
  const match = /^data:image\/(png|jpeg);base64,([A-Za-z0-9+/]*={0,2})$/.exec(src)
  if (!match || !match[2] || match[2].length % 4 || match[2].length > 4 * Math.ceil(STATIC_RASTER_LIMITS.imageBytes / 3)) return null
  try {
    const binary = atob(match[2])
    if (binary.length > STATIC_RASTER_LIMITS.imageBytes || btoa(binary) !== match[2]) return null
    const data = Uint8Array.from(binary, char => char.charCodeAt(0))
    const size = match[1] === 'png' ? png(data) : jpeg(data)
    return size ? { src, bytes: data.length, ...size } : null
  } catch { return null }
}

/** Preserve static responsive/animation rules, but refuse encoded keywords,
 * loading functions and every other at-rule, including nested imports.
 * CSP independently remains the external network boundary. */
export function staticCSS(css: string): boolean {
  const text = css.replace(/\/\*[\s\S]*?\*\//g, '')
  if (/\\/.test(text) || /(?:url|image|image-set|cross-fade|paint|src)\s*\(/i.test(text)) return false
  const rules = text.match(/@[a-z-]+/gi) ?? []
  if (text.replace(/@[a-z-]+/gi, '').includes('@')) return false
  return rules.every(rule => ['@media', '@supports', '@layer', '@keyframes', '@-webkit-keyframes'].includes(rule.toLowerCase()))
}
