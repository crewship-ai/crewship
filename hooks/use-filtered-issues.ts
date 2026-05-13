import { useMemo } from "react"
import type { Mission, IssuePriority, MissionStatus } from "@/lib/types/mission"

export interface FilteredIssuesArgs {
  issues: Mission[]
  search: string
  selectedProjectId: string | null
  filterProjectId: string | null
  filterCrewId: string | null
  filterAgentId: string | null
  filterStatuses: MissionStatus[]
  filterPriority: IssuePriority | null
}

export function useFilteredIssues({
  issues,
  search,
  selectedProjectId,
  filterProjectId,
  filterCrewId,
  filterAgentId,
  filterStatuses,
  filterPriority,
}: FilteredIssuesArgs): Mission[] {
  return useMemo(() => {
    let filtered = issues
    // Prefer explicit selection (user clicked a project) over saved-view filter.
    const effectiveProjectId = selectedProjectId ?? filterProjectId
    if (effectiveProjectId) {
      filtered = filtered.filter((i) => i.project_id === effectiveProjectId)
    }
    if (filterCrewId) {
      filtered = filtered.filter((i) => i.crew_id === filterCrewId)
    }
    if (filterAgentId) {
      filtered = filtered.filter((i) => i.assignee_id === filterAgentId)
    }
    if (filterStatuses.length > 0) {
      filtered = filtered.filter((i) => filterStatuses.includes(i.status))
    }
    if (filterPriority) {
      filtered = filtered.filter((i) => (i.priority || "none") === filterPriority)
    }
    if (search) {
      const q = search.toLowerCase()
      filtered = filtered.filter((i) =>
        i.title.toLowerCase().includes(q) ||
        (i.identifier && i.identifier.toLowerCase().includes(q)) ||
        (i.assignee_name && i.assignee_name.toLowerCase().includes(q)) ||
        (i.crew_name && i.crew_name.toLowerCase().includes(q))
      )
    }
    return filtered
  }, [issues, search, selectedProjectId, filterProjectId, filterCrewId, filterAgentId, filterStatuses, filterPriority])
}
