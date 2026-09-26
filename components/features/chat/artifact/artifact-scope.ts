/** Conservative client-facing artifact filter until file provenance is recorded. */
const previewExtensions = new Set(["pdf", "html", "htm", "csv", "tsv", "xlsx", "xls", "png", "jpg", "jpeg", "webp"])
const internalNames = new Set(["AGENTS.md", "CLAUDE.md", "GEMINI.md", "crewship.md"])
const internalFolders = new Set(["runs", "attachments", ".agents", ".claude", ".codex", ".cursor", ".factory", ".gemini", ".opencode"])

export function isClientArtifactPath(relativePath: string): boolean {
  const parts = relativePath.replace(/^\/+/, "").split("/")
  const name = parts.at(-1) ?? ""
  const extension = name.split(".").at(-1)?.toLowerCase() ?? ""
  if (!previewExtensions.has(extension) || internalNames.has(name)) return false
  return parts.slice(0, -1).every((part) => part && !part.startsWith(".") && !internalFolders.has(part))
}
