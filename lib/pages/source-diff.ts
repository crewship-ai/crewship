import type { SourceDiff, SourceDiffLine, SourceFileChange, SourceFileStatus } from "@/lib/pages/editor-contract"
import { SOURCE_DIFF_LIMITS } from "@/lib/pages/editor-contract"

/**
 * Comparison of two portable source projects (`internal/pages/project.go`).
 *
 * Everything here is derived from the two projects and nothing else: a review
 * screen that trusted a candidate's own account of what it changed would be
 * reading marketing. The module is pure and synchronous, never renders bytes it
 * could not decode, and never produces HTML — the consumer renders text.
 *
 * The line engine also serves `definition-diff.ts`. It lives here because this
 * is the side with the hard requirements (limits, degradation, counts); a
 * second implementation over there would be a second thing to get wrong.
 */

/** The shape both sides arrive in. `encoding` absent means the content is the text. */
export interface SourceProjectLike {
  files: Array<{ path: string; encoding?: string; content: string }>
}

interface SourceFileLike {
  path: string
  encoding?: string
  content: string
}

/**
 * The O(ND) core is bounded twice. `MAX_EDITS` is the edit budget — past it the
 * changed region is emitted as a wholesale replace, which is truthful (from the
 * renderer's point of view those lines really are all different) and is flagged
 * `truncated`. It is deliberately the render ceiling: a file with more edits
 * than that could not have been shown in full anyway.
 */
const MAX_EDITS = SOURCE_DIFF_LIMITS.linesPerFile

/**
 * And past this many lines in the *changed* region (common prefix and suffix
 * already trimmed, since that is what the quadratic part actually sees) the
 * algorithm is not started at all.
 */
const MAX_DIFF_LINES = 20_000

/** Unified-diff context, the universal three. */
const CONTEXT = 3

/** A NUL anywhere means "not text" — the same test `ProjectFile.Bytes` applies. */
const NUL = "\u0000"

const UTF8_ENCODINGS = new Set(["", "utf8", "utf-8"])

/**
 * Decode one project file for display.
 *
 * `text` is null when the bytes are not renderable as text — invalid base64,
 * invalid UTF-8, or a NUL byte anywhere. Such a file is described by its status
 * and never by its contents: handing undecodable bytes to a renderer is how a
 * viewer ends up driven by the file it was supposed to be reviewing.
 */
export function decodeSourceFile(file: { encoding?: string; content: string }): { text: string | null; bytes: number } {
  if (typeof file !== "object" || file === null) return { text: null, bytes: 0 }
  const content = typeof file.content === "string" ? file.content : ""
  const encoding = typeof file.encoding === "string" ? file.encoding.trim().toLowerCase() : ""
  if (encoding === "base64") {
    const bytes = decodeBase64(content)
    // An undecodable payload still has a size worth reporting, so the reviewer
    // sees "a 40 KiB file changed" rather than an empty row.
    if (bytes === null) return { text: null, bytes: Math.floor((content.replace(/[^A-Za-z0-9+/]/g, "").length * 3) / 4) }
    let text: string | null = null
    try {
      text = new TextDecoder("utf-8", { fatal: true }).decode(bytes)
    } catch {
      text = null
    }
    if (text !== null && text.includes(NUL)) text = null
    return { text, bytes: bytes.length }
  }
  const bytes = new TextEncoder().encode(content).length
  // An encoding the transport does not define is not guessed at.
  if (!UTF8_ENCODINGS.has(encoding)) return { text: null, bytes }
  return { text: content.includes(NUL) ? null : content, bytes }
}

function decodeBase64(content: string): Uint8Array | null {
  try {
    const binary = atob(content)
    const bytes = new Uint8Array(binary.length)
    for (let i = 0; i < binary.length; i++) bytes[i] = binary.charCodeAt(i) & 0xff
    return bytes
  } catch {
    return null
  }
}

/**
 * Compare two source projects.
 *
 * A rename is a removal plus an addition. Similarity heuristics guess, and a
 * wrong guess hides a rewrite inside something that reads as a move.
 */
export function compareSources(before: SourceProjectLike | null, after: SourceProjectLike | null): SourceDiff {
  const left = indexFiles(before)
  const right = indexFiles(after)
  const paths = [...new Set([...left.keys(), ...right.keys()])].sort()

  const changed: Array<{ path: string; status: SourceFileStatus; old: SourceFileLike | null; next: SourceFileLike | null }> = []
  for (const path of paths) {
    const old = left.get(path) ?? null
    const next = right.get(path) ?? null
    if (old && next && sameFile(old, next)) continue // an unchanged file is not part of a review
    changed.push({ path, status: !old ? "added" : !next ? "removed" : "modified", old, next })
  }

  const files: SourceFileChange[] = []
  let bodies = 0
  let truncated = false
  for (const entry of changed) {
    const oldSide = entry.old ? decodeSourceFile(entry.old) : { text: "", bytes: 0 }
    const newSide = entry.next ? decodeSourceFile(entry.next) : { text: "", bytes: 0 }
    const binary = oldSide.text === null || newSide.text === null
    const oversize = Math.max(oldSide.bytes, newSide.bytes) > SOURCE_DIFF_LIMITS.bytesPerFile

    if (binary) {
      // The change is stated; the bytes are neither counted as lines nor shown.
      files.push({ path: entry.path, status: entry.status, added: 0, removed: 0, binary: true, lines: [], truncated: false })
      continue
    }
    const oldLines = splitLines(oldSide.text ?? "")
    const newLines = splitLines(newSide.text ?? "")

    if (oversize) {
      // Listed, never line-diffed. The counts are the whole-file replace the
      // renderer would have had to produce — an upper bound, because a count
      // that understated the change would read as "less than this happened".
      files.push({
        path: entry.path,
        status: entry.status,
        added: newLines.length,
        removed: oldLines.length,
        binary: false,
        lines: [],
        truncated: true,
      })
      truncated = true
      continue
    }

    const diff = diffLines(oldLines, newLines)
    // Only a file that could have had a body spends the body budget; a binary
    // or oversized file was never going to render one.
    const withinBudget = bodies < SOURCE_DIFF_LIMITS.filesWithBodies
    if (withinBudget) bodies++
    const rendered = withinBudget ? toUnified(diff.ops) : { lines: [] as SourceDiffLine[], truncated: true }
    const fileTruncated = rendered.truncated || diff.degraded
    if (fileTruncated) truncated = true
    files.push({
      path: entry.path,
      status: entry.status,
      added: diff.added,
      removed: diff.removed,
      binary: false,
      lines: rendered.lines,
      truncated: fileTruncated,
    })
  }

  return {
    files,
    filesAdded: files.filter(file => file.status === "added").length,
    filesModified: files.filter(file => file.status === "modified").length,
    filesRemoved: files.filter(file => file.status === "removed").length,
    truncated,
  }
}

function indexFiles(project: SourceProjectLike | null | undefined): Map<string, SourceFileLike> {
  const index = new Map<string, SourceFileLike>()
  const files = project && typeof project === "object" ? (project as { files?: unknown }).files : null
  if (!Array.isArray(files)) return index
  for (const file of files) {
    if (typeof file !== "object" || file === null) continue
    const candidate = file as Partial<SourceFileLike>
    if (typeof candidate.path !== "string" || candidate.path === "") continue
    // First wins. The transport already refuses duplicate paths, so a second
    // one is malformed input rather than a state to reconcile.
    if (index.has(candidate.path)) continue
    index.set(candidate.path, {
      path: candidate.path,
      encoding: typeof candidate.encoding === "string" ? candidate.encoding : undefined,
      content: typeof candidate.content === "string" ? candidate.content : "",
    })
  }
  return index
}

/** Equal by decoded text where both decode, by raw bytes otherwise. */
function sameFile(a: SourceFileLike, b: SourceFileLike): boolean {
  if (a.content === b.content && (a.encoding ?? "") === (b.encoding ?? "")) return true
  const left = decodeSourceFile(a)
  const right = decodeSourceFile(b)
  if (left.text !== null && right.text !== null) return left.text === right.text
  return false
}

function splitLines(text: string): string[] {
  if (text === "") return []
  const lines = text.split("\n")
  // A trailing newline terminates the last line rather than starting an empty one.
  if (lines[lines.length - 1] === "") lines.pop()
  return lines
}

interface LineOp {
  kind: "context" | "add" | "del"
  text: string
  oldLine: number | null
  newLine: number | null
}

interface LineDiff {
  ops: LineOp[]
  added: number
  removed: number
  /** The edit budget or the size guard fired: the changed region is a replace. */
  degraded: boolean
}

/**
 * Line diff with the common prefix and suffix trimmed first — that trim is what
 * keeps the O(ND) core away from the parts of a file nobody edited.
 */
function diffLines(before: readonly string[], after: readonly string[]): LineDiff {
  let start = 0
  while (start < before.length && start < after.length && before[start] === after[start]) start++
  let endBefore = before.length
  let endAfter = after.length
  while (endBefore > start && endAfter > start && before[endBefore - 1] === after[endAfter - 1]) {
    endBefore--
    endAfter--
  }
  const midBefore = before.slice(start, endBefore)
  const midAfter = after.slice(start, endAfter)

  const ops: LineOp[] = []
  for (let i = 0; i < start; i++) ops.push({ kind: "context", text: before[i], oldLine: i + 1, newLine: i + 1 })

  let degraded = false
  const trace = midBefore.length > MAX_DIFF_LINES || midAfter.length > MAX_DIFF_LINES ? null : myers(midBefore, midAfter)
  if (trace === null) {
    degraded = true
    for (let i = 0; i < midBefore.length; i++) ops.push({ kind: "del", text: midBefore[i], oldLine: start + i + 1, newLine: null })
    for (let i = 0; i < midAfter.length; i++) ops.push({ kind: "add", text: midAfter[i], oldLine: null, newLine: start + i + 1 })
  } else {
    for (const op of backtrack(trace, midBefore, midAfter)) {
      ops.push({
        kind: op.kind,
        text: op.text,
        oldLine: op.oldLine === null ? null : op.oldLine + start,
        newLine: op.newLine === null ? null : op.newLine + start,
      })
    }
  }

  for (let i = 0; i < before.length - endBefore; i++) {
    ops.push({ kind: "context", text: before[endBefore + i], oldLine: endBefore + i + 1, newLine: endAfter + i + 1 })
  }

  let added = 0
  let removed = 0
  for (const op of ops) {
    if (op.kind === "add") added++
    else if (op.kind === "del") removed++
  }
  return { ops, added, removed, degraded }
}

interface MyersTrace {
  snapshots: Int32Array[]
  offset: number
}

/** Greedy O(ND) with a trace, capped at MAX_EDITS. null means "over budget". */
function myers(before: readonly string[], after: readonly string[]): MyersTrace | null {
  const n = before.length
  const m = after.length
  const max = Math.min(MAX_EDITS, n + m)
  const offset = max + 1
  const v = new Int32Array(2 * max + 3)
  const snapshots: Int32Array[] = []
  for (let d = 0; d <= max; d++) {
    snapshots.push(v.slice())
    for (let k = -d; k <= d; k += 2) {
      let x = k === -d || (k !== d && v[offset + k - 1] < v[offset + k + 1]) ? v[offset + k + 1] : v[offset + k - 1] + 1
      let y = x - k
      while (x < n && y < m && before[x] === after[y]) {
        x++
        y++
      }
      v[offset + k] = x
      if (x >= n && y >= m) return { snapshots, offset }
    }
  }
  return null
}

function backtrack(trace: MyersTrace, before: readonly string[], after: readonly string[]): LineOp[] {
  const reversed: LineOp[] = []
  let x = before.length
  let y = after.length
  for (let d = trace.snapshots.length - 1; d >= 0; d--) {
    const v = trace.snapshots[d]
    const k = x - y
    const prevK = k === -d || (k !== d && v[trace.offset + k - 1] < v[trace.offset + k + 1]) ? k + 1 : k - 1
    const prevX = v[trace.offset + prevK]
    const prevY = prevX - prevK
    while (x > prevX && y > prevY) {
      reversed.push({ kind: "context", text: before[x - 1], oldLine: x, newLine: y })
      x--
      y--
    }
    if (d === 0) break
    if (x > prevX) {
      reversed.push({ kind: "del", text: before[x - 1], oldLine: x, newLine: null })
      x--
    } else if (y > prevY) {
      reversed.push({ kind: "add", text: after[y - 1], oldLine: null, newLine: y })
      y--
    }
  }
  return reversed.reverse()
}

/**
 * Group the ops into hunks with three lines of context, stopping at the render
 * ceiling. `truncated` is the flag that keeps a partial view from being taken
 * for a reviewed one; the file's own counts stay whole.
 */
function toUnified(ops: readonly LineOp[], limit: number = SOURCE_DIFF_LIMITS.linesPerFile): { lines: SourceDiffLine[]; truncated: boolean } {
  const changedAt: number[] = []
  for (let i = 0; i < ops.length; i++) if (ops[i].kind !== "context") changedAt.push(i)
  if (changedAt.length === 0) return { lines: [], truncated: false }

  const ranges: Array<[number, number]> = []
  for (const index of changedAt) {
    const from = Math.max(0, index - CONTEXT)
    const to = Math.min(ops.length - 1, index + CONTEXT)
    const last = ranges[ranges.length - 1]
    if (last && from <= last[1] + 1) last[1] = Math.max(last[1], to)
    else ranges.push([from, to])
  }

  const lines: SourceDiffLine[] = []
  let truncated = false
  for (const [from, to] of ranges) {
    if (lines.length >= limit) {
      truncated = true
      break
    }
    let oldStart = 0
    let newStart = 0
    let oldCount = 0
    let newCount = 0
    for (let i = from; i <= to; i++) {
      const op = ops[i]
      if (op.oldLine !== null) {
        if (oldStart === 0) oldStart = op.oldLine
        oldCount++
      }
      if (op.newLine !== null) {
        if (newStart === 0) newStart = op.newLine
        newCount++
      }
    }
    lines.push({
      kind: "hunk",
      text: `@@ -${oldCount === 0 ? 0 : oldStart},${oldCount} +${newCount === 0 ? 0 : newStart},${newCount} @@`,
      oldLine: null,
      newLine: null,
    })
    for (let i = from; i <= to; i++) {
      if (lines.length >= limit) {
        truncated = true
        break
      }
      const op = ops[i]
      lines.push({ kind: op.kind, text: op.text, oldLine: op.oldLine, newLine: op.newLine })
    }
  }
  return { lines, truncated }
}

/**
 * A unified diff of two blocks of text, as text. Used for the definition diff's
 * `raw`, which must exist even when the two documents are identical — an
 * unmodelled field is only visible there — so identical input renders as
 * context rather than as nothing.
 */
export function unifiedDiffText(before: string, after: string, maxLines = 1200): string {
  const beforeLines = splitLines(before)
  const afterLines = splitLines(after)
  const diff = diffLines(beforeLines, afterLines)
  const out: string[] = []
  if (diff.added + diff.removed === 0) {
    const shown = Math.min(beforeLines.length, maxLines)
    for (let i = 0; i < shown; i++) out.push(` ${beforeLines[i]}`)
    if (beforeLines.length > shown) out.push(`… ${beforeLines.length - shown} further lines are not shown here.`)
    return out.join("\n")
  }
  const rendered = toUnified(diff.ops, maxLines)
  for (const line of rendered.lines) {
    out.push(line.kind === "hunk" ? line.text : `${line.kind === "add" ? "+" : line.kind === "del" ? "-" : " "}${line.text}`)
  }
  if (rendered.truncated || diff.degraded) out.push("… this diff is truncated; the documents are not shown in full.")
  return out.join("\n")
}
