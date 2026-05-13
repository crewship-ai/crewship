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
