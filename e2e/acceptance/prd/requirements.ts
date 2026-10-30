// How a test names the PRD requirement(s) it proves. The reporter turns each
// annotation into one result row.

export const PRD_REQUIREMENT = "prd-requirement"
export const PRD_EVIDENCE = "prd-evidence"

export interface PrdRequirement {
  /** Stable short id, e.g. "B.project.version" or "§7.3". */
  id: string
  /** Human-readable requirement text shown in the table. */
  title: string | null
}

const SEP = " :: "

/**
 * Annotation for test details:
 *   test("…", { annotation: prdRequirement("B.project.version", "Agent revises after a comment") }, …)
 * Pass an array of them to map one test to several requirements.
 */
export function prdRequirement(id: string, title?: string): { type: string; description: string } {
  if (!id.trim() || id.includes(SEP)) throw new Error(`invalid PRD requirement id ${JSON.stringify(id)}`)
  return { type: PRD_REQUIREMENT, description: title ? `${id.trim()}${SEP}${title.trim()}` : id.trim() }
}

export function parseRequirement(description: string): PrdRequirement {
  const i = description.indexOf(SEP)
  return i < 0 ? { id: description, title: null } : { id: description.slice(0, i), title: description.slice(i + SEP.length) }
}
