// Maps caret position in a routine's DSL source back to the step that
// line defines, so a code editor and a graph can point at the same
// thing. Editing `"type": "transform"` on line 81 should light up the
// node that line describes — otherwise the split view is two documents
// that happen to sit next to each other.
//
// Why a scanner and not JSON.parse: parse throws away positions, and
// the caret is a position. It also has to work on a buffer that is
// mid-edit and therefore not valid JSON yet — which is exactly when
// the user is looking at it.

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

  const lines = source.split("\n")

  // Phase 1: find the line holding the `"steps"` key. Anything before
  // it belongs to some other array and must not be scanned.
  let stepsLine = -1
  for (let i = 0; i < lines.length; i++) {
    if (/"steps"\s*:\s*\[/.test(stripStrings(lines[i], true))) {
      stepsLine = i
      break
    }
  }
  if (stepsLine === -1) return out

  // Phase 2: walk forward tracking brace/bracket depth relative to the
  // steps array. Depth 1 inside the array = one step object.
  let depth = 0 // nesting inside the steps array, in braces
  let arrayDepth = 0 // bracket nesting; 0 again = the array closed
  let current: { id: string | null; startLine: number } | null = null
  let sawArrayOpen = false

  for (let i = stepsLine; i < lines.length; i++) {
    const raw = lines[i]
    const code = stripStrings(raw, false)
    const lineNo = i + 1

    for (const ch of code) {
      if (ch === "[") {
        arrayDepth++
        sawArrayOpen = true
      } else if (ch === "]") {
        arrayDepth--
        if (sawArrayOpen && arrayDepth <= 0) {
          // steps array closed — anything after is a sibling key.
          return out
        }
      } else if (ch === "{") {
        depth++
        if (depth === 1) current = { id: null, startLine: lineNo }
      } else if (ch === "}") {
        if (depth === 1 && current) {
          if (current.id) {
            out.push({ id: current.id, startLine: current.startLine, endLine: lineNo })
          }
          current = null
        }
        depth--
        if (depth < 0) depth = 0
      }
    }

    // An `"id"` claimed at step level names the step. Nested objects sit
    // at depth > 1, so a `{"id": …}` inside a step body cannot steal it.
    if (current && current.id === null && depth === 1) {
      const m = raw.match(/"id"\s*:\s*"([^"]*)"/)
      if (m) current.id = m[1]
    }
  }

  return out
}

/** The step whose span contains `line` (1-indexed), or null. */
export function stepIdAtLine(ranges: readonly StepLineRange[], line: number): string | null {
  for (const r of ranges) {
    if (line >= r.startLine && line <= r.endLine) return r.id
  }
  return null
}

/**
 * Blank out string contents so their punctuation can't drive the scan.
 *
 * Replaces the inside of every double-quoted run with spaces, keeping
 * the quotes and the line length so column math stays honest. When
 * `keepKeys` is set the text is returned with strings intact — used for
 * the one place we need to MATCH a key rather than ignore it.
 */
function stripStrings(line: string, keepKeys: boolean): string {
  if (keepKeys) return line
  let out = ""
  let inString = false
  let escaped = false
  for (const ch of line) {
    if (escaped) {
      out += " "
      escaped = false
      continue
    }
    if (ch === "\\" && inString) {
      out += " "
      escaped = true
      continue
    }
    if (ch === '"') {
      inString = !inString
      out += '"'
      continue
    }
    out += inString ? " " : ch
  }
  return out
}
