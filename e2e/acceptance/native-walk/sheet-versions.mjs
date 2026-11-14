// What a Sheet version changed, judged from the workbook bytes the editor sent.
// Pure; tested in sheet-versions.test.mjs.
//
// The Sheet engine keeps the active cell (`sheets[*].view_active`) in the saved
// workbook. When Enter lands after the 300 ms autosave has already captured the
// typed value, the cursor move that Enter makes reaches the server as a version
// of its own, identical to its base except for the active cell (see the issue
// "Sheet versions a change of the active cell only", #5370). The walk accepts such a
// version only right after a version that changed content, and counts it
// separately; everything else stays a content change and is judged as before.

/** The workbook text with every sheet's active cell blanked, or null when it is not a workbook. */
export function withoutActiveCell(text) {
  let wb
  try { wb = JSON.parse(text) } catch { return null }
  if (!wb || typeof wb !== "object" || !Array.isArray(wb.sheets)) return null
  for (const sheet of wb.sheets) if (sheet && typeof sheet === "object" && "view_active" in sheet) sheet.view_active = null
  return JSON.stringify(wb)
}

/**
 * How `text` differs from `base`: "identical" (same bytes), "cursor-only" (equal once every
 * sheet's active cell is blanked, so only the active cell moved), or "content" (anything else,
 * including text that is not a workbook or is unknown).
 * @param {string | undefined} base
 * @param {string | undefined} text
 */
export function changeKind(base, text) {
  if (base === undefined || text === undefined) return "content"
  if (base === text) return "identical"
  const a = withoutActiveCell(base), b = withoutActiveCell(text)
  return a !== null && a === b ? "cursor-only" : "content"
}

/**
 * Classifies every committed version by the bytes that made it, against its base version.
 * A cursor-only version is accepted only when its base was itself made by a content change
 * in this walk; otherwise it is a problem (it does not follow a committed content save).
 * @param {{ version: string, text: string | undefined }} initial the document the walk opened
 * @param {{ version: string, base: string, text: string | undefined }[]} writes committed writes, in order
 * @returns {{ kinds: Record<string, "content" | "cursor-only" | "identical">, cursorOnly: { version: string, base: string }[], problems: string[] }}
 */
export function classifyVersions(initial, writes) {
  const texts = new Map([[initial.version, initial.text]])
  const kinds = {}
  const cursorOnly = []
  const problems = []
  for (const w of writes) {
    if (w.version in kinds) continue // a reconcile of a version already classified
    const kind = texts.has(w.base) ? changeKind(texts.get(w.base), w.text) : "content"
    kinds[w.version] = kind
    texts.set(w.version, w.text)
    if (kind === "cursor-only") {
      cursorOnly.push({ version: w.version, base: w.base })
      if (kinds[w.base] !== "content") problems.push(`v${w.version} changes only the active cell and does not follow a content change (v${w.base} is ${kinds[w.base] ?? "the opened document"})`)
    }
    if (kind === "identical") problems.push(`v${w.version} is a version with the same bytes as v${w.base}`)
  }
  return { kinds, cursorOnly, problems }
}

/** Are all versions after `from` up to `to` cursor-only (so `to` is `from` plus moves of the active cell)? */
export function onlyCursorAfter(kinds, from, to) {
  for (let v = BigInt(from) + 1n; v <= BigInt(to); v++) if (kinds[String(v)] !== "cursor-only") return false
  return true
}

/**
 * The rules after a dropped save reply: no save may repeat the lost attempt's bytes, the first
 * save must build on the version the lost save committed, and no save may change content
 * (the step types nothing after the lost marker).
 * @param {{ version: string, contentHash: string | undefined }} lost
 * @param {{ kind: string, baseVersion?: string, contentHash?: string, version?: string, unchanged?: boolean }[]} after requests sent after the loss, in order
 * @param {Record<string, string>} kinds from classifyVersions
 */
export function lostReplyProblems(lost, after, kinds) {
  const problems = []
  const saves = after.filter((o) => o.kind === "save")
  for (const s of saves) if (lost.contentHash !== undefined && s.contentHash === lost.contentHash) problems.push(`re-sent the lost attempt (${String(lost.contentHash).slice(7, 15)}) as a new save`)
  if (saves[0] && saves[0].baseVersion !== lost.version) problems.push(`the first save after the lost reply builds on v${saves[0].baseVersion}, not the lost save's v${lost.version}`)
  for (const s of saves) if (s.version !== undefined && s.unchanged !== true && kinds[s.version] !== "cursor-only") problems.push(`save v${s.version} after the lost reply is a ${kinds[s.version] ?? "unclassified"} change, not a move of the active cell`)
  return problems
}
