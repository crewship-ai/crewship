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

export interface FilteredIssues {
  /** What the board and the list render: every active filter applied. */
  visible: Mission[]
  /**
   * The same set with the **status** filter left out — the population the
   * status chips count.
   *
   * Counting `visible` instead is a trap the chip row cannot recover from:
   * every status the user did not pick reads 0, a zero-count chip is not
   * rendered at all (`issues-status-chips.tsx`), and so a second status can
   * never be added to the filter. The "All" pill has the same problem in
   * reverse — it would advertise the filtered count as the total.
   */
  statusFacet: Mission[]
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
}: FilteredIssuesArgs): FilteredIssues {
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
    // Status is applied last, and kept apart from the rest, so the chip row
    // can be handed what the *other* filters allow through — independently of
    // the status selection the chips themselves own.
    const statusFacet = filtered
    const visible =
      filterStatuses.length > 0
        ? filtered.filter((i) => filterStatuses.includes(i.status))
        : filtered
    return { visible, statusFacet }
  }, [issues, search, selectedProjectId, filterProjectId, filterCrewId, filterAgentId, filterStatuses, filterPriority])
}
