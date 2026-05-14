export function fmtSize(bytes: number): string {
  if (!bytes) return "—"
  const units = ["B", "KB", "MB", "GB"]
  const i = Math.floor(Math.log(bytes) / Math.log(1024))
  const v = bytes / Math.pow(1024, i)
  return `${v < 10 ? v.toFixed(1) : Math.round(v)} ${units[i]}`
}

export function fmtTime(modTime: string): string {
  const mins = Math.floor((Date.now() - new Date(modTime).getTime()) / 60000)
  if (mins < 1) return "Just now"
  if (mins < 60) return `${mins}m ago`
  const hrs = Math.floor(mins / 60)
  if (hrs < 24) return `${hrs}h ago`
  const days = Math.floor(hrs / 24)
  if (days === 1) return "Yesterday"
  if (days < 7) return `${days}d ago`
  return new Date(modTime).toLocaleDateString()
}

export function getLang(name: string): string {
  const ext = name.split(".").pop()?.toLowerCase() ?? ""
  const map: Record<string, string> = {
    ts: "typescript", tsx: "tsx", js: "javascript", jsx: "jsx",
    py: "python", go: "go", rs: "rust", sh: "bash",
    json: "json", yaml: "yaml", yml: "yaml", xml: "xml",
    html: "html", css: "css", md: "markdown", txt: "text",
    sql: "sql", toml: "toml", env: "bash",
  }
  return map[ext] ?? "text"
}

const PREVIEWABLE_EXTENSIONS = new Set([
  "txt", "md", "mdx", "py", "js", "jsx", "ts", "tsx", "go", "rs", "rb",
  "sh", "bash", "zsh", "fish", "bat", "ps1",
  "json", "yaml", "yml", "toml", "xml", "csv", "ini", "cfg", "log",
  "html", "css", "scss", "less", "svg",
  "sql", "graphql", "prisma",
  "gitignore", "gitattributes", "editorconfig", "prettierrc",
  "dockerfile", "makefile", "cmakelists",
  "c", "cpp", "h", "hpp", "java", "kt", "swift", "dart", "lua", "r",
  "tf", "hcl", "proto",
])

const PREVIEWABLE_FILENAMES = new Set([
  "dockerfile", "makefile", "cmakelists.txt", ".gitignore",
  ".gitattributes", ".editorconfig", ".prettierrc", ".eslintrc",
  "license", "readme", "changelog", "authors",
])

export function isPreviewable(name: string): boolean {
  const n = name.toLowerCase()
  if (PREVIEWABLE_FILENAMES.has(n)) return true
  const ext = n.split(".").pop() ?? ""
  return PREVIEWABLE_EXTENSIONS.has(ext)
}
