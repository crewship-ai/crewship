import { apiFetch } from "@/lib/api-fetch"
import type { Mission } from "@/lib/types/mission"

const MISSIONS_PAGE_SIZE = 100

/** Walk GET /api/v1/missions page by page until a short page. */
export async function fetchAllMissions(workspaceId: string, signal?: AbortSignal): Promise<Mission[]> {
  const rows: Mission[] = []
  let offset = 0
  do {
    const res = await apiFetch(
      `/api/v1/missions?workspace_id=${encodeURIComponent(workspaceId)}&limit=${MISSIONS_PAGE_SIZE}&offset=${offset}&include_tasks=true`,
      { signal },
    )
    if (!res.ok) throw new Error(`mission signals: ${res.status}`)
    const page = (await res.json()) as Mission[]
    rows.push(...page)
    if (page.length < MISSIONS_PAGE_SIZE) break
    offset += page.length
  } while (true)
  return rows
}
