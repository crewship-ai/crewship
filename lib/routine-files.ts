// routine-files — the files a routine runs, as the tree the card draws.
//
// The detail endpoint returns a flat `files` projection (contract §"Pipeline
// detail only"): every `steps[].script.path`, with language, the steps that
// use it, the header comment, size and whether it is on the crew share. The
// card draws it under /crew/shared/ as folders, the same shape the agent
// Files panel uses. An older server sends no `files`; the recipe's own script
// paths stand in, with presence unknown rather than claimed.

import { routineStepFiles } from "./routine-steps-layout"

export interface RoutineFile {
  path: string
  language: string
  interpreter?: string
  step_ids: string[]
  description?: string
  size_bytes?: number
  updated_at?: string
  /** false = declared but not on the share; undefined = the server did not say. */
  present?: boolean
  /**
   * What the share check established. "missing" only when the share was
   * listed and the path is not on it; "unverified" when it could not be
   * checked (crew container or volume unavailable, I/O budget spent). An
   * older server sends no status, which reads as unverified too.
   */
  status?: RoutineFileStatus
}
export type RoutineFileStatus = "present" | "missing" | "unverified"
export function routineFileStatus(file: Pick<RoutineFile, "present" | "status">): RoutineFileStatus {
  if (file.status === "present" || file.status === "missing" || file.status === "unverified") return file.status
  if (file.present === true) return "present"
  return "unverified"
}

export interface RoutineFileNode {
  path: string
  name: string
  is_dir: boolean
  children: RoutineFileNode[]
  /** Files under this folder, at any depth. */
  fileCount: number
  file?: RoutineFile
}

const LANGUAGES: Record<string, string> = {
  go: "go", py: "py", ts: "ts", tsx: "ts", js: "js", jsx: "js", sh: "sh", bash: "sh",
  yaml: "yaml", yml: "yaml", json: "json", md: "md", sql: "sql", rb: "rb", rs: "rs",
}

export const stripShareRoot = (path: string) => path.replace(/^\/?crew\/shared\//, "").replace(/^\/+/, "")

export function fileLanguage(path: string): string {
  const name = path.split("/").pop() ?? ""
  if (!name.includes(".")) return ""
  const ext = name.split(".").pop()?.toLowerCase() ?? ""
  return LANGUAGES[ext] ?? ext
}

export function formatFileSize(bytes: number | undefined): string {
  if (bytes == null || !Number.isFinite(bytes)) return ""
  if (bytes < 1024) return `${bytes} B`
  const kb = bytes / 1024
  if (kb < 1024) return `${kb.toFixed(1)} kB`
  return `${(kb / 1024).toFixed(1)} MB`
}

export function buildRoutineFileTree(files: RoutineFile[]): RoutineFileNode[] {
  const root: RoutineFileNode = { path: "", name: "", is_dir: true, children: [], fileCount: 0 }
  for (const file of files) {
    const clean = stripShareRoot(file.path)
    const parts = clean.split("/").filter(Boolean)
    if (!parts.length) continue
    let node = root
    node.fileCount += 1
    for (let i = 0; i < parts.length - 1; i += 1) {
      const dirPath = parts.slice(0, i + 1).join("/")
      let dir = node.children.find((c) => c.is_dir && c.path === dirPath)
      if (!dir) {
        dir = { path: dirPath, name: parts[i], is_dir: true, children: [], fileCount: 0 }
        node.children.push(dir)
      }
      dir.fileCount += 1
      node = dir
    }
    node.children.push({ path: clean, name: parts[parts.length - 1], is_dir: false, children: [], fileCount: 0, file: { ...file, path: clean } })
  }
  const sort = (nodes: RoutineFileNode[]) => {
    nodes.sort((a, b) => (a.is_dir !== b.is_dir ? (a.is_dir ? -1 : 1) : a.name.localeCompare(b.name)))
    nodes.forEach((n) => sort(n.children))
  }
  sort(root.children)
  return root.children
}

/** The recipe's script paths when the server sent no `files`. */
export function routineFilesFromDefinition(definition: unknown): RoutineFile[] {
  const dsl = definition && typeof definition === "object" ? (definition as Record<string, unknown>) : {}
  const interpreters = new Map<string, string>()
  const visit = (steps: unknown) => {
    if (!Array.isArray(steps)) return
    for (const s of steps) {
      if (!s || typeof s !== "object") continue
      const step = s as Record<string, unknown>
      const script = step.script as Record<string, unknown> | undefined
      if (script && typeof script.path === "string" && typeof script.interpreter === "string")
        interpreters.set(stripShareRoot(script.path), script.interpreter)
      const loop = step.foreach as Record<string, unknown> | undefined
      if (loop) visit(loop.steps)
    }
  }
  visit(dsl.steps)
  return routineStepFiles(dsl).map(({ path, step_ids }) => ({
    path,
    language: fileLanguage(path),
    interpreter: interpreters.get(path),
    step_ids,
    present: undefined,
    status: "unverified",
  }))
}
