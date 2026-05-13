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

export function parseSavedViews(raw: unknown): SavedView[] {
  if (Array.isArray(raw)) return raw as SavedView[]
  if (raw && typeof raw === "object" && "views" in raw) {
    const views = (raw as { views: unknown }).views
    return Array.isArray(views) ? (views as SavedView[]) : []
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
