import type { SavedView } from "@/lib/types/mission"

// Filter fields a saved view may carry. The `filters_json` payload schema
// is intentionally flexible — callers apply what's clearly mappable and
// ignore anything else, so unknown fields don't break older clients.
export interface SavedViewFilters {
  projectId: string | null
  crewId: string | null
  agentId: string | null
  search: string
}

// Minimal runtime guard so malformed entries (missing/typo'd `id`/`name`,
// non-string fields) don't crash consumers that read `v.id` / `v.name`.
// We intentionally don't validate every nested field — `filters_json`
// and `sort_json` are flexible payloads and `applySavedView` already
// tolerates malformed JSON.
function isSavedView(value: unknown): value is SavedView {
  if (!value || typeof value !== "object") return false
  const v = value as Record<string, unknown>
  return typeof v.id === "string" && typeof v.name === "string"
}

/**
 * Which surface a saved view belongs to.
 *
 * `GET /api/v1/saved-views` returns every view in the workspace, so the two
 * consumers have to tell theirs apart: a journal view applied to the issue
 * board sets no issue filter at all and reads as a broken menu entry.
 *
 * The discriminator is a key inside `filters_json`, NOT `view_type`, and that
 * is not a style choice: `saved_views.view_type` carries a
 * `CHECK (view_type IN ('board','list'))` constraint, so a third value is a
 * 500 from the insert — which is what a browser run of this feature produced
 * before the marker moved. `filters_json` is a free-form TEXT column and
 * already the place both surfaces keep their own vocabulary, so the journal
 * needs no schema change to store one.
 */
export const JOURNAL_SURFACE = "journal"

export function isJournalView(view: SavedView): boolean {
  if (!view.filters_json) return false
  try {
    const parsed: unknown = JSON.parse(view.filters_json)
    if (!parsed || typeof parsed !== "object") return false
    return (parsed as Record<string, unknown>).surface === JOURNAL_SURFACE
  } catch {
    // Unparseable filters belong to whoever wrote them; the journal only
    // claims what it can prove is its own.
    return false
  }
}

/** Views the issues surface can apply — everything that is not a journal view. */
export function issueViews(views: SavedView[]): SavedView[] {
  return views.filter((v) => !isJournalView(v))
}

/** Views the journal surface can apply. */
export function journalViews(views: SavedView[]): SavedView[] {
  return views.filter(isJournalView)
}

export function parseSavedViews(raw: unknown): SavedView[] {
  if (Array.isArray(raw)) return raw.filter(isSavedView)
  if (raw && typeof raw === "object" && "views" in raw) {
    const views = (raw as { views: unknown }).views
    return Array.isArray(views) ? views.filter(isSavedView) : []
  }
  return []
}

export function applySavedView(view: SavedView): SavedViewFilters {
  const empty: SavedViewFilters = {
    projectId: null,
    crewId: null,
    agentId: null,
    search: "",
  }
  if (!view.filters_json) return empty
  try {
    const parsed: Record<string, unknown> = JSON.parse(view.filters_json)
    const projectId = parsed.project_id ?? parsed.projectId
    const crewId = parsed.crew_id ?? parsed.crewId
    const agentId =
      parsed.assignee_id ?? parsed.assigneeId ?? parsed.agent_id
    const search = parsed.search ?? parsed.query
    return {
      projectId: typeof projectId === "string" ? projectId : null,
      crewId: typeof crewId === "string" ? crewId : null,
      agentId: typeof agentId === "string" ? agentId : null,
      search: typeof search === "string" ? search : "",
    }
  } catch {
    return empty
  }
}
