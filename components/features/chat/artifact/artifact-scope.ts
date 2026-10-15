/** Presentation filter for paths from the authorized agent output listing. */
const previewExtensions = new Set(["pdf", "html", "htm", "csv", "tsv", "xlsx", "xls", "xlsm", "png", "jpg", "jpeg", "webp"])
const internalNames = new Set(["AGENTS.md", "CLAUDE.md", "GEMINI.md", "crewship.md"])
const internalFolders = new Set(["runs", "attachments", ".agents", ".claude", ".codex", ".cursor", ".factory", ".gemini", ".opencode"])

export function isClientArtifactPath(relativePath: string): boolean {
  const parts = relativePath.replace(/^\/+/, "").split("/")
  const name = parts.at(-1) ?? ""
  const extension = name.split(".").at(-1)?.toLowerCase() ?? ""
  if (!previewExtensions.has(extension) || internalNames.has(name)) return false
  let folders = parts.slice(0, -1)
  // Only the canonical output-root prefix is published run output:
  // /output/<slug>/runs/<runID>/... (run_paths.go). HOME/config lives in
  // /crew/runs/<slug>/<runID>, outside this listing. Match ValidRunID's bound.
  if (folders[0] === "runs") {
    if (folders.length < 2 || !/^[A-Za-z0-9_-]{1,80}$/.test(folders[1]) || internalFolders.has(folders[1])) return false
    folders = folders.slice(2)
  }
  return folders.every((part) => part && !part.startsWith(".") && !internalFolders.has(part))
}
