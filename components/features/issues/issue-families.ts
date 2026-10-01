import type { Mission } from "@/lib/types/mission"

/**
 * Generated issues (a watcher's "infra-prehled/verdikt stopped reporting",
 * a scanner's "[e2e-scan] test-…") share a source prefix. In a 280px rail
 * that prefix is all that survives truncation, so fifteen of them read as
 * one row repeated. The rail folds a run of them into one entry (README §4:
 * priority, cap, fold). Presentation only: nothing is filtered out, and the
 * order of the list is kept.
 */

// "[e2e-scan]" or "source/check" as the first token, followed by a space,
// a colon or the end of the title. Both halves of a path need two chars so
// a stray "a/b" in prose does not become a family.
const FAMILY_RE = /^(\[[^\]\s]{2,}\]|[a-z0-9][\w.-]+\/[\w.-]{2,})(?=[\s:]|$)/i

export function familyKey(title: string): string | null {
  const m = FAMILY_RE.exec(title.trim())
  return m ? m[1] : null
}

/** The title with the family prefix and its separator removed. */
export function memberTitle(title: string, key: string): string {
  const t = title.trim()
  if (!t.startsWith(key)) return t
  const rest = t.slice(key.length).replace(/^[\s:–—-]+/, "")
  return rest || t
}

/** Statuses that need a person stay as their own row, never inside a fold. */
const NEEDS_A_PERSON = new Set(["FAILED"])

export type IssueRailEntry =
  | { kind: "issue"; issue: Mission }
  | { kind: "family"; key: string; issues: Mission[] }

export function foldIssueFamilies(
  issues: Mission[],
  { minGroup = 3, disabled = false }: { minGroup?: number; disabled?: boolean } = {},
): IssueRailEntry[] {
  if (disabled) return issues.map((issue) => ({ kind: "issue", issue }))
  const members = new Map<string, Mission[]>()
  for (const issue of issues) {
    if (NEEDS_A_PERSON.has(issue.status)) continue
    const key = familyKey(issue.title ?? "")
    if (!key) continue
    const list = members.get(key)
    if (list) list.push(issue)
    else members.set(key, [issue])
  }
  const out: IssueRailEntry[] = []
  const placed = new Set<string>()
  for (const issue of issues) {
    const key = NEEDS_A_PERSON.has(issue.status) ? null : familyKey(issue.title ?? "")
    const family = key ? members.get(key) : undefined
    if (!key || !family || family.length < minGroup) {
      out.push({ kind: "issue", issue })
      continue
    }
    if (placed.has(key)) continue
    placed.add(key)
    out.push({ kind: "family", key, issues: family })
  }
  return out
}
