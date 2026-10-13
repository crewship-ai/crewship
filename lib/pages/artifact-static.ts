import DOMPurify from "dompurify"

/** Static presentation only. Original source is never inserted into Studio. */
export function staticArtifactDocument(html: string): string {
  // DOMPurify intentionally returns its input unchanged in unsupported DOMs.
  // Never let that upstream compatibility behavior become an HTML bypass.
  const body = !DOMPurify.isSupported
    ? `<p>Static rendering is unavailable in this browser. Source is shown as text.</p><pre>${html.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;").replace(/"/g, "&quot;")}</pre>`
    : DOMPurify.sanitize(html, {
    FORCE_BODY: true,
    USE_PROFILES: { html: true },
    ADD_TAGS: ["style"],
    FORBID_TAGS: ["script", "iframe", "frame", "frameset", "object", "embed", "meta", "base", "link", "form", "input", "button", "textarea", "select", "option", "video", "audio", "source", "track", "svg", "math"],
    FORBID_ATTR: ["href", "xlink:href", "src", "srcset", "action", "formaction", "target", "download", "ping", "background", "poster"],
    ALLOW_DATA_ATTR: false,
  })
  const policy = "default-src 'none'; script-src 'none'; style-src 'unsafe-inline'; img-src 'none'; font-src 'none'; media-src 'none'; connect-src 'none'; frame-src 'none'; object-src 'none'; form-action 'none'; base-uri 'none'"
  return `<!doctype html><html><head><meta http-equiv="Content-Security-Policy" content="${policy}"><meta name="referrer" content="no-referrer"><style>body{font-family:system-ui,sans-serif;margin:24px;overflow-wrap:anywhere}img{max-width:100%}table{border-collapse:collapse}td,th{padding:8px;border:1px solid #ddd}</style></head><body>${body}</body></html>`
}
