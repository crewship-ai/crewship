// Static map of the PRD requirement IDs the acceptance specs emit, read from
// the prdRequirement("…") annotations in the spec sources. report.test.mjs
// compares it with required-ids.json so a renamed or forgotten row fails in
// CI instead of turning up as "missing" on the RC.
import * as fs from "node:fs"
import * as path from "node:path"

export const PRD_DIR = path.dirname(new URL(import.meta.url).pathname)

/**
 * Template placeholders the specs use inside an ID and the values they take.
 * Each loops over the two native engines, `wordcraft` → "word" and
 * `gridcraft` → "sheet". A placeholder not listed here is reported as
 * unresolved, so a new loop has to be added here deliberately.
 */
export const PLACEHOLDERS = {
  "label.toLowerCase()": ["word", "sheet"],
  key: ["word", "sheet"],
  slug: ["word", "sheet"],
}

/** Every `.ts` file under dir except requirements.ts (it defines prdRequirement). @param {string} dir */
function specFiles(dir) {
  /** @type {string[]} */
  const out = []
  for (const e of fs.readdirSync(dir, { withFileTypes: true })) {
    const p = path.join(dir, e.name)
    if (e.isDirectory()) out.push(...specFiles(p))
    else if (e.name.endsWith(".ts") && e.name !== "requirements.ts") out.push(p)
  }
  return out.sort()
}

/**
 * IDs emitted by `prdRequirement(<string or template literal>, …)` calls,
 * with comment lines ignored. Returns { emitted: Map<id, file[]>, unresolved }.
 * @param {string} [dir]
 */
export function scanEmittedIds(dir = PRD_DIR) {
  /** @type {Map<string, string[]>} */
  const emitted = new Map()
  /** @type {{ file: string, id: string }[]} */
  const unresolved = []
  for (const file of specFiles(dir)) {
    const rel = path.relative(dir, file)
    const src = fs
      .readFileSync(file, "utf8")
      .split("\n")
      .filter((l) => !/^\s*(\/\/|\/\*|\*)/.test(l))
      .join("\n")
    for (const m of src.matchAll(/prdRequirement\(\s*(["'`])((?:(?!\1)[^\\]|\\.)*)\1/g)) {
      const raw = m[2]
      const holes = [...raw.matchAll(/\$\{([^}]*)\}/g)].map((h) => h[1].trim())
      if (holes.some((h) => !(h in PLACEHOLDERS)) || new Set(holes).size > 1) {
        unresolved.push({ file: rel, id: raw })
        continue
      }
      const ids = holes.length === 0 ? [raw] : PLACEHOLDERS[/** @type {keyof typeof PLACEHOLDERS} */ (holes[0])].map((v) => raw.replace(/\$\{[^}]*\}/g, v))
      for (const id of ids) emitted.set(id, [...(emitted.get(id) ?? []), rel])
    }
  }
  return { emitted, unresolved }
}

/**
 * Problems between what the specs emit and what the gate requires:
 * an emitted ID that is neither required nor listed as diagnostic, a required
 * ID nobody emits, an ID emitted by more than one call (the gate would pick
 * one row and hide the other), and placeholders that cannot be resolved.
 * @param {{ emitted: Map<string, string[]>, unresolved: { file: string, id: string }[] }} scan
 * @param {{ rows: { id: string }[], diagnostic?: string[] }} required
 */
export function idCoverageProblems(scan, required) {
  const req = new Set(required.rows.map((r) => r.id))
  const diag = new Set(required.diagnostic ?? [])
  /** @type {string[]} */
  const problems = []
  for (const u of scan.unresolved) problems.push(`unresolved: ${u.id} in ${u.file}`)
  for (const [id, files] of scan.emitted) {
    if (!req.has(id) && !diag.has(id)) problems.push(`not required: ${id} (${files.join(", ")})`)
    if (files.length > 1) problems.push(`emitted more than once: ${id} (${files.join(", ")})`)
  }
  for (const id of req) if (!scan.emitted.has(id)) problems.push(`no emitting scenario: ${id}`)
  for (const id of diag) {
    if (req.has(id)) problems.push(`both required and diagnostic: ${id}`)
    if (!scan.emitted.has(id)) problems.push(`diagnostic ID nobody emits: ${id}`)
  }
  return problems
}
