/**
 * Catalog filters, the share consequence and bulk results (ART-03, CAT-02).
 *
 * Everything here works on rows the server already admitted. A private item
 * that is not in the list is not in any facet, count or preview built from it:
 * nothing in this file ever asks for, or counts, a row it was not handed.
 */
import type { PageView } from "@/hooks/use-pages"
import { PANEL_STATES, type PanelState } from "@/components/features/pages/panels/types"

export type CatalogType = "document" | "page"
/** `unknown` is a server that did not classify the audience — never "private". */
export type CatalogScope = "private" | "shared" | "unknown"

export interface CatalogFilters {
  types: CatalogType[]
  scopes: CatalogScope[]
  /** Owner or custodian, as `user/<id>` or `crew/<slug>`. */
  owners: string[]
  states: PanelState[]
}

export const EMPTY_CATALOG_FILTERS: CatalogFilters = { types: [], scopes: [], owners: [], states: [] }

export function catalogFilterCount(f: CatalogFilters): number {
  return f.types.length + f.scopes.length + f.owners.length + f.states.length
}

export function catalogItemType(page: PageView): CatalogType {
  return page.hasDocument === true ? "document" : "page"
}

export function catalogItemScope(page: PageView): CatalogScope {
  if (page.privateAudience === true) return "private"
  if (page.privateAudience === false && page.folder) return "shared"
  return "unknown"
}

export function filterCatalog(pages: readonly PageView[], f: CatalogFilters): PageView[] {
  return pages.filter(p =>
    (f.types.length === 0 || f.types.includes(catalogItemType(p)))
    && (f.scopes.length === 0 || f.scopes.includes(catalogItemScope(p)))
    && (f.owners.length === 0 || (p.ownerRef != null && f.owners.includes(p.ownerRef)))
    && (f.states.length === 0 || f.states.some(s => p.tally[s] > 0)))
}

export interface CatalogFacets {
  types: Record<CatalogType, number>
  scopes: Record<CatalogScope, number>
  owners: { ref: string; label: string; count: number }[]
  states: Record<PanelState, number>
}

export function catalogFacets(pages: readonly PageView[]): CatalogFacets {
  const types: Record<CatalogType, number> = { document: 0, page: 0 }
  const scopes: Record<CatalogScope, number> = { private: 0, shared: 0, unknown: 0 }
  const states = Object.fromEntries(PANEL_STATES.map(s => [s, 0])) as Record<PanelState, number>
  const owners = new Map<string, { ref: string; label: string; count: number }>()
  for (const p of pages) {
    types[catalogItemType(p)] += 1
    scopes[catalogItemScope(p)] += 1
    for (const s of PANEL_STATES) if (p.tally[s] > 0) states[s] += 1
    if (!p.ownerRef) continue
    const cur = owners.get(p.ownerRef)
    if (cur) cur.count += 1
    else owners.set(p.ownerRef, { ref: p.ownerRef, label: p.ownerLabel ?? p.ownerRef, count: 1 })
  }
  return { types, scopes, states, owners: [...owners.values()].sort((a, b) => a.label.localeCompare(b.label)) }
}

export function toggle<T>(list: readonly T[], value: T): T[] {
  return list.includes(value) ? list.filter(v => v !== value) : [...list, value]
}

// ── Share (move into a working folder, same ID) ────────────────────────────

export interface ShareConsequenceInput {
  name: string
  targetName: string
  /** The audience sentence from the folder ACL, built by lib/pages/folder-sharing. */
  audience: string
  /** Version count when known; null states the rule without a number. */
  versions: number | null
  /** Whether anyone beyond the owner gains access; null when the folder's sharing is unknown. */
  gainsAccess: boolean | null
  /** Several items: the history line applies to each of them. */
  each?: boolean
}

export interface ShareConsequence {
  audience: string
  history: string
  identity: string
  copy: string
}

export function shareConsequence({ name, targetName, audience, versions, gainsAccess, each }: ShareConsequenceInput): ShareConsequence {
  const count = versions != null && versions > 0 ? ` (${versions} ${versions === 1 ? "version" : "versions"})` : ""
  const subject = each ? "For each item, everyone" : "Everyone"
  const history = gainsAccess === false
    ? "Nobody gains access, so no one else sees the content or its history."
    : gainsAccess === null
      ? "Who gains access could not be determined, so who sees the content and history is not known."
      : `${subject} who gains access sees the current content and the full history${count}.`
  return {
    audience: `${name} moves into ${targetName}. ${audience}`,
    history,
    identity: "It is the same item with the same ID, so links, comments and permissions keep pointing at it. There is no second copy.",
    copy: "To keep a private original, cancel and use Duplicate instead.",
  }
}

// ── Bulk ───────────────────────────────────────────────────────────────────

export type BulkOutcome = "success" | "forbidden" | "conflict" | "not_found" | "failed"

export interface BulkItemResult {
  slug: string
  name: string
  outcome: BulkOutcome
  message?: string
}

export function outcomeOf(error: { status?: number } | null): BulkOutcome {
  if (!error) return "success"
  switch (error.status) {
    case 403: return "forbidden"
    case 404: return "not_found"
    case 409: return "conflict"
    default: return "failed"
  }
}

export function summariseBulk(results: readonly BulkItemResult[]): Record<BulkOutcome, number> {
  const tally: Record<BulkOutcome, number> = { success: 0, forbidden: 0, conflict: 0, not_found: 0, failed: 0 }
  for (const r of results) tally[r.outcome] += 1
  return tally
}

export const BULK_OUTCOME_LABEL: Record<BulkOutcome, string> = {
  success: "Moved",
  forbidden: "Not allowed",
  conflict: "Changed meanwhile",
  not_found: "Not found",
  failed: "Failed",
}
