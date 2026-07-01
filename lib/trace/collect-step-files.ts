import { extractArtifacts, type ArtifactKind } from "./extract-artifacts"
import type { StepKind, SubSpan, SubSpanKind } from "./types"

// collectStepFiles — the data behind the side panel's Files tab.
//
// A step's files come from two places:
//   1. ACTIONS — sub-spans carrying `attributes.artifact_path`: the
//      concrete files the agent read/wrote while running the step.
//      These are real on-disk paths in the agent's output dir, so the
//      viewer can download them from the agent files endpoint.
//   2. OUTPUT — paths/JSON inferred from the step's text output via
//      extractArtifacts (file refs in stdout, `<file path="…">` tags,
//      a JSON http/transform response). File refs are fetchable; inline
//      JSON/text artifacts have no on-disk path and carry their content.
//
// Pure + deterministic so it's unit-testable and replay-stable. No fetch.

export interface StepFileTouch {
  kind: SubSpanKind
  name: string
}

export interface StepFile {
  // Full path (action) or inferred ref (output). For inline artifacts
  // this is the synthetic name (e.g. "response.json").
  path: string
  // Basename for display.
  name: string
  // "action" → a sub-span wrote/read it. "output" → inferred from output.
  source: "action" | "output"
  // Which sub-span actions touched it (action source only).
  touchedBy: StepFileTouch[]
  // Downloadable from the agent files endpoint? Real paths → yes; inline
  // JSON/text artifacts → no (content carried below).
  fetchable: boolean
  // Inline (non-fetchable) artifact payload — rendered without a fetch.
  inlineContent?: unknown
  inlineKind?: ArtifactKind
}

const MAX_FILES = 24

export function basename(path: string): string {
  const parts = path.split(/[\\/]/).filter(Boolean)
  return parts[parts.length - 1] || path
}

// collectStepFiles — merge action artifact paths + output-inferred
// artifacts into one deduped, action-first list.
export function collectStepFiles(
  subSpans: SubSpan[] | undefined,
  stepType: StepKind,
  output: unknown,
): StepFile[] {
  const byPath = new Map<string, StepFile>()
  const order: string[] = []

  const ensure = (key: string, make: () => StepFile): StepFile => {
    let f = byPath.get(key)
    if (!f) {
      f = make()
      byPath.set(key, f)
      order.push(key)
    }
    return f
  }

  // 1) Action artifacts — these win (fetchable, carry provenance).
  for (const span of subSpans ?? []) {
    const raw = span.attributes?.artifact_path
    if (typeof raw !== "string") continue
    const path = raw.trim()
    if (!path) continue
    const f = ensure(path, () => ({
      path,
      name: basename(path),
      source: "action",
      touchedBy: [],
      fetchable: true,
    }))
    // Dedupe identical (kind,name) touches so a step that re-reads the
    // same file ten times shows one provenance chip, not ten.
    if (!f.touchedBy.some((t) => t.kind === span.kind && t.name === span.name)) {
      f.touchedBy.push({ kind: span.kind, name: span.name })
    }
  }

  // 2) Output-inferred artifacts.
  for (const a of extractArtifacts(stepType, output)) {
    if (a.kind === "file_ref") {
      const path = String(a.content).trim()
      if (!path) continue
      // Don't clobber an action entry — that one has provenance + is
      // already known fetchable.
      ensure(path, () => ({
        path,
        name: basename(path),
        source: "output",
        touchedBy: [],
        fetchable: true,
      }))
    } else {
      // json / text — inline, keyed by synthetic name so it can't
      // collide with a real path.
      const key = `inline:${a.kind}:${a.name}`
      ensure(key, () => ({
        path: a.name,
        name: a.name,
        source: "output",
        touchedBy: [],
        fetchable: false,
        inlineContent: a.content,
        inlineKind: a.kind,
      }))
    }
  }

  return order.slice(0, MAX_FILES).map((k) => byPath.get(k) as StepFile)
}
