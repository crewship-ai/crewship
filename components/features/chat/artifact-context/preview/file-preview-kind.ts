export type PreviewKind = "table" | "text" | "code" | "image" | "pdf" | "none"

const TABLE = new Set(["text/csv", "text/tab-separated-values"])
const IMAGE = new Set(["image/png", "image/jpeg", "image/gif", "image/webp", "image/avif"])
const CODE_EXT = /\.(ts|tsx|js|jsx|py|go|rs|java|rb|sh|sql|json|ya?ml|toml|css|html?|xml)$/i

/** Chooses a preview from the server-verified MIME. SVG and unknown binaries are never rendered inline. */
export function previewKind(mime: string, name: string): PreviewKind {
  const type = mime.split(";")[0].trim().toLowerCase()
  if (TABLE.has(type)) return "table"
  if (IMAGE.has(type)) return "image"
  if (type === "application/pdf") return "pdf"
  if (type === "text/plain" || type === "text/markdown") return CODE_EXT.test(name) ? "code" : "text"
  if (type === "application/json" || type.startsWith("text/x-") || type === "application/javascript") return "code"
  return "none"
}

export interface FileActions { open: boolean; download: boolean }

/** Only actions the server granted for this reader; no invented defaults. */
export function fileActions(granted: { open?: boolean; download?: boolean }, kind: PreviewKind): FileActions {
  return { open: granted.open === true && kind !== "none", download: granted.download === true }
}
