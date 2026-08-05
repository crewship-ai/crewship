// Maps caret position in a routine's DSL source back to the step that
// line defines, so a code editor and a graph can point at the same
// thing. Editing `type: transform` on line 81 should light up the node
// that line describes — otherwise the split view is two documents that
// happen to sit next to each other.
//
// Built on the YAML document AST rather than a hand-rolled scanner.
// YAML 1.2 is a superset of JSON, so one implementation serves both
// formats, and the two cases a scanner has to get right by hand — a
// nested object carrying its own `id`, and an unbalanced brace inside a
// prompt string — stop being special cases and become properties of the
// parser. (Both of those did slip past an earlier hand-rolled version
// until mutation testing forced a hostile fixture; the tests below are
// that fixture, kept as a regression guard.)
//
// The parser is error-tolerant: a half-typed buffer yields whatever
// structure it could recover, which degrades to "selection stops
// updating" rather than "the editor explodes". That matters because a
// mid-edit buffer is exactly when the caret is moving.

import { isMap, isSeq, parseDocument } from "yaml"

export interface StepLineRange {
  id: string
  /** 1-indexed, inclusive — matches what an editor gutter shows. */
  startLine: number
  endLine: number
}

/**
 * Line span of every object inside the top-level `steps` array.
 *
 * Only `steps` — an `inputs` array of objects must not produce phantom
 * steps. Depth is tracked with a string-aware scanner so a brace inside
 * a prompt ("reply with { ok: true }") cannot close a step early; that
 * is not a hypothetical, agent prompts are full of them.
 *
 * Never throws. A malformed or half-typed buffer yields whatever ranges
 * were unambiguous before the damage, which degrades to "selection
 * stops updating" rather than "the editor explodes".
 */
export function stepLineRanges(source: string): StepLineRange[] {
  const out: StepLineRange[] = []
  if (!source) return out

  let doc: ReturnType<typeof parseDocument>
  try {
    doc = parseDocument(source, { prettyErrors: false })
  } catch {
    return out
  }

  const contents = doc.contents
  if (!isMap(contents)) return out
  const steps = contents.get("steps", true)
  if (!isSeq(steps)) return out

  const lineAt = lineResolver(source)
  for (const item of steps.items) {
    if (!isMap(item)) continue
    const id = item.get("id")
    if (typeof id !== "string" || id === "") continue
    const range = (item as unknown as { range?: [number, number, number] }).range
    if (!range) continue
    const startLine = lineAt(range[0])
    // range[1] is the end of the node's value; range[2] includes any
    // trailing comment/whitespace up to the next node, which would make
    // one step's span swallow the blank line before the next.
    const endLine = lineAt(Math.max(range[0], range[1] - 1))
    if (startLine === undefined || endLine === undefined) continue
    out.push({ id, startLine, endLine })
  }
  return out
}

/**
 * Offset → 1-indexed line, via a prefix table built once per call.
 *
 * Counting newlines per lookup would re-scan the whole buffer for every
 * step in a long routine.
 */
function lineResolver(text: string): (offset: number) => number | undefined {
  const starts: number[] = [0]
  for (let i = 0; i < text.length; i++) {
    if (text[i] === "\n") starts.push(i + 1)
  }
  return (offset: number) => {
    if (!Number.isFinite(offset)) return undefined
    let lo = 0
    let hi = starts.length - 1
    while (lo < hi) {
      const mid = Math.ceil((lo + hi) / 2)
      if (starts[mid] <= offset) lo = mid
      else hi = mid - 1
    }
    return lo + 1
  }
}

/** The step whose span contains `line` (1-indexed), or null. */
export function stepIdAtLine(ranges: readonly StepLineRange[], line: number): string | null {
  for (const r of ranges) {
    if (line >= r.startLine && line <= r.endLine) return r.id
  }
  return null
}
