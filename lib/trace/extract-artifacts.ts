import type { StepKind } from "./types"

// Artifact — one openable thing produced by a step. The Files tab in
// the side panel lists these. v1 covers JSON responses and inferred
// file paths; richer kinds (binary downloads, image previews) come
// when the executor persists artifacts to a structured table.

export type ArtifactKind = "json" | "file_ref" | "text"

export interface Artifact {
  name: string
  kind: ArtifactKind
  // Truncated preview shown inline before the user opens the artifact.
  preview: string
  // For JSON artifacts: the parsed value, ready for the JSONViewer.
  // For file_ref: the inferred path string.
  // For text: the raw content.
  content: unknown
}

// Match leading "./", "/" or word.ext-style filename references in
// stdout / agent output. Captures common code/asset/doc extensions.
// We deliberately keep this narrow — broader matches catch too many
// false positives (versions, IDs, etc).
const FILE_REF_RE =
  /(?:^|\s)((?:[./]|[A-Za-z]:[\\/])?[\w./-]+\.(?:ts|tsx|js|jsx|go|py|rs|java|kt|swift|md|json|yaml|yml|toml|html|css|scss|sh|sql|txt|csv|xml|svg|png|jpg|jpeg|gif|pdf|env))\b/g

// `<file path="...">` markup that some agent prompts/responses emit.
// Pulled from the Crewship handoff_context patterns we've seen in
// real runs.
const FILE_TAG_RE = /<file\s+path=["']([^"']+)["']/g

// extractArtifacts — derive a flat list of openable artifacts from a
// step's output. Pure function, no fetch.
//
// Heuristics by step kind:
//   - http     : try JSON.parse(output) → JSON artifact
//   - code     : regex stdout for file paths → file_ref artifacts
//   - agent_run: same regex + <file path="..."> tags
//   - transform: try JSON.parse → JSON artifact (transforms usually
//                emit reshaped JSON)
//   - others   : nothing
//
// Cap at 12 artifacts per step to keep the panel quick. Anything
// beyond that is almost always noise (e.g. a long ls -la in stdout).
export function extractArtifacts(kind: StepKind, output: unknown): Artifact[] {
  if (output === undefined || output === null || output === "") return []

  const out: Artifact[] = []
  const seen = new Set<string>()
  const push = (a: Artifact) => {
    if (out.length >= 12) return
    const key = `${a.kind}:${a.name}`
    if (seen.has(key)) return
    seen.add(key)
    out.push(a)
  }

  // JSON detection — common to http and transform.
  if (kind === "http" || kind === "transform") {
    const json = tryParseJSON(output)
    if (json !== undefined) {
      const summary = summarize(json)
      push({
        name: kind === "http" ? "response.json" : "result.json",
        kind: "json",
        preview: summary,
        content: json,
      })
    }
  }

  // File path inference — code stdout + agent_run text output.
  if (kind === "code" || kind === "agent_run") {
    const text = typeof output === "string" ? output : JSON.stringify(output)

    FILE_REF_RE.lastIndex = 0
    let m: RegExpExecArray | null
    while ((m = FILE_REF_RE.exec(text)) !== null) {
      const path = m[1].trim()
      if (path.length < 3 || path.length > 256) continue
      push({
        name: path,
        kind: "file_ref",
        preview: path,
        content: path,
      })
    }

    if (kind === "agent_run") {
      FILE_TAG_RE.lastIndex = 0
      while ((m = FILE_TAG_RE.exec(text)) !== null) {
        const path = m[1]
        push({
          name: path,
          kind: "file_ref",
          preview: path,
          content: path,
        })
      }
    }
  }

  return out
}

function tryParseJSON(value: unknown): unknown | undefined {
  if (value && typeof value === "object") return value
  if (typeof value !== "string") return undefined
  const t = value.trim()
  if (!(t.startsWith("{") || t.startsWith("["))) return undefined
  try {
    return JSON.parse(t)
  } catch {
    return undefined
  }
}

function summarize(value: unknown): string {
  if (Array.isArray(value)) {
    return `array · ${value.length} item${value.length === 1 ? "" : "s"}`
  }
  if (value && typeof value === "object") {
    const keys = Object.keys(value as Record<string, unknown>)
    if (keys.length === 0) return "{} (empty object)"
    return `object · ${keys.length} field${keys.length === 1 ? "" : "s"} · ${keys
      .slice(0, 3)
      .join(", ")}${keys.length > 3 ? ", …" : ""}`
  }
  const s = String(value)
  return s.length > 80 ? s.slice(0, 79) + "…" : s
}
