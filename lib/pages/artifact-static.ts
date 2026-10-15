import DOMPurify from "dompurify"
import { STATIC_RASTER_LIMITS, staticCSS, staticRaster, validStaticPNG } from "./artifact-raster"

/** Static presentation only. Original source is never inserted into Studio. */
export async function staticArtifactDocument(html: string, signal?: AbortSignal): Promise<string> {
  if (signal?.aborted) throw new DOMException("Preview replaced", "AbortError")
  let body: string
  if (html.length > STATIC_RASTER_LIMITS.sourceBytes || new TextEncoder().encode(html).length > STATIC_RASTER_LIMITS.sourceBytes) {
    body = '<p>Static preview exceeds the 10 MiB source limit. Download the original source to inspect it.</p>'
  } else if (!DOMPurify.isSupported) {
    // DOMPurify returns input unchanged in unsupported DOMs. Fail closed.
    body = `<p>Static rendering is unavailable in this browser. Source is shown as text.</p><pre>${html.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;").replace(/"/g, "&quot;")}</pre>`
  } else {
    // Per-call purifier: image budgets and hooks never leak to other sanitizers.
    const purifier = DOMPurify(window)
    let bytes = 0, pixels = 0, images = 0
    purifier.addHook('uponSanitizeAttribute', (node, attribute) => {
      if (attribute.attrName === 'src') {
        const image = node.nodeName === 'IMG' ? staticRaster(attribute.attrValue) : null
        if (!image || images >= STATIC_RASTER_LIMITS.images || bytes + image.bytes > STATIC_RASTER_LIMITS.totalBytes || pixels + image.width * image.height > STATIC_RASTER_LIMITS.totalPixels) attribute.keepAttr = false
        else { images++; bytes += image.bytes; pixels += image.width * image.height }
      }
      if (attribute.attrName === 'style' && !staticCSS(attribute.attrValue)) attribute.keepAttr = false
    })
    purifier.addHook('uponSanitizeElement', node => {
      if (node.nodeName === 'STYLE' && !staticCSS(node.textContent ?? '')) node.textContent = ''
    })
    const fragment = purifier.sanitize(html, {
      RETURN_DOM_FRAGMENT: true,
      FORCE_BODY: true,
      USE_PROFILES: { html: true },
      ADD_TAGS: ["style"],
      FORBID_TAGS: ["template", "noscript", "script", "iframe", "frame", "frameset", "object", "embed", "meta", "base", "link", "form", "input", "button", "textarea", "select", "option", "video", "audio", "source", "track", "svg", "math"],
      FORBID_ATTR: ["href", "xlink:href", "srcset", "action", "formaction", "target", "download", "ping", "background", "poster"],
      ALLOW_DATA_ATTR: false,
    })
    // Header/container validation bounds memory before the native decoder sees
    // compressed bytes. Never publish an IMG src based on metadata alone.
    for (const node of fragment.querySelectorAll('img[src]')) {
      if (signal?.aborted) throw new DOMException('Preview replaced', 'AbortError')
      const src = node.getAttribute('src')!
      const image = staticRaster(src)!
      let bitmap: ImageBitmap | undefined
      try {
        if (typeof createImageBitmap !== 'function') throw new Error('Raster decoder unavailable')
        const data = Uint8Array.from(atob(src.slice(src.indexOf(',') + 1)), char => char.charCodeAt(0))
        if (src.startsWith('data:image/png;') && !await validStaticPNG(data, signal)) throw new Error('Invalid PNG scanlines')
        // Native decoders may apply JPEG EXIF orientation (including 90°).
        bitmap = await createImageBitmap(new Blob([data]))
        if (!((bitmap.width === image.width && bitmap.height === image.height) || (bitmap.width === image.height && bitmap.height === image.width))) throw new Error('Raster dimensions changed')
      } catch {
        node.removeAttribute('src')
      } finally { bitmap?.close() }
    }
    if (signal?.aborted) throw new DOMException('Preview replaced', 'AbortError')
    const holder = document.createElement('div')
    holder.append(fragment)
    body = holder.innerHTML
  }
  // data: is permitted only here, after the IMG-only PNG/JPEG validation above.
  const policy = "default-src 'none'; script-src 'none'; style-src 'unsafe-inline'; img-src data:; font-src 'none'; media-src 'none'; connect-src 'none'; frame-src 'none'; object-src 'none'; form-action 'none'; base-uri 'none'"
  return `<!doctype html><html><head><meta http-equiv="Content-Security-Policy" content="${policy}"><meta name="referrer" content="no-referrer"><style>body{font-family:system-ui,sans-serif;margin:24px;overflow-wrap:anywhere}img{max-width:100%}table{border-collapse:collapse}td,th{padding:8px;border:1px solid #ddd}</style></head><body>${body}</body></html>`
}
